// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/webauth"
)

const challengeCookie = "__Host-opg3d_challenge"
const sessionCookie = "__Host-opg3d_session"

func (s *Server) registerWebAuthRoutes() {
	for _, route := range []string{"POST /api/v1/web-auth/challenge", "POST /api/v1/web-auth/verify", "POST /api/v1/web-auth/cancel", "GET /api/v1/web-auth/me", "POST /api/v1/web-auth/logout"} {
		s.mux.HandleFunc(route, s.handleWebAuth)
	}
	s.registerWebLoginPage()
}
func webIdentity(a store.Account) webauth.Identity {
	// A recreated/rebound/renamed account must not inherit an old web login.
	h := sha256.Sum256([]byte(a.PlayerID + "\x00" + webOwnerFingerprint(a)))
	return webauth.Identity{PlayerID: a.PlayerID, Fingerprint: hex.EncodeToString(h[:])}
}

// Stable across canonical-ID reconciliation, not account recreation/rebinding.
func webOwnerFingerprint(a store.Account) string {
	h := sha256.Sum256([]byte(a.DeviceID + "\x00" + a.SocketDevice + "\x00" + a.CreatedAt.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(h[:])
}
func (s *Server) validWebIdentity(identity webauth.Identity, ip string) bool {
	identity.Epoch = 0
	a, err := s.st.Account(identity.PlayerID)
	if err != nil || webIdentity(a) != identity {
		return false
	}
	if _, b := s.st.MatchBan(a.PlayerID, a.DeviceID, ip); b {
		return false
	}
	if _, b := s.st.MatchBan(a.PlayerID, a.SocketDevice, ip); b {
		return false
	}
	return true
}
func webPeer(r *http.Request) (string, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return host, ip != nil && ip.IsLoopback()
}

// SECURITY: forwarded headers are trusted ONLY from the loopback reverse proxy.
// Public requests cannot choose the rate-limit key or pretend to use HTTPS.
func webClientIP(r *http.Request) string {
	peer, local := webPeer(r)
	if !local {
		return peer
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		if ip := net.ParseIP(strings.TrimSpace(parts[i])); ip != nil && !ip.IsLoopback() {
			return ip.String()
		}
	}
	return peer
}
func (s *Server) webRequestAllowed(r *http.Request) bool {
	if s.webAuth == nil || s.cfg.WebAuthOrigin == "" || s.cfg.StableSessions {
		return false
	}
	origin, err := url.Parse(s.cfg.WebAuthOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host != r.Host {
		return false
	}
	_, local := webPeer(r)
	if r.TLS == nil && !(local && r.Header.Get("X-Forwarded-Proto") == "https") {
		return false
	}
	if r.URL.RawQuery != "" {
		if r.URL.Path != "/api/v1/hub/support/admin" && r.URL.Path != "/api/v1/hub/support" {
			return false
		}
		for k, v := range r.URL.Query() {
			if len(v) != 1 {
				return false
			}
			if k == "offset" {
				n, e := strconv.Atoi(v[0])
				if e != nil || n < 0 || n > 10000 {
					return false
				}
			} else if k == "status" {
				if v[0] != "" && v[0] != "open" && v[0] != "needs_info" && v[0] != "closed" && v[0] != "resolved" {
					return false
				}
			} else {
				return false
			}
		}
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if r.Method != "GET" && r.Header.Get("Origin") != s.cfg.WebAuthOrigin {
		return false
	}
	return true
}
func readWebJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	dec.DisallowUnknownFields()
	return dec.Decode(v) == nil && dec.Decode(&struct{}{}) == io.EOF
}
func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}
func setWebCookie(w http.ResponseWriter, name, value string, age int) {
	c := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: age}
	if age > 0 {
		c.Expires = time.Now().Add(time.Duration(age) * time.Second)
	}
	if age < 0 {
		c.Expires = time.Unix(1, 0)
	}
	http.SetCookie(w, c)
}
func (s *Server) handleWebAuth(w http.ResponseWriter, r *http.Request) {
	s.hubAuthMu.Lock()
	defer s.hubAuthMu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !s.webRequestAllowed(r) {
		s.writeError(w, 403, "web_login_unavailable", "")
		return
	}
	ip := webClientIP(r)
	valid := func(i webauth.Identity) bool { return s.validWebIdentity(i, ip) }
	switch r.URL.Path {
	case "/api/v1/web-auth/challenge":
		var body struct {
			PlayerID string `json:"player_id"`
		}
		if !readWebJSON(w, r, &body) || len(body.PlayerID) < 1 || len(body.PlayerID) > 32 || strings.IndexFunc(body.PlayerID, func(c rune) bool { return c < '0' || c > '9' }) >= 0 {
			s.writeError(w, 400, "invalid_request", "")
			return
		}
		identity := webauth.Identity{}
		accountKey := body.PlayerID
		if a, err := s.st.Account(body.PlayerID); err == nil {
			accountKey = a.PlayerID
			if valid(webIdentity(a)) {
				identity = webIdentity(a)
			}
		}
		ch, err := s.webAuth.Start(ip, accountKey, identity)
		if err != nil {
			w.Header().Set("Retry-After", "600")
			s.writeError(w, 429, "try_later", "")
			return
		}
		// Replacing this browser's challenge does not revoke other browsers' requests.
		s.webAuth.Cancel(cookieValue(r, challengeCookie))
		setWebCookie(w, challengeCookie, ch.Token, int(webauth.ChallengeTTL.Seconds()))
		s.writeJSON(w, 202, map[string]any{"reference": ch.Reference, "expires_in": int(webauth.ChallengeTTL.Seconds())})
	case "/api/v1/web-auth/verify":
		var body struct {
			Code string `json:"code"`
		}
		if !readWebJSON(w, r, &body) {
			s.writeError(w, 400, "invalid_request", "")
			return
		}
		login, err := s.webAuth.Verify(ip, cookieValue(r, challengeCookie), body.Code, valid)
		if err != nil {
			status := 401
			if err == webauth.ErrLimited {
				status = 429
			}
			s.writeError(w, status, "invalid_or_expired_code", "")
			return
		}
		if s.stageHubMFA(w, r, login) {
			return
		}
		s.webAuth.Revoke(login.Token)
		login, err = s.webAuth.Issue(s.sessionIdentity(login.Identity))
		if err != nil {
			s.writeError(w, 503, "security_unavailable", "")
			return
		}
		// Do not retain a previous session on a successful account switch.
		if prior, e := s.webAuth.Current(cookieValue(r, sessionCookie), valid); e == nil {
			s.webAuth.Logout(cookieValue(r, sessionCookie), prior.CSRF)
		}
		setWebCookie(w, challengeCookie, "", -1)
		setWebCookie(w, sessionCookie, login.Token, int(webauth.SessionTTL.Seconds()))
		s.writeJSON(w, 200, map[string]any{"player_id": login.Identity.PlayerID, "csrf_token": login.CSRF, "scopes": []string{"hub", "exchange", "bug_reports"}, "expires_at": login.ExpiresAt})
	case "/api/v1/web-auth/me":
		login, err := s.webAuth.Current(cookieValue(r, sessionCookie), func(i webauth.Identity) bool { return s.validHubSessionIdentity(i, ip) })
		if err != nil {
			setWebCookie(w, sessionCookie, "", -1)
			s.writeError(w, 401, "not_authenticated", "")
			return
		}
		a, _ := s.st.Account(login.Identity.PlayerID)
		s.writeJSON(w, 200, map[string]any{"player_id": a.PlayerID, "nick": a.Nick, "csrf_token": login.CSRF, "scopes": []string{"hub", "exchange", "bug_reports"}, "expires_at": login.ExpiresAt})
	case "/api/v1/web-auth/cancel":
		s.webAuth.Cancel(cookieValue(r, challengeCookie))
		s.dropHubFlow(r)
		setWebCookie(w, hubFlowCookie, "", -1)
		setWebCookie(w, challengeCookie, "", -1)
		s.writeJSON(w, 200, map[string]bool{"ok": true})
	case "/api/v1/web-auth/logout":
		if !s.webAuth.Logout(cookieValue(r, sessionCookie), r.Header.Get("X-CSRF-Token")) {
			s.writeError(w, 403, "invalid_csrf", "")
			return
		}
		setWebCookie(w, sessionCookie, "", -1)
		s.dropHubFlow(r)
		setWebCookie(w, hubFlowCookie, "", -1)
		s.writeJSON(w, 200, map[string]bool{"ok": true})
	}
}
