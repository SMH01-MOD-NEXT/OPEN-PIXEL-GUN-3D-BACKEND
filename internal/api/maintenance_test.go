// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/config"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

func TestMaintenanceWindowBoundaries(t *testing.T) {
	window := maintenanceSchedule{"2026-10-26T15:00:00+03:00", "2026-11-02T15:00:00+03:00"}
	if err := window.validate(); err != nil {
		t.Fatal(err)
	}
	window = window.utc()
	if window.Start != "2026-10-26T12:00:00Z" || window.End != "2026-11-02T12:00:00Z" {
		t.Fatal(window)
	}
	start, _ := time.Parse(time.RFC3339, window.Start)
	end, _ := time.Parse(time.RFC3339, window.End)
	for _, tc := range []struct {
		at     time.Time
		active bool
	}{{start.Add(-time.Nanosecond), false}, {start, true}, {end.Add(-time.Nanosecond), true}, {end, false}, {end.Add(time.Hour), false}} {
		if got := window.active(tc.at); got != tc.active {
			t.Fatalf("at %v: %v want %v", tc.at, got, tc.active)
		}
	}
	for _, bad := range []maintenanceSchedule{{Start: window.Start}, {End: window.End}, {Start: "invalid", End: window.End}, {Start: window.End, End: window.Start}, {Start: window.Start, End: window.Start}} {
		if bad.validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
func TestMaintenanceSchedulePersistenceAndRollback(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir(), PhotonRegion: "eu"}
	settings := newSettings(cfg)
	now := time.Now().UTC()
	plan := maintenanceSchedule{now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)}
	if _, err := settings.apply(settingsPatch{MaintenanceSchedule: &plan}); err != nil {
		t.Fatal(err)
	}
	if err := settings.setMaintenanceAllowed("123", true); err != nil {
		t.Fatal(err)
	}
	fresh := newSettings(cfg)
	if err := fresh.load(); err != nil {
		t.Fatal(err)
	}
	if !fresh.maintenanceFor("") || fresh.maintenanceFor("123") || fresh.snapshot().MaintenanceManual {
		t.Fatal("active schedule or allowlist lost on restart")
	}
	// Effective activity must not get persisted as the manual switch.
	snap := fresh.snapshot()
	snap.MaintenanceSchedule = maintenanceSchedule{now.Add(-2 * time.Hour).Format(time.RFC3339), now.Add(-time.Hour).Format(time.RFC3339)}
	if _, err := fresh.apply(snap.patch()); err != nil {
		t.Fatal(err)
	}
	again := newSettings(cfg)
	if err := again.load(); err != nil {
		t.Fatal(err)
	}
	if again.maintenanceFor("") {
		t.Fatal("expired window latched maintenance on")
	}
	oldPath := fresh.path
	block := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(block, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	fresh.path = filepath.Join(block, "settings.json")
	on := true
	if _, err := fresh.apply(settingsPatch{Maintenance: &on}); err == nil {
		t.Fatal("save should fail")
	}
	if fresh.snapshot().MaintenanceManual {
		t.Fatal("failed save changed live state")
	}
	if err := fresh.setMaintenanceAllowed("456", true); err == nil {
		t.Fatal("allowlist save should fail")
	}
	if fresh.maintenanceAllowed("456") {
		t.Fatal("failed save granted bypass")
	}
	fresh.path = oldPath
	// Snapshot maps are copies, not mutable access to live state.
	copy := fresh.snapshot()
	copy.MaintenanceAllowlist["789"] = true
	if fresh.maintenanceAllowed("789") {
		t.Fatal("snapshot leaked map")
	}
}
func TestMaintenanceConcurrentPersistence(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir(), PhotonRegion: "eu"}
	settings := newSettings(cfg)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := settings.setMaintenanceAllowed(fmt.Sprint(i), true); err != nil {
				t.Error(err)
			}
			settings.snapshot()
			settings.maintenanceFor("0")
		}(i)
	}
	wg.Wait()
	fresh := newSettings(cfg)
	if err := fresh.load(); err != nil {
		t.Fatal(err)
	}
	if len(fresh.snapshot().MaintenanceAllowlist) != 20 {
		t.Fatal("concurrent grant lost")
	}
}
func TestMaintenanceAdminAccessAndGameWire(t *testing.T) {
	s, _ := newTestServer(t)
	account, _, err := s.st.RegisterOrLogin("maintenance-test-device", "Tester", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "/api/v1/admin/players/" + account.PlayerID + "/maintenance-access"
	if r := do(t, s, "PUT", endpoint, map[string]bool{"enabled": true}, nil); r.Code != 401 {
		t.Fatal(r.Code)
	}
	headers := map[string]string{"Authorization": "Bearer admin-secret", "Content-Type": "application/json"}
	for _, bad := range []any{map[string]any{}, map[string]any{"enabled": "yes"}} {
		if r := do(t, s, "PUT", endpoint, bad, headers); r.Code != 400 {
			t.Fatal(r.Code)
		}
	}
	if r := do(t, s, "PUT", "/api/v1/admin/players/nonexistent/maintenance-access", map[string]bool{"enabled": true}, headers); r.Code != 404 {
		t.Fatal(r.Code)
	}
	on := true
	if _, err := s.settings.apply(settingsPatch{Maintenance: &on}); err != nil {
		t.Fatal(err)
	}
	session := authorisedSession("0123456789abcdef01234567", account.PlayerID)
	if _, err := s.st.EnsureProfile(account.PlayerID, account.Nick); err != nil {
		t.Fatal(err)
	}
	if r := do(t, s, "PUT", endpoint, map[string]bool{"enabled": true}, headers); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if got := s.answerProgressStatus(session, sioHead{}, 1)[sioProgressEnabledKey]; got != sioProgressOpen {
		t.Fatal(got)
	}
	if row := s.buildAdminPlayer(account); !row.MaintenanceAllowed || row.SupportAdmin {
		t.Fatal("bypass missing or granted hub role")
	}
	if s.progressStatusCode() != sioProgressMaintenance || s.progressStatusCode("unlisted") != sioProgressMaintenance {
		t.Fatal("unlisted player bypassed maintenance")
	}
	if _, err := s.st.MergeProfileProgress(account.PlayerID, map[string]any{"level": float64(1)}); err != nil {
		t.Fatal(err)
	}
	if got := s.answerProgressStatus(session, sioHead{}, 1)[sioProgressEnabledKey]; got != sioProgressSynchronize {
		t.Fatal(got)
	}
	if r := do(t, s, "PUT", endpoint, map[string]bool{"enabled": false}, headers); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if got := s.answerProgressStatus(session, sioHead{}, 1)[sioProgressEnabledKey]; got != sioProgressMaintenance {
		t.Fatal(got)
	}
	// Loader has no identity yet: deliver signed native code, then gate game login.
	loader := decode(t, do(t, s, "GET", "/api/v1/loader/info", nil, nil))
	if loader["maintenance"] != false || loader["game_maintenance"] != true {
		t.Fatal(loader)
	}
	if !strings.Contains(do(t, s, "GET", "/api/v1/ping", nil, nil).Body.String(), `"maintenance":true`) {
		t.Fatal("global status hidden")
	}
}
func TestMaintenanceBypassNeverOverridesBan(t *testing.T) {
	s, _ := newTestServer(t)
	account, _, err := s.st.RegisterOrLogin("maintenance-banned-device", "Tester", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.settings.setMaintenanceAllowed(account.PlayerID, true); err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := s.settings.apply(settingsPatch{Maintenance: &on}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.AddBan(store.Ban{Kind: store.BanPlayer, Subject: account.PlayerID, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if r := do(t, s, "POST", "/api/v1/sessions/refresh", map[string]string{"player_id": account.PlayerID, "session": account.Session}, nil); r.Code != 403 {
		t.Fatalf("allowlisted banned account refreshed session: %d", r.Code)
	}
	if r := do(t, s, "GET", "/api/v1/photon/appid?mode=pun&player_id="+account.PlayerID, nil, map[string]string{"Authorization": "Bearer " + account.Session}); r.Code != 403 {
		t.Fatalf("allowlisted banned account obtained Photon credential: %d", r.Code)
	}
}

func TestMaintenanceScheduleAdminValidationAndStop(t *testing.T) {
	s, _ := newTestServer(t)
	headers := map[string]string{"Authorization": "Bearer admin-secret", "Content-Type": "application/json"}
	for _, p := range []string{"/api/v1/admin/settings"} {
		if r := do(t, s, "PATCH", p, map[string]any{"maintenance": true}, nil); r.Code != 401 {
			t.Fatal(r.Code)
		}
	}
	now := time.Now().UTC()
	start := now.Add(-time.Hour).Format(time.RFC3339)
	end := now.Add(time.Hour).Format(time.RFC3339)
	if r := do(t, s, "PATCH", "/api/v1/admin/settings", map[string]any{"maintenance_schedule": maintenanceSchedule{start, end}}, headers); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if !s.settings.snapshot().Maintenance {
		t.Fatal("schedule not active")
	}
	if r := do(t, s, "PATCH", "/api/v1/admin/settings", map[string]any{"maintenance": false, "maintenance_schedule": maintenanceSchedule{end, start}}, headers); r.Code != 400 {
		t.Fatal(r.Body.String())
	}
	if !s.settings.snapshot().Maintenance {
		t.Fatal("bad patch changed schedule")
	}
	if r := do(t, s, "PATCH", "/api/v1/admin/settings", map[string]any{"maintenance": false, "maintenance_schedule": maintenanceSchedule{}}, headers); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if s.settings.snapshot().Maintenance {
		t.Fatal("stop did not clear maintenance")
	}
}
func TestMaintenanceConsoleMarkup(t *testing.T) {
	html := inventoryConsoleHTML()
	for _, marker := range []string{`data-route="maintenance"`, `id="page-maintenance"`, `id="maintenanceForm"`, `id="maintenanceAllowedFlag"`, `id="saveMaintenanceAccess"`, `/maintenance-access`, `maintenance_schedule`, `maintenanceNowConfirm`, `maintenanceStopConfirm`} {
		if !strings.Contains(html, marker) {
			t.Fatalf("missing %s", marker)
		}
	}
}

func TestMaintenanceLegacyHTTPContract(t *testing.T) {
	s, _ := newTestServer(t)
	a, _, err := s.st.RegisterOrLogin("maintenance-http-device", "Tester", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err = s.settings.apply(settingsPatch{Maintenance: &on}); err != nil {
		t.Fatal(err)
	}
	request := func(session string) (int, map[string]string) {
		t.Helper()
		q := url.Values{"opg3d_player": {a.PlayerID}, "opg3d_session": {session}}
		raw, err := encodeLegacyAuth(map[string]string{"device_id": a.DeviceID, "hash": "preserve-hash"}, 77)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest("POST", "/auth_v2/?"+q.Encode(), bytes.NewReader(raw)))
		if rec.Code != 200 {
			return rec.Code, nil
		}
		fields, err := decodeLegacyAuth(rec.Body.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		return rec.Code, fields
	}
	checkDenied := func() {
		t.Helper()
		code, fields := request(a.Session)
		if code != 200 || fields["err_code"] != "3000" || fields["player_id"] != a.PlayerID || fields["hash"] != "preserve-hash" {
			t.Fatalf("wrong maintenance contract: %d %+v", code, fields)
		}
		var notification struct {
			Critical bool `json:"critical"`
		}
		if err := json.Unmarshal([]byte(fields["notification"]), &notification); err != nil || !notification.Critical {
			t.Fatal("stock critical notification missing", err)
		}
		if fields["url"] != "" || fields["token"] != "" {
			t.Fatal("maintenance granted socket access")
		}
	}
	checkOpen := func() {
		t.Helper()
		code, fields := request(a.Session)
		if code != 200 || fields["err_code"] != "" || fields["token"] != a.Session || fields["url"] == "" {
			t.Fatal("normal auth did not resume")
		}
	}
	checkDenied()
	if code, _ := request("wrong-session"); code != 401 {
		t.Fatal("maintenance bypassed credential validation")
	}
	// Supplying a whitelisted ID without its valid session never grants access.
	if err := s.settings.setMaintenanceAllowed(a.PlayerID, true); err != nil {
		t.Fatal(err)
	}
	checkOpen()
	if code, _ := request("wrong-session"); code != 401 {
		t.Fatal("spoofed whitelist")
	}
	if err := s.settings.setMaintenanceAllowed(a.PlayerID, false); err != nil {
		t.Fatal(err)
	}
	checkDenied()
	on = false
	if _, err := s.settings.apply(settingsPatch{Maintenance: &on}); err != nil {
		t.Fatal(err)
	}
	checkOpen()
	now := time.Now().UTC()
	plan := maintenanceSchedule{now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339)}
	if _, err := s.settings.apply(settingsPatch{MaintenanceSchedule: &plan}); err != nil {
		t.Fatal(err)
	}
	checkDenied()
	plan = maintenanceSchedule{now.Add(-2 * time.Minute).Format(time.RFC3339), now.Add(-time.Minute).Format(time.RFC3339)}
	if _, err := s.settings.apply(settingsPatch{MaintenanceSchedule: &plan}); err != nil {
		t.Fatal(err)
	}
	checkOpen()
	on = true
	if _, err := s.settings.apply(settingsPatch{Maintenance: &on}); err != nil {
		t.Fatal(err)
	}
	if err := s.settings.setMaintenanceAllowed(a.PlayerID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.st.AddBan(store.Ban{Kind: store.BanPlayer, Subject: a.PlayerID}); err != nil {
		t.Fatal(err)
	}
	if code, fields := request(a.Session); code != 200 || fields["err_code"] != "1402" {
		t.Fatal("maintenance or whitelist overrode ban")
	}
}

func TestMaintenanceSocketRaceFailsClosed(t *testing.T) {
	s, _ := newTestServer(t)
	a, _, err := s.st.AdoptSocketIdentity("740333804", "maintenance-race-device", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := s.settings.apply(settingsPatch{Maintenance: &on}); err != nil {
		t.Fatal(err)
	}
	session := &sioSession{sid: "0123456789abcdef01234567"}
	head := sioHead{"uniq_id": a.PlayerID, "device": a.SocketDevice}
	body := s.answerAuth(session, head, 7)
	if body["err_code"] != sioErrTechnicalWorkBlocking || body["error"] != sioErrTechnicalWorkBlocking {
		t.Fatal(body)
	}
	if _, _, player := session.identity(); player != "" {
		t.Fatal("denied socket remained authorized")
	}
	if err := s.settings.setMaintenanceAllowed(a.PlayerID, true); err != nil {
		t.Fatal(err)
	}
	body = s.answerAuth(session, head, 8)
	if _, denied := body["err_code"]; denied {
		t.Fatal(body)
	}
	if _, _, player := session.identity(); player != a.PlayerID {
		t.Fatal("allowlisted socket not authorized")
	}
	if err := s.settings.setMaintenanceAllowed(a.PlayerID, false); err != nil {
		t.Fatal(err)
	}
	body = s.answerProgressStatus(session, head, 9)
	if body["err_code"] != sioErrTechnicalWorkBlocking || body[sioProgressEnabledKey] != sioProgressMaintenance {
		t.Fatal("revoked socket was given normal progress status")
	}
}
