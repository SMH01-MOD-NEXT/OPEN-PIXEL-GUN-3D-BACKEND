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

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/clienterrors"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/logs"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

// registerAdminRoutes mounts the operator API and console.
func (s *Server) registerAdminRoutes() {
	s.mux.HandleFunc("GET /api/v1/admin/mailbox/news", s.admin(s.handleAdminMailboxNews))
	s.mux.HandleFunc("POST /api/v1/admin/mailbox/news", s.admin(s.handleAdminMailboxNews))
	s.mux.HandleFunc("POST /api/v1/admin/players/{id}/inventory/bulk/{mode}", s.admin(s.handleAdminInventoryBulk))
	s.mux.HandleFunc("GET /api/v1/admin/catalog", s.admin(s.handleAdminCatalog))
	s.mux.HandleFunc("GET /api/v1/admin/players/{id}/inventory", s.admin(s.handleAdminInventory))
	s.mux.HandleFunc("POST /api/v1/admin/players/{id}/inventory", s.admin(s.handleAdminEditInventory))
	s.mux.HandleFunc("GET /api/v1/admin/client-errors", s.admin(s.handleAdminClientErrors))
	s.mux.HandleFunc("GET /api/v1/admin/stats", s.admin(s.handleAdminStats))
	s.mux.HandleFunc("GET /api/v1/admin/players", s.admin(s.handleAdminPlayers))
	s.mux.HandleFunc("GET /api/v1/admin/players/{id}", s.admin(s.handleAdminPlayer))
	s.mux.HandleFunc("PUT /api/v1/admin/players/{id}/profile", s.admin(s.handleAdminSaveProfile))
	s.mux.HandleFunc("PUT /api/v1/admin/players/{id}/state", s.admin(s.handleAdminSaveRawState))
	s.mux.HandleFunc("POST /api/v1/admin/players/{id}/ban", s.admin(s.handleAdminBanPlayer))
	s.mux.HandleFunc("POST /api/v1/admin/players/{id}/unban", s.admin(s.handleAdminUnbanPlayer))
	s.mux.HandleFunc("DELETE /api/v1/admin/players/{id}", s.admin(s.handleAdminDeletePlayer))
	s.mux.HandleFunc("POST /api/v1/admin/change-id", s.admin(s.handleAdminChangeID))
	s.mux.HandleFunc("POST /api/v1/admin/transfer", s.admin(s.handleAdminTransfer))
	s.mux.HandleFunc("POST /api/v1/admin/reconcile", s.admin(s.handleAdminReconcile))
	s.mux.HandleFunc("GET /api/v1/admin/settings", s.admin(s.handleAdminSettings))
	s.mux.HandleFunc("PATCH /api/v1/admin/settings", s.admin(s.handleAdminSaveSettings))
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
	PlayerID     string         `json:"player_id"`
	DeviceID     string         `json:"device_id"`
	SocketDevice string         `json:"socket_device,omitempty"`
	Nick         string         `json:"nick"`
	CreatedAt    time.Time      `json:"created_at"`
	LastSeenAt   time.Time      `json:"last_seen_at"`
	LastIP       string         `json:"last_ip"`
	Aliases      []string       `json:"aliases"`
	Profile      *store.Profile `json:"profile,omitempty"`
	Banned       bool           `json:"banned"`
	BanKind      string         `json:"ban_kind,omitempty"`
	BanReason    string         `json:"ban_reason,omitempty"`
	BanUntil     *time.Time     `json:"ban_until,omitempty"`
}

func (s *Server) buildAdminPlayer(account store.Account) adminPlayer {
	row := adminPlayer{
		PlayerID: account.PlayerID, DeviceID: account.DeviceID, SocketDevice: account.SocketDevice,
		Nick: account.Nick, CreatedAt: account.CreatedAt, LastSeenAt: account.LastSeenAt,
		LastIP: account.LastIP, Aliases: s.st.Aliases(account.PlayerID),
	}
	if profile, err := s.st.Profile(account.PlayerID); err == nil {
		row.Profile = &profile
	}
	if ban, banned := s.accountBan(account, account.LastIP); banned {
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
		"client_errors": s.clientErrors.Query(clienterrors.Query{}, time.Now()).Summary,
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
		row := s.buildAdminPlayer(account)
		if query != "" && !adminMatches(row, query) {
			continue
		}
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

func adminMatches(account adminPlayer, query string) bool {
	fields := []string{
		account.PlayerID,
		account.Nick,
		account.DeviceID,
		account.SocketDevice,
		account.LastIP,
	}
	fields = append(fields, account.Aliases...)
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
	Nick               string `json:"nick"`
	Level              int    `json:"level"`
	Experience         int64  `json:"experience"`
	Coins              int64  `json:"coins"`
	Gems               int64  `json:"gems"`
	PixelPassCurrency  *int64 `json:"pixel_pass_currency"`
	BattlePassCurrency *int64 `json:"battle_pass_currency"`
}

func (s *Server) handleAdminSaveProfile(w http.ResponseWriter, r *http.Request) {
	var req adminProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if req.Level < 0 || req.Experience < 0 || req.Coins < 0 || req.Gems < 0 {
		s.writeError(w, http.StatusBadRequest, "invalid_profile", "level, experience, coins and gems cannot be negative")
		return
	}
	passCurrencies := store.PassCurrencyEdit{PixelPass: req.PixelPassCurrency, BattlePass: req.BattlePassCurrency}
	if !passCurrencies.Valid() {
		s.writeError(w, http.StatusBadRequest, "invalid_profile", "pass currency balances must be integers between 0 and 2147483647")
		return
	}
	id := r.PathValue("id")
	before, _ := s.st.Profile(id)
	profile, err := s.st.SetProfile(id, req.Nick, req.Level, req.Experience, req.Coins, req.Gems, passCurrencies)
	if err != nil {
		s.writeAdminError(w, err)
		return
	}
	s.log.Event("admin.profile_saved", map[string]any{
		"player_id":            id,
		"nick":                 req.Nick,
		"level":                req.Level,
		"coins":                req.Coins,
		"gems":                 req.Gems,
		"pixel_pass_currency":  profile.ProgressSlots["3"].(map[string]any)["PixelPassCurrency"],
		"battle_pass_currency": profile.ProgressSlots["3"].(map[string]any)["BattlePassCurrency"],
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"profile": profile})
	if profile.ProgressRevision != before.ProgressRevision {
		s.reloginOnlinePlayerAfterProgressEdit(id, profile.ProgressRevision)
	}
}

type adminRawStateRequest struct {
	Prefs            map[string]string `json:"prefs"`
	Progress         map[string]any    `json:"progress"`
	ProgressDocument map[string]any    `json:"progress_document"`
	ProgressSlots    map[string]any    `json:"progress_slots"`
	ClientState      map[string]any    `json:"client_state"`
}

func decodeStrictAdminJSON(body io.Reader, dst any) error {
	decoder := json.NewDecoder(body)
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("only one JSON document is allowed")
		}
		return err
	}
	return nil
}

func (s *Server) handleAdminSaveRawState(w http.ResponseWriter, r *http.Request) {
	var req adminRawStateRequest
	if err := decodeStrictAdminJSON(r.Body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if req.Prefs == nil || req.Progress == nil || req.ProgressDocument == nil || req.ProgressSlots == nil || req.ClientState == nil {
		s.writeError(w, http.StatusBadRequest, "incomplete_state", "prefs, progress, progress_document, progress_slots and client_state must all be JSON objects")
		return
	}
	id := r.PathValue("id")
	before, _ := s.st.Profile(id)
	profile, err := s.st.ReplaceProfileRawState(id, req.Prefs, req.Progress, req.ProgressDocument, req.ProgressSlots, req.ClientState)
	if err != nil {
		s.writeAdminError(w, err)
		return
	}
	s.log.Event("admin.profile_state_saved", map[string]any{
		"player_id": id,
		"prefs":     len(req.Prefs), "progress": len(req.Progress),
		"progress_document": len(req.ProgressDocument), "progress_slots": len(req.ProgressSlots),
		"client_state": len(req.ClientState),
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"profile": profile})
	if profile.ProgressRevision != before.ProgressRevision {
		s.reloginOnlinePlayerAfterProgressEdit(id, profile.ProgressRevision)
	}
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

	until, err := banDeadline(req.Minutes)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_duration", err.Error())
		return
	}
	subjects := playerBanSubjects(account)
	includeDevice := req.IncludeDevice == nil || *req.IncludeDevice
	if !includeDevice {
		subjects = subjects[:1]
	}
	for i := range subjects {
		subjects[i].Reason = req.Reason
		subjects[i].Until = until
	}
	s.banMu.Lock()
	defer s.banMu.Unlock()
	if err := s.st.AddBans(subjects); err != nil {
		s.writeBanMutationError(w, err)
		return
	}
	s.enforceOnlineBans()

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

// handleAdminUnbanPlayer lifts player and both device bans atomically.
// Shared IP bans remain explicit and are reported via still_banned.
func (s *Server) handleAdminUnbanPlayer(w http.ResponseWriter, r *http.Request) {
	account, err := s.st.Account(r.PathValue("id"))
	if err != nil {
		s.writeAdminError(w, err)
		return
	}

	s.banMu.Lock()
	defer s.banMu.Unlock()
	removed, err := s.st.RemoveBanSubjects(playerBanSubjects(account))
	if err != nil {
		s.writeBanMutationError(w, err)
		return
	}
	_, stillBanned := s.accountBan(account, account.LastIP)

	s.log.Event("admin.player_unbanned", map[string]any{
		"player_id": account.PlayerID,
		"nick":      account.Nick,
		"removed":   removed,
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed, "still_banned": stillBanned})
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
	s.reloginOnlinePlayerAfterProgressEdit(req.ToID, profile.ProgressRevision)
	if source, err := s.st.Profile(req.FromID); err == nil {
		s.reloginOnlinePlayerAfterProgressEdit(req.FromID, source.ProgressRevision)
	}
}

type adminReconcileRequest struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
}

// handleAdminReconcile combines a known duplicate native account into the game
// account while retaining the source id as an alias. The store refuses the
// operation if both profiles contain payload, so this support action cannot
// silently choose which progress to discard.
func (s *Server) handleAdminReconcile(w http.ResponseWriter, r *http.Request) {
	var req adminReconcileRequest
	if err := decodeStrictAdminJSON(r.Body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	result, err := s.st.ReconcileAccounts(req.SourceID, req.TargetID)
	if err != nil {
		s.writeAdminError(w, err)
		return
	}
	s.log.Event("admin.accounts_reconciled", map[string]any{
		"source_id": req.SourceID, "target_id": result.Account.PlayerID, "alias": result.AliasAdded,
		"note": "the source login remains valid through its canonical alias",
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"player": s.buildAdminPlayer(result.Account)})
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
	flusher, ok := findResponseFlusher(w)
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

type responseWriterUnwrapper interface {
	Unwrap() http.ResponseWriter
}

// findResponseFlusher preserves streaming through middleware wrappers. The
// top-level trace recorder deliberately captures writes, but flushing belongs
// to the underlying network writer.
func findResponseFlusher(w http.ResponseWriter) (http.Flusher, bool) {
	for w != nil {
		if flusher, ok := w.(http.Flusher); ok {
			return flusher, true
		}
		unwrapper, ok := w.(responseWriterUnwrapper)
		if !ok {
			return nil, false
		}
		next := unwrapper.Unwrap()
		if next == w {
			return nil, false
		}
		w = next
	}
	return nil, false
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
	case errors.Is(err, store.ErrIdentityConflict):
		s.writeError(w, http.StatusConflict, "identity_conflict", "both accounts contain progress; transfer or clear one profile before reconciling")
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
