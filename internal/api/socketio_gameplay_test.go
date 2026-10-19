// SPDX-License-Identifier: GPL-3.0-or-later
package api

import "testing"

func gameplayBatch(ci string, id int, p map[string]any) sioHead {
	return sioHead{"id": float64(63), "i": "outer-" + ci, "p": map[string]any{"c": []any{map[string]any{"id": float64(id), "ci": ci, "p": p}}}}
}
func TestUpdateProgressPersistsPurchaseAndChestAfterOneAdminSync(t *testing.T) {
	s, session, revision := newOperatorEditedProgress(t)
	completeRevisionDownload(t, s, session, revision)
	spend := gameplayBatch("spend-coins", 66, map[string]any{"c": "Coins", "v": float64(2265)})
	for _, head := range []sioHead{spend, spend, gameplayBatch("chest-xp", 65, map[string]any{"e": float64(0)})} {
		body := s.answerUpdateProgress(session, head, 40)
		if body["err_code"] != sioErrNone {
			t.Fatalf("counter update failed: %#v", body)
		}
		if body["i"] != head["i"] {
			t.Fatal("lost command correlation")
		}
	}
	p, _ := s.st.Profile(revisionTestPlayerID)
	if p.Coins != 123123123-2265 || p.Experience != 0 {
		t.Fatalf("gameplay counters not persisted: %d/%d", p.Coins, p.Experience)
	}
	if got := s.answerProgressStatus(session, sioHead{}, 41)["coins"]; got != p.Coins {
		t.Fatalf("status currency %v disagrees with store %d", got, p.Coins)
	}
	// Reloading does not need another raw-echo boot, nor regrant the admin money.
	restart := revisionTestSession("new-gameplay-login")
	restart.setProgressWriteRevision(revision)
	body := s.answerGetProgress(restart, sioHead{}, 42)
	slots := body[sioSlotsKey].(map[string]any)
	if got, _ := slotCounter(slots["3"].(map[string]any)["Coins"]); got != p.Coins {
		t.Fatal("download returned old admin balance")
	}
}
func TestUpdateProgressFailureDoesNotAcknowledgeCounterCommand(t *testing.T) {
	s, session, revision := newOperatorEditedProgress(t)
	completeRevisionDownload(t, s, session, revision)
	body := s.answerUpdateProgress(session, gameplayBatch("bad", 66, map[string]any{"c": "Coins", "v": float64(999999999)}), 44)
	if body["err_code"] == sioErrNone || body["i"] != "outer-bad" {
		t.Fatal("failed command acknowledged or lost correlation")
	}
	p, _ := s.st.Profile(revisionTestPlayerID)
	if p.Coins != 123123123 {
		t.Fatal("failed command mutated currency")
	}
}
func TestNewAdminEditFencesAlreadySyncedSessionAndLeavesOtherPlayerUntouched(t *testing.T) {
	s, old, revision := newOperatorEditedProgress(t)
	completeRevisionDownload(t, s, old, revision)
	other, _, err := s.st.AdoptSocketIdentity("612672163", "foreign-device", "")
	if err != nil {
		t.Fatal(err)
	}
	foreignBefore, _ := s.st.Profile(other.PlayerID)
	current, err := s.st.SetProfile(revisionTestPlayerID, "Veteran", 32, 500, 50, 60)
	if err != nil {
		t.Fatal(err)
	}
	body := s.answerUpdateProgress(old, gameplayBatch("old", 66, map[string]any{"c": "Coins", "v": float64(1)}), 45)
	if body["err_code"] == sioErrNone {
		t.Fatal("old session changed new revision")
	}
	s.answerPlayerSuccessInit(old, sioHead{}, 46)
	p, _ := s.st.Profile(revisionTestPlayerID)
	if p.Coins != 50 {
		t.Fatal("new admin coins lost")
	}
	if got, pending := p.PendingProgressRevision("device"); !pending || got != current.ProgressRevision {
		t.Fatal("old Login cleared new pending flag")
	}
	foreignAfter, _ := s.st.Profile(other.PlayerID)
	if foreignAfter.ProgressRevision != foreignBefore.ProgressRevision {
		t.Fatal("global revision change")
	}
}
