// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"reflect"
	"testing"
)

func TestSyncFullReturnsStoredServerSlots(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")
	want := map[string]any{
		"35": []any{},
		"25": map[string]any{"v": map[string]any{}},
	}
	if _, err := s.st.MergeProfileProgressSlots("918930694", want); err != nil {
		t.Fatal(err)
	}

	body := s.answerSyncFull(session, sioHead{
		sioRequestIDKey: float64(17),
		sioSlotsKey: map[string]any{
			"version_slot": "23.1.3",
			"25":           "eaad6657ed2d5b766ed1a18b166337cf",
		},
		"prop": "measured-client-token",
	}, 17)

	got, ok := body[sioSlotsKey].(map[string]any)
	if !ok {
		t.Fatalf("slots is %T, want the Dictionary<string, object> document used by the stock callback", body[sioSlotsKey])
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("server slots are %#v, want %#v", got, want)
	}
	if _, present := body[sioUnknownSlotsKey]; present {
		t.Fatal("normal reconciliation used _unknown_slots, which Run 152 proved enters Empty state 4")
	}
	if _, present := body["err_code"]; present {
		t.Fatal("successful server-slot application unexpectedly returned err_code")
	}
	if body["error"] != sioErrNone || body[sioRequestIDKey] != 17 {
		t.Fatalf("base acknowledgement changed: %v", body)
	}
}

func TestSyncFullWithoutStoredProfileStaysSuccessful(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")

	body := s.answerSyncFull(session, sioHead{
		sioRequestIDKey: float64(17),
		sioSlotsKey:     map[string]any{"version_slot": "23.1.3"},
	}, 17)
	if _, present := body[sioSlotsKey]; present {
		t.Fatalf("fresh profile received a made-up server document: %v", body)
	}
	if _, present := body[sioUnknownSlotsKey]; present {
		t.Fatalf("fresh profile was marked as a conflict: %v", body)
	}
}

func TestSyncFullIsDispatched(t *testing.T) {
	if sioAnswerFor(sioSyncFullEvent) == nil {
		t.Fatal("sync_full still falls through instead of reaching its protocol handler")
	}
}
