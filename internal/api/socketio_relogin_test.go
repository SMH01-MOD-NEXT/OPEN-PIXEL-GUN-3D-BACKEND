// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type testReloginTransport struct {
	aborts  int
	onAbort func()
}

func (t *testReloginTransport) abortForRelogin() bool {
	t.aborts++
	if t.onAbort != nil {
		t.onAbort()
	}
	return true
}

type orderedResponseRecorder struct {
	*httptest.ResponseRecorder
	wrote bool
}

func (r *orderedResponseRecorder) Write(body []byte) (int, error) {
	r.wrote = true
	return r.ResponseRecorder.Write(body)
}

func addReloginTestSession(t *testing.T, s *Server, playerID, device string, transport sioReloginTransport) *sioSession {
	t.Helper()
	session, err := s.sio.create(eioV4)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session.setIdentity(playerID, device, playerID)
	session.attachReloginTransport(transport)
	return session
}

func putAdminJSON(t *testing.T, s *Server, target string, body any, recorder http.ResponseWriter) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, target, bytes.NewReader(raw))
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("Authorization", "Bearer admin-secret")
	req.Header.Set("Content-Type", "application/json")
	s.ServeHTTP(recorder, req)
}

func TestAdminProfileEditReloginsOnlyMatchingOnlinePlayer(t *testing.T) {
	s, _ := newTestServer(t)
	target, _, err := s.st.AdoptSocketIdentity("612672163", "target-device", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := s.st.AdoptSocketIdentity("918930694", "foreign-device", "192.0.2.11")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.SetProfile(target.PlayerID, "Target", 11, 0, 0, 0); err != nil {
		t.Fatal(err)
	}

	recorder := &orderedResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
	responseWasWritten := true
	first := &testReloginTransport{onAbort: func() { responseWasWritten = responseWasWritten && recorder.wrote }}
	second := &testReloginTransport{onAbort: func() { responseWasWritten = responseWasWritten && recorder.wrote }}
	other := &testReloginTransport{}
	addReloginTestSession(t, s, target.PlayerID, "target-a", first)
	addReloginTestSession(t, s, target.PlayerID, "target-b", second)
	addReloginTestSession(t, s, foreign.PlayerID, "foreign", other)

	putAdminJSON(t, s, "/api/v1/admin/players/"+target.PlayerID+"/profile", map[string]any{
		"nick": "Target", "level": 11, "experience": 0, "coins": 77, "gems": 9,
	}, recorder)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	if !responseWasWritten {
		t.Fatal("transport was aborted before the admin success body was written")
	}
	if first.aborts != 1 || second.aborts != 1 {
		t.Fatalf("matching aborts=(%d,%d), want (1,1)", first.aborts, second.aborts)
	}
	if other.aborts != 0 {
		t.Fatalf("foreign player transport was aborted %d time(s)", other.aborts)
	}
	if got := s.sio.reloginPlayer(target.PlayerID); got != 0 {
		t.Fatalf("consumed transports were aborted again: %d", got)
	}
	profile, err := s.st.Profile(target.PlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Coins != 77 || profile.Gems != 9 {
		t.Fatalf("edited progress was not durable before relogin: %#v", profile)
	}
}

func TestAdminProfileNickOnlyEditReloginsTarget(t *testing.T) {
	s, _ := newTestServer(t)
	account, _, err := s.st.AdoptSocketIdentity("612672163", "target-device", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.st.SetProfile(account.PlayerID, "Before", 11, 4, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	transport := &testReloginTransport{}
	addReloginTestSession(t, s, account.PlayerID, "target-device", transport)

	recorder := httptest.NewRecorder()
	putAdminJSON(t, s, "/api/v1/admin/players/"+account.PlayerID+"/profile", map[string]any{
		"nick": "After", "level": 11, "experience": 4, "coins": 3, "gems": 2,
	}, recorder)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	if transport.aborts != 1 {
		t.Fatalf("nick-only edit aborted transport %d time(s)", transport.aborts)
	}
	after, err := s.st.Profile(account.PlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ProgressRevision == before.ProgressRevision || after.Nick != "After" {
		t.Fatalf("nick-only edit did not advance revision or lost nick: before=%#v after=%#v", before, after)
	}
}

func TestAdminRawStateEditReloginsOnlyMatchingOnlinePlayer(t *testing.T) {
	s, _ := newTestServer(t)
	account, _, err := s.st.AdoptSocketIdentity("612672163", "target-device", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	target := &testReloginTransport{}
	foreign := &testReloginTransport{}
	addReloginTestSession(t, s, account.PlayerID, "target-device", target)
	addReloginTestSession(t, s, "918930694", "foreign-device", foreign)

	recorder := httptest.NewRecorder()
	putAdminJSON(t, s, "/api/v1/admin/players/"+account.PlayerID+"/state", map[string]any{
		"prefs":             map[string]string{},
		"progress":          map[string]any{"level": 12, "coins": 21, "gems": 4},
		"progress_document": map[string]any{"4": map[string]any{"l": 12, "e": 5}},
		"progress_slots":    map[string]any{"3": map[string]any{"Coins": 21, "GemsCurrency": 4}},
		"client_state":      map[string]any{},
	}, recorder)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	if target.aborts != 1 || foreign.aborts != 0 {
		t.Fatalf("target aborts=%d foreign aborts=%d", target.aborts, foreign.aborts)
	}
}

func TestOldWebSocketDetachCannotRemoveReplacementTransport(t *testing.T) {
	session := &sioSession{}
	oldTransport := &testReloginTransport{}
	newTransport := &testReloginTransport{}
	session.setIdentity("612672163", "device", "612672163")
	session.attachReloginTransport(oldTransport)
	session.attachReloginTransport(newTransport)
	session.detachReloginTransport(oldTransport)
	if !session.abortReloginTransportForPlayer("612672163") {
		t.Fatal("replacement transport was detached with the old connection")
	}
	if oldTransport.aborts != 0 || newTransport.aborts != 1 {
		t.Fatalf("old aborts=%d new aborts=%d", oldTransport.aborts, newTransport.aborts)
	}
}

func TestUnchangedAdminProfileDoesNotDisconnectPlayer(t *testing.T) {
	s, _ := newTestServer(t)
	a, _, err := s.st.AdoptSocketIdentity("612672163", "device", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.st.SetProfile(a.PlayerID, "Target", 65, 500, 7, 8)
	if err != nil {
		t.Fatal(err)
	}
	transport := &testReloginTransport{}
	addReloginTestSession(t, s, a.PlayerID, "device", transport)
	recorder := httptest.NewRecorder()
	putAdminJSON(t, s, "/api/v1/admin/players/"+a.PlayerID+"/profile", map[string]any{"nick": "Target", "level": 65, "experience": 500, "coins": 7, "gems": 8}, recorder)
	if recorder.Code != http.StatusOK || transport.aborts != 0 {
		t.Fatal("unchanged edit disconnected player")
	}
	after, _ := s.st.Profile(a.PlayerID)
	if after.ProgressRevision != p.ProgressRevision {
		t.Fatal("unchanged edit advanced revision")
	}
}
