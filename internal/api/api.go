// SPDX-License-Identifier: GPL-3.0-or-later

// Package api is the HTTP surface of the backend.
//
// Two route families are served on the same socket:
//
//   - /api/v1/...  the real protocol, owned by this project;
//   - legacy paths (/auth_v2/, /time, /problem) so the 23.1.3 client can be
//     pointed here by URL rewriting alone, before the client-side cleanup is
//     finished.
//
// Every request and response is logged with its body, because the whole reason
// this server exists is to see what the game sent, what it received, and what
// it expected when it fell over.
package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/config"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/logs"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/secrets"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

// maxRequestBody caps what the server will read and buffer per request.
const maxRequestBody = 1 << 20

// Server implements http.Handler.
type Server struct {
	cfg *config.Config
	log *logs.Logger
	st  *store.Store
	mux *http.ServeMux
	// sio holds the live socket.io sessions of the config channel, which
	// outlive the individual requests that carry them.
	sio *sioHub
}

// New builds the router.
func New(cfg *config.Config, log *logs.Logger, st *store.Store) *Server {
	s := &Server{cfg: cfg, log: log, st: st, mux: http.NewServeMux(), sio: newSioHub(cfg.StableSessions)}
	s.routes()
	return s
}

func (s *Server) routes() {
	// Real protocol.
	s.mux.HandleFunc("GET /api/v1/ping", s.handlePing)
	s.mux.HandleFunc("GET /api/v1/time", s.handleTime)
	s.mux.HandleFunc("GET /api/v1/config", s.handleConfig)
	s.mux.HandleFunc("POST /api/v1/accounts/register", s.handleRegister)
	s.mux.HandleFunc("POST /api/v1/sessions/refresh", s.handleRefresh)
	s.mux.HandleFunc("GET /api/v1/photon/appid", s.handlePhotonAppID)
	s.mux.HandleFunc("GET /api/v1/update", s.handleUpdate)
	s.mux.HandleFunc("POST /api/v1/problem", s.handleProblem)

	// Administration. Guarded by OPG3D_ADMIN_TOKEN; disabled when unset.
	s.mux.HandleFunc("GET /api/v1/admin/accounts", s.admin(s.handleAdminAccounts))
	s.mux.HandleFunc("GET /api/v1/admin/bans", s.admin(s.handleAdminListBans))
	s.mux.HandleFunc("POST /api/v1/admin/bans", s.admin(s.handleAdminAddBan))
	s.mux.HandleFunc("DELETE /api/v1/admin/bans", s.admin(s.handleAdminRemoveBan))

	// Legacy client compatibility.
	s.mux.HandleFunc("/auth_v2/", s.handleLegacyAuth)
	s.mux.HandleFunc("/auth_v2", s.handleLegacyAuth)
	s.mux.HandleFunc("/time", s.handleTime)
	s.mux.HandleFunc("/problem", s.handleProblem)
	s.mux.HandleFunc("POST /events/add_event.php", s.handleLegacyEvent)
	s.mux.HandleFunc("POST /event_stat_add_pl.php", s.handleLegacyStat)
	s.mux.HandleFunc("POST /log_stat_event.php", s.handleLegacyStat)
	s.mux.HandleFunc("GET "+contentPrefix, s.handleRemoteConfig)
	s.mux.HandleFunc(sioPath, s.handleSocketIO)
	s.mux.HandleFunc("/", s.handleUnknown)
}

// recorder captures the status and body of a response for the trace log.
type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

// Hijack forwards the takeover the WebSocket upgrade of the config channel
// needs. Without it the trace wrapper would hide net/http's own Hijacker and
// no socket could ever be upgraded.
func (r *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("the underlying response writer cannot be hijacked")
	}
	if r.status == 0 {
		r.status = http.StatusSwitchingProtocols
	}
	return hijacker.Hijack()
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body.Write(p)
	return r.ResponseWriter.Write(p)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rid, err := secrets.RandomToken(6)
	if err != nil {
		rid = "norid"
	}

	rawPath := r.URL.Path
	r.URL.Path = normalizePath(rawPath)

	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
		r.Body = io.NopCloser(bytes.NewReader(body))
	}

	s.log.Event("http.request", map[string]any{
		"rid":      rid,
		"method":   r.Method,
		"path":     r.URL.Path,
		"raw_path": rawPath,
		"query":    r.URL.RawQuery,
		"ip":       clientIP(r),
		"ua":       r.UserAgent(),
		"type":     r.Header.Get("Content-Type"),
		"bytes":    len(body),
		"body":     s.log.Body(body),
	})

	rec := &recorder{ResponseWriter: w}
	s.mux.ServeHTTP(rec, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}

	s.log.Event("http.response", map[string]any{
		"rid":    rid,
		"status": rec.status,
		"ms":     time.Since(start).Milliseconds(),
		"bytes":  rec.body.Len(),
		"body":   s.log.Body(rec.body.Bytes()),
	})
}

// normalizePath strips the "/@original.host" prefix used by the client-side URL
// rewriter, so one socket can serve traffic that used to hit several hosts.
func normalizePath(path string) string {
	if !strings.HasPrefix(path, "/@") {
		return path
	}
	if idx := strings.Index(path[1:], "/"); idx >= 0 {
		return path[idx+1:]
	}
	return "/"
}

func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first, _, ok := strings.Cut(forwarded, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(forwarded)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		s.log.Event("http.encode_failed", map[string]any{"err": err.Error()})
		http.Error(w, `{"error":"encode"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(raw)
}

func (s *Server) writeError(w http.ResponseWriter, status int, code, detail string) {
	s.writeJSON(w, status, map[string]any{"error": code, "detail": detail})
}
