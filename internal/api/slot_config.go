// SPDX-License-Identifier: GPL-3.0-or-later

package api

// The one config document this server hosts itself: CommandsWithFullSlots.
//
// This is the document whose staleness held the client at 79% of the loading
// bar. Its shape was not guessed. A trace hooked into the client's own parser -
// Progress.<class>.<parser>, dump2313.cs:284933, RVA 0x1B3F618, declared
// `private void parse(string document)` - printed and saved the document the
// client actually reads, measured on 2026-08-29:
//
//	{"16.2.2":{"slots":[2,7,14,21],"commands":{}},
//	 "16.2.1":{"slots":[2,7,14,21],"commands":{}},
//	 ...
//	 "16.0.2":{"slots":[2,7,14],"commands":{}},
//	 "16.0.1":{"slots":[2,14],"commands":{}},
//	 "16.0.0":{"slots":[2],"commands":{}},
//	 "15.99.2":{"slots":[2],"commands":{}}}
//
// 596 characters, fourteen versions from 15.99.2 to 16.2.2, every entry
// carrying exactly two members. That root object is the versionDict the client
// names in its own failure:
//
//	ParseFullSlotConfig: versionDict not contains version 23.1.3
//
// So nothing is wrong with the client. The copy shipped inside its APK simply
// stops several releases before the build this deployment serves, the lookup
// misses, and the last of the five checks in that parser fails. A stale
// embedded document can only be replaced by a server, which is what this file
// makes this server do.
//
// The body travels as a nested JSON object. Text was the first attempt, on the
// strength of the client's cache plaintext h_<16 hex>_c_<json> and a parser
// that takes a single string, and the device refuted it - see fullSlotsObject
// for the timestamps that did it. The cache plaintext is evidently something
// the client builds after re-serialising the object, not a copy of the wire
// format.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const (
	// sioFullSlotsConfig is the name the client polls this document under, seen
	// verbatim in the captured poll:
	// "CommandsWithFullSlots":{"h":"23fe54a91b31bc6c","b":0}
	sioFullSlotsConfig = "CommandsWithFullSlots"

	// sioClientVersion is the build this deployment exists to serve. The client
	// looks this exact string up in the document and names it when it fails.
	sioClientVersion = "23.1.3"

	// fullSlotsVersion is the document version the answer declares.
	//
	// A config entry has three keys in this protocol - h, c and v - and the
	// device refuted an answer carrying only the first two, in both spellings
	// of the body: text at 09:19:59.369 and a nested object at 09:24:35.790,
	// each followed 3.5 seconds later by the client parsing its own embedded
	// copy again. A version-gated consumer, which is what a v key next to a
	// body usually means, would discard exactly that way, so the answer now
	// declares one.
	fullSlotsVersion = 1
)

// fullSlotsEntry is one version's worth of the document.
//
// Commands is a map rather than a struct because every measured entry carries
// it as an empty object, and nothing is yet known about what a populated one
// would hold. It is never nil, so it renders as {} rather than null: null is a
// different document from the one the client was measured to accept.
type fullSlotsEntry struct {
	Slots    []int          `json:"slots"`
	Commands map[string]any `json:"commands"`
}

// fullSlotsVersions is the measured document with the served build added.
//
// The historical entries are kept verbatim rather than trimmed to the single
// version this server needs. They are what the client shipped with, they cost
// well under a kilobyte, and a client that ever looks an older build up finds
// the same answer it always did.
//
// The served build inherits the newest measured shape. Across the captured
// releases the list only ever grows - 16.0.0 [2], 16.0.1 [2 14], 16.0.2
// [2 7 14], 16.1.0 [2 7 14 21] - and never shrinks, so for a later build the
// newest measured value is the one defensible choice.
func fullSlotsVersions() map[string]fullSlotsEntry {
	document := map[string]fullSlotsEntry{}
	add := func(slots []int, versions ...string) {
		for _, version := range versions {
			document[version] = fullSlotsEntry{Slots: slots, Commands: map[string]any{}}
		}
	}
	add([]int{2}, "15.99.2", "16.0.0")
	add([]int{2, 14}, "16.0.1")
	add([]int{2, 7, 14}, "16.0.2", "16.0.3", "16.0.4", "16.0.5", "16.0.6")
	add([]int{2, 7, 14, 21}, "16.1.0", "16.1.1", "16.1.2", "16.2.0", "16.2.1", "16.2.2")
	add([]int{2, 7, 14, 21}, sioClientVersion)
	return document
}

// fullSlotsPayload renders the document and the hash that identifies it.
//
// The hash is derived from the body, so it is stable across restarts: a client
// that has already cached this copy gets told it is current instead of being
// handed the same document again on every poll. Sixteen hex digits is the width
// every hash in the captured poll uses.
func fullSlotsPayload() (body string, hash string, ok bool) {
	raw, err := json.Marshal(fullSlotsVersions())
	if err != nil {
		return "", "", false
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:8]), true
}

// hostedConfigReply builds the poll entry for one config, or returns nil when
// this server does not host that document and the client's own copy has to be
// confirmed as it stands.
//
// The second return reports whether the reply carries a body, which is what the
// poll log counts as served.
//
// Which documents exist is not a question this function answers any more: it
// asks hostedConfigDocument, which reads content/sio-configs first and falls
// back to the one document that is built in. See config_documents.go for the
// run-56 measurement that made the missing documents files.
func (s *Server) hostedConfigReply(name, cachedHash string) (map[string]any, bool) {
	hosted, ok := s.hostedConfigDocument(name)
	if !ok {
		return nil, false
	}
	if cachedHash == hosted.hash {
		return map[string]any{sioConfigHashKey: hosted.hash}, false
	}
	return map[string]any{
		sioConfigHashKey:    hosted.hash,
		sioConfigBodyKey:    hosted.document,
		sioConfigVersionKey: hostedConfigVersion,
	}, true
}

// fullSlotsObject re-reads the rendered document so the reply can carry it as a
// nested object.
//
// Measured 2026-08-29: the text form is not what the client accepts. This
// server answered the poll with c as a JSON string at 09:19:59.369; 3.5 seconds
// later the client's parser still received its own 596-character embedded copy
// and logged the same "versionDict not contains version 23.1.3", and nothing
// was written to its config cache. The entry was read and discarded, so the
// spelling of the body is what has to change.
//
// The hash stays derived from the canonical text, which keeps it stable however
// the body is spelled on the wire.
func fullSlotsObject(body string) (map[string]any, bool) {
	var document map[string]any
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		return nil, false
	}
	return document, true
}
