// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

// wrapReplacementSlots renders a slot document the way the client sends it, as
// {"d": <slot>} wrappers, so these tests exercise the same unwrapping the wire
// goes through instead of a shape only the tests use.
func wrapReplacementSlots(slots map[string]any) map[string]any {
	wire := make(map[string]any, len(slots))
	for id, value := range slots {
		wire[id] = map[string]any{sioProgressSlotDataKey: value}
	}
	return wire
}

// The erased operator edit, pinned.
//
// This is the 2026-09-01 device run written down as a test. An operator set the
// profile to level 32 with 123123123 of each currency, the client booted, this
// server answered the slot handshake with the 87 stored ids, and 62 ms later the
// client uploaded its own blank slot 3 and slot 4. The stored profile went to
// zero coins and level 1 in that one write, and the edit was gone before the
// player ever saw a menu.
//
// The upload is not a reset. It is what the client believes before anyone has
// told it otherwise, and the four counters an operator can edit are the
// server's to keep.
func TestReplaceProgressKeepsTheOperatorEditWhenTheUploadIsBlank(t *testing.T) {
	s, _ := newTestServer(t)
	const playerID = "918930694"
	session := authorisedSession("0123456789abcdef01234567", playerID)

	if _, err := s.st.EnsureProfile(playerID, "Player0694"); err != nil {
		t.Fatalf("ensure profile: %v", err)
	}
	if _, err := s.st.MergeProfileProgress(playerID, map[string]any{
		"level":      float64(32),
		"experience": float64(20000),
		"coins":      float64(123123123),
		"gems":       float64(123123123),
	}); err != nil {
		t.Fatalf("store the operator edit: %v", err)
	}

	head := sioHead{sioSlotsKey: wrapReplacementSlots(map[string]any{
		"1":  map[string]any{"n": "Player0694"},
		"3":  map[string]any{},
		"4":  map[string]any{"l": float64(1), "e": float64(0), "cr": float64(1), "cre": float64(0)},
		"56": map[string]any{"t": []any{float64(1100), float64(1500)}},
	})}
	s.answerReplaceProgress(session, head, 19)

	profile, err := s.st.Profile(playerID)
	if err != nil {
		t.Fatalf("reload profile: %v", err)
	}
	if profile.Coins != 123123123 {
		t.Fatalf("coins are %d after a blank upload, want the operator edit 123123123", profile.Coins)
	}
	if profile.Gems != 123123123 {
		t.Fatalf("gems are %d after a blank upload, want the operator edit 123123123", profile.Gems)
	}
	if profile.Level != 32 {
		t.Fatalf("level is %d after a blank upload, want the operator edit 32", profile.Level)
	}
	if profile.Experience != 20000 {
		t.Fatalf("experience is %d after a blank upload, want the operator edit 20000", profile.Experience)
	}

	// The stored slot document has to carry the repaired values too, because
	// that document is what get_progress_slots and get_progress serve back on
	// the next boot. Repairing only the flat counters would leave the two
	// disagreeing, which is the failure store.Profile.Counters exists to record.
	currencies, ok := profile.ProgressSlots["3"].(map[string]any)
	if !ok {
		t.Fatalf("stored slot 3 is %T, want the currency object", profile.ProgressSlots["3"])
	}
	if got := readSlotCounter(currencies, "Coins"); got != 123123123 {
		t.Fatalf("stored slot 3 carries %d coins, want 123123123", got)
	}
	if got := readSlotCounter(currencies, "GemsCurrency"); got != 123123123 {
		t.Fatalf("stored slot 3 carries %d gems, want 123123123", got)
	}

	// Everything the client owns is still stored exactly as it arrived. The
	// guard is meant to be narrow, and a guard that quietly dropped the other
	// slots would be a worse bug than the one it fixes.
	if len(profile.ProgressSlots) != 4 {
		t.Fatalf("stored %d slots, want all 4 the client uploaded", len(profile.ProgressSlots))
	}
	if _, present := profile.ProgressSlots["56"]; !present {
		t.Fatal("the tutorials section the client uploaded was dropped")
	}
}

// The other direction, so the guard cannot quietly become "the server always
// wins". With it off, the client's snapshot is installed verbatim, which is the
// behaviour every run before 2026-09-01 measured and the one a genuine reset
// needs.
func TestReplaceProgressInstallsABlankSnapshotWhenTheGuardIsOff(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ProgressBlankUploadGuard = false

	const playerID = "918930694"
	session := authorisedSession("0123456789abcdef01234567", playerID)
	if _, err := s.st.EnsureProfile(playerID, "Player0694"); err != nil {
		t.Fatalf("ensure profile: %v", err)
	}
	if _, err := s.st.MergeProfileProgress(playerID, map[string]any{
		"level": float64(32), "coins": float64(123123123),
	}); err != nil {
		t.Fatalf("store the operator edit: %v", err)
	}

	head := sioHead{sioSlotsKey: wrapReplacementSlots(map[string]any{
		"3": map[string]any{},
		"4": map[string]any{"l": float64(1), "e": float64(0)},
	})}
	s.answerReplaceProgress(session, head, 19)

	profile, err := s.st.Profile(playerID)
	if err != nil {
		t.Fatalf("reload profile: %v", err)
	}
	if profile.Coins != 0 {
		t.Fatalf("coins are %d with the guard off, want the client's zero", profile.Coins)
	}
	if profile.Level != 1 {
		t.Fatalf("level is %d with the guard off, want the client's 1", profile.Level)
	}
}

// A blank upload against a blank profile must not be mistaken for an edit worth
// defending, and a real value the client sends must never be overwritten by an
// older stored one. Those are the two ways this guard could corrupt an account
// rather than protect it.
func TestRepairBlankCoreSlotsLeavesHonestUploadsAlone(t *testing.T) {
	blank := map[string]any{"3": map[string]any{}, "4": map[string]any{"l": float64(1)}}
	if repaired := repairBlankCoreSlots(blank, 1, 0, 0, 0); len(repaired) != 0 {
		t.Fatalf("repaired %v against a blank profile, want nothing touched", repaired)
	}

	richer := map[string]any{
		"3": map[string]any{"Coins": float64(50)},
		"4": map[string]any{"l": float64(9)},
	}
	if repaired := repairBlankCoreSlots(richer, 32, 20000, 123123123, 0); len(repaired) != 0 {
		t.Fatalf("repaired %v over a non-blank upload, want the client's own values kept", repaired)
	}
	currencies := richer["3"].(map[string]any)
	if got := readSlotCounter(currencies, "Coins"); got != 50 {
		t.Fatalf("the client's 50 coins became %d", got)
	}

	// A slot the upload omits entirely stays omitted: the projection never
	// touches a counter whose slot is absent, so inventing one here would write
	// a document the client never sent.
	partial := map[string]any{"4": map[string]any{"l": float64(1), "e": float64(0)}}
	repaired := repairBlankCoreSlots(partial, 32, 20000, 123123123, 123123123)
	if len(repaired) != 1 || repaired[0] != "4" {
		t.Fatalf("repaired %v, want only slot 4", repaired)
	}
	if _, present := partial["3"]; present {
		t.Fatal("slot 3 was invented for an upload that did not carry it")
	}
}

// The download channel the 23.3.2 protocol defines and this server had no case
// for. The member has to be a dictionary for the same reason init_progress does:
// the client casts it without checking.
func TestGetProgressServesTheStoredSlotDocument(t *testing.T) {
	s, _ := newTestServer(t)
	const playerID = "918930694"
	session := authorisedSession("0123456789abcdef01234567", playerID)

	if _, err := s.st.EnsureProfile(playerID, "Player0694"); err != nil {
		t.Fatalf("ensure profile: %v", err)
	}
	if _, err := s.st.MergeProfileProgressSlots(playerID, map[string]any{
		"3":  map[string]any{"Coins": float64(123123123), "GemsCurrency": float64(123123123)},
		"4":  map[string]any{"l": float64(32), "e": float64(20000)},
		"56": map[string]any{"t": []any{float64(1100), float64(1500)}},
	}); err != nil {
		t.Fatalf("store slots: %v", err)
	}

	full := s.answerGetProgress(session, sioHead{}, 7)
	document, ok := full[sioProgressDocumentKey].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want the dictionary the client casts it to", sioProgressDocumentKey, full[sioProgressDocumentKey])
	}
	if len(document) != 3 {
		t.Fatalf("served %d slots, want all 3 stored", len(document))
	}
	for _, key := range []string{"level", "experience", "coins", "gems"} {
		if _, present := full[key]; !present {
			t.Fatalf("the answer carries no %s: the client reads the document and the scalars out of one object", key)
		}
	}

	// The reference sends the filter as JSON numbers while every stored id is a
	// string, so the coercion is the part worth pinning.
	filtered := s.answerGetProgress(session, sioHead{sioSlotsKey: []any{float64(3), "56"}}, 8)
	document, ok = filtered[sioProgressDocumentKey].(map[string]any)
	if !ok {
		t.Fatalf("filtered %s is %T, want a dictionary", sioProgressDocumentKey, filtered[sioProgressDocumentKey])
	}
	if len(document) != 2 {
		t.Fatalf("a filter of two ids served %d slots", len(document))
	}
	if _, present := document["3"]; !present {
		t.Fatal("slot 3 was asked for as the number 3 and not served")
	}
	if _, present := document["56"]; !present {
		t.Fatal("slot 56 was asked for as the string 56 and not served")
	}
}

// SECURITY: the payload names the account it wants, so an authenticated socket
// must not be able to read another player's progress by asking for it by id.
func TestGetProgressRefusesAnotherAccount(t *testing.T) {
	s, _ := newTestServer(t)
	const playerID = "918930694"
	session := authorisedSession("0123456789abcdef01234567", playerID)
	if _, err := s.st.MergeProfileProgressSlots(playerID, map[string]any{
		"3": map[string]any{"Coins": float64(123123123)},
	}); err != nil {
		t.Fatalf("store slots: %v", err)
	}

	denied := s.answerGetProgress(session, sioHead{"player_id": "111111111"}, 9)
	if _, present := denied[sioProgressDocumentKey]; present {
		t.Fatal("a socket asking for another account was answered with a progress document")
	}
	if got := denied["result"]; got != "not_authorized" {
		t.Fatalf("the refusal answered result=%v, want not_authorized", got)
	}

	// Its own id spelled out is the ordinary case and must still be served.
	owned := s.answerGetProgress(session, sioHead{"player_id": playerID}, 10)
	if _, ok := owned[sioProgressDocumentKey].(map[string]any); !ok {
		t.Fatalf("naming its own id was refused: %v", owned)
	}
}
