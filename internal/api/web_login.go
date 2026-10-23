// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"embed"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/webauth"
)

//go:embed web_login/*
var webLoginFiles embed.FS
var webLoginText = loadWebLoginText()

func loadWebLoginText() map[string]map[string]string {
	b, err := webLoginFiles.ReadFile("web_login/text.json")
	if err != nil {
		panic(err)
	}
	var out map[string]map[string]string
	if err = json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}
func (s *Server) registerWebLoginPage() {
	for route, file := range map[string]string{"GET /account/login": "index.html", "GET /account/login.js": "login.js", "GET /account/text.json": "text.json"} {
		s.mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			if !s.webRequestAllowed(r) {
				s.writeError(w, 403, "web_login_unavailable", "")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			contentType := "text/html; charset=utf-8"
			if file == "login.js" {
				contentType = "text/javascript; charset=utf-8"
			}
			if file == "text.json" {
				contentType = "application/json; charset=utf-8"
			}
			w.Header().Set("Content-Type", contentType)
			b, _ := webLoginFiles.ReadFile("web_login/" + file)
			w.Write(b)
		})
	}
}
func (s *Server) personalMailboxIdentity(session *sioSession) (webauth.Identity, bool) {
	if s.webAuth == nil {
		return webauth.Identity{}, false
	}
	player, ok := s.socketPlayerID(session)
	if !ok {
		return webauth.Identity{}, false
	}
	// Game auth must have verified a native session or the stored device
	// binding; remembering a public player ID alone never grants personal mail.
	a, err := s.st.Account(player)
	if err != nil {
		return webauth.Identity{}, false
	}
	session.mu.Lock()
	fingerprint := session.mailboxOwnerFingerprint
	bootstrapFingerprint := session.bootstrapOwnerFingerprint
	session.mu.Unlock()
	if fingerprint == "" && bootstrapFingerprint != "" {
		bootstrap, _ := session.bootstrapIdentity()
		canonical, ok := s.st.ResolvePlayerID(bootstrap)
		if ok && canonical == player {
			fingerprint = bootstrapFingerprint
		}
	}
	if fingerprint == "" || fingerprint != webOwnerFingerprint(a) {
		return webauth.Identity{}, false
	}
	identity := webIdentity(a)
	_, ip := session.bootstrapIdentity()
	if !s.validWebIdentity(identity, ip) {
		return webauth.Identity{}, false
	}
	return identity, true
}
func (s *Server) personalMailboxLetters(session *sioSession) []webauth.Letter {
	identity, ok := s.personalMailboxIdentity(session)
	if !ok {
		return nil
	}
	session.mu.Lock()
	seen := make(map[int]bool, len(session.personalMailboxSeen))
	for id, v := range session.personalMailboxSeen {
		seen[id] = v
	}
	session.mu.Unlock()
	return s.webAuth.Letters(identity, seen)
}
func (s *Server) personalMailboxWire(session *sioSession, l webauth.Letter) map[string]any {
	identity, ok := s.personalMailboxIdentity(session)
	if !ok {
		return nil
	}
	header, short, body := map[string]string{}, map[string]string{}, map[string]string{}
	for lang, text := range webLoginText {
		title, preview, message := text["mail_title"], text["mail_short"], text["mail_body"]
		if !l.Active {
			title = text["mail_closed_title"]
			preview = title
			message = text["mail_closed_body"]
		}
		replace := strings.NewReplacer("{reference}", l.Reference, "{code}", l.Code, "{origin}", s.cfg.WebAuthOrigin)
		wireLanguage := map[string]string{"ru": "Russian", "en": "English"}[lang]
		header[wireLanguage] = replace.Replace(title)
		short[wireLanguage] = replace.Replace(preview)
		body[wireLanguage] = replace.Replace(message)
	}
	return map[string]any{"short_header": header, "header": header, "short_description": short, "description": body, "toId": identity.PlayerID, "type": "DeveloperMessage", "date": l.CreatedAt.Unix(), "ctime_change": l.Changed}
}
func (s *Server) personalMailboxMap(session *sioSession) map[string]any {
	body := map[string]any{}
	for _, n := range s.st.MailboxNews() {
		if n.Type == "DeveloperMessage" && n.VisibleAt(time.Now()) {
			body[strconv.Itoa(n.ID)] = n.Changed
		}
	}
	for _, l := range s.personalMailboxLetters(session) {
		body[strconv.Itoa(l.ID)] = l.Changed
	}
	return body
}
