// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestShippingVeteranChestPresentation(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")
	hosted, ok := s.hostedConfigDocument("LootBoxes")
	if !ok {
		t.Fatal("LootBoxes must be hosted")
	}
	rows, ok := hosted.document["d"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("unexpected chest definitions: %#v", hosted.document["d"])
	}
	row := rows[0].(map[string]any)
	if row["i"] != float64(1027) || row["t"] != float64(1) || row["tag"] != "LootBox_Veteran_Red" {
		t.Fatalf("wrong veteran chest mapping: %#v", row)
	}
	batches := s.configPushBatches(map[string]any{"LootBoxes": map[string]any{sioConfigHashKey: ""}})
	if len(batches) != 1 || len(batches[0].documents) != 0 {
		t.Fatalf("expected inline JSON batch: %#v", batches)
	}
	entry := batches[0].entries["LootBoxes"].(map[string]any)
	body, ok := entry[sioPushBodyKey].(string)
	if !ok {
		t.Fatalf("body type %T, want string", entry[sioPushBodyKey])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, hosted.document) || entry[sioPushHashKey] != hosted.hash {
		t.Fatal("push must preserve document and fingerprint")
	}
	if len(s.configPushBatches(map[string]any{})) != 0 {
		t.Fatal("do not push unsolicited configs")
	}
	for _, name := range []string{"LootBoxesProviderClan", "AssetBundlesAndroid", "TopUp"} {
		if configPushUsesInlineBody(name) {
			t.Fatalf("unverified %s delivery changed", name)
		}
	}
}

// Stock 23.1.3 selects one item per nonempty Item.LootGroup. Separate
// group names turn display alternatives into cumulative rewards.
func TestShippingVeteranSingleRewardRoulette(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")
	boxes, ok := s.hostedConfigDocument("LootBoxes")
	if !ok {
		t.Fatal("missing presentation")
	}
	box := boxes.document["d"].([]any)[0].(map[string]any)
	if box["o"] != float64(2) || box["bo"] != false || box["ao"] != false {
		t.Fatalf("require stock Roulette=2, manual single opening: %#v", box)
	}
	provider, ok := s.hostedConfigDocument("LootBoxesProviderUniversal")
	if !ok {
		t.Fatal("missing universal provider")
	}
	rates := provider.document["rd"].([]any)
	if len(rates) != 1 {
		t.Fatalf("want one rate, got %d", len(rates))
	}
	rate := rates[0].(map[string]any)
	if rate["in"] != float64(1027) || rate["c"] != float64(1) || rate["ch"] != float64(1) {
		t.Fatalf("one guaranteed veteran draw required: %#v", rate)
	}
	items := provider.document["id"].([]any)
	if len(items) < 2 {
		t.Fatal("roulette must retain alternative prizes")
	}
	group := ""
	for _, v := range items {
		item := v.(map[string]any)
		g, _ := item["rg"].(string)
		if g == "" {
			t.Fatal("empty group uses independent Bernoulli drops")
		}
		if group == "" {
			group = g
		}
		if g != group || item["ri"] != rate["ri"] || item["c"].(float64) <= 0 {
			t.Fatalf("all alternatives must share one rate and weighted pool: %#v", item)
		}
		rewards, ok := item["r"].([]any)
		if !ok || len(rewards) != 1 {
			t.Fatalf("bundled prizes forbidden: %#v", item)
		}
	}
	batches := s.configPushBatches(map[string]any{"LootBoxesProviderUniversal": map[string]any{sioConfigHashKey: ""}})
	if len(batches) != 1 || len(batches[0].documents) != 0 {
		t.Fatal("provider must use verified inline JSON delivery")
	}
	entry := batches[0].entries["LootBoxesProviderUniversal"].(map[string]any)
	body, ok := entry[sioPushBodyKey].(string)
	if !ok {
		t.Fatal("provider body must be JSON string")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, provider.document) || entry[sioPushHashKey] != provider.hash {
		t.Fatal("provider document/hash changed in transit")
	}
}
