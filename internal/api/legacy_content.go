// SPDX-License-Identifier: GPL-3.0-or-later

package api

// The 23.1.3 client blocks its loading screen on three retired services:
// analytics events, gameplay statistics, and static remote config JSON. A 404
// is worse than a wrong answer there, because the client waits out its own HTTP
// timeout before continuing, which reads as a frozen progress bar. These
// handlers answer immediately, record what was asked for, and serve remote
// config from an editable directory on disk.

import (
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// contentPrefix is the remote-config namespace the client asks for.
const contentPrefix = "/pixelgun3d-config/"

// contentFallbacks holds the shape to serve while a config file has not been
// authored yet. An empty JSON array means "this list is currently empty", which
// every list consumer in the client can parse; anything else defaults to an
// empty JSON object.
var contentFallbacks = map[string]string{
	"rating-maps/rating-map-all.json": "[]",
	"rating-maps/rating-map-top.json": "[]",
}

const contentFallbackObject = "{}"

// handleLegacyEvent is the analytics sink at /events/add_event.php. The client
// reports its loading milestones here, which makes this log the most precise
// map of how far a boot got.
func (s *Server) handleLegacyEvent(w http.ResponseWriter, r *http.Request) {
	form := s.form(r)
	query := r.URL.Query()
	backendPlayer := query.Get("opg3d_player")
	clientPlayer := form.Get("player_id")
	clientDevice := form.Get("device_id")
	if backendPlayer != "" && clientPlayer != "" && backendPlayer != clientPlayer {
		result, err := s.st.ReconcileAuthenticatedIdentity(
			backendPlayer, query.Get("opg3d_session"), clientPlayer, clientDevice, clientIP(r),
		)
		if err != nil {
			s.log.Event("identity.legacy_reconciliation_rejected", map[string]any{
				"bootstrap_player": backendPlayer, "game_player": clientPlayer, "err": err.Error(),
			})
		} else if result.Changed {
			s.log.Event("identity.legacy_reconciled", map[string]any{
				"canonical_player": result.Account.PlayerID, "retired_alias": result.AliasAdded,
			})
			backendPlayer = result.Account.PlayerID
		}
	}
	s.log.Event("client.event", map[string]any{
		"ip":             clientIP(r),
		"event_id":       form.Get("event_id"),
		"progress":       form.Get("progress"),
		"version":        form.Get("version"),
		"device_model":   form.Get("device_model"),
		"client_player":  clientPlayer,
		"client_device":  clientDevice,
		"backend_player": backendPlayer,
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"result": "ok", "status": "ok"})
}

// handleLegacyStat is the gameplay/diagnostic statistics sink. The payload is
// large and already captured verbatim by the request trace, so only its size
// and owner are summarised here.
func (s *Server) handleLegacyStat(w http.ResponseWriter, r *http.Request) {
	form := s.form(r)
	s.log.Event("client.stat", map[string]any{
		"ip":             clientIP(r),
		"path":           r.URL.Path,
		"client_player":  firstNonEmpty(form.Get("muid"), form.Get("uid")),
		"backend_player": r.URL.Query().Get("opg3d_player"),
		"events":         len(form.Get("events")),
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"result": "ok", "status": "ok"})
}

// handleRemoteConfig serves static remote config from the content directory.
// Missing files are answered with an empty document instead of 404 so the
// client keeps loading, and every fallback is logged as a to-do.
func (s *Server) handleRemoteConfig(w http.ResponseWriter, r *http.Request) {
	rel, ok := contentRelPath(r.URL.Path)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "bad_content_path", r.URL.Path)
		return
	}

	full := filepath.Join(s.cfg.ContentDir, contentPrefix[1:len(contentPrefix)-1], filepath.FromSlash(rel))
	raw, err := os.ReadFile(full)
	switch {
	case err == nil:
		s.log.Event("content.served", map[string]any{"path": rel, "file": full, "bytes": len(raw)})
		s.writeRaw(w, rel, raw)
		return
	case errors.Is(err, fs.ErrNotExist):
		// Expected until the file is authored; fall through to the fallback.
	default:
		s.log.Event("content.read_failed", map[string]any{"path": rel, "file": full, "err": err.Error()})
	}

	body, ok := contentFallbacks[rel]
	if !ok {
		body = contentFallbackObject
	}
	s.log.Event("content.fallback_served", map[string]any{
		"path": rel,
		"hint": "create " + full + " to serve real content",
	})
	s.writeRaw(w, rel, []byte(body))
}

// contentRelPath validates and normalises a remote-config request path. It
// refuses anything that escapes the content directory.
func contentRelPath(requested string) (string, bool) {
	rest, ok := strings.CutPrefix(requested, contentPrefix)
	if !ok {
		return "", false
	}
	clean := path.Clean("/" + rest)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." || strings.HasPrefix(clean, "..") {
		return "", false
	}
	return clean, true
}

func (s *Server) writeRaw(w http.ResponseWriter, rel string, raw []byte) {
	contentType := "application/octet-stream"
	if strings.HasSuffix(rel, ".json") {
		contentType = "application/json; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}

// form parses a urlencoded body without disturbing the request trace, which has
// already buffered and replayed it.
func (s *Server) form(r *http.Request) url.Values {
	raw, err := readAll(r)
	if err != nil {
		s.log.Event("http.read_failed", map[string]any{"path": r.URL.Path, "err": err.Error()})
		return url.Values{}
	}
	values, err := url.ParseQuery(string(raw))
	if err != nil {
		return url.Values{}
	}
	return values
}
