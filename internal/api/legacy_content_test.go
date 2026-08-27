// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// A 404 on the analytics sink freezes the client's loading bar until its own
// HTTP timeout expires, so these paths must answer.
func TestLegacyEventAndStatSinksAnswerOK(t *testing.T) {
	s, _ := newTestServer(t)

	for _, target := range []string{
		"/@acct.pixelgun3dserver.com/events/add_event.php?opg3d_player=123456789",
		"/@pixelgun-cl-stat.pixelgunserver.com/event_stat_add_pl.php",
	} {
		rec := doForm(t, s, target, "event_id=3&progress=loading30&player_id=350604106&muid=350604106")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body = %s", target, rec.Code, rec.Body)
		}
		if decode(t, rec)["result"] != "ok" {
			t.Fatalf("%s: body = %s", target, rec.Body)
		}
	}
}

func TestRemoteConfigServesFileThenFallback(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = t.TempDir()

	dir := filepath.Join(cfg.ContentDir, "pixelgun3d-config", "rating-maps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	authored := `[{"map":"pool_party"}]`
	if err := os.WriteFile(filepath.Join(dir, "rating-map-all.json"), []byte(authored), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	served := do(t, s, http.MethodGet,
		"/@secure.pixelgun3dserver.com/pixelgun3d-config/rating-maps/rating-map-all.json", nil, nil)
	if served.Code != http.StatusOK || served.Body.String() != authored {
		t.Fatalf("status = %d, body = %s", served.Code, served.Body)
	}

	// A file that has not been authored yet still has to answer, with an empty
	// document rather than 404.
	missing := do(t, s, http.MethodGet,
		"/pixelgun3d-config/rating-maps/rating-map-top.json", nil, nil)
	if missing.Code != http.StatusOK || missing.Body.String() != "[]" {
		t.Fatalf("status = %d, body = %s", missing.Code, missing.Body)
	}
	unknown := do(t, s, http.MethodGet,
		"/pixelgun3d-config/BuffSettings1031/BuffSettings_android.json", nil, nil)
	if unknown.Code != http.StatusOK || unknown.Body.String() != "{}" {
		t.Fatalf("status = %d, body = %s", unknown.Code, unknown.Body)
	}
}

func TestRemoteConfigRefusesEscapingPaths(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, http.MethodGet, "/pixelgun3d-config/../../../etc/passwd", nil, nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("escaping path was served: %s", rec.Body)
	}
}
