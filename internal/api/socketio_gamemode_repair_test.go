// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAuthRepairsOnlyTheAuthenticatedLegacyGameModeHubSlot(t *testing.T) {
	s, _ := newTestServer(t)
	const (
		playerID = "918930694"
		device   = "918ad658854434c89292b80c5e573229"
		otherID  = "350713954"
		otherDev = "45fdd0566d816ff910dc99a9ea672134"
	)
	if _, _, err := s.st.AdoptSocketIdentity(playerID, device, ""); err != nil {
		t.Fatalf("seed authenticated account: %v", err)
	}
	if _, _, err := s.st.AdoptSocketIdentity(otherID, otherDev, ""); err != nil {
		t.Fatalf("seed other account: %v", err)
	}

	sibling67 := map[string]any{"c": float64(0), "l": []any{}}
	if _, err := s.st.MergeProfileProgressSlots(playerID, map[string]any{
		"67": sibling67,
		"75": map[string]any{"c": nil, "d": float64(0)},
		"76": map[string]any{"owned": true},
	}); err != nil {
		t.Fatalf("seed target slots: %v", err)
	}
	other75 := map[string]any{"c": []any{}, "d": float64(20695)}
	if _, err := s.st.MergeProfileProgressSlots(otherID, map[string]any{"75": other75}); err != nil {
		t.Fatalf("seed other profile: %v", err)
	}

	session := &sioSession{sid: "auth-repair-test"}
	body := s.answerAuth(session, sioHead{
		"uniq_id":   playerID,
		"device":    device,
		"player_id": otherID, // must never select the repair target
	}, 1)
	if body["player_id"] != playerID {
		t.Fatalf("auth selected %v, want stock socket identity %s", body["player_id"], playerID)
	}
	_, _, bound := session.identity()
	if bound != playerID {
		t.Fatalf("session bound %q, want %q", bound, playerID)
	}

	profile, err := s.st.Profile(playerID)
	if err != nil {
		t.Fatalf("load repaired profile: %v", err)
	}
	encoded, _ := json.Marshal(profile.ProgressSlots["75"])
	if got, want := string(encoded), `{"c":[],"d":20696}`; got != want {
		t.Fatalf("slot 75 = %s, want %s", got, want)
	}
	if !reflect.DeepEqual(profile.ProgressSlots["67"], sibling67) {
		t.Fatalf("slot 67 changed: %#v", profile.ProgressSlots["67"])
	}
	if !reflect.DeepEqual(profile.ProgressSlots["76"], map[string]any{"owned": true}) {
		t.Fatalf("sibling slot 76 changed: %#v", profile.ProgressSlots["76"])
	}
	other, _ := s.st.Profile(otherID)
	if !reflect.DeepEqual(other.ProgressSlots["75"], other75) {
		t.Fatalf("unauthenticated profile was rewritten: %#v", other.ProgressSlots["75"])
	}

	updatedAt := profile.UpdatedAt
	s.answerAuth(session, sioHead{"uniq_id": playerID, "device": device}, 2)
	again, _ := s.st.Profile(playerID)
	if !again.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("idempotent auth rewrote the profile: %s -> %s", updatedAt, again.UpdatedAt)
	}
}

func TestAuthLeavesUnknownGameModeHubRevisionUntouched(t *testing.T) {
	s, _ := newTestServer(t)
	const (
		playerID = "812345678"
		device   = "0123456789abcdef0123456789abcdef"
	)
	if _, _, err := s.st.AdoptSocketIdentity(playerID, device, ""); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	unknown := map[string]any{"c": nil, "d": float64(20695)}
	if _, err := s.st.MergeProfileProgressSlots(playerID, map[string]any{"75": unknown}); err != nil {
		t.Fatalf("seed unknown revision: %v", err)
	}
	s.answerAuth(&sioSession{sid: "unknown-revision"}, sioHead{
		"uniq_id": playerID,
		"device":  device,
	}, 1)
	profile, _ := s.st.Profile(playerID)
	if !reflect.DeepEqual(profile.ProgressSlots["75"], unknown) {
		t.Fatalf("unknown revision was guessed at: %#v", profile.ProgressSlots["75"])
	}
}
