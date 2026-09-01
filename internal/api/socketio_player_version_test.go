// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

// TestPlayerVersionReplyMatchesTheLiveCallback pins the member name, type and
// correlation id read from the 23.1.3 callback during run72.
func TestPlayerVersionReplyMatchesTheLiveCallback(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")

	body := s.answerPlayerVersion(session, sioHead{sioRequestIDKey: float64(25)}, 25)
	version, ok := body[sioPlayerVersionKey].(string)
	if !ok {
		t.Fatalf("%s is %T; the client casts it to System.String", sioPlayerVersionKey, body[sioPlayerVersionKey])
	}
	if version != sioClientVersion {
		t.Fatalf("%s is %q, want the served client build %q", sioPlayerVersionKey, version, sioClientVersion)
	}
	if body[sioRequestIDKey] != 25 {
		t.Fatalf("req_id is %v, so nothing completes the client's pending request", body[sioRequestIDKey])
	}
}

// TestPlayerVersionUsesTheStoredProgressVersion keeps the response tied to
// server-owned profile state rather than an unrelated release constant.
func TestPlayerVersionUsesTheStoredProgressVersion(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")

	s.answerInitProgress(session, sioHead{
		sioRequestIDKey: float64(17),
		sioProgressDocumentKey: map[string]any{
			"version_slot": "23.1.3-device",
		},
	}, 17)

	body := s.answerPlayerVersion(session, sioHead{sioRequestIDKey: float64(25)}, 25)
	if got := body[sioPlayerVersionKey]; got != "23.1.3-device" {
		t.Fatalf("%s is %v, want the version stored from init_progress", sioPlayerVersionKey, got)
	}
}

// TestPlayerVersionEventHasADedicatedAnswer ensures it cannot silently fall
// back to the bare acknowledgement that run72 proved omits the required key.
func TestPlayerVersionEventHasADedicatedAnswer(t *testing.T) {
	s, _ := newTestServer(t)
	answer := sioAnswerFor(sioPlayerVersionEvent)
	if answer == nil {
		t.Fatal("get_player_version has no answer builder")
	}
	body := answer(s, authorisedSession("0123456789abcdef01234567", "918930694"), sioHead{}, 25)
	if _, ok := body[sioPlayerVersionKey].(string); !ok {
		t.Fatalf("dispatched reply has %T under %s, want System.String", body[sioPlayerVersionKey], sioPlayerVersionKey)
	}
}
