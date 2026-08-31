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

// TestProgressSlotsWithoutStoredSlotsOmitsTheMember pins the run-58
// regression: an empty dictionary is not a substitute for having no remote
// slots, because its presence suppresses the client's create_slots upload.
func TestProgressSlotsWithoutStoredSlotsOmitsTheMember(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")

	body := s.answerProgressSlots(session, sioHead{
		sioRequestIDKey: float64(18),
		"player_id":     "918930694",
	}, 18)

	if _, present := body[sioSlotsKey]; present {
		t.Fatalf("the empty-profile reply carries %s, and run 58 measured that as a regression to 87%%", sioSlotsKey)
	}
	if body[sioRequestIDKey] != 18 {
		t.Fatalf("req_id is %v, so nothing completes the client's pending request", body[sioRequestIDKey])
	}
	if body["error"] != sioErrNone {
		t.Fatalf("error is %v, want the plain acknowledgement", body["error"])
	}
}

// TestProgressSlotsServesTheStoredNonEmptyDictionary covers the second boot:
// the client has already uploaded its slot set, and the run-75 callback can now
// turn the returned dictionary keys into its remote-slot HashSet.
func TestProgressSlotsServesTheStoredNonEmptyDictionary(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")
	uploaded := clientSlotUpload()

	s.answerCreateSlots(session, sioHead{
		sioRequestIDKey: float64(19),
		sioSlotsKey:     uploaded,
	}, 19)
	body := s.answerProgressSlots(session, sioHead{
		sioRequestIDKey: float64(18),
		"player_id":     "918930694",
	}, 18)

	slots, ok := body[sioSlotsKey].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want the Dictionary<string, object> wire shape", sioSlotsKey, body[sioSlotsKey])
	}
	if len(slots) != len(uploaded) {
		t.Fatalf("served %d slots, want %d stored slots", len(slots), len(uploaded))
	}
	for id := range uploaded {
		if _, present := slots[id]; !present {
			t.Fatalf("stored slot %s was not served", id)
		}
	}
}

// TestCreateSlotsStoresTheUploadWithoutEchoingIt pins both halves: the server
// owns the slots afterwards, and the wire is unchanged from run 57.
func TestCreateSlotsStoresTheUploadWithoutEchoingIt(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")

	body := s.answerCreateSlots(session, sioHead{
		sioRequestIDKey: float64(19),
		sioSlotsKey:     clientSlotUpload(),
	}, 19)
	// The acknowledgement never echoes the upload back - run 57 measured that
	// the client boots on the bare reply - but it does carry the one section
	// this server authors, the tutorials section, and nothing else.
	if slots, present := body[sioSlotsKey].(map[string]any); present {
		for id := range slots {
			if id != sioTutorialSectionID {
				t.Fatalf("the reply echoes slot %s back; run 57's acknowledgement is what the client boots on", id)
			}
		}
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
}

// TestSlotEventsAreDispatched pins that both events reach their handlers, so
// the uploads are recorded instead of vanishing into the fallback.
func TestSlotEventsAreDispatched(t *testing.T) {
	for _, event := range []string{sioProgressSlotsEvent, sioCreateSlotsEvent} {
		if sioAnswerFor(event) == nil {
			t.Fatalf("%s has no answer builder", event)
		}
	}
}
