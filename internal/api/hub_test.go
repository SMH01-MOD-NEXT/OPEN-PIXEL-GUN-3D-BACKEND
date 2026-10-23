// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"bytes"
	"encoding/json"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/hubsecurity"
	"github.com/pquerna/otp/totp"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hubTest(t *testing.T) *Server {
	s, cfg := newTestServer(t)
	enableTestWeb(t, s)
	cfg.DataDir = t.TempDir()
	cfg.HubSecurityKey = bytes.Repeat([]byte{1}, 32)
	s.initHub()
	if s.hubWA == nil || s.hubSecurity == nil {
		t.Fatal("hub initialization")
	}
	return s
}
func hubCall(t *testing.T, s *Server, method, path string, data any, csrf string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(data)
	r := httptest.NewRequest(method, "https://game.example"+path, bytes.NewReader(b))
	r.RemoteAddr = "192.0.2.10:5555"
	r.Header.Set("Origin", "https://game.example")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", csrf)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestHubOTPThenMFAAndSecurityRevocation(t *testing.T) {
	s := hubTest(t)
	seedSocketAccount(t, s, "123456789", "owner")
	a, _ := s.st.Account("123456789")
	identity := webIdentity(a)
	s.hubSecurity.Ensure(identity.PlayerID, identity.Fingerprint)
	secret, _, _ := hubsecurity.NewTOTP(identity.PlayerID)
	code, _ := totp.GenerateCode(secret, time.Now())
	recovery, e := s.hubSecurity.Enable(identity.PlayerID, identity.Fingerprint, secret, code)
	if e != nil {
		t.Fatal(e)
	}
	ch, e := s.webAuth.Start("192.0.2.10", a.PlayerID, identity)
	if e != nil {
		t.Fatal(e)
	}
	letters := s.webAuth.Letters(identity, nil)
	if len(letters) != 1 {
		t.Fatal("letter")
	}
	w := webRequest(t, s, "verify", `{"code":"`+letters[0].Code+`"}`, &http.Cookie{Name: challengeCookie, Value: ch.Token})
	if w.Code != 202 || !strings.Contains(w.Body.String(), "mfa_required") {
		t.Fatal(w.Code, w.Body.String())
	}
	flow := namedCookie(t, w, hubFlowCookie)
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Fatal("full cookie before MFA")
		}
	}
	if hubCall(t, s, "GET", "/api/v1/hub/account", nil, "", flow).Code != 401 {
		t.Fatal("MFA bypass")
	}
	w = hubCall(t, s, "POST", "/api/v1/hub/auth/mfa", map[string]string{"code": recovery[0]}, "", flow)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	session := namedCookie(t, w, sessionCookie)
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	json.Unmarshal(w.Body.Bytes(), &login)
	if hubCall(t, s, "GET", "/api/v1/hub/account", nil, "", session).Code != 200 {
		t.Fatal("missing full session")
	}
	if hubCall(t, s, "POST", "/api/v1/hub/auth/totp/disable", map[string]string{"code": recovery[1]}, "wrong", session).Code != 403 {
		t.Fatal("CSRF")
	}
	w = hubCall(t, s, "POST", "/api/v1/hub/auth/totp/disable", map[string]string{"code": recovery[1]}, login.CSRF, session)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if hubCall(t, s, "GET", "/api/v1/hub/account", nil, "", session).Code != 401 {
		t.Fatal("old session survived settings change")
	}
	if hubCall(t, s, "POST", "/api/v1/hub/auth/mfa", map[string]string{"code": recovery[0]}, "", flow).Code != 401 {
		t.Fatal("flow replay")
	}
}
func TestHubFailClosedAndPublicPrivacy(t *testing.T) {
	s := hubTest(t)
	for _, path := range []string{"/", "/account/login", "/hub/app.js", "/hub/text.json", "/hub/assets/coins.png", "/api/v1/hub/stats"} {
		w := hubCall(t, s, "GET", path, nil, "")
		if w.Code != 200 {
			t.Fatal(path, w.Code)
		}
		if strings.Contains(w.Body.String(), "device_id") {
			t.Fatal("private data leak")
		}
	}
	if hubCall(t, s, "GET", "/api/v1/hub/shop", nil, "").Code != 401 {
		t.Fatal("private shop")
	}
	path := filepath.Join(s.cfg.DataDir, "hub-security.enc")
	os.WriteFile(path, []byte("corrupt"), 0600)
	s.initHub()
	if s.webAuth != nil {
		t.Fatal("corrupt factor store bypass")
	}
	enableTestWeb(t, s)
	s.cfg.HubSecurityKey = nil
	s.initHub()
	if s.webAuth != nil {
		t.Fatal("missing key bypass")
	}
}
func TestHubPasskeyChallengeOneUseAndUserVerification(t *testing.T) {
	s := hubTest(t)
	w := hubCall(t, s, "POST", "/api/v1/hub/auth/passkey/login/begin", map[string]any{}, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"userVerification":"required"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	flow := namedCookie(t, w, hubFlowCookie)
	for n := 0; n < 2; n++ {
		w = hubCall(t, s, "POST", "/api/v1/hub/auth/passkey/login/finish", map[string]any{}, "", flow)
		if w.Code != 401 {
			t.Fatal("invalid or replayed passkey", w.Code)
		}
	}
}

func TestHubTOTPEnrollmentFreshnessAndSecretIsolation(t *testing.T) {
	s := hubTest(t)
	seedSocketAccount(t, s, "123456789", "owner")
	a, _ := s.st.Account("123456789")
	i := webIdentity(a)
	login, _ := s.webAuth.Issue(i)
	cookie := &http.Cookie{Name: sessionCookie, Value: login.Token}
	old, _ := s.webAuth.IssueAt(i, time.Now().Add(-6*time.Minute))
	if hubCall(t, s, "POST", "/api/v1/hub/auth/totp/begin", map[string]any{}, old.CSRF, &http.Cookie{Name: sessionCookie, Value: old.Token}).Code != 403 {
		t.Fatal("fresh auth bypass")
	}
	w := hubCall(t, s, "POST", "/api/v1/hub/auth/totp/begin", map[string]any{}, login.CSRF, cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	flow := namedCookie(t, w, hubFlowCookie)
	var setup struct{ Secret, QR string }
	json.Unmarshal(w.Body.Bytes(), &setup)
	if !strings.HasPrefix(setup.QR, "data:image/png;base64,") || setup.Secret == "" {
		t.Fatal("enrollment response")
	}
	code, _ := totp.GenerateCode(setup.Secret, time.Now())
	w = hubCall(t, s, "POST", "/api/v1/hub/auth/totp/confirm", map[string]string{"code": code}, old.CSRF, &http.Cookie{Name: sessionCookie, Value: old.Token}, flow)
	if w.Code != 403 {
		t.Fatal("flow/session binding")
	}
	w = hubCall(t, s, "POST", "/api/v1/hub/auth/totp/confirm", map[string]string{"code": code}, login.CSRF, cookie, flow)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	updated := namedCookie(t, w, sessionCookie)
	w = hubCall(t, s, "GET", "/api/v1/hub/auth/settings", nil, "", updated)
	if w.Code != 200 || strings.Contains(w.Body.String(), setup.Secret) || strings.Contains(w.Body.String(), "recovery_codes") {
		t.Fatal("secret leak")
	}
	if hubCall(t, s, "GET", "/api/v1/hub/account", nil, "", cookie).Code != 401 {
		t.Fatal("old credential survived enrollment")
	}
}
func TestHubOnlineIsUniqueLiveGameSocketsNotWebSessions(t *testing.T) {
	s := hubTest(t)
	a := stockTestSession(t, s, "123456701")
	stockTestSession(t, s, "123456701")
	b := stockTestSession(t, s, "123456702")
	b.mu.Lock()
	b.lastSeen = time.Now().Add(-61 * time.Second)
	b.mu.Unlock()
	owner, _ := s.st.Account("123456701")
	s.webAuth.Issue(webIdentity(owner))
	w := hubCall(t, s, "GET", "/api/v1/hub/stats", nil, "")
	var result struct{ Online, Today int }
	json.Unmarshal(w.Body.Bytes(), &result)
	if w.Code != 200 || result.Online != 1 || result.Today != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	a.mu.Lock()
	a.lastSeen = time.Now().Add(-61 * time.Second)
	a.mu.Unlock()
}

func TestHubHeroArtworkHasNoCaption(t *testing.T) {
	s := hubTest(t)
	for _, path := range []string{"/", "/account/login"} {
		w := hubCall(t, s, "GET", path, nil, "")
		html := w.Body.String()
		if w.Code != 200 || !strings.Contains(html, `src="/hub/assets/hero.png"`) {
			t.Fatal("hero image missing", path)
		}
		if strings.Contains(html, `class="image-caption"`) || strings.Contains(html, `data-t="world"`) || strings.Contains(html, "OPEN PIXEL GUN 3D") {
			t.Fatal("unexpected hero caption", path)
		}
	}
}
