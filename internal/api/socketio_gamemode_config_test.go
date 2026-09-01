// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

var shippingGameModeEventConfigs = []string{
	"GameModeEvents",
	"CompetionGameModeEvents",
	"RotationGameModeEvents",
}

func TestShippingGameModeEventDocumentsUseExactEvsSchema(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")

	for _, name := range shippingGameModeEventConfigs {
		hosted, ok := s.hostedConfigDocument(name)
		if !ok {
			t.Fatalf("shipping document %s is not hosted", name)
		}
		want := fmt.Sprintf(`{"configId":"%s","evs":[]}`, name)
		if got := string(hosted.body); got != want {
			t.Fatalf("%s body = %s, want exact stock schema %s", name, got, want)
		}
		if _, wrong := hosted.document["events"]; wrong {
			t.Fatalf("%s still carries the rejected root key events", name)
		}
		events, ok := hosted.document["evs"].([]any)
		if !ok || len(events) != 0 {
			t.Fatalf("%s evs = %#v, want a valid empty List", name, hosted.document["evs"])
		}
	}
}

func TestGameModeEventsAreSelectivelyInline(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = t.TempDir()

	list := map[string]any{}
	for _, name := range shippingGameModeEventConfigs {
		hostDocument(t, cfg.ContentDir, name, fmt.Sprintf(`{"configId":"%s","evs":[]}`, name))
		list[name] = map[string]any{sioConfigHashKey: ""}
	}
	const binaryName = "MeasuredBinaryConfig"
	hostDocument(t, cfg.ContentDir, binaryName, `{"configId":"MeasuredBinaryConfig"}`)
	list[binaryName] = map[string]any{sioConfigHashKey: ""}

	batches := s.configPushBatches(list)
	if len(batches) != 1 {
		t.Fatalf("built %d batches, want one deterministic mixed push", len(batches))
	}
	batch := batches[0]
	if len(batch.entries) != 4 || len(batch.documents) != 1 {
		t.Fatalf("mixed push has %d names and %d binary documents, want 4 and 1",
			len(batch.entries), len(batch.documents))
	}
	binary := batch.entries[binaryName].(map[string]any)
	if binary[sioPushBodyKey] != 0 {
		t.Fatalf("unmeasured config val = %#v, want binary attachment index 0", binary[sioPushBodyKey])
	}

	for _, name := range shippingGameModeEventConfigs {
		entry := batch.entries[name].(map[string]any)
		text, ok := entry[sioPushBodyKey].(string)
		if !ok {
			t.Fatalf("%s val is %T, want inline JSON text", name, entry[sioPushBodyKey])
		}
		var document map[string]any
		if err := json.Unmarshal([]byte(text), &document); err != nil {
			t.Fatalf("%s inline body: %v", name, err)
		}
		if document["configId"] != name || !reflect.DeepEqual(document["evs"], []any{}) {
			t.Fatalf("%s inline document = %#v", name, document)
		}
	}
}

func TestConfigPushDoesNotInjectUnpolledCompanions(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = t.TempDir()
	for _, name := range shippingGameModeEventConfigs {
		hostDocument(t, cfg.ContentDir, name, fmt.Sprintf(`{"configId":"%s","evs":[]}`, name))
	}

	batches := s.configPushBatches(map[string]any{
		"GameModeEvents": map[string]any{sioConfigHashKey: ""},
	})
	if len(batches) != 1 || len(batches[0].entries) != 1 {
		t.Fatalf("push injected a document the client did not poll: %#v", batches)
	}
	if _, ok := batches[0].entries["GameModeEvents"]; !ok {
		t.Fatalf("the one polled name was not pushed: %#v", batches[0].entries)
	}
}
