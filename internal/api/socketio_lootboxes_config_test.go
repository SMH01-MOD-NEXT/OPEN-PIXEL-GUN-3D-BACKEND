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
	for _, name := range []string{"LootBoxesProviderUniversal", "LootBoxesProviderClan", "AssetBundlesAndroid", "TopUp"} {
		if configPushUsesInlineBody(name) {
			t.Fatalf("unverified %s delivery changed", name)
		}
	}
}
