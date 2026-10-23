// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"errors"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/support"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const supportCookie = "__Host-opg3d_support"

func (s *Server) registerSupportRoutes() {
	for _, route := range []string{"GET /api/v1/hub/support", "POST /api/v1/hub/support/tickets", "POST /api/v1/hub/support/unlock", "POST /api/v1/hub/support/forget", "GET /api/v1/hub/support/tickets/{id}", "POST /api/v1/hub/support/tickets/{id}", "GET /api/v1/hub/support/admin", "GET /api/v1/hub/support/admin/{id}", "POST /api/v1/hub/support/admin/{id}"} {
		s.mux.HandleFunc(route, s.handleSupport)
	}
	for _, route := range []string{"GET /api/v1/admin/support", "GET /api/v1/admin/support/{id}", "POST /api/v1/admin/support/{id}", "PUT /api/v1/admin/players/{id}/support-role"} {
		s.mux.HandleFunc(route, s.admin(s.handleOperatorSupport))
	}
}
func readSupportJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	return d.Decode(out) == nil && d.Decode(&struct{}{}) == io.EOF
}
func (s *Server) supportError(w http.ResponseWriter, e error) {
	status, code := 503, "support_unavailable"
	switch {
	case errors.Is(e, support.ErrDenied):
		status, code = 404, "ticket_not_found"
	case errors.Is(e, support.ErrConflict):
		status, code = 409, "ticket_changed"
	case errors.Is(e, support.ErrInvalid):
		status, code = 400, "invalid_request"
	case errors.Is(e, support.ErrLimit):
		status, code = 429, "try_later"
	}
	s.writeError(w, status, code, "")
}
func (s *Server) supportActor(r *http.Request) support.Actor {
	a := support.Actor{Browser: cookieValue(r, supportCookie)}
	if login, ok := s.currentHubSession(r, false, false); ok {
		a.PlayerID = login.Identity.PlayerID
		a.Fingerprint = login.Identity.Fingerprint
	}
	return a
}
func (s *Server) handleSupport(w http.ResponseWriter, r *http.Request) {
	hubHeaders(w)
	if !s.webRequestAllowed(r) {
		s.writeError(w, 403, "web_login_unavailable", "")
		return
	}
	s.hubAuthMu.Lock()
	defer s.hubAuthMu.Unlock()
	if s.support == nil {
		s.writeError(w, 503, "support_unavailable", "")
		return
	}
	if !s.webAuth.Limit("support-read:"+webClientIP(r), 300) {
		s.writeError(w, 429, "try_later", "")
		return
	}
	a := s.supportActor(r)
	if r.URL.Path == "/api/v1/hub/support" {
		raw, csrf, e := s.support.Browser(a.Browser)
		if e != nil {
			s.supportError(w, e)
			return
		}
		a.Browser = raw
		setWebCookie(w, supportCookie, raw, int(support.TTL.Seconds()))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		rows, total, e := s.support.List(a, false, offset, "")
		if e != nil {
			s.supportError(w, e)
			return
		}
		s.writeJSON(w, 200, map[string]any{"tickets": rows, "total": total, "support_csrf": csrf, "support_admin": s.support.Admin(a), "logged_in": a.PlayerID != ""})
		return
	}
	if r.Method != "GET" {
		if !s.support.CSRF(a.Browser, r.Header.Get("X-Support-CSRF")) {
			s.writeError(w, 403, "invalid_support_csrf", "")
			return
		}
		if !s.webAuth.Limit("support-write:"+webClientIP(r), 60) {
			s.writeError(w, 429, "try_later", "")
			return
		}
	}
	if r.URL.Path == "/api/v1/hub/support/forget" {
		if e := s.support.Forget(a.Browser); e != nil {
			s.supportError(w, e)
			return
		}
		setWebCookie(w, supportCookie, "", -1)
		s.writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.URL.Path == "/api/v1/hub/support/unlock" {
		var b struct {
			ID   string `json:"id"`
			Code string `json:"code"`
		}
		if !readSupportJSON(w, r, &b) || len(b.ID) != 32 || len(b.Code) > 80 {
			s.writeError(w, 400, "invalid_request", "")
			return
		}
		if !s.webAuth.Limit("support-unlock:"+webClientIP(r), 10) {
			s.writeError(w, 429, "try_later", "")
			return
		}
		if e := s.support.Unlock(a, b.ID, b.Code); e != nil {
			s.supportError(w, e)
			return
		}
		s.writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.URL.Path == "/api/v1/hub/support/tickets" {
		var b support.Create
		if !readSupportJSON(w, r, &b) {
			s.writeError(w, 400, "invalid_request", "")
			return
		}
		if a.PlayerID == "" && b.Kind != "recovery" {
			s.writeError(w, 401, "login_required", "")
			return
		}
		if !s.webAuth.Limit("support-create:"+webClientIP(r), 10) {
			s.writeError(w, 429, "try_later", "")
			return
		}
		t, code, e := s.support.Create(a, b, webClientIP(r))
		if e != nil {
			s.supportError(w, e)
			return
		}
		s.log.Event("support.created", map[string]any{"ticket_id": t.ID, "kind": t.Kind, "actor": a.PlayerID})
		out := map[string]any{"ticket": t}
		if t.OwnerID == "" {
			out["access_code"] = code
		}
		s.writeJSON(w, 201, out)
		return
	}
	admin := strings.HasPrefix(r.URL.Path, "/api/v1/hub/support/admin")
	if admin && !s.support.Admin(a) {
		s.writeError(w, 403, "support_admin_required", "")
		return
	}
	s.serveSupportData(w, r, a, admin)
}
func (s *Server) serveSupportData(w http.ResponseWriter, r *http.Request, a support.Actor, admin bool) {
	id := r.PathValue("id")
	if id == "" {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		rows, total, e := s.support.List(a, admin, offset, r.URL.Query().Get("status"))
		if e != nil {
			s.supportError(w, e)
			return
		}
		s.writeJSON(w, 200, map[string]any{"tickets": rows, "total": total})
		return
	}
	if len(id) != 32 {
		s.writeError(w, 404, "ticket_not_found", "")
		return
	}
	if r.Method == "GET" {
		t, e := s.support.Get(id, a, admin)
		if e != nil {
			s.supportError(w, e)
			return
		}
		out := map[string]any{"ticket": t}
		if admin {
			out["audit"] = t.Audit
			out["submission_ip"] = t.IP
			target := t.TargetID
			if target == "" {
				target = t.OwnerID
			}
			if owner, e := s.st.Account(target); e == nil {
				out["player_evidence"] = s.buildSupportEvidence(owner)
			}
		}
		s.writeJSON(w, 200, out)
		return
	}
	var u support.Update
	if !readSupportJSON(w, r, &u) {
		s.writeError(w, 400, "invalid_request", "")
		return
	}
	t, e := s.support.Update(id, a, admin, u)
	if e != nil {
		s.supportError(w, e)
		return
	}
	s.log.Event("support.updated", map[string]any{"ticket_id": id, "status": t.Status, "admin": admin, "actor": a.PlayerID})
	s.writeJSON(w, 200, map[string]any{"ticket": t})
}
func (s *Server) handleOperatorSupport(w http.ResponseWriter, r *http.Request) {
	if s.support == nil {
		s.writeError(w, 503, "support_unavailable", "")
		return
	}
	if strings.HasSuffix(r.URL.Path, "/support-role") {
		var b struct {
			Enabled bool `json:"enabled"`
		}
		if !readSupportJSON(w, r, &b) {
			s.writeError(w, 400, "invalid_request", "")
			return
		}
		owner, e := s.st.Account(r.PathValue("id"))
		if e != nil {
			s.writeError(w, 404, "not_found", "")
			return
		}
		if e = s.support.SetAdmin(owner.PlayerID, webIdentity(owner).Fingerprint, b.Enabled, "operator"); e != nil {
			s.supportError(w, e)
			return
		}
		s.log.Event("support.role_changed", map[string]any{"player_id": owner.PlayerID, "enabled": b.Enabled})
		s.writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	s.serveSupportData(w, r, support.Actor{Operator: true}, true)
}
