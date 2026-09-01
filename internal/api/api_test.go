// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/config"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/logs"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/secrets"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

func newTestServer(t *testing.T) (*Server, *config.Config) {
	t.Helper()

	key, err := secrets.DeriveKey("test-appid-key")
	if err != nil {
		t.Fatalf("derive key: %v", err)
	}
	cfg := &config.Config{
		ServerName:   "test",
		PhotonAppIDs: map[string]string{"pun": "app-id-pun", "realtime": "app-id-realtime"},
		PhotonRegion: "eu",
		AppIDKey:     key,
		AdminToken:   "admin-secret",
		// Mirrors the shipping default: the tutorials section is published by
		// the server, which is what replaced the frontend force.
		TutorialSectionSeed:           true,
		TutorialSectionIDs:            []int{sioRanchoTutorialID, sioIntroTutorialID},
		TutorialSectionViaCreateSlots: true,
		// Mirrors the shipping default so the wire-code tests keep measuring
		// the escalation the earlier runs settled on.
		ProgressSynchronizeMode:  true,
		ProgressBlankUploadGuard: true,
		Update: config.Update{
			Version: "23.1.3-1",
			APKURL:  "http://example.invalid/opg3d.apk",
		},
	}
	logger, err := logs.New("", true, 1024)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { logger.Close() })

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return New(cfg, logger, st), cfg
}

func do(t *testing.T, s *Server, method, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
	req.RemoteAddr = "192.0.2.10:5555"
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// doForm posts a urlencoded body the way the retired PHP endpoints were called.
func doForm(t *testing.T, s *Server, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestRegisterMintsNineDigitIDAndRotatesSession(t *testing.T) {
	s, _ := newTestServer(t)

	first := do(t, s, http.MethodPost, "/api/v1/accounts/register",
		map[string]string{"device_id": "device-a", "nick": "Tester"}, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", first.Code, first.Body)
	}
	one := decode(t, first)
	playerID, _ := one["player_id"].(string)
	if len(playerID) != 9 {
		t.Fatalf("player_id = %q, want 9 digits", playerID)
	}
	if created, _ := one["created"].(bool); !created {
		t.Fatal("first registration should report created=true")
	}

	second := do(t, s, http.MethodPost, "/api/v1/accounts/register",
		map[string]string{"device_id": "device-a"}, nil)
	two := decode(t, second)
	if two["player_id"] != one["player_id"] {
		t.Fatalf("player id changed for the same device: %v -> %v", one["player_id"], two["player_id"])
	}
	if two["session"] == one["session"] {
		t.Fatal("session token was not rotated on login")
	}
	if created, _ := two["created"].(bool); created {
		t.Fatal("second registration should report created=false")
	}
}

func TestPhotonAppIDIsSealedAndBoundToPlayer(t *testing.T) {
	s, cfg := newTestServer(t)

	account := decode(t, do(t, s, http.MethodPost, "/api/v1/accounts/register",
		map[string]string{"device_id": "device-b"}, nil))
	playerID, _ := account["player_id"].(string)
	session, _ := account["session"].(string)

	rec := do(t, s, http.MethodGet, "/api/v1/photon/appid?mode=realtime&player_id="+playerID, nil,
		map[string]string{"Authorization": "Bearer " + session})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("app-id-realtime")) {
		t.Fatal("AppID leaked in clear text")
	}

	var payload struct {
		Envelope secrets.Envelope `json:"envelope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	plain, err := secrets.Open(cfg.AppIDKey, payload.Envelope)
	if err != nil {
		t.Fatalf("open envelope: %v", err)
	}
	if string(plain) != "app-id-realtime" {
		t.Fatalf("plaintext = %q", plain)
	}
	if payload.Envelope.AAD != playerID+"|realtime" {
		t.Fatalf("aad = %q, want %q", payload.Envelope.AAD, playerID+"|realtime")
	}

	denied := do(t, s, http.MethodGet, "/api/v1/photon/appid?player_id="+playerID, nil,
		map[string]string{"Authorization": "Bearer wrong-session"})
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", denied.Code)
	}
}

func TestBannedDeviceCannotRegister(t *testing.T) {
	s, _ := newTestServer(t)

	ban := map[string]any{"kind": "device", "subject": "device-c", "reason": "testing"}
	rec := do(t, s, http.MethodPost, "/api/v1/admin/bans", ban,
		map[string]string{"Authorization": "Bearer admin-secret"})
	if rec.Code != http.StatusOK {
		t.Fatalf("ban status = %d, body = %s", rec.Code, rec.Body)
	}

	blocked := do(t, s, http.MethodPost, "/api/v1/accounts/register",
		map[string]string{"device_id": "device-c"}, nil)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", blocked.Code)
	}
	if decode(t, blocked)["error"] != "banned" {
		t.Fatalf("body = %s", blocked.Body)
	}
}

func TestAdminRequiresToken(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, http.MethodGet, "/api/v1/admin/accounts", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// The client-side rewriter keeps the original host as the first path segment;
// the server must route on the remainder.
func TestLegacyRewrittenPathRoutes(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, http.MethodGet, "/@server-v2.pixelgun3dserver.com/auth_v2/?device_id=device-d", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	for _, alias := range []string{"id_player", "player_id", "id", "user_id"} {
		if _, ok := body[alias]; !ok {
			t.Fatalf("legacy answer is missing alias %q: %s", alias, rec.Body)
		}
	}
}

func TestUpdateEndpointReportsAvailability(t *testing.T) {
	s, _ := newTestServer(t)

	same := decode(t, do(t, s, http.MethodGet, "/api/v1/update?version=23.1.3-1", nil, nil))
	if available, _ := same["available"].(bool); available {
		t.Fatal("same version must not be offered as an update")
	}
	older := decode(t, do(t, s, http.MethodGet, "/api/v1/update?version=23.1.3-0", nil, nil))
	if available, _ := older["available"].(bool); !available {
		t.Fatal("older version must be offered an update")
	}
}

func TestHTTPTraceRedactsSessionQuery(t *testing.T) {
	s, _ := newTestServer(t)
	const secret = "reusable-session-query-secret"
	rec := do(t, s, http.MethodGet, "/api/v1/ping?opg3d_session="+secret+"&visible=value", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}

	foundRedaction := false
	for _, entry := range s.log.Recent(0) {
		if strings.Contains(entry.Line, secret) {
			t.Fatalf("session query reached trace: %s", entry.Line)
		}
		if entry.Kind == "http.request" && strings.Contains(entry.Line, "opg3d_session=%3Credacted%3E") {
			foundRedaction = true
		}
	}
	if !foundRedaction {
		t.Fatal("request trace did not retain a redacted session-query marker")
	}
}
