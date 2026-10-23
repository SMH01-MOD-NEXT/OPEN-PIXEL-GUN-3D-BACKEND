// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/webauth"
)

func enableTestWeb(t *testing.T, s *Server) {
	t.Helper()
	s.cfg.WebAuthOrigin = "https://game.example"
	var e error
	s.webAuth, e = webauth.New()
	if e != nil {
		t.Fatal(e)
	}
}
func webRequest(t *testing.T, s *Server, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	method := "POST"
	if path == "me" {
		method = "GET"
	}
	r := httptest.NewRequest(method, "https://game.example/api/v1/web-auth/"+path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.10:5555"
	r.Header.Set("Origin", "https://game.example")
	r.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func namedCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("missing cookie %s", name)
	return nil
}
func TestWebAuthEndToEndAndMailboxIsolation(t *testing.T) {
	s, _ := newTestServer(t)
	enableTestWeb(t, s)
	seedSocketAccount(t, s, "123456789", "owner")
	seedSocketAccount(t, s, "987654321", "other")
	owner := authorisedSession("owner-sid", "123456789")
	owner.out = make(chan sioFrame, 64)
	other := authorisedSession("other-sid", "987654321")
	other.setBootstrapIdentity("987654321", "192.0.2.10")
	w := webRequest(t, s, "challenge", `{"player_id":"123456789"}`)
	if w.Code != 202 {
		t.Fatal(w.Code)
	}
	cookie := namedCookie(t, w, challengeCookie)
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatal("unsafe challenge cookie")
	}
	if len(s.personalMailboxLetters(owner)) != 0 {
		t.Fatal("unproven game identity received code")
	}
	owner.setBootstrapIdentity("987654321", "192.0.2.10")
	if len(s.personalMailboxLetters(owner)) != 0 {
		t.Fatal("bootstrap owner mismatch")
	}
	owner.setBootstrapIdentity("123456789", "192.0.2.10")
	a, _ := s.st.Account("123456789")
	owner.bootstrapOwnerFingerprint = webOwnerFingerprint(a)
	ls := s.personalMailboxLetters(owner)
	if len(ls) != 1 {
		t.Fatal("missing owner letter")
	}
	letter := ls[0]
	if len(s.personalMailboxLetters(other)) != 0 {
		t.Fatal("cross-account subscription")
	}
	head := sioHead{"ids": []any{float64(letter.ID)}}
	if len(s.answerMailboxMessages(other, head, 1)["news"].(map[string]any)) != 0 {
		t.Fatal("cross-account guessed ID")
	}
	wire := s.answerMailboxMessages(owner, head, 1)["news"].(map[string]any)[fmt.Sprint(letter.ID)].(map[string]any)
	for _, key := range []string{"items", "id_reward"} {
		if wire[key] != nil {
			t.Fatal("OTP has rewards")
		}
	}
	if wire["type"] != "DeveloperMessage" || wire["toId"] != "123456789" {
		t.Fatal("wrong personal schema")
	}
	desc := wire["description"].(map[string]string)
	if !strings.Contains(desc["Russian"], letter.Code) || !strings.Contains(desc["English"], letter.Code) {
		t.Fatal("stock language keys")
	}
	if strings.Contains(w.Body.String(), letter.Code) {
		t.Fatal("website leaked code")
	}
	if bad := webRequest(t, s, "verify", `{"code":"`+letter.Code+`"}`); bad.Code != 401 {
		t.Fatal("unbound browser accepted")
	}
	w = webRequest(t, s, "verify", `{"code":"`+letter.Code+`"}`, cookie)
	if w.Code != 200 {
		t.Fatal("verify", w.Code)
	}
	login := namedCookie(t, w, sessionCookie)
	if w = webRequest(t, s, "verify", `{"code":"`+letter.Code+`"}`, cookie); w.Code != 401 {
		t.Fatal("replayed code")
	}
	w = webRequest(t, s, "me", "", login)
	if w.Code != 200 {
		t.Fatal("session me", w.Code)
	}
	me := decode(t, w)
	csrf := me["csrf_token"].(string)
	if strings.Contains(w.Body.String(), "device") || strings.Contains(w.Body.String(), "admin") {
		t.Fatal("privileged account response")
	}
	if w = webRequest(t, s, "logout", `{}`, login); w.Code != 403 {
		t.Fatal("logout no CSRF")
	}
	r := httptest.NewRequest("POST", "https://game.example/api/v1/web-auth/logout", nil)
	r.Header.Set("Origin", s.cfg.WebAuthOrigin)
	r.Header.Set("X-CSRF-Token", csrf)
	r.AddCookie(login)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("logout", w.Code)
	}
	if w = webRequest(t, s, "me", "", login); w.Code != 401 {
		t.Fatal("revoked session")
	}
	if !s.replyEncrypted(owner, "/sio", "test", codecForPinnedSid(t), "mailbox_get_messages", 1, map[string]any{"news": wire}) {
		t.Fatal("encrypted mail delivery failed")
	}
	for _, entry := range s.log.Recent(0) {
		if strings.Contains(entry.Line, letter.Code) || strings.Contains(entry.Line, cookie.Value) || strings.Contains(entry.Line, login.Value) || strings.Contains(entry.Line, csrf) {
			t.Fatal("secret in trace")
		}
	}
	if len(s.st.MailboxNews()) != 0 {
		t.Fatal("OTP persisted as news")
	}
}
func TestWebAuthTransportOriginProxyAndValidation(t *testing.T) {
	s, _ := newTestServer(t)
	if w := webRequest(t, s, "challenge", `{"player_id":"123"}`); w.Code != 403 {
		t.Fatal("not disabled by default")
	}
	enableTestWeb(t, s)
	cases := []struct{ url, origin, remote, forwarded, proto, site string }{
		{"http://game.example/api/v1/web-auth/challenge", s.cfg.WebAuthOrigin, "192.0.2.10:5", "1.2.3.4", "https", ""},
		{"https://other.example/api/v1/web-auth/challenge", s.cfg.WebAuthOrigin, "192.0.2.10:5", "", "", ""},
		{"https://game.example/api/v1/web-auth/challenge", "https://evil.example", "192.0.2.10:5", "", "", ""},
		{"https://game.example/api/v1/web-auth/challenge", s.cfg.WebAuthOrigin, "192.0.2.10:5", "", "", "cross-site"},
		{"https://game.example/api/v1/web-auth/challenge?code=secret", s.cfg.WebAuthOrigin, "192.0.2.10:5", "", "", ""},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("POST", tc.url, strings.NewReader(`{"player_id":"123"}`))
		r.RemoteAddr = tc.remote
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Forwarded-For", tc.forwarded)
		r.Header.Set("X-Forwarded-Proto", tc.proto)
		r.Header.Set("Sec-Fetch-Site", tc.site)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("unsafe request", tc.url, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "http://game.example/api/v1/web-auth/challenge", strings.NewReader(`{"player_id":"123"}`))
	r.RemoteAddr = "127.0.0.1:5"
	r.Header.Set("Origin", s.cfg.WebAuthOrigin)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forwarded-For", "198.51.100.1")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatal("loopback TLS proxy", w.Code)
	}
	r.RemoteAddr = "192.0.2.10:5"
	if webClientIP(r) != "192.0.2.10" {
		t.Fatal("forged rate limit IP")
	}
	for _, body := range []string{`{}`, `{"player_id":"x"}`, `{"player_id":"123","admin":true}`, `{"player_id":"123"} {}`, strings.Repeat("1", 1025)} {
		if w := webRequest(t, s, "challenge", body); w.Code != 400 {
			t.Fatal("invalid input", w.Code)
		}
	}
	// Unknown accounts return the same accepted structure without generating mail.
	w = webRequest(t, s, "challenge", `{"player_id":"999"}`)
	if w.Code != 202 || len(s.personalMailboxMap(&sioSession{})) != 0 {
		t.Fatal("unknown account response")
	}
	page := do(t, s, "GET", "https://game.example/account/login", nil, nil)
	if page.Code != 200 || !strings.Contains(page.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("login page headers")
	}
}

func TestPersonalMailRequiresExistingDeviceOrSessionProof(t *testing.T) {
	s, _ := newTestServer(t)
	enableTestWeb(t, s)
	seedSocketAccount(t, s, "123456789", "owner-device")
	w := webRequest(t, s, "challenge", `{"player_id":"123456789"}`)
	if w.Code != 202 {
		t.Fatal(w.Code)
	}
	owner := &sioSession{sid: "owner", out: make(chan sioFrame, 64)}
	s.answerAuth(owner, sioHead{"uniq_id": "123456789", "device": "owner-device"}, 1)
	if len(s.personalMailboxLetters(owner)) != 1 {
		t.Fatal("stock matching device cannot receive OTP")
	}
	before, _ := s.st.Account("123456789")
	for _, device := range []string{"", "attacker-device"} {
		attacker := &sioSession{sid: "attacker", out: make(chan sioFrame, 64)}
		result := s.answerAuth(attacker, sioHead{"uniq_id": "123456789", "device": device}, 1)
		if result["err_code"] != sioErrNotAuthorized || len(s.personalMailboxLetters(attacker)) != 0 {
			t.Fatal("ID-only impersonation", device)
		}
	}
	after, _ := s.st.Account("123456789")
	if before.SocketDevice != after.SocketDevice {
		t.Fatal("device binding overwritten")
	}
	n, e := s.st.CreateMailboxNews(mailboxTestDraft())
	if e != nil {
		t.Fatal(e)
	}
	draft := mailboxTestDraft()
	draft.Type = "DeveloperMessage"
	gift, e := s.st.CreateMailboxNews(draft)
	if e != nil {
		t.Fatal(e)
	}
	manifest := s.personalMailboxMap(owner)
	if manifest[fmt.Sprint(gift.ID)] == nil || manifest[fmt.Sprint(n.ID)] != nil {
		t.Fatal("stock personal cleanup can erase a global DeveloperMessage")
	}
	codec := codecForPinnedSid(t)
	s.pushMailboxSubscription(owner, "/sio", "test", codec, sioHead{"type": mailboxPersonalUpdated})
	if owner.personalMailboxCodec == nil || len(owner.personalMailboxSeen) != 2 {
		t.Fatal("personal subscription not remembered")
	}
	c := namedCookie(t, w, challengeCookie)
	s.webAuth.Cancel(c.Value)
	s.refreshMailbox(owner)
	ls := s.personalMailboxLetters(owner)
	if len(ls) != 1 || ls[0].Active || ls[0].Code != "" {
		t.Fatal("cached code not cleared")
	}
	body := s.personalMailboxWire(owner, ls[0])
	if !strings.Contains(body["description"].(map[string]string)["Russian"], "больше не действует") {
		t.Fatal("missing neutral replacement")
	}
	fresh := &sioSession{sid: "fresh", out: make(chan sioFrame, 64)}
	s.answerAuth(fresh, sioHead{"uniq_id": "123456789", "device": "owner-device"}, 1)
	if len(s.personalMailboxLetters(fresh)) != 0 {
		t.Fatal("consumed code in fresh subscription")
	}
	// Rebinding via a real native session invalidates old sockets and web identities.
	s.st.AdoptSocketIdentityForAccount("123456789", "123456789", "replacement-device", "")
	if len(s.personalMailboxLetters(owner)) != 0 {
		t.Fatal("old device socket survived rebind")
	}
}

func TestWebAuthBansInvalidateDeliveryAndSessions(t *testing.T) {
	for _, kind := range []string{"player", "device", "ip"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := newTestServer(t)
			enableTestWeb(t, s)
			seedSocketAccount(t, s, "123456789", "owner-device")
			owner := &sioSession{sid: "owner", out: make(chan sioFrame, 64)}
			owner.setRemoteIP("192.0.2.10")
			s.answerAuth(owner, sioHead{"uniq_id": "123456789", "device": "owner-device"}, 1)
			w := webRequest(t, s, "challenge", `{"player_id":"123456789"}`)
			c := namedCookie(t, w, challengeCookie)
			code := s.personalMailboxLetters(owner)[0].Code
			w = webRequest(t, s, "verify", `{"code":"`+code+`"}`, c)
			if w.Code != 200 {
				t.Fatal(w.Code)
			}
			login := namedCookie(t, w, sessionCookie)
			subject := map[string]string{"player": "123456789", "device": "owner-device", "ip": "192.0.2.10"}[kind]
			if err := s.st.AddBan(store.Ban{Kind: kind, Subject: subject, Reason: "test"}); err != nil {
				t.Fatal(err)
			}
			if len(s.personalMailboxLetters(owner)) != 0 {
				t.Fatal("banned socket mail")
			}
			if w := webRequest(t, s, "me", "", login); w.Code != 401 {
				t.Fatal("banned web session")
			}
			if w := webRequest(t, s, "challenge", `{"player_id":"123456789"}`); w.Code != 202 {
				t.Fatal("banned existence oracle")
			}
		})
	}
}

func TestStaleNativeSocketCannotRebindReplacementOwner(t *testing.T) {
	s, _ := newTestServer(t)
	enableTestWeb(t, s)
	seedSocketAccount(t, s, "123456789", "owner-device")
	a, _ := s.st.Account("123456789")
	old := &sioSession{sid: "old", out: make(chan sioFrame, 64)}
	old.setBootstrapIdentity(a.PlayerID, "192.0.2.10")
	old.bootstrapOwnerFingerprint = webOwnerFingerprint(a)
	// Authorized device replacement must not be reversible with the stale socket.
	if _, err := s.st.AdoptSocketIdentityForAccount(a.PlayerID, a.PlayerID, "new-device", ""); err != nil {
		t.Fatal(err)
	}
	b := s.answerAuth(old, sioHead{"uniq_id": a.PlayerID, "device": "owner-device"}, 1)
	if b["err_code"] != sioErrNotAuthorized {
		t.Fatal("stale native proof accepted")
	}
	current, _ := s.st.Account(a.PlayerID)
	if current.SocketDevice != "new-device" {
		t.Fatal("new binding overwritten")
	}
	if _, err := s.st.AdoptSocketIdentityForAccount(a.PlayerID, a.PlayerID, "owner-device", "", a); err != store.ErrIdentityConflict {
		t.Fatal("owner CAS missing")
	}
}
