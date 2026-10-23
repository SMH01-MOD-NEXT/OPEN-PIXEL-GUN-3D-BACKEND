// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"embed"
	"errors"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/exchange"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/pixelpass"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
	"io/fs"
	"net/http"
	"time"
)

//go:embed hub/* hub/assets/*
var hubFiles embed.FS

func (s *Server) registerHubRoutes() {
	root, _ := fs.Sub(hubFiles, "hub")
	files := http.FileServer(http.FS(root))
	s.mux.HandleFunc("GET /{$}", s.serveHubPage)
	s.mux.Handle("GET /hub/", http.StripPrefix("/hub/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hubHeaders(w)
		files.ServeHTTP(w, r)
	})))
	for _, route := range []string{"GET /api/v1/hub/stats", "GET /api/v1/hub/account", "GET /api/v1/hub/shop", "POST /api/v1/hub/exchange"} {
		s.mux.HandleFunc(route, s.handleHub)
	}
	for _, route := range []string{"GET /api/v1/hub/auth/settings", "POST /api/v1/hub/auth/mfa", "POST /api/v1/hub/auth/passkey/login/begin", "POST /api/v1/hub/auth/passkey/login/finish", "POST /api/v1/hub/auth/passkey/register/begin", "POST /api/v1/hub/auth/passkey/register/finish", "POST /api/v1/hub/auth/passkey/remove", "POST /api/v1/hub/auth/totp/begin", "POST /api/v1/hub/auth/totp/confirm", "POST /api/v1/hub/auth/totp/disable", "POST /api/v1/hub/auth/recovery/renew"} {
		s.mux.HandleFunc(route, s.handleHubAuth)
	}
}
func hubHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Permissions-Policy", "publickey-credentials-get=(self), publickey-credentials-create=(self)")
}
func (s *Server) serveHubPage(w http.ResponseWriter, r *http.Request) {
	if !s.webRequestAllowed(r) {
		s.writeError(w, 403, "web_login_unavailable", "")
		return
	}
	hubHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	b, _ := hubFiles.ReadFile("hub/index.html")
	w.Write(b)
}
func hubWallet(p store.Profile) map[string]any {
	wallet := map[string]any{"Coins": p.Coins, "GemsCurrency": p.Gems, "PixelPassCurrency": int64(0), "BattlePassCurrency": int64(0)}
	if raw, ok := p.ProgressSlots["3"].(map[string]any); ok {
		for k := range wallet {
			if v, ok := raw[k]; ok {
				wallet[k] = v
			}
		}
	}
	return map[string]any{"player_id": p.PlayerID, "nick": p.Nick, "wallet": wallet, "revision": p.ProgressRevision, "updated_at": p.UpdatedAt}
}
func (s *Server) handleHub(w http.ResponseWriter, r *http.Request) {
	hubHeaders(w)
	if !s.webRequestAllowed(r) {
		s.writeError(w, 403, "web_login_unavailable", "")
		return
	}
	if r.URL.Path == "/api/v1/hub/stats" {
		now := time.Now().UTC()
		midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		today := 0
		for _, a := range s.st.Accounts() {
			if !a.LastSeenAt.Before(midnight) && !a.LastSeenAt.After(now) {
				today++
			}
		}
		online := map[string]bool{}
		for _, session := range s.stockMapSessions() {
			session.mu.Lock()
			alive := !session.mapPresence.retired && now.Sub(session.lastSeen) <= stockMapPresenceLiveness
			session.mu.Unlock()
			if alive {
				if id, ok := s.socketPlayerID(session); ok {
					online[id] = true
				}
			}
		}
		s.writeJSON(w, 200, map[string]any{"online": len(online), "today": today, "day_utc": midnight.Format("2006-01-02"), "download_url": s.cfg.HubDownloadURL})
		return
	}
	// Serializes factor enrollment/revocation with use of web sessions.
	s.hubAuthMu.Lock()
	defer s.hubAuthMu.Unlock()
	login, ok := s.currentHubSession(r, r.Method == "POST", false)
	if !ok {
		s.writeError(w, 401, "login_required", "")
		return
	}
	if r.URL.Path == "/api/v1/hub/shop" {
		s.writeJSON(w, 200, map[string]any{"offers": exchange.Offers(), "catalog_version": store.ExchangeCatalogVersion(), "active": pixelpass.Active(time.Now())})
		return
	}
	if r.URL.Path == "/api/v1/hub/exchange" {
		if !s.webAuth.Limit("exchange:"+login.Identity.PlayerID, 60) {
			s.writeError(w, 429, "try_later", "")
			return
		}
		var body store.WebExchangeRequest
		if !readWebJSON(w, r, &body) {
			s.writeError(w, 400, "invalid_request", "")
			return
		}
		owner, err := s.st.Account(login.Identity.PlayerID)
		if err != nil || webIdentity(owner) != login.Identity {
			s.writeError(w, 401, "login_required", "")
			return
		}
		p, applied, err := s.st.WebExchangeOwned(owner, webClientIP(r), body)
		if err != nil {
			status, code := 400, "invalid_exchange"
			switch {
			case errors.Is(err, store.ErrInventoryConflict):
				status, code = 409, "wallet_changed"
			case errors.Is(err, store.ErrWebFunds):
				status, code = 422, "insufficient_funds"
			case errors.Is(err, store.ErrInventoryPersistence):
				status, code = 503, "try_later"
			}
			s.writeError(w, status, code, "")
			return
		}
		out := hubWallet(p)
		out["applied"] = applied
		out["csrf_token"] = login.CSRF
		s.writeJSON(w, 200, out)
		return
	}
	p, err := s.st.Profile(login.Identity.PlayerID)
	if err != nil {
		s.writeError(w, 404, "not_found", "")
		return
	}
	out := hubWallet(p)
	out["csrf_token"] = login.CSRF
	s.writeJSON(w, 200, out)
}
