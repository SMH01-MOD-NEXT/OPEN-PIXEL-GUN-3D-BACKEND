// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

// clientSlotUpload is the shape run 57 captured on the wire: slots indexed by
// number, each carrying the client's own compact encoding.
func clientSlotUpload() map[string]any {
	return map[string]any{
		"25": map[string]any{"v": map[string]any{"29005": float64(0), "138005": float64(0)}},
		"26": map[string]any{"c": map[string]any{}, "g": float64(0)},
		"27": map[string]any{},
		"35": []any{},
	}
}

// TestProgressSlotsAlwaysAnswersADictionary is the 90% stall, pinned. A first
// boot owns no slots yet, and the member still has to be there: the client
// indexes it out of the reply before it looks at anything else.
func TestProgressSlotsAlwaysAnswersADictionary(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")

	body := s.answerProgressSlots(session, sioHead{
		sioRequestIDKey: float64(18),
		"player_id":     "918930694",
	}, 18)

	if _, ok := body[sioSlotsKey].(map[string]any); !ok {
		t.Fatalf("%s is %T, want the dictionary the client casts it to", sioSlotsKey, body[sioSlotsKey])
	}
	if body[sioRequestIDKey] != 18 {
		t.Fatalf("req_id is %v, so nothing completes the client's pending request", body[sioRequestIDKey])
	}
	if _, refused := body["err_code"]; refused {
		t.Fatal("err_code is present, and consumers return on it before reading the document")
	}
}

// TestCreateSlotsStoresAndServesTheUpload pins the round trip: what the client
// uploads is what the next boot is told it has.
func TestCreateSlotsStoresAndServesTheUpload(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")

	created := s.answerCreateSlots(session, sioHead{
		sioRequestIDKey: float64(19),
		sioSlotsKey:     clientSlotUpload(),
	}, 19)
	answered, ok := created[sioSlotsKey].(map[string]any)
	if !ok || len(answered) != len(clientSlotUpload()) {
		t.Fatalf("create_slots answered %v, want the %d slots that were sent up", created[sioSlotsKey], len(clientSlotUpload()))
	}

	profile, err := s.st.Profile("918930694")
	if err != nil {
		t.Fatalf("the upload did not reach a profile: %v", err)
	}
	if len(profile.ProgressSlots) != len(clientSlotUpload()) {
		t.Fatalf("stored %d slots, want %d", len(profile.ProgressSlots), len(clientSlotUpload()))
	}
	if _, mixed := profile.ProgressDocument["25"]; mixed {
		t.Fatal("the slot upload leaked into the init_progress document, which the client applies slot by slot")
	}

	served := s.answerProgressSlots(session, sioHead{sioRequestIDKey: float64(20)}, 20)
	slots, ok := served[sioSlotsKey].(map[string]any)
	if !ok || len(slots) != len(clientSlotUpload()) {
		t.Fatalf("a later boot was served %v, want the stored slots", served[sioSlotsKey])
	}
}

// TestProgressSlotsRefusesAnotherPlayersID covers the field the client fills in
// itself: it names the account, so it cannot be taken at face value.
func TestProgressSlotsRefusesAnotherPlayersID(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")

	body := s.answerProgressSlots(session, sioHead{
		sioRequestIDKey: float64(18),
		"player_id":     "111111111",
	}, 18)

	if body["error"] != sioErrNotAuthorized {
		t.Fatalf("error is %v, want the client's not-authorized code", body["error"])
	}
	if _, leaked := body[sioSlotsKey]; leaked {
		t.Fatal("a refused request still carried a slot document")
	}
}

// TestSlotEventsAreDispatched pins that both events reach their handlers
// instead of the fallback acknowledgement they ran on through run 57.
func TestSlotEventsAreDispatched(t *testing.T) {
	for _, event := range []string{sioProgressSlotsEvent, sioCreateSlotsEvent} {
		if sioAnswerFor(event) == nil {
			t.Fatalf("%s has no answer builder", event)
		}
	}
}
