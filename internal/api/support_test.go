// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/support"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func supportCall(t *testing.T, s *Server, method, path string, data any, csrf string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(data)
	r := httptest.NewRequest(method, "https://game.example"+path, bytes.NewReader(b))
	r.RemoteAddr = "192.0.2.10:1234"
	r.Header.Set("Origin", "https://game.example")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Support-CSRF", csrf)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func supportInit(t *testing.T, s *Server, cookies ...*http.Cookie) (*http.Cookie, string) {
	t.Helper()
	w := supportCall(t, s, "GET", "/api/v1/hub/support", nil, "", cookies...)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var d struct {
		CSRF string `json:"support_csrf"`
	}
	json.Unmarshal(w.Body.Bytes(), &d)
	c := namedCookie(t, w, supportCookie)
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.MaxAge != 7776000 {
		t.Fatal("guest cookie")
	}
	return c, d.CSRF
}
func TestSupportHTTPGuestOwnershipAdminAndRestart(t *testing.T) {
	s := hubTest(t)
	seedSocketAccount(t, s, "123456789", "owner-private-sentinel")
	seedSocketAccount(t, s, "987654321", "staff-private-sentinel")
	guest, csrf := supportInit(t, s)
	body := support.Create{Kind: "recovery", TargetID: "123456789", Subject: "Help recover", Answers: map[string]string{"old_device": "unknown", "changed_device": "no"}, Operation: fmt.Sprintf("%032x", 1)}
	if w := supportCall(t, s, "POST", "/api/v1/hub/support/tickets", body, "wrong", guest); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	w := supportCall(t, s, "POST", "/api/v1/hub/support/tickets", body, csrf, guest)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var d struct {
		Ticket support.Ticket `json:"ticket"`
		Code   string         `json:"access_code"`
	}
	json.Unmarshal(w.Body.Bytes(), &d)
	if d.Code == "" || d.Ticket.OwnerID != "" {
		t.Fatal("claim became login")
	}
	id := d.Ticket.ID
	if supportCall(t, s, "GET", "/api/v1/hub/account", nil, "", guest).Code != 401 {
		t.Fatal("account recovery capability bypass")
	}
	other, otherCSRF := supportInit(t, s)
	path := "/api/v1/hub/support/tickets/" + id
	if supportCall(t, s, "GET", path, nil, "", other).Code != 404 {
		t.Fatal("IDOR")
	}
	if supportCall(t, s, "POST", "/api/v1/hub/support/unlock", map[string]string{"id": id, "code": d.Code}, otherCSRF, other).Code != 200 {
		t.Fatal("unlock")
	}
	if supportCall(t, s, "GET", "/api/v1/hub/support/admin", nil, "", guest).Code != 403 {
		t.Fatal("anonymous admin")
	}
	if supportCall(t, s, "GET", "/api/v1/hub/support?secret=x", nil, "", guest).Code != 403 {
		t.Fatal("secret query")
	}
	if supportCall(t, s, "GET", "/api/v1/hub/support?offset=30", nil, "", guest).Code != 200 {
		t.Fatal("pagination")
	}
	staff, _ := s.st.Account("987654321")
	login, _ := s.webAuth.Issue(s.sessionIdentity(webIdentity(staff)))
	staffCookie := &http.Cookie{Name: sessionCookie, Value: login.Token}
	staffGuest, staffCSRF := supportInit(t, s, staffCookie)
	if supportCall(t, s, "GET", "/api/v1/hub/support/admin", nil, "", staffCookie, staffGuest).Code != 403 {
		t.Fatal("unprivileged")
	}
	role := "/api/v1/admin/players/987654321/support-role"
	if do(t, s, "PUT", role, map[string]bool{"enabled": true}, nil).Code != 401 {
		t.Fatal("public role")
	}
	if w := do(t, s, "PUT", role, map[string]bool{"enabled": true}, map[string]string{"Authorization": "Bearer admin-secret", "Content-Type": "application/json"}); w.Code != 200 {
		t.Fatal("operator role", w.Code, w.Body.String())
	}
	w = supportCall(t, s, "GET", "/api/v1/hub/support/admin/"+id, nil, "", staffCookie, staffGuest)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "player_evidence") {
		t.Fatal("staff evidence", w.Code)
	}
	owner, _ := s.st.Account("123456789")
	if !strings.Contains(w.Body.String(), owner.DeviceID) {
		t.Fatal("missing private evidence")
	}
	u := support.Update{Status: "needs_info", Text: "Which device?", Revision: 1, Operation: fmt.Sprintf("%032x", 2)}
	w = supportCall(t, s, "POST", "/api/v1/hub/support/admin/"+id, u, staffCSRF, staffCookie, staffGuest)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.initHub()
	w = supportCall(t, s, "GET", path, nil, "", other)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "needs_info") || strings.Contains(w.Body.String(), owner.DeviceID) || strings.Contains(w.Body.String(), "submission_ip") || strings.Contains(w.Body.String(), d.Code) {
		t.Fatal("restart or evidence leak", w.Code)
	}
	if supportCall(t, s, "GET", "/api/v1/hub/support/admin/"+id, nil, "", staffCookie, staffGuest).Code != 200 {
		t.Fatal("persisted role/login")
	}
	do(t, s, "PUT", role, map[string]bool{"enabled": false}, map[string]string{"Authorization": "Bearer admin-secret", "Content-Type": "application/json"})
	if supportCall(t, s, "GET", "/api/v1/hub/support/admin/"+id, nil, "", staffCookie, staffGuest).Code != 403 {
		t.Fatal("role revocation")
	}
	bug := body
	bug.Kind = "bug"
	bug.Answers = nil
	bug.Details = "Something broke"
	if supportCall(t, s, "POST", "/api/v1/hub/support/tickets", bug, csrf, guest).Code != 401 {
		t.Fatal("guest bug")
	}
}
func TestHubDurableLogoutAndFreshAuth(t *testing.T) {
	s := hubTest(t)
	seedSocketAccount(t, s, "123456789", "owner")
	a, _ := s.st.Account("123456789")
	issued, _ := s.webAuth.IssueAt(s.sessionIdentity(webIdentity(a)), time.Now().Add(-6*time.Minute))
	cookie := &http.Cookie{Name: sessionCookie, Value: issued.Token}
	s.initHub()
	if hubCall(t, s, "GET", "/api/v1/hub/account", nil, "", cookie).Code != 200 {
		t.Fatal("old login invalidated")
	}
	if w := hubCall(t, s, "POST", "/api/v1/hub/auth/totp/begin", map[string]string{}, issued.CSRF, cookie); w.Code != 403 {
		t.Fatal("fresh login bypass", w.Code)
	}
	w := hubCall(t, s, "POST", "/api/v1/web-auth/logout", map[string]string{}, issued.CSRF, cookie)
	if w.Code != 200 {
		t.Fatal("logout", w.Code, w.Body.String())
	}
	s.initHub()
	if hubCall(t, s, "GET", "/api/v1/hub/account", nil, "", cookie).Code != 401 {
		t.Fatal("logout lost on restart")
	}
}

func TestHubCookieNavigationRestartEpochAndMissingKey(t *testing.T) {
	s := hubTest(t)
	seedSocketAccount(t, s, "123456789", "owner")
	a, _ := s.st.Account("123456789")
	i := webIdentity(a)
	ch, _ := s.webAuth.Start("192.0.2.10", a.PlayerID, i)
	letters := s.webAuth.Letters(i, nil)
	w := webRequest(t, s, "verify", `{"code":"`+letters[0].Code+`"}`, &http.Cookie{Name: challengeCookie, Value: ch.Token})
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	cookie := namedCookie(t, w, sessionCookie)
	if cookie.MaxAge != 7776000 || cookie.Expires.Before(time.Now().Add(89*24*time.Hour)) || !cookie.HttpOnly || !cookie.Secure {
		t.Fatal("90 day cookie")
	}
	for n := 0; n < 2; n++ {
		for _, path := range []string{"/api/v1/hub/account", "/api/v1/hub/shop", "/", "/api/v1/hub/account"} {
			if hubCall(t, s, "GET", path, nil, "", cookie).Code != 200 {
				t.Fatal("navigation", path)
			}
		}
		s.initHub()
	}
	// Security epoch invalidates durable sessions even if a process died before session revocation.
	s.hubSecurity.Ensure(i.PlayerID, i.Fingerprint)
	if e := s.support.SetAdmin(i.PlayerID, i.Fingerprint, true, "operator"); e != nil {
		t.Fatal(e)
	}
	// A durable session must match the persisted security epoch, not just the account.
	old := s.sessionIdentity(i)
	old.Epoch++
	token, _ := s.webAuth.Issue(old)
	s.initHub()
	if hubCall(t, s, "GET", "/api/v1/hub/account", nil, "", &http.Cookie{Name: sessionCookie, Value: token.Token}).Code != 401 {
		t.Fatal("epoch bypass")
	}
	for _, name := range []string{"web-sessions.enc", "support.enc"} {
		t.Run(name, func(t *testing.T) {
			server := hubTest(t)
			os.WriteFile(filepath.Join(server.cfg.DataDir, name), []byte("exists"), 0600)
			server.cfg.HubSecurityKey = nil
			server.initHub()
			if server.webAuth != nil {
				t.Fatal("missing key bypass")
			}
		})
	}
}

func TestSupportHTTPOwnerClose(t *testing.T) {
	s := hubTest(t)
	guest, csrf := supportInit(t, s)
	w := supportCall(t, s, "POST", "/api/v1/hub/support/tickets", support.Create{Kind: "recovery", TargetID: "123456789", Subject: "Recover account", Answers: map[string]string{"old_device": "unknown", "changed_device": "no"}, Operation: fmt.Sprintf("%032x", 1)}, csrf, guest)
	if w.Code != 201 {
		t.Fatal(w.Code)
	}
	var d struct {
		Ticket support.Ticket `json:"ticket"`
	}
	json.Unmarshal(w.Body.Bytes(), &d)
	path := "/api/v1/hub/support/tickets/" + d.Ticket.ID
	request := support.Update{Status: "closed", Revision: 1, Operation: fmt.Sprintf("%032x", 2)}
	if w = supportCall(t, s, "POST", path, request, "wrong", guest); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	other, otherCSRF := supportInit(t, s)
	if w = supportCall(t, s, "POST", path, request, otherCSRF, other); w.Code != 404 {
		t.Fatal("foreign", w.Code)
	}
	for i := 0; i < 2; i++ {
		w = supportCall(t, s, "POST", path, request, csrf, guest)
		if w.Code != 200 {
			t.Fatal("close/retry", w.Code)
		}
		json.Unmarshal(w.Body.Bytes(), &d)
		if d.Ticket.Status != "closed" || d.Ticket.Revision != 2 {
			t.Fatal("status/revision")
		}
		if strings.Contains(w.Body.String(), "closed_by_player") {
			t.Fatal("private audit leaked")
		}
	}
	request.Text = "reply"
	request.Status = ""
	request.Revision = 2
	request.Operation = fmt.Sprintf("%032x", 3)
	if w = supportCall(t, s, "POST", path, request, csrf, guest); w.Code != 404 {
		t.Fatal("reply after close", w.Code)
	}
}

func TestSupportHTTPResolvedReplyAndClose(t *testing.T) {
	s := hubTest(t)
	guest, csrf := supportInit(t, s)
	w := supportCall(t, s, "POST", "/api/v1/hub/support/tickets", support.Create{Kind: "recovery", TargetID: "123456789", Subject: "Recover account", Answers: map[string]string{"old_device": "unknown", "changed_device": "no"}, Operation: fmt.Sprintf("%032x", 1)}, csrf, guest)
	if w.Code != 201 {
		t.Fatal(w.Code)
	}
	var d struct {
		Ticket support.Ticket `json:"ticket"`
	}
	json.Unmarshal(w.Body.Bytes(), &d)
	id := d.Ticket.ID
	path := "/api/v1/hub/support/tickets/" + id
	// Use the actual store's privileged actor, never an owner-supplied status.
	ticket, err := s.support.Update(id, support.Actor{Operator: true}, true, support.Update{Status: "resolved", Revision: 1, Operation: fmt.Sprintf("%032x", 2)})
	if err != nil {
		t.Fatal(err)
	}
	request := support.Update{Text: "Need more help", Revision: ticket.Revision, Operation: fmt.Sprintf("%032x", 3)}
	w = supportCall(t, s, "POST", path, request, csrf, guest)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &d)
	if d.Ticket.Status != "open" {
		t.Fatal("not reopened")
	}
	ticket, err = s.support.Update(id, support.Actor{Operator: true}, true, support.Update{Status: "resolved", Revision: d.Ticket.Revision, Operation: fmt.Sprintf("%032x", 4)})
	if err != nil {
		t.Fatal(err)
	}
	w = supportCall(t, s, "POST", path, support.Update{Status: "closed", Revision: ticket.Revision, Operation: fmt.Sprintf("%032x", 5)}, csrf, guest)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &d)
	if d.Ticket.Status != "closed" {
		t.Fatal("not closed")
	}
}
