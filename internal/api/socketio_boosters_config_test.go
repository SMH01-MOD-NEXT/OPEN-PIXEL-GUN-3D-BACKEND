// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestShippingBoostersUsesInlineJSONWithoutChangingOtherContracts(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")
	hosted, ok := s.hostedConfigDocument("Boosters")
	if !ok {
		t.Fatal("shipping Boosters is not hosted")
	}
	for _, key := range []string{"bs", "gm"} {
		if !reflect.DeepEqual(hosted.document[key], []any{}) {
			t.Fatalf("%s must be a non-null empty list, got %#v", key, hosted.document[key])
		}
	}
	batches := s.configPushBatches(map[string]any{"Boosters": map[string]any{sioConfigHashKey: ""}})
	if len(batches) != 1 || len(batches[0].entries) != 1 || len(batches[0].documents) != 0 {
		t.Fatalf("Boosters must be one inline entry, got %#v", batches)
	}
	entry := batches[0].entries["Boosters"].(map[string]any)
	body, ok := entry[sioPushBodyKey].(string)
	if !ok {
		t.Fatalf("Boosters val is %T, want JSON text", entry[sioPushBodyKey])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, hosted.document) {
		t.Fatal("inline body must preserve the hosted document")
	}
	if entry[sioPushHashKey] != hosted.hash {
		t.Fatal("fingerprint must describe the inline document")
	}
	for _, name := range []string{"TopUp", "CommandsWithFullSlots", "AssetBundlesAndroid", "UnknownConfig"} {
		if configPushUsesInlineBody(name) {
			t.Fatalf("unmeasured %s must retain binary delivery", name)
		}
	}
}

func TestBoostersIsNotPushedUnlessPolled(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")
	if batches := s.configPushBatches(map[string]any{}); len(batches) != 0 {
		t.Fatalf("empty poll unexpectedly produced %#v", batches)
	}
}
