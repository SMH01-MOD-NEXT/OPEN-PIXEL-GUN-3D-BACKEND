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
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/clienterrors"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/config"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/hubsecurity"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/logs"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/secrets"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/webauth"
	wa "github.com/go-webauthn/webauthn/webauthn"
)

// maxRequestBody caps what the server will read and buffer per request.
const maxRequestBody = 1 << 20

// Server implements http.Handler.
type Server struct {
	// Serializes ban commits against encrypted gameplay dispatch.
	clientErrors           *clienterrors.Store
	diagnosticsCredentials *clienterrors.Credentials
	banMu                  sync.RWMutex
	cfg                    *config.Config
	log                    *logs.Logger
	st                     *store.Store
	mux                    *http.ServeMux
	// sio holds the live socket.io sessions of the config channel, which
	// outlive the individual requests that carry them.
	sio *sioHub

	// settings holds the values an operator may change while the server is
	// running. Handlers read them through a snapshot instead of reading the
	// environment seed directly.
	settings *settings

	// configNonce salts hosted document fingerprints when ConfigHashNonce is
	// on. It is fixed for the life of the process, so the poll answer and the
	// push state the same fingerprint for the same document.
	configNonce string
	webAuth     *webauth.Service
	hubSecurity *hubsecurity.Store
	hubWA       *wa.WebAuthn
	hubAuthMu   sync.Mutex
	hubMu       sync.Mutex
	hubFlows    map[string]*hubFlow
}

// New builds the router.
func New(cfg *config.Config, log *logs.Logger, st *store.Store) *Server {
	s := &Server{cfg: cfg, log: log, st: st, mux: http.NewServeMux(), sio: newSioHub(cfg.StableSessions, newSioCodec(cfg, log)), settings: newSettings(cfg)}
	s.clientErrors, _ = clienterrors.Open(cfg.DataDir)
	s.diagnosticsCredentials, _ = clienterrors.OpenCredentials(cfg.DataDir)
	if s.clientErrors.Query(clienterrors.Query{}, time.Now()).Summary.Unavailable {
		log.Event("client.diagnostics_unavailable", nil)
	}
	s.configNonce = newConfigNonce()
	if err := s.settings.load(); err != nil {
		// A settings file that cannot be read must not stop the server: the
		// environment seed is still a valid configuration to serve from.
		log.Event("settings.load_failed", map[string]any{"error": err.Error()})
	}
	if cfg.WebAuthOrigin != "" && !cfg.StableSessions {
		s.webAuth, _ = webauth.New()
		if s.webAuth == nil {
			log.Event("web_auth.unavailable", nil)
		}
	}
	s.initHub()
	s.routes()
	return s
}

func (s *Server) routes() {
	s.registerWebAuthRoutes()
	s.registerHubRoutes()
	// Native-only launcher protocol; no credentials or profile writes.
	s.mux.HandleFunc("GET /api/v1/loader/info", s.handleLoaderInfo)
	s.mux.HandleFunc("GET /api/v1/frontend/manifest", s.handleFrontendManifest)
	s.mux.HandleFunc("GET /api/v1/frontend/{hash}/library.so", s.handleFrontendBinary)
	s.mux.HandleFunc("POST /api/v1/client-errors", s.handleClientErrors)
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
	s.registerAdminRoutes()

	// Legacy client compatibility.
	s.mux.HandleFunc("/auth_v2/", s.handleLegacyAuth)
	s.mux.HandleFunc("/auth_v2", s.handleLegacyAuth)
	s.mux.HandleFunc("/time", s.handleTime)
	s.mux.HandleFunc("/problem", s.handleProblem)
	s.mux.HandleFunc("POST /events/add_event.php", s.handleLegacyEvent)
	s.mux.HandleFunc("POST /event_stat_add_pl.php", s.handleLegacyStat)
	s.mux.HandleFunc("POST /log_stat_event.php", s.handleLegacyStat)
	s.mux.HandleFunc("GET "+contentPrefix, s.handleRemoteConfig)
	s.mux.HandleFunc("GET "+bundlePrefix, s.handleBundle)
	s.mux.HandleFunc(sioPath, s.handleSocketIO)
	s.mux.HandleFunc("/", s.handleUnknown)
}

// recorder captures the status and body of a response for the trace log.
type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
	// skipBody keeps a response out of the trace buffer. Set it for payloads
	// that are large and opaque, such as asset bundles.
	skipBody   bool
	redactBody bool
	written    int
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
// Unwrap lets capability-aware handlers reach optional interfaces such as
// http.Flusher on the real network writer without bypassing trace capture.
func (r *recorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

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
	if !r.skipBody {
		r.body.Write(p)
	}
	n, err := r.ResponseWriter.Write(p)
	r.written += n
	return n, err
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rid, err := secrets.RandomToken(6)
	if err != nil {
		rid = "norid"
	}

	rawPath := r.URL.Path
	r.URL.Path = normalizePath(rawPath)
	// Asset bundle downloads are the one rewritten family whose original host
	// decides the route, so they are mapped onto the internal /bundles/
	// namespace instead of being flattened into the shared path space.
	if mapped, ok := bundleRequestPath(rawPath); ok {
		r.URL.Path = mapped
	}

	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
		r.Body = io.NopCloser(bytes.NewReader(body))
	}

	redactBody := redactHTTPBodyTrace(r)
	requestBodyTrace := s.log.Body(body)
	if redactBody && len(body) > 0 {
		requestBodyTrace = redactedHTTPBody(len(body))
	}
	s.log.Event("http.request", map[string]any{
		"rid":      rid,
		"method":   r.Method,
		"path":     r.URL.Path,
		"raw_path": rawPath,
		"query":    redactedRawQuery(r),
		"ip":       clientIP(r),
		"ua":       r.UserAgent(),
		"type":     r.Header.Get("Content-Type"),
		"bytes":    len(body),
		"body":     requestBodyTrace,
	})

	rec := &recorder{ResponseWriter: w, skipBody: redactBody, redactBody: redactBody}
	s.mux.ServeHTTP(rec, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}

	responseBodyTrace := s.log.Body(rec.body.Bytes())
	if rec.redactBody && rec.written > 0 {
		responseBodyTrace = redactedHTTPBody(rec.written)
	}
	s.log.Event("http.response", map[string]any{
		"rid":    rid,
		"status": rec.status,
		"ms":     time.Since(start).Milliseconds(),
		"bytes":  rec.written,
		"body":   responseBodyTrace,
	})
}

// redactHTTPBodyTrace keeps support-only state and credentials out of the
// durable/live trace. The response is still sent in full to the authenticated
// operator; only the diagnostic copy is omitted.
func redactHTTPBodyTrace(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/v1/hub/") || strings.HasPrefix(r.URL.Path, "/hub/") || strings.HasPrefix(r.URL.Path, "/api/v1/web-auth/") || strings.HasPrefix(r.URL.Path, "/account/") || strings.HasPrefix(r.URL.Path, "/api/v1/admin/") || r.URL.Path == "/api/v1/client-errors"
}

func redactedHTTPBody(size int) string {
	return fmt.Sprintf("<%d bytes; authenticated admin body redacted>", size)
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

// discardTraceBody stops the recorder from buffering the response body of the
// current request. Megabytes of bundle bytes in the trace log would cost memory
// and tell a reader nothing the served name and size do not already say.
func discardTraceBody(w http.ResponseWriter) {
	if rec, ok := w.(*recorder); ok {
		rec.skipBody = true
	}
}

// redactedRawQuery keeps routable query context in diagnostics without writing
// reusable account credentials to disk.
func redactedRawQuery(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/api/v1/hub/") || strings.HasPrefix(r.URL.Path, "/api/v1/web-auth/") || strings.HasPrefix(r.URL.Path, "/account/") {
		return "<redacted>"
	}
	query := r.URL.Query()
	for _, key := range []string{"opg3d_session", "session", "token", "access_token", "socket_ticket"} {
		if _, present := query[key]; present {
			query.Set(key, "<redacted>")
		}
	}
	return query.Encode()
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
