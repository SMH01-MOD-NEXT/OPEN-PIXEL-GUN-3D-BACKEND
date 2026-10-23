package api

import (
	"fmt"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/clienterrors"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
	"strings"
	"testing"
	"time"
)

func TestClientErrorsAuthValidationPrivacyAndAdmin(t *testing.T) {
	s, _ := newTestServer(t)
	a, _, err := s.st.RegisterOrLogin("diag-device", "Diag", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer " + a.Session, "X-OPG3D-Player": a.PlayerID}
	event := map[string]any{"id": "event-one", "level": "error", "code": "unity_exception", "message": "NullReferenceException\nBearer should-never-appear", "stack": "at Test.Method()", "version": "23.1.3"}
	batch := map[string]any{"events": []any{event}}
	if r := do(t, s, "POST", "/api/v1/client-errors", batch, nil); r.Code != 401 {
		t.Fatal(r.Code)
	}
	event["player_id"] = "victim"
	if r := do(t, s, "POST", "/api/v1/client-errors", batch, headers); r.Code != 400 {
		t.Fatal(r.Code)
	}
	delete(event, "player_id")
	if r := do(t, s, "POST", "/api/v1/client-errors", batch, headers); r.Code != 202 {
		t.Fatal(r.Code, r.Body)
	}
	if got := decode(t, do(t, s, "POST", "/api/v1/client-errors", batch, headers))["accepted"]; got != float64(0) {
		t.Fatal("retry duplicate", got)
	}
	for _, line := range s.log.Recent(0) {
		if strings.Contains(line.Line, "should-never-appear") {
			t.Fatal("raw body traced")
		}
	}
	admin := map[string]string{"Authorization": "Bearer admin-secret"}
	if r := do(t, s, "GET", "/api/v1/admin/client-errors", nil, nil); r.Code != 401 {
		t.Fatal(r.Code)
	}
	r := do(t, s, "GET", "/api/v1/admin/client-errors", nil, admin)
	body := decode(t, r)
	if body["total"] != float64(1) || strings.Contains(r.Body.String(), "should-never-appear") {
		t.Fatal(r.Body)
	}
	stats := decode(t, do(t, s, "GET", "/api/v1/admin/stats", nil, admin))["client_errors"].(map[string]any)
	if stats["errors"] != float64(1) || stats["players"] != float64(1) {
		t.Fatal(stats)
	}
	event["level"] = "info"
	if r := do(t, s, "POST", "/api/v1/client-errors", batch, headers); r.Code != 400 {
		t.Fatal(r.Code)
	}
	event["level"] = "error"
	event["message"] = strings.Repeat("x", 4097)
	if r := do(t, s, "POST", "/api/v1/client-errors", batch, headers); r.Code != 400 {
		t.Fatal(r.Code)
	}
	event["message"] = "error"
	for i := 0; i < 58; i++ {
		event["id"] = fmt.Sprint(i)
		if r := do(t, s, "POST", "/api/v1/client-errors", batch, headers); r.Code != 202 {
			t.Fatal(i, r.Code)
		}
	}
	if r := do(t, s, "POST", "/api/v1/client-errors", batch, headers); r.Code != 429 || r.Header().Get("Retry-After") != "60" {
		t.Fatal(r.Code)
	}
}
func TestDiagnosticServerEventsAndUnavailability(t *testing.T) {
	s, cfg := newTestServer(t)
	s.diagnostic("a", "warning", "resync_required", "Pixel Pass conflict", 12)
	s.diagnostic("a", "info", "resync_completed", "applied", 12)
	q := s.clientErrors.Query(clienterrors.Query{}, time.Now())
	if q.Summary.Resyncs != 1 || q.Summary.Completed != 1 {
		t.Fatal(q)
	}
	cfg.AdminToken = ""
	if r := do(t, s, "GET", "/api/v1/admin/client-errors", nil, map[string]string{"Authorization": "Bearer admin-secret"}); r.Code != 503 {
		t.Fatal(r.Code)
	}
}

func TestDiagnosticsCredentialSurvivesGameplaySessionRotation(t *testing.T) {
	s, _ := newTestServer(t)
	reg := decode(t, do(t, s, "POST", "/api/v1/accounts/register", map[string]string{"device_id": "diagnostics-scoped"}, nil))
	player := reg["player_id"].(string)
	token := reg["diagnostics_token"].(string)
	if token == "" {
		t.Fatal("missing scoped credential")
	}
	if _, _, e := s.st.RegisterOrLogin("diagnostics-scoped", "", "192.0.2.10"); e != nil {
		t.Fatal(e)
	}
	headers := map[string]string{"X-OPG3D-Player": player, "X-OPG3D-Diagnostics": token}
	batch := map[string]any{"events": []any{map[string]any{"id": "rotation-test", "code": "unity_error", "level": "error", "message": "rotation survived"}}}
	if r := do(t, s, "POST", "/api/v1/client-errors", batch, headers); r.Code != 202 {
		t.Fatal(r.Code, r.Body)
	}
	headers["X-OPG3D-Player"] = "victim"
	if r := do(t, s, "POST", "/api/v1/client-errors", batch, headers); r.Code != 401 {
		t.Fatal("spoof", r.Code)
	}
	// Scoped credential cannot authenticate a gameplay API or the admin console.
	for _, path := range []string{"/api/v1/photon/appid?mode=pun&player_id=" + player, "/api/v1/admin/stats"} {
		r := do(t, s, "GET", path, nil, map[string]string{"Authorization": "Bearer " + token, "X-OPG3D-Player": player})
		if r.Code == 200 {
			t.Fatal("credential escaped scope", path)
		}
	}
}

func TestDiagnosticsCanonicalIdentityAndBans(t *testing.T) {
	s, _ := newTestServer(t)
	a, _, err := s.st.RegisterOrLogin("diag-canonical", "", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	token := s.diagnosticsCredentials.Mint(a.PlayerID, time.Now())
	result, err := s.st.AdoptSocketIdentityForAccount(a.PlayerID, "612672163", "socket-device", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"X-OPG3D-Player": a.PlayerID, "X-OPG3D-Diagnostics": token}
	batch := map[string]any{"events": []any{map[string]any{"id": "canonical-test", "code": "unity_error", "level": "error", "message": "canonical"}}}
	if r := do(t, s, "POST", "/api/v1/client-errors", batch, headers); r.Code != 202 {
		t.Fatal(r.Code, r.Body)
	}
	groups := s.clientErrors.Query(clienterrors.Query{}, time.Now()).Groups
	if len(groups) != 1 || groups[0].Player != result.Account.PlayerID {
		t.Fatal("noncanonical event", groups)
	}
	if err := s.st.AddBan(store.Ban{Kind: store.BanPlayer, Subject: result.Account.PlayerID}); err != nil {
		t.Fatal(err)
	}
	if r := do(t, s, "POST", "/api/v1/client-errors", batch, headers); r.Code != 403 {
		t.Fatal("ban bypass", r.Code)
	}
	if r := do(t, s, "POST", "/api/v1/sessions/refresh", refreshRequest{PlayerID: a.PlayerID, Session: token}, nil); r.Code != 401 {
		t.Fatal("scope escape", r.Code)
	}
}
