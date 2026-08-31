// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// hostDocument writes one document into the directory this server hosts from.
func hostDocument(t *testing.T, dir, name, body string) {
	t.Helper()

	full := filepath.Join(dir, hostedConfigDir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatalf("create %s: %v", full, err)
	}
	if err := os.WriteFile(filepath.Join(full, name+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write the %s document: %v", name, err)
	}
}

// TestUncachedConfigIsServedFromAFile is the run-56 stop, from the poll the
// client sends to the answer it reads.
//
// TopUp arrives with an empty hash in every poll of the day, which is the
// client saying it holds no copy; before this server hosted files the name was
// answered with that empty hash echoed back and the consumer logged
// [TopUp] ---- _config == null.
func TestUncachedConfigIsServedFromAFile(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = t.TempDir()
	hostDocument(t, cfg.ContentDir, "TopUp", `{"configId": "TopUp"}`)

	reply, changed := s.hostedConfigReply("TopUp", "")
	if !changed {
		t.Fatalf("the uncached document was not served: %v", reply)
	}
	text, ok := reply[sioConfigBodyKey].(string)
	if !ok {
		t.Fatalf("the body is %T, want the document text the reader reads back", reply[sioConfigBodyKey])
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(text), &document); err != nil {
		t.Fatalf("the served text is not a document: %v", err)
	}
	if document["configId"] != "TopUp" {
		t.Fatalf("the document names itself %v, want TopUp", document["configId"])
	}
	if reply[sioConfigVersionKey] != hostedConfigVersion {
		t.Fatalf("the reply states version %v, want %d", reply[sioConfigVersionKey], hostedConfigVersion)
	}
	hash, ok := reply[sioConfigHashKey].(string)
	if !ok || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(hash) {
		t.Fatalf("hash %v is not the 16 lowercase hex digits every hash in the captured poll uses", reply[sioConfigHashKey])
	}

	// A client that already holds this copy is told so instead of being
	// handed the same document on every poll.
	again, changedAgain := s.hostedConfigReply("TopUp", hash)
	if changedAgain {
		t.Fatal("a cached copy was served again, so the client would refetch on every poll")
	}
	if _, sent := again[sioConfigBodyKey]; sent {
		t.Fatalf("the confirmation carries a body: %v", again)
	}
}

// TestUnhostedConfigIsStillOnlyConfirmed keeps the truthful half of the answer:
// a name with no file and no built-in document gets the client's own hash back,
// never an invented document.
func TestUnhostedConfigIsStillOnlyConfirmed(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = t.TempDir()

	if reply, changed := s.hostedConfigReply("Ads", "aec53fac26135f4c"); reply != nil || changed {
		t.Fatalf("an unhosted config produced %v", reply)
	}
}

// TestHostedConfigFileRefusesNamesThatAddressOtherFiles covers the fact that
// every polled name arrives from the client.
func TestHostedConfigFileRefusesNamesThatAddressOtherFiles(t *testing.T) {
	for _, name := range []string{"", "..", "../.env", "Clan/Common", `Clan\Common`, "Top Up", "Top.Up"} {
		if file, ok := hostedConfigFile(name); ok {
			t.Fatalf("%q was accepted as the file %q", name, file)
		}
	}
	file, ok := hostedConfigFile("ConfrontationEvent")
	if !ok || file != "ConfrontationEvent.json" {
		t.Fatalf("a plain config name mapped to %q (%v)", file, ok)
	}
}

// TestUnparsableDocumentIsNotServed keeps an operator's typo from reaching the
// client as content.
func TestUnparsableDocumentIsNotServed(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = t.TempDir()
	hostDocument(t, cfg.ContentDir, "Boosters", "{ this is not json")

	if _, ok := s.hostedConfigDocument("Boosters"); ok {
		t.Fatal("a document that does not parse here would not parse on the device either")
	}
}

// TestConfigPushSplitsDocumentsAcrossPushes pins the attachment cap.
//
// One push carries two attachments of its own, so the bodies that do not fit
// go out in further pushes rather than being dropped: the client polls once per
// boot, so a document left out of the only push it ever hears is a document it
// never receives.
func TestConfigPushSplitsDocumentsAcrossPushes(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = t.TempDir()

	const extra = 3
	list := make(map[string]any, configPushDocumentLimit+extra)
	for i := 0; i < configPushDocumentLimit+extra; i++ {
		name := fmt.Sprintf("Config%02d", i)
		hostDocument(t, cfg.ContentDir, name, fmt.Sprintf(`{"configId": %q}`, name))
		list[name] = map[string]any{sioConfigHashKey: ""}
	}

	batches := s.configPushBatches(list)
	if len(batches) != 2 {
		t.Fatalf("built %d pushes for %d documents, want 2", len(batches), configPushDocumentLimit+extra)
	}
	if len(batches[0].documents) != configPushDocumentLimit {
		t.Fatalf("the first push carries %d bodies, want the cap of %d", len(batches[0].documents), configPushDocumentLimit)
	}
	if len(batches[1].documents) != extra {
		t.Fatalf("the second push carries %d bodies, want %d", len(batches[1].documents), extra)
	}

	named := map[string]bool{}
	for number, batch := range batches {
		if len(batch.entries) != len(batch.documents) {
			t.Fatalf("push %d names %d configs for %d bodies", number, len(batch.entries), len(batch.documents))
		}
		indexed := map[int]bool{}
		for name, raw := range batch.entries {
			if named[name] {
				t.Fatalf("%s is pushed twice", name)
			}
			named[name] = true

			entry, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("%s carries %T, want an entry", name, raw)
			}
			index, ok := entry[sioPushBodyKey].(int)
			if !ok || index < 0 || index >= len(batch.documents) {
				t.Fatalf("%s indexes attachment %v of push %d, which carries %d bodies", name, entry[sioPushBodyKey], number, len(batch.documents))
			}
			if indexed[index] {
				t.Fatalf("two names index attachment %d of push %d", index, number)
			}
			indexed[index] = true
		}
	}
	if len(named) != configPushDocumentLimit+extra {
		t.Fatalf("%d documents were pushed, want all %d", len(named), configPushDocumentLimit+extra)
	}
}

// TestASaltedFingerprintCannotEqualTheOneTheClientHolds pins the run-64
// measurement: the client skips a delivered document when the fingerprint it
// already holds equals the one being delivered, so a run may state a
// fingerprint the client cannot have stored earlier.
func TestASaltedFingerprintCannotEqualTheOneTheClientHolds(t *testing.T) {
	s, cfg := newTestServer(t)
	body := []byte(`{"configId":"Boosters"}`)

	honest := s.hostedConfigHash(body)

	cfg.ConfigHashNonce = true
	s.configNonce = "run-65"
	salted := s.hostedConfigHash(body)

	if salted == honest {
		t.Fatalf("the salt left the fingerprint unchanged, so the client would skip the body again: %s", salted)
	}
	if again := s.hostedConfigHash(body); again != salted {
		t.Fatalf("the fingerprint moved within one run (%s then %s), so the poll answer and the push would disagree", salted, again)
	}
	if len(salted) != len(honest) {
		t.Fatalf("the client reads a %d character fingerprint, this one is %d", len(honest), len(salted))
	}

	s.configNonce = "run-66"
	if next := s.hostedConfigHash(body); next == salted {
		t.Fatal("two runs stated the same fingerprint, so the second run would be skipped")
	}
}

// TestFullSlotsKeepsTheNestedObjectBody pins which spelling each reader gets.
//
// Runs 128 and 129 measured the document the boot waits on, GameModeEvents,
// being read back as text, and the full slots document was measured on
// 2026-08-29 being read as a nested object and discarded as text.
func TestFullSlotsKeepsTheNestedObjectBody(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = t.TempDir()

	reply, changed := s.hostedConfigReply(sioFullSlotsConfig, "")
	if !changed {
		t.Fatalf("the built-in document was not served: %v", reply)
	}
	if _, ok := reply[sioConfigBodyKey].(map[string]any); !ok {
		t.Fatalf("the full slots body is %T, want the nested object", reply[sioConfigBodyKey])
	}

	hostDocument(t, cfg.ContentDir, "GameModeEvents", `{"configId": "GameModeEvents"}`)
	events, served := s.hostedConfigReply("GameModeEvents", "")
	if !served {
		t.Fatalf("the hosted document was not served: %v", events)
	}
	text, ok := events[sioConfigBodyKey].(string)
	if !ok {
		t.Fatalf("the document body is %T, want the text the reader reads back", events[sioConfigBodyKey])
	}
	if !strings.Contains(text, "GameModeEvents") {
		t.Fatalf("the served text does not carry the document: %q", text)
	}
}
