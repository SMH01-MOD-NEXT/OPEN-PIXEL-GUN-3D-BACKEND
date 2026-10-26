// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"reflect"
	"testing"
)

const tutorialResetPlayerID = "740333804"

// firstMatchUpdate is the update_progress a brand-new account sends after its
// first battle (2026-10-26 AWS analysis): the level-up 1 -> 2 (64), an
// analytics event (777), two snapshot children (74), the per-weapon level
// record (139, all 2 in the logged batch) and the FirstMatch completion (253).
func firstMatchUpdate(reqID int) sioHead {
	child := func(id int, ci string, p map[string]any, slot string) map[string]any {
		digest := map[string]any{}
		if slot != "" {
			digest[slot] = "0ada6bbf2ef5ca9603de372cd30c3d3a"
		}
		return map[string]any{"id": float64(id), sioUpdateProgressChildIDKey: ci, "p": p, "h": digest, "v": "23.1.3"}
	}
	tutorial := child(253, "adc3e4ec", map[string]any{"t": float64(1300)}, sioTutorialSectionID)
	tutorial["u"] = []any{float64(140)}
	return sioHead{
		sioRequestIDKey:               float64(reqID),
		"id":                          float64(63),
		sioUpdateProgressCommandIDKey: "33a7714f",
		sioUpdateProgressPayloadKey: map[string]any{
			sioUpdateProgressCommandsKey: []any{
				child(64, "e5efb632", map[string]any{"l": float64(2)}, "4"),
				child(777, "7550aba3", map[string]any{"eid": float64(52), "params": map[string]any{}}, ""),
				child(74, "629012d0", map[string]any{"1": "Coins", "2": "Gems"}, "21"),
				child(139, "15972246", map[string]any{"d": map[string]any{"16001": float64(2), "25001": float64(2), "968001": float64(2)}}, "29"),
				tutorial,
			},
		},
		"new_err": float64(1),
	}
}

func sectionTutorialIDs(t *testing.T, slots map[string]any) []int {
	t.Helper()
	section, ok := slots[sioTutorialSectionID].(map[string]any)
	if !ok {
		t.Fatalf("section %s is %T, want the tutorials section", sioTutorialSectionID, slots[sioTutorialSectionID])
	}
	return tutorialCompletedIDs(section)
}

// TestFirstMatchProgressSurvivesResync is the 2026-10-26 reset end to end: the
// first-match batch must be acknowledged and stored, and the sync_full that
// follows must deliver level 2 together with every completed tutorial id
// instead of the create_slots snapshot (level 1, {"t":[]}).
func TestFirstMatchProgressSurvivesResync(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", tutorialResetPlayerID)
	s.answerCreateSlots(session, sioHead{
		sioRequestIDKey: float64(19),
		sioSlotsKey: map[string]any{
			"3":                  map[string]any{"Coins": float64(0), "GemsCurrency": float64(0)},
			"4":                  map[string]any{"l": float64(1), "e": float64(0)},
			"29":                 map[string]any{},
			sioTutorialSectionID: map[string]any{sioTutorialSectionKey: []any{}},
		},
	}, 19)
	stored, err := s.st.Profile(tutorialResetPlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if got := sectionTutorialIDs(t, stored.ProgressSlots); len(got) != 0 {
		t.Fatalf("precondition: stored tutorial history is %v, want the uploaded empty list the affected accounts hold", got)
	}

	body := s.answerUpdateProgress(session, firstMatchUpdate(20), 20)
	if body[sioUpdateProgressErrCodeKey] != sioErrNone || body[sioUpdateProgressCommandIDKey] != "33a7714f" {
		t.Fatalf("first-match batch was not acknowledged: %v", body)
	}
	stored, err = s.st.Profile(tutorialResetPlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if level, _ := slotCounter(stored.ProgressSlots["4"].(map[string]any)["l"]); stored.Level != 2 || level != 2 {
		t.Fatalf("stored level is %d (slot 4: %d), want the level-up to 2", stored.Level, level)
	}
	if got := sectionTutorialIDs(t, stored.ProgressSlots); !reflect.DeepEqual(got, []int{1300}) {
		t.Fatalf("stored tutorial history is %v, want only the reported completion", got)
	}

	reply := s.answerSyncFull(session, sioHead{
		sioRequestIDKey: float64(21),
		sioSlotsKey: map[string]any{
			"version_slot":       "23.1.3",
			"4":                  "88a4f560d58a9a4b63d567b245c6a3fb",
			sioTutorialSectionID: "0ada6bbf2ef5ca9603de372cd30c3d3a",
		},
	}, 21)
	slots, ok := reply[sioSlotsKey].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want the stored slot document", sioSlotsKey, reply[sioSlotsKey])
	}
	if level, _ := slotCounter(slots["4"].(map[string]any)["l"]); level != 2 {
		t.Fatalf("sync_full delivers level %d, want 2", level)
	}
	completed := sectionTutorialIDs(t, slots)
	for _, want := range []int{1300, sioRanchoTutorialID, sioIntroTutorialID} {
		if !containsTutorialID(completed, want) {
			t.Fatalf("sync_full tutorial history is %v, missing %d", completed, want)
		}
	}
	if len(completed) != 3 {
		t.Fatalf("sync_full tutorial history is %v, want each id once", completed)
	}

	after, err := s.st.Profile(tutorialResetPlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.ProgressSlots, stored.ProgressSlots) || after.ProgressRevision != stored.ProgressRevision {
		t.Fatal("sync_full changed the stored document; the seed belongs to the reply only")
	}
}

// TestSyncFullDoesNotRetractPublishedTutorials covers the accounts that already
// hold the stale section: the reply carries the seed the create_slots
// acknowledgement published, keeps the section's other members and leaves the
// stored copy alone. A document without section 56 stays as stored, which
// TestSyncFullReturnsStoredServerSlots pins with the switch on.
func TestSyncFullDoesNotRetractPublishedTutorials(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", tutorialResetPlayerID)
	held := map[string]any{
		"4":                  map[string]any{"l": float64(1), "e": float64(0)},
		sioTutorialSectionID: map[string]any{sioTutorialSectionKey: []any{}, "future": "kept"},
	}
	if _, err := s.st.MergeProfileProgressSlots(tutorialResetPlayerID, held); err != nil {
		t.Fatal(err)
	}
	before, err := s.st.Profile(tutorialResetPlayerID)
	if err != nil {
		t.Fatal(err)
	}
	sync := func() map[string]any {
		t.Helper()
		reply := s.answerSyncFull(session, sioHead{sioRequestIDKey: float64(17), sioSlotsKey: map[string]any{"version_slot": "23.1.3"}}, 17)
		slots, ok := reply[sioSlotsKey].(map[string]any)
		if !ok {
			t.Fatalf("%s is %T, want the stored slot document", sioSlotsKey, reply[sioSlotsKey])
		}
		return slots
	}

	slots := sync()
	if got := sectionTutorialIDs(t, slots); !reflect.DeepEqual(got, []int{sioRanchoTutorialID, sioIntroTutorialID}) {
		t.Fatalf("sync_full tutorial history is %v, want the published seed", got)
	}
	if slots[sioTutorialSectionID].(map[string]any)["future"] != "kept" {
		t.Fatalf("the section's other members were dropped: %v", slots[sioTutorialSectionID])
	}
	after, err := s.st.Profile(tutorialResetPlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.ProgressSlots, before.ProgressSlots) {
		t.Fatalf("stored slots changed to %v", after.ProgressSlots)
	}

	s.cfg.TutorialSectionViaSyncFull = false
	if slots := sync(); !reflect.DeepEqual(slots, before.ProgressSlots) {
		t.Fatalf("with the switch off sync_full returned %v, want the stored slots %v", slots, before.ProgressSlots)
	}
}
