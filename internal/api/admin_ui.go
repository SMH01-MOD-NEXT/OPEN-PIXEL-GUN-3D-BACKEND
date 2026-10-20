// SPDX-License-Identifier: GPL-3.0-or-later

package api

// The operator console: one self-contained page served by this server.
//
// It has no external dependencies on purpose. This backend runs on a LAN with
// no route to the internet, so a page that pulled a framework from a CDN would
// be a blank screen exactly when it is needed.

import "net/http"

// handleAdminConsole serves the console page.
//
// The page itself is deliberately not behind the admin token: it has to load
// before an operator can type one. It ships no data of its own, only markup and
// script, and every request it makes carries the token in an Authorization
// header. The token lives in sessionStorage rather than localStorage so closing
// the tab ends the session, and it is never placed in a URL, where it would be
// captured by this server's own request log.
func (s *Server) handleAdminConsole(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// Everything is inline and no external origin is contacted, so the policy
	// can be this tight.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(inventoryConsoleHTML()))
}
