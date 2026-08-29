// SPDX-License-Identifier: GPL-3.0-or-later

package api

// Tests for the hosted CommandsWithFullSlots document.
//
// Every expectation here is pinned to the document measured off the device on
// 2026-08-29 by hooking the client's own parser, not to whatever this server
// finds convenient to send. See slot_config.go.

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// clientCachedFullSlotsHash is the hash the client reported for its own stale
// embedded copy in the captured poll.
const clientCachedFullSlotsHash = "23fe54a91b31bc6c"

// measuredFullSlots is the document the client's parser actually received,
// transcribed verbatim from the device dump.
const measuredFullSlots = `{"16.2.2":{"slots":[2,7,14,21],"commands":{}},"16.2.1":{"slots":[2,7,14,21],"commands":{}},"16.2.0":{"slots":[2,7,14,21],"commands":{}},"16.1.2":{"slots":[2,7,14,21],"commands":{}},"16.1.1":{"slots":[2,7,14,21],"commands":{}},"16.1.0":{"slots":[2,7,14,21],"commands":{}},"16.0.6":{"slots":[2,7,14],"commands":{}},"16.0.5":{"slots":[2,7,14],"commands":{}},"16.0.4":{"slots":[2,7,14],"commands":{}},"16.0.3":{"slots":[2,7,14],"commands":{}},"16.0.2":{"slots":[2,7,14],"commands":{}},"16.0.1":{"slots":[2,14],"commands":{}},"16.0.0":{"slots":[2],"commands":{}},"15.99.2":{"slots":[2],"commands":{}}}`

// TestMeasuredFullSlotsIsIntact guards the transcription itself: the capture was
// 596 characters, so a literal of any other length was damaged on its way into
// this file and every test below would be measuring the wrong thing.
func TestMeasuredFullSlotsIsIntact(t *testing.T) {
	if len(measuredFullSlots) != 596 {
		t.Fatalf("the transcribed document is %d characters, want the measured 596", len(measuredFullSlots))
	}
}

// TestFullSlotsKeepsEveryMeasuredVersion pins that serving this document does
// not quietly drop what the client shipped with.
func TestFullSlotsKeepsEveryMeasuredVersion(t *testing.T) {
	var measured map[string]fullSlotsEntry
	if err := json.Unmarshal([]byte(measuredFullSlots), &measured); err != nil {
		t.Fatalf("the measured document does not parse: %v", err)
	}

	served := fullSlotsVersions()
	for version, want := range measured {
		got, ok := served[version]
		if !ok {
			t.Fatalf("version %s is in the client's copy but not in the served one", version)
		}
		if len(got.Slots) != len(want.Slots) {
			t.Fatalf("version %s serves slots %v, want the measured %v", version, got.Slots, want.Slots)
		}
		for i := range want.Slots {
			if got.Slots[i] != want.Slots[i] {
				t.Fatalf("version %s serves slots %v, want the measured %v", version, got.Slots, want.Slots)
			}
		}
	}
	if len(served) != len(measured)+1 {
		t.Fatalf("the served document has %d versions, want the measured %d plus the served build", len(served), len(measured))
	}
}

// TestFullSlotsAnswersTheServedBuild is the blocker itself, expressed as a
// test: the client looks 23.1.3 up in this document and logs
// "versionDict not contains version 23.1.3" when the lookup misses.
func TestFullSlotsAnswersTheServedBuild(t *testing.T) {
	entry, ok := fullSlotsVersions()[sioClientVersion]
	if !ok {
		t.Fatalf("the served document has no %s entry, which is the whole reason it is hosted", sioClientVersion)
	}
	if len(entry.Slots) == 0 {
		t.Fatal("the served build must carry slots; an empty list is not what any measured version holds")
	}
	if entry.Commands == nil {
		t.Fatal("commands must be an empty object, not null: null is a different document")
	}

	body, _, ok := fullSlotsPayload()
	if !ok {
		t.Fatal("the document did not render")
	}
	if !strings.Contains(body, `"`+sioClientVersion+`":{"slots":[2,7,14,21],"commands":{}}`) {
		t.Fatalf("the served build renders unlike every measured entry: %s", body)
	}
	if strings.Contains(body, "null") {
		t.Fatalf("the rendered document contains null, which no measured entry does: %s", body)
	}
}

// TestFullSlotsHashIsStableAndNotTheClientsOwn covers both halves of the poll
// contract: the hash has to differ from the stale one the client holds, or the
// document is never fetched, and it has to be stable, or every poll hands the
// same document over again forever.
func TestFullSlotsHashIsStableAndNotTheClientsOwn(t *testing.T) {
	_, hash, ok := fullSlotsPayload()
	if !ok {
		t.Fatal("the document did not render")
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(hash) {
		t.Fatalf("hash %q is not the 16 lowercase hex digits every hash in the captured poll uses", hash)
	}
	if hash == clientCachedFullSlotsHash {
		t.Fatal("the served hash equals the stale one the client holds, so it would never fetch the document")
	}
	if _, again, _ := fullSlotsPayload(); again != hash {
		t.Fatalf("hash changed between calls (%s then %s); the client would refetch on every poll", hash, again)
	}
}

// TestSubscribeServesTheStaleSlotDocument is the end-to-end shape of the reply,
// including that an unhosted config is still only confirmed rather than
// invented.
func TestSubscribeServesTheStaleSlotDocument(t *testing.T) {
	s, _ := newTestServer(t)
	session := &sioSession{sid: "0123456789abcdef01234567"}
	head := sioHead{
		sioRequestIDKey: float64(2),
		"list": map[string]any{
			sioFullSlotsConfig: map[string]any{sioConfigHashKey: clientCachedFullSlotsHash, "b": float64(0)},
			"Ads":              map[string]any{sioConfigHashKey: "aec53fac26135f4c", "b": float64(0)},
		},
	}

	body := s.answerSubscribe(session, head, 2)
	list, ok := body["list"].(map[string]any)
	if !ok {
		t.Fatalf("the reply carries no config list: %v", body)
	}

	ads, ok := list["Ads"].(map[string]any)
	if !ok {
		t.Fatalf("Ads is missing from the answer: %v", list)
	}
	if _, sent := ads[sioConfigBodyKey]; sent {
		t.Fatal("a config this server does not host must be confirmed, never sent a body")
	}
	if ads[sioConfigHashKey] != "aec53fac26135f4c" {
		t.Fatalf("Ads hash is %v, want the client's own echoed back", ads[sioConfigHashKey])
	}

	slots, ok := list[sioFullSlotsConfig].(map[string]any)
	if !ok {
		t.Fatalf("%s is missing from the answer: %v", sioFullSlotsConfig, list)
	}
	if slots[sioConfigHashKey] == clientCachedFullSlotsHash {
		t.Fatal("the stale hash was echoed back, which tells the client its outdated copy is current")
	}
	document, ok := slots[sioConfigBodyKey].(map[string]any)
	if !ok {
		t.Fatalf("the document body is %T, want the nested object the client accepts", slots[sioConfigBodyKey])
	}
	if _, ok := document[sioClientVersion]; !ok {
		t.Fatalf("the served body has no %s entry: %v", sioClientVersion, document)
	}
	if slots[sioConfigVersionKey] != fullSlotsVersion {
		t.Fatalf("the entry declares version %v, want %d", slots[sioConfigVersionKey], fullSlotsVersion)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("the served body does not render: %v", err)
	}
	var parsed map[string]fullSlotsEntry
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("the served body is not a version dictionary: %v", err)
	}
	if len(parsed[sioClientVersion].Slots) == 0 {
		t.Fatalf("the served %s entry carries no slots: %s", sioClientVersion, raw)
	}
}

// TestSubscribeConfirmsAnAlreadyCurrentSlotDocument pins the second poll. Once
// the client has cached this copy it reports the served hash, and handing the
// same document over again would make every poll heavier for no reason.
func TestSubscribeConfirmsAnAlreadyCurrentSlotDocument(t *testing.T) {
	s, _ := newTestServer(t)
	_, hash, ok := fullSlotsPayload()
	if !ok {
		t.Fatal("the document did not render")
	}
	session := &sioSession{sid: "0123456789abcdef01234567"}
	head := sioHead{
		sioRequestIDKey: float64(2),
		"list": map[string]any{
			sioFullSlotsConfig: map[string]any{sioConfigHashKey: hash, "b": float64(0)},
		},
	}

	body := s.answerSubscribe(session, head, 2)
	list, _ := body["list"].(map[string]any)
	entry, ok := list[sioFullSlotsConfig].(map[string]any)
	if !ok {
		t.Fatalf("%s is missing from the answer: %v", sioFullSlotsConfig, list)
	}
	if _, sent := entry[sioConfigBodyKey]; sent {
		t.Fatal("a copy the client already holds must be confirmed, not sent again")
	}
	if entry[sioConfigHashKey] != hash {
		t.Fatalf("hash is %v, want the served %s", entry[sioConfigHashKey], hash)
	}
}
