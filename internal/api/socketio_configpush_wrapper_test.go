// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestConfigPushWrapperStatesTheDocumentUnderTheCacheKey pins the record shape
// run 75 measured the client's reader consulting.
//
// The reader resolves a pushed name to an entry that holds two members, both
// spelled in the cache vocabulary: TopUp is {c: "", h: "5181351cf6887c00"} and
// Boosters is {c: "", h: "c5078ace838a9a63"}. The fingerprints are this
// server's own, so the handler read the record and wrote the entry, and the
// text is String.Empty because a bare document has no c member for it to copy.
func TestConfigPushWrapperStatesTheDocumentUnderTheCacheKey(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ConfigPushWrapper = true

	batches := s.configPushBatches(map[string]any{
		sioFullSlotsConfig: map[string]any{sioConfigHashKey: ""},
	})
	if len(batches) != 1 {
		t.Fatalf("built %d pushes, want the one this server has a body for", len(batches))
	}
	if len(batches[0].documents) != 1 {
		t.Fatalf("pushed %d bodies, want one", len(batches[0].documents))
	}

	record := string(batches[0].documents[0])

	entry, named := batches[0].entries[sioFullSlotsConfig].(map[string]any)
	if !named {
		t.Fatalf("%s is not named at the top level: %v", sioFullSlotsConfig, batches[0].entries)
	}
	hash, _ := entry[sioPushHashKey].(string)
	if hash == "" {
		t.Fatal("the entry states no fingerprint; the handler compares it before it takes the text")
	}

	opening := cacheRecordHashPrefix + hash + cacheRecordBodySeparator
	if !strings.HasPrefix(record, opening) {
		t.Fatalf("the record reads %.48q, want the cache plaintext opening %q", record, opening)
	}

	var document map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(record, opening)), &document); err != nil {
		t.Fatalf("the text after %q is not the document itself: %v", opening, err)
	}
	if len(document) == 0 {
		t.Fatal("the wrapped document is empty, which is the String.Empty run 75 read back")
	}
}

// TestConfigPushCarriesTheBareDocumentByDefault keeps the wrapper diagnostic.
//
// Every measurement from run 57 onwards was taken against the bare document, so
// the default has to keep producing it or the runs stop comparing.
func TestConfigPushCarriesTheBareDocumentByDefault(t *testing.T) {
	s, _ := newTestServer(t)

	batches := s.configPushBatches(map[string]any{
		sioFullSlotsConfig: map[string]any{sioConfigHashKey: ""},
	})
	if len(batches) != 1 || len(batches[0].documents) != 1 {
		t.Fatalf("built %d pushes, want one carrying one body", len(batches))
	}

	var document map[string]any
	if err := json.Unmarshal(batches[0].documents[0], &document); err != nil {
		t.Fatalf("the pushed body is not a JSON object: %v", err)
	}
	if _, wrapped := document[sioConfigBodyKey]; wrapped {
		t.Fatalf("the default push carries the cache record rather than the document: %v", document)
	}
}

// TestConfigPushInlineStatesTheDocumentInTheEntry pins the inline push.
//
// Every run from 57 to 78 sent the body as an attachment and named it with an
// integer index, and the client's reader answered String.Empty for every hosted
// name in every one of them. With the flag on the text travels in the entry and
// no attachment is sent at all, so a handler that reads the member as a string
// has the document in hand.
func TestConfigPushInlineStatesTheDocumentInTheEntry(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ConfigPushInline = true

	batches := s.configPushBatches(map[string]any{
		sioFullSlotsConfig: map[string]any{sioConfigHashKey: ""},
	})
	if len(batches) != 1 {
		t.Fatalf("built %d pushes, want the one this server has a body for", len(batches))
	}
	if len(batches[0].documents) != 0 {
		t.Fatalf("the inline push still carries %d attachment(s); the text belongs in the entry",
			len(batches[0].documents))
	}

	entry, named := batches[0].entries[sioFullSlotsConfig].(map[string]any)
	if !named {
		t.Fatalf("%s is not named at the top level: %v", sioFullSlotsConfig, batches[0].entries)
	}
	text, stated := entry[sioPushBodyKey].(string)
	if !stated {
		t.Fatalf("the entry states %T under %q, want the document text",
			entry[sioPushBodyKey], sioPushBodyKey)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(text), &document); err != nil {
		t.Fatalf("the text under %q is not the document itself: %v", sioPushBodyKey, err)
	}
	if hash, _ := entry[sioPushHashKey].(string); hash == "" {
		t.Fatal("the entry states no fingerprint; the handler compares it before it takes the text")
	}
}

// TestConfigPushSendsAnAttachmentByDefault keeps the inline push diagnostic.
func TestConfigPushSendsAnAttachmentByDefault(t *testing.T) {
	s, _ := newTestServer(t)

	batches := s.configPushBatches(map[string]any{
		sioFullSlotsConfig: map[string]any{sioConfigHashKey: ""},
	})
	if len(batches) != 1 || len(batches[0].documents) != 1 {
		t.Fatalf("built %d pushes, want one carrying one attachment", len(batches))
	}
	entry, _ := batches[0].entries[sioFullSlotsConfig].(map[string]any)
	if _, indexed := entry[sioPushBodyKey].(int); !indexed {
		t.Fatalf("the default entry states %T under %q, want the attachment index",
			entry[sioPushBodyKey], sioPushBodyKey)
	}
}
