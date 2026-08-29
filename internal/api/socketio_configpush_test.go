// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

// TestConfigPushCarriesNoReplyEnvelope pins the second defect run 48 exposed.
//
// The config system reads every top-level key of a push as a config name, so
// the reply envelope this server uses elsewhere turned into three configs it
// had never heard of and not one document was ingested.
func TestConfigPushCarriesNoReplyEnvelope(t *testing.T) {
	entries, documents := configPushArgument(map[string]any{
		sioFullSlotsConfig: map[string]any{sioConfigHashKey: clientCachedFullSlotsHash},
		"Ads":              map[string]any{sioConfigHashKey: "aec53fac26135f4c"},
	})

	if len(documents) != 1 {
		t.Fatalf("pushed %d bodies, want the one document this server hosts", len(documents))
	}
	for _, wrapper := range []string{"error", "result", "list"} {
		if _, present := entries[wrapper]; present {
			t.Fatalf("%q sits at the top level, where the client reads it as a config name", wrapper)
		}
	}
	entry, ok := entries[sioFullSlotsConfig].(map[string]any)
	if !ok {
		t.Fatalf("%s is not named at the top level: %v", sioFullSlotsConfig, entries)
	}
	if entry[sioPushBodyKey] != 0 {
		t.Fatalf("the entry indexes attachment %v, want 0", entry[sioPushBodyKey])
	}
	if hash, named := entry[sioPushHashKey].(string); !named || hash == "" {
		t.Fatalf("the entry names no body hash under %q: %v", sioPushHashKey, entry)
	}
	for _, polled := range []string{sioConfigHashKey, sioConfigBodyKey, sioConfigVersionKey} {
		if _, present := entry[polled]; present {
			t.Fatalf("%q is the poll's spelling, which the push handler never reads", polled)
		}
	}
	if _, named := entries["Ads"]; named {
		t.Fatal("a config this server does not host must not be named in a push")
	}
}
