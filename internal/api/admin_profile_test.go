// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var adminAuthHeader = map[string]string{"Authorization": "Bearer admin-secret"}

func TestAdminRawStateRoundTripPreservesUnknownFields(t *testing.T) {
	s, _ := newTestServer(t)
	account, _, err := s.st.AdoptSocketIdentity("612672163", "socket-device", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"prefs": map[string]string{"language": "ru"},
		"progress": map[string]any{
			"level": 7, "coins": 33, "gems": 4,
			"future_progress": map[string]any{"enabled": true},
		},
		"progress_document": map[string]any{"4": map[string]any{"l": 9, "e": 123, "future": "kept"}},
		"progress_slots":    map[string]any{"91": map[string]any{"c": []any{}, "d": 20696}},
		"client_state":      map[string]any{"target_ad": 1, "future_state": []any{"a", float64(2)}},
	}
	rec := do(t, s, http.MethodPut, "/api/v1/admin/players/"+account.PlayerID+"/state", body, adminAuthHeader)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	profile, err := s.st.Profile(account.PlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Level != 9 || profile.Experience != 123 || profile.Coins != 33 || profile.Gems != 4 {
		t.Fatalf("projected counters=%+v", profile)
	}
	if profile.Progress["future_progress"].(map[string]any)["enabled"] != true {
		t.Fatal("unknown progress was dropped")
	}
	if profile.ClientState["future_state"].([]any)[0] != "a" {
		t.Fatal("unknown client state was dropped")
	}
}

func TestAdminRawStateRejectsIncompleteOrUnknownShape(t *testing.T) {
	s, _ := newTestServer(t)
	account, _, err := s.st.AdoptSocketIdentity("612672163", "socket-device", "")
	if err != nil {
		t.Fatal(err)
	}
	incomplete := map[string]any{
		"prefs": map[string]string{}, "progress": map[string]any{},
		"progress_document": map[string]any{}, "progress_slots": map[string]any{},
	}
	rec := do(t, s, http.MethodPut, "/api/v1/admin/players/"+account.PlayerID+"/state", incomplete, adminAuthHeader)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("incomplete status=%d body=%s", rec.Code, rec.Body)
	}

	req := `{"prefs":{},"progress":{},"progress_document":{},"progress_slots":{},"client_state":{},"typo":{}}`
	raw := doRawAdmin(t, s, http.MethodPut, "/api/v1/admin/players/"+account.PlayerID+"/state", req)
	if raw.Code != http.StatusBadRequest {
		t.Fatalf("unknown status=%d body=%s", raw.Code, raw.Body)
	}
}

func TestAdminReconcileKeepsAliasSearchableAndHidesSession(t *testing.T) {
	s, _ := newTestServer(t)
	source, _, err := s.st.RegisterOrLogin("native-device", "Bootstrap", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := s.st.AdoptSocketIdentity("612672163", "socket-device", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.MergeProfileProgress(target.PlayerID, map[string]any{"coins": 33}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, s, http.MethodPost, "/api/v1/admin/reconcile", map[string]string{
		"source_id": source.PlayerID, "target_id": target.PlayerID,
	}, adminAuthHeader)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"session"`) || strings.Contains(rec.Body.String(), source.Session) {
		t.Fatal("admin player response exposed a reusable account session")
	}
	var answer struct {
		Player struct {
			PlayerID string   `json:"player_id"`
			Aliases  []string `json:"aliases"`
		} `json:"player"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Player.PlayerID != target.PlayerID || len(answer.Player.Aliases) != 1 || answer.Player.Aliases[0] != source.PlayerID {
		t.Fatalf("answer=%+v", answer)
	}

	found := do(t, s, http.MethodGet, "/api/v1/admin/players?q="+source.PlayerID, nil, adminAuthHeader)
	if found.Code != http.StatusOK || !strings.Contains(found.Body.String(), target.PlayerID) {
		t.Fatalf("alias search status=%d body=%s", found.Code, found.Body)
	}
	if strings.Contains(found.Body.String(), `"session"`) {
		t.Fatal("admin list exposed a reusable account session")
	}
}

func TestAdminReconcileRefusesTwoPopulatedProfiles(t *testing.T) {
	s, _ := newTestServer(t)
	source, _, err := s.st.RegisterOrLogin("native-device", "Bootstrap", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := s.st.AdoptSocketIdentity("612672163", "socket-device", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.MergeProfileProgress(source.PlayerID, map[string]any{"coins": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.MergeProfileProgress(target.PlayerID, map[string]any{"coins": 2}); err != nil {
		t.Fatal(err)
	}
	rec := do(t, s, http.MethodPost, "/api/v1/admin/reconcile", map[string]string{
		"source_id": source.PlayerID, "target_id": target.PlayerID,
	}, adminAuthHeader)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if got := len(s.st.Accounts()); got != 2 {
		t.Fatalf("accounts=%d, want 2", got)
	}
}

func doRawAdmin(t *testing.T, s *Server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("Authorization", "Bearer admin-secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestAdminStateBodiesAreRedactedFromTrace(t *testing.T) {
	s, _ := newTestServer(t)
	account, _, err := s.st.AdoptSocketIdentity("612672163", "socket-device", "192.0.2.10")
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	const secret = "private-session-or-push-token"
	request := map[string]any{
		"prefs":             map[string]string{},
		"progress":          map[string]any{},
		"progress_document": map[string]any{},
		"progress_slots":    map[string]any{},
		"client_state":      map[string]any{"future_token": secret},
	}
	rec := do(t, s, http.MethodPut, "/api/v1/admin/players/"+account.PlayerID+"/state", request,
		map[string]string{"Authorization": "Bearer admin-secret"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	for _, entry := range s.log.Recent(0) {
		if strings.Contains(entry.Line, secret) {
			t.Fatalf("admin state credential reached trace: %s", entry.Line)
		}
	}
}

func TestAdminProfileRejectsNegativeCounters(t *testing.T) {
	s, _ := newTestServer(t)
	account, _, err := s.st.AdoptSocketIdentity("612672163", "socket-device", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.SetProfile(account.PlayerID, "Before", 7, 100, 20, 3); err != nil {
		t.Fatal(err)
	}
	rec := do(t, s, http.MethodPut, "/api/v1/admin/players/"+account.PlayerID+"/profile", map[string]any{
		"nick": "After", "level": 7, "experience": 100, "coins": -1, "gems": 3,
	}, adminAuthHeader)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	profile, _ := s.st.Profile(account.PlayerID)
	if profile.Nick != "Before" || profile.Coins != 20 {
		t.Fatalf("rejected edit modified profile: %#v", profile)
	}
}

func TestAdminConsoleExposesFullProfileEditor(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, http.MethodGet, "/admin", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
		t.Fatalf("content security policy = %q", got)
	}
	for _, marker := range []string{
		`id="pfLevel"`, `id="pfExp"`, `id="pfCoins"`, `id="pfGems"`,
		`id="rawPrefs"`, `id="rawProgress"`, `id="rawDocument"`,
		`id="rawSlots"`, `id="rawClientState"`, `id="doReconcile"`,
		`/api/v1/admin/reconcile`, `/state`,
	} {
		if !strings.Contains(rec.Body.String(), marker) {
			t.Fatalf("admin console omitted %q", marker)
		}
	}
}

type cancelFlushingRecorder struct {
	*httptest.ResponseRecorder
	cancel  context.CancelFunc
	flushes int
}

func (w *cancelFlushingRecorder) Flush() {
	w.flushes++
	w.ResponseRecorder.Flush()
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
}

func TestAdminLogStreamFlushesThroughTraceRecorder(t *testing.T) {
	s, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/logs/stream", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer admin-secret")
	rec := &cancelFlushingRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}

	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if rec.flushes == 0 {
		t.Fatal("admin log stream was never flushed")
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type=%q", got)
	}
	if !strings.Contains(rec.Body.String(), "data: ") {
		t.Fatalf("stream did not replay the trace backlog: %s", rec.Body)
	}
}
