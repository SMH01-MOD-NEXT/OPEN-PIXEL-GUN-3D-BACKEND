// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A saved patch has to be visible to the live handlers, not just to the
// console: the whole point of the layer is that an operator can change these
// values without a restart. The Photon ids must never come back in clear text.
func TestAdminSettingsRoundTrip(t *testing.T) {
	s, _ := newTestServer(t)

	body := `{"maintenance":true,"photon_region":"us","update":{"version":"23.1.4","apk_url":"http://example.invalid/opg3d.apk"}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin-secret")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body %s", rec.Code, rec.Body.String())
	}

	var view struct {
		Maintenance  bool              `json:"maintenance"`
		PhotonRegion string            `json:"photon_region"`
		PhotonAppIDs map[string]string `json:"photon_app_ids"`
		Update       struct {
			Version string `json:"version"`
		} `json:"update"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if !view.Maintenance || view.PhotonRegion != "us" || view.Update.Version != "23.1.4" {
		t.Fatalf("unexpected view: %+v", view)
	}
	if len(view.PhotonAppIDs) == 0 {
		t.Fatal("the view lists no Photon modes")
	}
	for mode, appID := range view.PhotonAppIDs {
		if strings.Contains(appID, "app-id") {
			t.Fatalf("mode %s exposes the live app id: %s", mode, appID)
		}
	}

	ping := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	pingRec := httptest.NewRecorder()
	s.mux.ServeHTTP(pingRec, ping)
	if !strings.Contains(pingRec.Body.String(), `"maintenance":true`) {
		t.Fatalf("ping ignored the maintenance switch: %s", pingRec.Body.String())
	}

	update := httptest.NewRequest(http.MethodGet, "/api/v1/update?version=23.1.3", nil)
	update.Header.Set("Authorization", "Bearer admin-secret")
	updateRec := httptest.NewRecorder()
	s.mux.ServeHTTP(updateRec, update)
	if !strings.Contains(updateRec.Body.String(), "23.1.4") {
		t.Fatalf("the update endpoint still serves the environment seed: %s", updateRec.Body.String())
	}
}

// An announced version with nowhere to download it from would strand every
// client in an update prompt it cannot satisfy, so the patch is refused whole.
func TestAdminSettingsRejectsUpdateWithoutURL(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings",
		strings.NewReader(`{"update":{"version":"23.1.5"}}`))
	req.Header.Set("Authorization", "Bearer admin-secret")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := s.settings.snapshot().Update.Version; got != "23.1.3-1" {
		t.Fatalf("a rejected patch changed the announced version to %q", got)
	}
}

// These endpoints can rotate credentials and close the server, so a missing or
// wrong token must refuse rather than fall through.
func TestAdminSettingsRequiresToken(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
}

// The technical works screen belongs to the server. The socket answer that the
// auth scene reads must carry whatever the operator's switch says at that
// moment, on the same footing as the liveness endpoint - if this ever goes back
// to a constant, maintenance mode stops reaching the client entirely.
func TestProgressEnabledFollowsMaintenance(t *testing.T) {
	s, _ := newTestServer(t)

	if got := s.progressStatusCode(); got != sioProgressOpen {
		t.Fatalf("open server answers %d, want %d", got, sioProgressOpen)
	}

	on := true
	if _, err := s.settings.apply(settingsPatch{Maintenance: &on}); err != nil {
		t.Fatalf("apply maintenance: %v", err)
	}
	if got := s.progressStatusCode(); got != sioProgressMaintenance {
		t.Fatalf("server in maintenance answers %d, want %d", got, sioProgressMaintenance)
	}
	if !s.settings.snapshot().Maintenance {
		t.Fatal("the snapshot the liveness answer reads lost the flag")
	}

	off := false
	if _, err := s.settings.apply(settingsPatch{Maintenance: &off}); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := s.progressStatusCode(); got != sioProgressOpen {
		t.Fatalf("reopened server answers %d, want %d", got, sioProgressOpen)
	}
}
