// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

// TestCreateSlotsPublishesTheTutorialSection pins the answer that replaced the
// frontend hook: the acknowledgement of the client's slot upload carries the
// tutorials section, in the section and under the member names read out of the
// live client (section 56, member "t").
func TestCreateSlotsPublishesTheTutorialSection(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")
	head := sioHead{
		sioRequestIDKey: float64(19),
		sioSlotsKey:     map[string]any{"25": map[string]any{"v": map[string]any{}}},
	}

	body := s.answerCreateSlots(session, head, 19)

	slots, ok := body[sioSlotsKey].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want the slot dictionary the client applies", sioSlotsKey, body[sioSlotsKey])
	}
	section, ok := slots[sioTutorialSectionID].(map[string]any)
	if !ok {
		t.Fatalf("section %s is %T, so the boot's tutorial question stays unanswered", sioTutorialSectionID, slots[sioTutorialSectionID])
	}
	completed, ok := section[sioTutorialSectionKey].([]any)
	if !ok {
		t.Fatalf("member %q is %T, want the list of completed tutorial ids", sioTutorialSectionKey, section[sioTutorialSectionKey])
	}
	if len(completed) != 2 || completed[0] != sioRanchoTutorialID || completed[1] != sioIntroTutorialID {
		t.Fatalf("completed ids are %v, want [%d %d]", completed, sioRanchoTutorialID, sioIntroTutorialID)
	}
	if body[sioRequestIDKey] != 19 {
		t.Fatalf("req_id is %v, so nothing completes the client's pending upload", body[sioRequestIDKey])
	}
}

// TestTutorialSectionKeepsHistoryItDidNotAuthor is the 90% wall of runs 111-113,
// pinned. The client replaces its local tutorial history with the section that
// arrives, so an id the player already finished must survive the merge: run 107
// answered yes for 1500 out of the device's own history, and a section that
// dropped it turned that into a no and stopped the ladder at 90%.
func TestTutorialSectionKeepsHistoryItDidNotAuthor(t *testing.T) {
	s, _ := newTestServer(t)
	s.cfg.TutorialSectionIDs = []int{sioRanchoTutorialID}

	document := map[string]any{
		sioTutorialSectionID: map[string]any{
			sioTutorialSectionKey: []any{float64(sioIntroTutorialID), float64(1700)},
			"unrelated":           "kept",
		},
	}

	if !s.applyTutorialSection(document) {
		t.Fatal("the missing gate id was not added to a section that lacks it")
	}

	section, _ := document[sioTutorialSectionID].(map[string]any)
	completed := tutorialCompletedIDs(section)
	for _, want := range []int{sioIntroTutorialID, 1700, sioRanchoTutorialID} {
		if !containsTutorialID(completed, want) {
			t.Fatalf("completed ids are %v, want %d kept or added", completed, want)
		}
	}
	if len(completed) != 3 {
		t.Fatalf("completed ids are %v, want the stored two plus the one gate id", completed)
	}
	if section["unrelated"] != "kept" {
		t.Fatalf("the section's other members were dropped: %v", section)
	}
}

// TestTutorialSectionAddsNothingTwice pins that a repeat boot is a no-op, so a
// stored section does not grow a duplicate id on every handshake.
func TestTutorialSectionAddsNothingTwice(t *testing.T) {
	s, _ := newTestServer(t)
	document := map[string]any{}

	if !s.applyTutorialSection(document) {
		t.Fatal("the first pass published nothing")
	}
	if s.applyTutorialSection(document) {
		t.Fatal("the second pass changed a document that already held every id")
	}
	section, _ := document[sioTutorialSectionID].(map[string]any)
	if got := tutorialCompletedIDs(section); len(got) != 2 {
		t.Fatalf("completed ids are %v, want the two seeded ids exactly once", got)
	}
}

// TestTutorialSectionSeedIsSwitchable pins that the seed is an operator
// decision, so a run can measure the boot with it off without a rebuild.
func TestTutorialSectionSeedIsSwitchable(t *testing.T) {
	s, _ := newTestServer(t)
	s.cfg.TutorialSectionSeed = false
	session := authorisedSession("0123456789abcdef01234567", "918930694")
	head := sioHead{sioRequestIDKey: float64(19), sioSlotsKey: map[string]any{}}

	body := s.answerCreateSlots(session, head, 19)

	if _, seeded := body[sioSlotsKey]; seeded {
		t.Fatal("the seed is still published with the switch off")
	}
}
