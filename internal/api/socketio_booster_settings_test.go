// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestShippingXPBoosterSettings2313(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")
	hosted, ok := s.hostedConfigDocument("Boosters")
	if !ok {
		t.Fatal("missing Boosters config")
	}
	rows, ok := hosted.document["bs"].([]any)
	if !ok || len(rows) != 4 {
		t.Fatal("require all four stock XP boosters")
	}
	for i, percent := range []float64{25, 50, 100, 200} {
		row := rows[i].(map[string]any)
		n := i + 1
		want := map[string]any{
			"id": fmt.Sprintf("booster%d", n), "lt": float64(1800),
			"t":  fmt.Sprintf("offericons/booster/icon_booster%d", n),
			"st": fmt.Sprintf("offericons/booster/icon_booster%d_n", n),
			"mi": fmt.Sprintf("offerminiicons/icon_booster%d_mini", n),
			"bp": map[string]any{"3": percent},
		}
		if !reflect.DeepEqual(row, want) {
			t.Fatalf("wrong stock item, paths, lifetime or XP property: %#v", row)
		}
	}
	modes, ok := hosted.document["gm"].([]any)
	if !ok || len(modes) == 0 {
		t.Fatal("non-null supported mode list required by post-match UI")
	}
	if hosted.document["bes"].(map[string]any)["e"] != false {
		t.Fatal("do not enable an invented seasonal event")
	}
	batches := s.configPushBatches(map[string]any{"Boosters": map[string]any{sioConfigHashKey: ""}})
	if len(batches) != 1 || len(batches[0].documents) != 0 {
		t.Fatal("Boosters must remain inline JSON")
	}
	entry := batches[0].entries["Boosters"].(map[string]any)
	body, ok := entry[sioPushBodyKey].(string)
	if !ok {
		t.Fatal("expected JSON string")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, hosted.document) || entry[sioPushHashKey] != hosted.hash {
		t.Fatal("document/hash mismatch")
	}
}
