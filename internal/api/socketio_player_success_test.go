// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

func TestPlayerSuccessInitUsesMeasuredDedicatedReply(t *testing.T) {
	s, _ := newTestServer(t)
	const playerID = "918930694"
	session := authorisedSession("player-success", playerID)

	answer := sioAnswerFor(sioPlayerSuccessInitEvent)
	if answer == nil {
		t.Fatal("player_success_init still falls through to the generic ack")
	}
	body := answer(s, session, sioHead{"progress_status": float64(2)}, 31)
	for _, key := range []string{"err_code", "clan_timeout", "clan_war_timeout"} {
		if body[key] != 0 {
			t.Fatalf("%s = %#v, want measured zero", key, body[key])
		}
	}
	if body[sioRequestIDKey] != 31 || body["error"] != sioErrNone {
		t.Fatalf("reply lost correlation or success: %#v", body)
	}
}

func TestPlayerSuccessInitRejectsUnboundSocket(t *testing.T) {
	s, _ := newTestServer(t)
	body := s.answerPlayerSuccessInit(&sioSession{sid: "unbound"}, sioHead{}, 31)
	if body["error"] != sioErrNotAuthorized {
		t.Fatalf("unbound reply = %#v, want authorization error", body)
	}
}
