// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/clienterrors"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

func (s *Server) diagnostic(player, level, code, message string, revision uint64) {
	_, err := s.clientErrors.Add(player, "backend", []clienterrors.Event{{Level: level, Code: code, Message: message, Revision: revision}}, time.Now())
	if err != nil {
		s.log.Event("client.diagnostic_store_failed", map[string]any{"code": code})
	}
	// Only sanitized, fixed-size diagnostics are mirrored into the ordinary log.
	s.log.Event("client.diagnostic", map[string]any{"player_id": player, "level": level, "code": code, "message": clienterrors.Clean(message, 1024), "revision": revision})
}
func (s *Server) handleClientErrors(w http.ResponseWriter, r *http.Request) {
	s.banMu.RLock()
	defer s.banMu.RUnlock()
	player := r.Header.Get("X-OPG3D-Player")
	var account store.Account
	if token := r.Header.Get("X-OPG3D-Diagnostics"); token != "" {
		if !s.diagnosticsCredentials.Valid(token, player, time.Now()) {
			s.writeError(w, 401, "bad_diagnostics_credential", "restart client to renew diagnostics credential")
			return
		}
		var err error
		account, err = s.st.Account(player)
		if err != nil {
			s.writeError(w, 401, "bad_diagnostics_credential", "unknown account")
			return
		}
		if ban, banned := s.accountBan(account, clientIP(r)); banned {
			s.writeBan(w, ban)
			return
		}
	} else {
		var ok bool
		account, ok = s.authenticate(w, r, player, bearer(r))
		if !ok {
			return
		}
	}
	var batch struct {
		Events []clienterrors.Event `json:"events"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&batch); err != nil {
		s.writeError(w, 400, "invalid_diagnostics", "invalid diagnostic batch")
		return
	}
	if dec.Decode(&struct{}{}) != io.EOF || len(batch.Events) == 0 || len(batch.Events) > 10 {
		s.writeError(w, 400, "invalid_diagnostics", "expected 1..10 events")
		return
	}
	for _, e := range batch.Events {
		allowed := map[string]string{"unity_error": "error", "unity_exception": "error", "unity_assert": "error", "resync_started": "info", "resync_failed": "error", "resync_lobby_ready": "info"}
		level, ok := allowed[e.Code]
		if !ok || e.Level != level || len(e.ID) < 1 || len(e.ID) > 80 || strings.TrimSpace(e.Message) == "" || len(e.Message) > 4096 || len(e.Stack) > 8192 || len(e.Version) > 80 || len(e.Module) > 160 || e.Source != "" || e.Player != "" || !e.At.IsZero() || e.Fingerprint != "" {
			s.writeError(w, 400, "invalid_diagnostics", "invalid diagnostic event")
			return
		}
	}
	n, err := s.clientErrors.Add(account.PlayerID, "client", batch.Events, time.Now())
	if errors.Is(err, clienterrors.ErrRate) {
		w.Header().Set("Retry-After", "60")
		s.writeError(w, 429, "rate_limited", "diagnostic rate limit")
		return
	}
	if err != nil {
		s.writeError(w, 503, "diagnostics_unavailable", "diagnostic storage unavailable")
		return
	}
	s.writeJSON(w, http.StatusAccepted, map[string]any{"accepted": n})
}
func (s *Server) handleAdminClientErrors(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	result := s.clientErrors.Query(clienterrors.Query{Text: q.Get("q"), Player: q.Get("player"), Level: q.Get("level"), Source: q.Get("source"), Offset: offset, Limit: limit}, time.Now())
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, 200, result)
}
