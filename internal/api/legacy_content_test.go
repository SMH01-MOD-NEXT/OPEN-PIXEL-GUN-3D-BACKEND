// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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

func TestAuthoredMiniGameRatingDocumentMatchesThe2313Parser(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")

	rec := do(t, s, http.MethodGet,
		"/@secure.pixelgun3dserver.com/pixelgun3d-config/rating-minigame/rating-minigame-count-all.json",
		nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}

	// The 23.1.3 MiniGameRatingDownloader parses a dictionary keyed by an
	// integer game-mode id. Each value is another dictionary with optional
	// numeric "rating" and integer "count" fields. A second accessor looks up
	// the same game-mode id plus 1000, so the local zero-vote baseline covers
	// both groups instead of relying on an empty-object fallback.
	var document map[string]struct {
		Rating float64 `json:"rating"`
		Count  int     `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode authored rating document: %v", err)
	}
	if len(document) != 86 {
		t.Fatalf("document has %d modes, want 0-42 and 1000-1042", len(document))
	}
	for mode := 0; mode <= 42; mode++ {
		for _, id := range []int{mode, mode + 1000} {
			value, ok := document[strconv.Itoa(id)]
			if !ok {
				t.Fatalf("rating document omits mode id %d", id)
			}
			if value.Rating != 0 || value.Count != 0 {
				t.Fatalf("fresh local mode %d is rating=%v count=%d, want zero votes", id, value.Rating, value.Count)
			}
		}
	}
}
