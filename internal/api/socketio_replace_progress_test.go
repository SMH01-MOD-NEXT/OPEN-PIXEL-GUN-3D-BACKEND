// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"reflect"
	"testing"
)

func measuredReplacementSlots() map[string]any {
	return map[string]any{
		"1": map[string]any{
			"d": map[string]any{"n": "Result Player"},
			"h": "name-checksum",
		},
		"2": map[string]any{
			"d": map[string]any{"r": float64(17), "pr": float64(0), "l": float64(0), "mr": float64(17)},
			"h": "rating-checksum",
		},
		"3": map[string]any{
			"d": map[string]any{"Coins": float64(28), "GemsCurrency": float64(5)},
			"h": "currency-checksum",
		},
		"4": map[string]any{
			"d": map[string]any{"l": float64(2), "e": float64(15), "cr": float64(1), "cre": float64(0)},
			"h": "level-checksum",
		},
	}
}

func TestReplaceProgressStoresTheCompleteUnwrappedSnapshot(t *testing.T) {
	s, _ := newTestServer(t)
	const playerID = "918930694"
	session := authorisedSession("0123456789abcdef01234567", playerID)
	if _, err := s.st.MergeProfileProgressSlots(playerID, map[string]any{
		"99": map[string]any{"stale": true},
	}); err != nil {
		t.Fatalf("seed stale slots: %v", err)
	}

	wire := measuredReplacementSlots()
	body := s.answerReplaceProgress(session, sioHead{
		sioRequestIDKey:            float64(19),
		sioReplaceProgressIndexKey: nil,
		sioSlotsKey:                wire,
	}, 19)
	if body["error"] != sioErrNone || body[sioRequestIDKey] != 19 {
		t.Fatalf("replacement reply is not a successful correlated acknowledgement: %v", body)
	}

	profile, err := s.st.Profile(playerID)
	if err != nil {
		t.Fatalf("load replaced profile: %v", err)
	}
	if len(profile.ProgressSlots) != 4 {
		t.Fatalf("stored %d slots, want the four-slot complete snapshot", len(profile.ProgressSlots))
	}
	if _, stale := profile.ProgressSlots["99"]; stale {
		t.Fatal("full replacement retained a slot omitted by the new snapshot")
	}
	currency, ok := profile.ProgressSlots["3"].(map[string]any)
	if !ok || currency["Coins"] != float64(28) || currency["GemsCurrency"] != float64(5) {
		t.Fatalf("stored slot 3 is not the raw d member: %#v", profile.ProgressSlots["3"])
	}
	if _, wrapperLeaked := currency["h"]; wrapperLeaked {
		t.Fatalf("wire checksum wrapper leaked into the stored slot: %#v", currency)
	}
	if profile.Nick != "Result Player" || profile.Level != 2 || profile.Experience != 15 || profile.Coins != 28 || profile.Gems != 5 {
		t.Fatalf("projected state is nick=%q level=%d exp=%d coins=%d gems=%d", profile.Nick, profile.Level, profile.Experience, profile.Coins, profile.Gems)
	}

	wire["3"].(map[string]any)["d"].(map[string]any)["Coins"] = float64(999)
	afterMutation, err := s.st.Profile(playerID)
	if err != nil {
		t.Fatalf("reload after request mutation: %v", err)
	}
	if got := afterMutation.ProgressSlots["3"].(map[string]any)["Coins"]; got != float64(28) {
		t.Fatalf("stored snapshot aliases the decoded request: Coins=%v", got)
	}
}

func TestReplaceProgressRejectsMalformedSnapshotsWithoutClearingState(t *testing.T) {
	s, _ := newTestServer(t)
	const playerID = "918930694"
	session := authorisedSession("0123456789abcdef01234567", playerID)
	original := map[string]any{"25": map[string]any{"value": float64(7)}}
	if _, err := s.st.MergeProfileProgressSlots(playerID, original); err != nil {
		t.Fatalf("seed original slots: %v", err)
	}

	requests := []sioHead{
		{sioRequestIDKey: float64(20)},
		{sioRequestIDKey: float64(21), sioSlotsKey: map[string]any{}},
		{sioRequestIDKey: float64(22), sioSlotsKey: map[string]any{
			"25": map[string]any{"h": "missing-d"},
		}},
		{sioRequestIDKey: float64(23), sioSlotsKey: map[string]any{
			"not-a-slot": map[string]any{"d": map[string]any{}},
		}},
	}
	for i, request := range requests {
		body := s.answerReplaceProgress(session, request, 20+i)
		if body["error"] == sioErrNone {
			t.Fatalf("malformed request %d was accepted: %v", i, body)
		}
		profile, err := s.st.Profile(playerID)
		if err != nil {
			t.Fatalf("load after malformed request %d: %v", i, err)
		}
		if !reflect.DeepEqual(profile.ProgressSlots, original) {
			t.Fatalf("malformed request %d changed the snapshot to %#v", i, profile.ProgressSlots)
		}
	}
}

func TestReplaceProgressRequiresTheAuthenticatedSessionIdentity(t *testing.T) {
	s, _ := newTestServer(t)
	const playerID = "918930694"
	original := map[string]any{"25": map[string]any{"value": float64(7)}}
	if _, err := s.st.MergeProfileProgressSlots(playerID, original); err != nil {
		t.Fatalf("seed original slots: %v", err)
	}

	body := s.answerReplaceProgress(&sioSession{sid: "0123456789abcdef01234567"}, sioHead{
		sioRequestIDKey: float64(19),
		sioSlotsKey:     measuredReplacementSlots(),
	}, 19)
	if body["error"] != sioErrNotAuthorized {
		t.Fatalf("unbound replacement was not denied: %v", body)
	}
	profile, err := s.st.Profile(playerID)
	if err != nil {
		t.Fatalf("load protected profile: %v", err)
	}
	if !reflect.DeepEqual(profile.ProgressSlots, original) {
		t.Fatalf("unbound replacement changed another profile: %#v", profile.ProgressSlots)
	}
}

func TestReplaceProgressIsDispatchedInsteadOfUsingTheFallback(t *testing.T) {
	s, _ := newTestServer(t)
	const playerID = "918930694"
	answer := sioAnswerFor(sioReplaceProgressEvent)
	if answer == nil {
		t.Fatal("replace_progress has no answer builder")
	}
	body := answer(s, authorisedSession("0123456789abcdef01234567", playerID), sioHead{
		sioRequestIDKey: float64(19),
		sioSlotsKey:     measuredReplacementSlots(),
	}, 19)
	if body["error"] != sioErrNone {
		t.Fatalf("dedicated replacement handler failed: %v", body)
	}
	profile, err := s.st.Profile(playerID)
	if err != nil || len(profile.ProgressSlots) == 0 {
		t.Fatalf("dispatch did not persist the replacement: profile=%#v err=%v", profile, err)
	}
}
