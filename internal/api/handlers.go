// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/secrets"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"name":        s.cfg.ServerName,
		"unix":        time.Now().UTC().Unix(),
		"maintenance": s.cfg.Maintenance,
	})
}

// handleTime replaces the retired server-time endpoint. The client uses this
// value for craft and upgrade timers, so it must be plain UTC seconds.
func (s *Server) handleTime(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	s.writeJSON(w, http.StatusOK, map[string]any{
		"unix": now.Unix(),
		"utc":  now.Format(time.RFC3339),
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"server_name":   s.cfg.ServerName,
		"maintenance":   s.cfg.Maintenance,
		"photon_region": s.cfg.PhotonRegion,
		"photon_modes":  photonModes(s.cfg.PhotonAppIDs),
		"features":      map[string]any{},
	})
}

func photonModes(appIDs map[string]string) []string {
	modes := make([]string, 0, len(appIDs))
	for mode := range appIDs {
		modes = append(modes, mode)
	}
	return modes
}

type registerRequest struct {
	DeviceID string `json:"device_id"`
	Nick     string `json:"nick"`
}

type accountResponse struct {
	PlayerID string `json:"player_id"`
	Session  string `json:"session"`
	Nick     string `json:"nick"`
	Created  bool   `json:"created"`
}

// handleRegister is account registration and login in one call: the device id
// identifies the account, the server mints the player id and rotates the
// session token. Nothing about the account lives on the device.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if strings.TrimSpace(req.DeviceID) == "" {
		s.writeError(w, http.StatusBadRequest, "missing_device_id", "device_id is required")
		return
	}
	ip := clientIP(r)
	if ban, banned := s.st.MatchBan("", req.DeviceID, ip); banned {
		s.writeBan(w, ban)
		return
	}

	account, created, err := s.st.RegisterOrLogin(req.DeviceID, req.Nick, ip)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "register_failed", err.Error())
		return
	}
	// A ban on the resolved player id is only visible after resolution.
	if ban, banned := s.st.MatchBan(account.PlayerID, "", ""); banned {
		s.writeBan(w, ban)
		return
	}

	s.log.Event("account.login", map[string]any{
		"player_id": account.PlayerID,
		"device_id": account.DeviceID,
		"nick":      account.Nick,
		"ip":        ip,
		"created":   created,
	})
	s.writeJSON(w, http.StatusOK, accountResponse{
		PlayerID: account.PlayerID,
		Session:  account.Session,
		Nick:     account.Nick,
		Created:  created,
	})
}

type refreshRequest struct {
	PlayerID string `json:"player_id"`
	Session  string `json:"session"`
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	account, ok := s.authenticate(w, r, req.PlayerID, req.Session)
	if !ok {
		return
	}
	s.writeJSON(w, http.StatusOK, accountResponse{
		PlayerID: account.PlayerID,
		Session:  account.Session,
		Nick:     account.Nick,
	})
}

// handlePhotonAppID hands the Photon AppID to an authenticated client sealed
// with AES-256-GCM. SECURITY: the AppID is a credential; it is bound to the
// requesting player id as additional authenticated data and never logged.
func (s *Server) handlePhotonAppID(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.SealsAppIDs() {
		s.writeError(w, http.StatusServiceUnavailable, "appid_unconfigured",
			"set OPG3D_APPID_KEY and at least one OPG3D_PHOTON_APPID_*")
		return
	}
	playerID := r.URL.Query().Get("player_id")
	session := bearer(r)
	account, ok := s.authenticate(w, r, playerID, session)
	if !ok {
		return
	}

	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "pun"
	}
	appID, ok := s.cfg.PhotonAppIDs[mode]
	if !ok {
		s.writeError(w, http.StatusNotFound, "unknown_mode", mode)
		return
	}

	envelope, err := secrets.Seal(s.cfg.AppIDKey, []byte(appID), []byte(account.PlayerID+"|"+mode))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "seal_failed", err.Error())
		return
	}
	s.log.Event("photon.appid_issued", map[string]any{
		"player_id":     account.PlayerID,
		"mode":          mode,
		"region":        s.cfg.PhotonRegion,
		"from_realtime": mode == "pun" && s.cfg.PhotonPUNFromRealtime,
	})
	s.writeJSON(w, http.StatusOK, map[string]any{
		"mode":     mode,
		"region":   s.cfg.PhotonRegion,
		"envelope": envelope,
	})
}

// handleUpdate lets players update in a couple of taps: the client asks the
// backend, not a hardcoded store link, where the current build lives.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Update.Version == "" {
		s.writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	current := r.URL.Query().Get("version")
	s.writeJSON(w, http.StatusOK, map[string]any{
		"available": current != s.cfg.Update.Version,
		"current":   current,
		"update":    s.cfg.Update,
	})
}

// handleProblem is the client crash/diagnostic sink. The payload is logged
// verbatim so a failure can be read back exactly as the client reported it.
func (s *Server) handleProblem(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "read_failed", err.Error())
		return
	}
	s.log.Event("client.problem", map[string]any{
		"ip":        clientIP(r),
		"player_id": r.URL.Query().Get("player_id"),
		"bytes":     len(raw),
		"report":    s.log.Body(raw),
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"accepted": true})
}

// handleLegacyAuth answers the retired /auth_v2/ contract. It returns every
// spelling this client's parsers are known to read, because a JSON consumer
// ignores members it does not know and covering the aliases removes a class of
// guesswork at no cost.
func (s *Server) handleLegacyAuth(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	deviceID := firstNonEmpty(
		r.Header.Get("X-OPG3D-Device"),
		query.Get("device_id"),
		query.Get("udid"),
		query.Get("id_player"),
	)
	if deviceID == "" {
		// A legacy client with no identifier still has to boot; the address it
		// came from is the only stable handle available.
		deviceID = "legacy-" + clientIP(r)
	}
	ip := clientIP(r)
	if ban, banned := s.st.MatchBan("", deviceID, ip); banned {
		s.writeBan(w, ban)
		return
	}

	account, created, err := s.st.RegisterOrLogin(deviceID, query.Get("nick"), ip)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "register_failed", err.Error())
		return
	}
	if ban, banned := s.st.MatchBan(account.PlayerID, "", ""); banned {
		s.writeBan(w, ban)
		return
	}
	s.log.Event("account.legacy_auth", map[string]any{
		"player_id": account.PlayerID,
		"device_id": deviceID,
		"ip":        ip,
		"created":   created,
	})
	s.writeJSON(w, http.StatusOK, map[string]any{
		"result":    "ok",
		"id_player": account.PlayerID,
		"player_id": account.PlayerID,
		"id":        account.PlayerID,
		"user_id":   account.PlayerID,
		"session":   account.Session,
		"token":     account.Session,
		"nick":      account.Nick,
		"name":      account.Nick,
		"time":      time.Now().UTC().Unix(),
	})
}

// handleUnknown records every path the client asks for that this server does
// not implement yet. That log is the to-do list for endpoint coverage.
func (s *Server) handleUnknown(w http.ResponseWriter, r *http.Request) {
	s.log.Event("http.unhandled", map[string]any{
		"method": r.Method,
		"path":   r.URL.Path,
		"query":  r.URL.RawQuery,
		"ip":     clientIP(r),
	})
	s.writeError(w, http.StatusNotFound, "unhandled_route", r.URL.Path)
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, playerID, session string) (store.Account, bool) {
	ip := clientIP(r)
	account, ok := s.st.Authenticate(playerID, session, ip)
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "bad_session", "unknown player id or session")
		return store.Account{}, false
	}
	if ban, banned := s.st.MatchBan(account.PlayerID, account.DeviceID, ip); banned {
		s.writeBan(w, ban)
		return store.Account{}, false
	}
	return account, true
}

func (s *Server) writeBan(w http.ResponseWriter, ban store.Ban) {
	payload := map[string]any{
		"error":  "banned",
		"kind":   ban.Kind,
		"reason": ban.Reason,
	}
	if ban.Until != nil {
		payload["until"] = ban.Until.UTC().Format(time.RFC3339)
	}
	s.log.Event("account.ban_enforced", map[string]any{
		"kind":    ban.Kind,
		"subject": ban.Subject,
		"reason":  ban.Reason,
	})
	s.writeJSON(w, http.StatusForbidden, payload)
}

// admin wraps a handler with the shared-token check. With no token configured
// the endpoints are closed rather than open.
func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminToken == "" {
			s.writeError(w, http.StatusServiceUnavailable, "admin_disabled", "set OPG3D_ADMIN_TOKEN")
			return
		}
		if bearer(r) != s.cfg.AdminToken {
			s.writeError(w, http.StatusUnauthorized, "admin_denied", "bad admin token")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleAdminAccounts(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"accounts": s.st.Accounts()})
}

func (s *Server) handleAdminListBans(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"bans": s.st.Bans()})
}

type banRequest struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Reason  string `json:"reason"`
	Minutes int    `json:"minutes"`
}

func (s *Server) handleAdminAddBan(w http.ResponseWriter, r *http.Request) {
	var req banRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	ban := store.Ban{Kind: req.Kind, Subject: req.Subject, Reason: req.Reason}
	if req.Minutes > 0 {
		until := time.Now().UTC().Add(time.Duration(req.Minutes) * time.Minute)
		ban.Until = &until
	}
	if err := s.st.AddBan(ban); err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, store.ErrBadBanKind) {
			status = http.StatusInternalServerError
		}
		s.writeError(w, status, "ban_failed", err.Error())
		return
	}
	s.log.Event("admin.ban_added", map[string]any{
		"kind": ban.Kind, "subject": ban.Subject, "reason": ban.Reason, "minutes": req.Minutes,
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminRemoveBan(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	subject := r.URL.Query().Get("subject")
	removed, err := s.st.RemoveBans(kind, subject)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "unban_failed", err.Error())
		return
	}
	s.log.Event("admin.ban_removed", map[string]any{"kind": kind, "subject": subject, "removed": removed})
	s.writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

func bearer(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if value, ok := strings.CutPrefix(header, "Bearer "); ok {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(r.Header.Get("X-OPG3D-Session"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func readAll(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	return io.ReadAll(r.Body)
}
