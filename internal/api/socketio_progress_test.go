// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

// clientInitProgressSnapshot is the shape run 48 captured on the device: slots
// indexed by number, alongside the client's own non-numeric version marker.
func clientInitProgressSnapshot() map[string]any {
	return map[string]any{
		"1":            map[string]any{"n": ""},
		"4":            map[string]any{"l": float64(1), "e": float64(0)},
		"18":           map[string]any{"c": float64(0), "s": float64(0)},
		"version_slot": "23.1.3",
	}
}

func authorisedSession(sid, playerID string) *sioSession {
	session := &sioSession{sid: sid}
	session.setIdentity(playerID, "device", playerID)
	return session
}

// TestInitProgressAnswersWithADictionaryDocument is the 87% stall, pinned.
//
// The client enters its applier on the presence of this member alone and casts
// it without checking, so a scalar here reaches the applier as null and throws
// a NullReferenceException that kills the boot coroutine outright.
func TestInitProgressAnswersWithADictionaryDocument(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")
	head := sioHead{
		sioRequestIDKey: float64(17),
		"hash":          "",
		"progress":      clientInitProgressSnapshot(),
	}

	body := s.answerInitProgress(session, head, 17)

	document, ok := body[sioProgressDocumentKey].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want the dictionary the client casts it to", sioProgressDocumentKey, body[sioProgressDocumentKey])
	}
	if len(document) != len(clientInitProgressSnapshot()) {
		t.Fatalf("answered %d slots, want the %d that were sent up", len(document), len(clientInitProgressSnapshot()))
	}
	if _, refused := body["err_code"]; refused {
		t.Fatal("err_code is present, and the client returns on it before it ever reads the document")
	}
	if body[sioRequestIDKey] != 17 {
		t.Fatalf("req_id is %v, so nothing completes the client's pending request", body[sioRequestIDKey])
	}
}

// TestInitProgressStoresTheSnapshot pins that the server owns the document
// afterwards: a second boot that sends nothing is still answered from disk.
func TestInitProgressStoresTheSnapshot(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")

	s.answerInitProgress(session, sioHead{
		sioRequestIDKey: float64(17),
		"progress":      clientInitProgressSnapshot(),
	}, 17)

	profile, err := s.st.Profile("918930694")
	if err != nil {
		t.Fatalf("the snapshot did not reach a profile: %v", err)
	}
	if got := profile.ProgressDocument["version_slot"]; got != "23.1.3" {
		t.Fatalf("stored version_slot is %v, want the client's own 23.1.3", got)
	}
	if _, mixed := profile.Progress["version_slot"]; mixed {
		t.Fatal("the boot document leaked into the flat counters register_push writes, and the applier reads every member of it as a slot")
	}

	second := s.answerInitProgress(session, sioHead{sioRequestIDKey: float64(3)}, 3)
	document, ok := second[sioProgressDocumentKey].(map[string]any)
	if !ok || len(document) == 0 {
		t.Fatalf("a boot that sent no snapshot was answered %v, want the stored document", second[sioProgressDocumentKey])
	}
}

// TestInitProgressBeforeAuthStillAnswersADictionary covers the ordering this
// server does not control. An empty document is still a document, and it is
// what keeps the client's unchecked cast alive.
func TestInitProgressBeforeAuthStillAnswersADictionary(t *testing.T) {
	s, _ := newTestServer(t)
	body := s.answerInitProgress(&sioSession{sid: "0123456789abcdef01234567"}, sioHead{sioRequestIDKey: float64(17)}, 17)
	if _, ok := body[sioProgressDocumentKey].(map[string]any); !ok {
		t.Fatalf("%s is %T; even an empty answer has to survive the cast", sioProgressDocumentKey, body[sioProgressDocumentKey])
	}
}
