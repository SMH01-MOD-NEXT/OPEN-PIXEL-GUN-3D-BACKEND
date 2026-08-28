// SPDX-License-Identifier: GPL-3.0-or-later

package api

// The operator API behind the admin console.
//
// Every route here is wrapped by admin(), which closes the endpoints when no
// OPG3D_ADMIN_TOKEN is configured rather than leaving them open. These handlers
// can rename players, move progress between accounts and ban people, so the
// failure mode of a missing token has to be "refuse", never "allow".
//
// Each mutation is logged with what it did and to whom. An operator tool whose
// actions are not auditable is worse than none: when an account is restored
// onto the wrong id, the log is the only way back.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/logs"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

// registerAdminRoutes mounts the operator API and console.
func (s *Server) registerAdminRoutes() {
	s.mux.HandleFunc("GET /api/v1/admin/stats", s.admin(s.handleAdminStats))
	s.mux.HandleFunc("GET /api/v1/admin/players", s.admin(s.handleAdminPlayers))
	s.mux.HandleFunc("GET /api/v1/admin/players/{id}", s.admin(s.handleAdminPlayer))
	s.mux.HandleFunc("PUT /api/v1/admin/players/{id}/profile", s.admin(s.handleAdminSaveProfile))
	s.mux.HandleFunc("POST /api/v1/admin/players/{id}/ban", s.admin(s.handleAdminBanPlayer))
	s.mux.HandleFunc("POST /api/v1/admin/players/{id}/unban", s.admin(s.handleAdminUnbanPlayer))
	s.mux.HandleFunc("DELETE /api/v1/admin/players/{id}", s.admin(s.handleAdminDeletePlayer))
	s.mux.HandleFunc("POST /api/v1/admin/change-id", s.admin(s.handleAdminChangeID))
	s.mux.HandleFunc("POST /api/v1/admin/transfer", s.admin(s.handleAdminTransfer))
	s.mux.HandleFunc("GET /api/v1/admin/logs", s.admin(s.handleAdminLogs))
	s.mux.HandleFunc("GET /api/v1/admin/logs/stream", s.admin(s.handleAdminLogStream))
	s.mux.HandleFunc("GET /admin", s.handleAdminConsole)
	s.mux.HandleFunc("GET /admin/", s.handleAdminConsole)
}

// adminPlayer is one row of the console's player list: the account, the
// profile that holds its progress, and whether a ban currently applies. They
// are joined here because an operator judging an account needs all three at
// once, and three round trips per row would make the list useless.
type adminPlayer struct {
	store.Account
	Profile   *store.Profile `json:"profile,omitempty"`
	Banned    bool           `json:"banned"`
	BanKind   string         `json:"ban_kind,omitempty"`
	BanReason string         `json:"ban_reason,omitempty"`
	BanUntil  *time.Time     `json:"ban_until,omitempty"`
}

func (s *Server) buildAdminPlayer(account store.Account) adminPlayer {
	row := adminPlayer{Account: account}
	if profile, err := s.st.Profile(account.PlayerID); err == nil {
		row.Profile = &profile
	}
	if ban, banned := s.st.MatchBan(account.PlayerID, firstNonEmpty(account.SocketDevice, account.DeviceID), account.LastIP); banned {
		row.Banned = true
		row.BanKind = ban.Kind
		row.BanReason = ban.Reason
		row.BanUntil = ban.Until
	}
	return row
}

func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"stats":         s.st.Stats(),
		"server":        s.cfg.ServerName,
		"version":       s.cfg.Update.Version,
		"maintenance":   s.cfg.Maintenance,
		"sio_reply":     s.cfg.SioReply,
		"sessions":      s.sio.count(),
		"logs_dropped":  s.log.Dropped(),
		"photon_region": s.cfg.PhotonRegion,
	})
}

// handleAdminPlayers lists players, optionally filtered. The filter matches an
// id, a nick, a device fingerprint or an address, because those are the four
// things an operator has in hand when somebody asks for help or gets reported.
func (s *Server) handleAdminPlayers(w http.ResponseWriter, r *http.Request) {
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	onlyBanned := r.URL.Query().Get("banned") == "1"

	accounts := s.st.Accounts()
	rows := make([]adminPlayer, 0, len(accounts))
	for _, account := range accounts {
		if query != "" && !adminMatches(account, query) {
			continue
		}
		row := s.buildAdminPlayer(account)
		if onlyBanned && !row.Banned {
			continue
		}
		rows = append(rows, row)
		if len(rows) >= limit {
			break
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"players": rows,
		"total":   len(accounts),
		"shown":   len(rows),
	})
}

func adminMatches(account store.Account, query string) bool {
	fields := []string{
		account.PlayerID,
		account.Nick,
		account.DeviceID,
		account.SocketDevice,
		account.LastIP,
	}
	for _, field := range fields {
		if field != "" && strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

func (s *Server) handleAdminPlayer(w http.ResponseWriter, r *http.Request) {
	account, err := s.st.Account(r.PathValue("id"))
	if err != nil {
		s.writeAdminError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"player": s.buildAdminPlayer(account)})
}

type adminProfileRequest struct {
	Nick       string `json:"nick"`
	Level      int    `json:"level"`
	Experience int64  `json:"experience"`
	Coins      int64  `json:"coins"`
	Gems       int64  `json:"gems"`
}

func (s *Server) handleAdminSaveProfile(w http.ResponseWriter, r *http.Request) {
	var req adminProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	id := r.PathValue("id")
	profile, err := s.st.SetProfile(id, req.Nick, req.Level, req.Experience, req.Coins, req.Gems)
	if err != nil {
		s.writeAdminError(w, err)
		return
	}
	if req.Nick != "" {
		// The account carries the nick the game logs in with, so an edit that
		// only touched the profile would leave the two disagreeing.
		if err := s.st.SetNick(id, req.Nick); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.writeAdminError(w, err)
			return
		}
	}
	s.log.Event("admin.profile_saved", map[string]any{
		"player_id": id,
		"nick":      req.Nick,
		"level":     req.Level,
		"coins":     req.Coins,
		"gems":      req.Gems,
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"profile": profile})
}

type adminBanRequest struct {
	Reason string `json:"reason"`
	// Minutes of zero means permanent, matching the existing ban endpoint.
	Minutes int `json:"minutes"`
	// IncludeDevice also bans the handset fingerprint. Banning only the player
	// id stops one account; a cheater simply registers again. This defaults to
	// on for exactly that reason and can be turned off when an operator is
	// deliberately banning a single account on a shared device.
	IncludeDevice *bool `json:"include_device,omitempty"`
}

// handleAdminBanPlayer is the one-click ban: it bans the account and, unless
// told otherwise, the device behind it.
func (s *Server) handleAdminBanPlayer(w http.ResponseWriter, r *http.Request) {
	var req adminBanRequest
	// An empty body is a valid permanent ban with no reason given, so a decode
	// failure on an empty request must not be an error.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	account, err := s.st.Account(r.PathValue("id"))
	if err != nil {
		s.writeAdminError(w, err)
		return
	}

	var until *time.Time
	if req.Minutes > 0 {
		deadline := time.Now().UTC().Add(time.Duration(req.Minutes) * time.Minute)
		until = &deadline
	}

	subjects := []store.Ban{{
		Kind:    store.BanPlayer,
		Subject: account.PlayerID,
		Reason:  req.Reason,
		Until:   until,
	}}
	includeDevice := req.IncludeDevice == nil || *req.IncludeDevice
	if device := firstNonEmpty(account.SocketDevice, account.DeviceID); includeDevice && device != "" {
		subjects = append(subjects, store.Ban{
			Kind:    store.BanDevice,
			Subject: device,
			Reason:  req.Reason,
			Until:   until,
		})
	}

	for _, ban := range subjects {
		if err := s.st.AddBan(ban); err != nil {
			s.writeAdminError(w, err)
			return
		}
	}
	s.log.Event("admin.player_banned", map[string]any{
		"player_id":      account.PlayerID,
		"nick":           account.Nick,
		"reason":         req.Reason,
		"minutes":        req.Minutes,
		"include_device": includeDevice,
		"bans":           len(subjects),
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bans": len(subjects)})
}

// handleAdminUnbanPlayer lifts every ban that could apply to this account, so
// correcting a mistake takes one click rather than one click per subject.
func (s *Server) handleAdminUnbanPlayer(w http.ResponseWriter, r *http.Request) {
	account, err := s.st.Account(r.PathValue("id"))
	if err != nil {
		s.writeAdminError(w, err)
		return
	}

	removed, err := s.st.RemoveBans(store.BanPlayer, account.PlayerID)
	if err != nil {
		s.writeAdminError(w, err)
		return
	}
	if device := firstNonEmpty(account.SocketDevice, account.DeviceID); device != "" {
		lifted, err := s.st.RemoveBans(store.BanDevice, device)
		if err != nil {
			s.writeAdminError(w, err)
			return
		}
		removed += lifted
	}
	s.log.Event("admin.player_unbanned", map[string]any{
		"player_id": account.PlayerID,
		"nick":      account.Nick,
		"removed":   removed,
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}

func (s *Server) handleAdminDeletePlayer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.st.DeleteAccount(id); err != nil {
		s.writeAdminError(w, err)
		return
	}
	s.log.Event("admin.player_deleted", map[string]any{"player_id": id})
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type adminChangeIDRequest struct {
	OldID string `json:"old_id"`
	NewID string `json:"new_id"`
}

func (s *Server) handleAdminChangeID(w http.ResponseWriter, r *http.Request) {
	var req adminChangeIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	account, err := s.st.ChangePlayerID(req.OldID, req.NewID)
	if err != nil {
		s.writeAdminError(w, err)
		return
	}
	s.log.Event("admin.id_changed", map[string]any{
		"old_id": req.OldID,
		"new_id": account.PlayerID,
		"note":   "bans on the old id were rewritten to the new one",
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"player": s.buildAdminPlayer(account)})
}

type adminTransferRequest struct {
	FromID string `json:"from_id"`
	ToID   string `json:"to_id"`
}

// handleAdminTransfer is the account-restoration flow: a player who lost access
// to an old id keeps playing on a new one, and both logins stay valid.
func (s *Server) handleAdminTransfer(w http.ResponseWriter, r *http.Request) {
	var req adminTransferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	profile, err := s.st.TransferProgress(req.FromID, req.ToID)
	if err != nil {
		s.writeAdminError(w, err)
		return
	}
	s.log.Event("admin.progress_transferred", map[string]any{
		"from_id": req.FromID,
		"to_id":   req.ToID,
		"level":   profile.Level,
		"note":    "the source profile was emptied so the same progress cannot be handed out twice",
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"profile": profile})
}

func (s *Server) handleAdminLogs(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"lines":   s.log.Recent(limit),
		"dropped": s.log.Dropped(),
	})
}

// handleAdminLogStream streams log lines as server-sent events.
//
// The console reads this with fetch() rather than EventSource, because
// EventSource cannot set an Authorization header and the alternative would be
// putting the admin token in a URL, where it would end up in this server's own
// access logs.
func (s *Server) handleAdminLogStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "no_flush", "this connection cannot stream")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Without this a reverse proxy may buffer the stream into uselessness.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Subscribe before replaying the backlog, so a line produced between the
	// two is delivered late rather than lost. The console tolerates a repeat;
	// it cannot tolerate a gap.
	id, feed := s.log.Subscribe()
	defer s.log.Unsubscribe(id)

	for _, entry := range s.log.Recent(200) {
		if !writeSSE(w, entry) {
			return
		}
	}
	flusher.Flush()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case entry, open := <-feed:
			if !open {
				return
			}
			if !writeSSE(w, entry) {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			// A comment frame keeps idle proxies from closing the stream.
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, entry logs.Entry) bool {
	payload, err := json.Marshal(entry)
	if err != nil {
		return true
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	return err == nil
}

// writeAdminError maps a store failure onto the status an operator tool needs
// to tell "you asked for something impossible" apart from "this server broke".
func (s *Server) writeAdminError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, store.ErrIDTaken):
		s.writeError(w, http.StatusConflict, "id_taken", err.Error())
	case errors.Is(err, store.ErrBadPlayerID):
		s.writeError(w, http.StatusBadRequest, "bad_player_id", err.Error())
	case errors.Is(err, store.ErrSameAccount):
		s.writeError(w, http.StatusBadRequest, "same_account", err.Error())
	case errors.Is(err, store.ErrBadBanKind):
		s.writeError(w, http.StatusBadRequest, "bad_ban_kind", err.Error())
	case errors.Is(err, store.ErrIDExhausted):
		s.writeError(w, http.StatusInternalServerError, "id_exhausted", err.Error())
	default:
		s.writeError(w, http.StatusInternalServerError, "admin_failed", err.Error())
	}
}
