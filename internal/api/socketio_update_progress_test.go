// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

func measuredAfterProgressSyncUpdate(reqID int) sioHead {
	return sioHead{
		sioRequestIDKey:               float64(reqID),
		"id":                          float64(63),
		sioUpdateProgressCommandIDKey: "3a1089d2",
		sioUpdateProgressPayloadKey: map[string]any{
			sioUpdateProgressCommandsKey: []any{
				map[string]any{
					"id":                        float64(106),
					sioUpdateProgressChildIDKey: "a319d868",
					"u":                         []any{float64(274)},
				},
			},
		},
		"new_err": float64(1),
	}
}

func TestUpdateProgressEchoesPendingCommandIdentifiers(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")

	body := s.answerUpdateProgress(session, measuredAfterProgressSyncUpdate(25), 25)

	if body[sioRequestIDKey] != 25 {
		t.Fatalf("req_id is %v, want 25", body[sioRequestIDKey])
	}
	if body[sioUpdateProgressErrCodeKey] != sioErrNone {
		t.Fatalf("err_code is %v, want the successful zero branch", body[sioUpdateProgressErrCodeKey])
	}
	if body[sioUpdateProgressCommandIDKey] != "3a1089d2" {
		t.Fatalf("i is %v, want the outer pending-dictionary key", body[sioUpdateProgressCommandIDKey])
	}
	if body[sioUpdateProgressChildIDKey] != "a319d868" {
		t.Fatalf("ci is %v, want the measured child command id", body[sioUpdateProgressChildIDKey])
	}
}

func TestUpdateProgressPlainUpdateEchoesOnlyOuterIdentifier(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")
	head := sioHead{
		sioRequestIDKey:               float64(26),
		sioUpdateProgressCommandIDKey: "4d17b944",
		sioUpdateProgressPayloadKey: map[string]any{
			"1": map[string]any{"2": float64(1)},
		},
	}

	body := s.answerUpdateProgress(session, head, 26)

	if body[sioUpdateProgressCommandIDKey] != "4d17b944" {
		t.Fatalf("i is %v, want the outer pending-dictionary key", body[sioUpdateProgressCommandIDKey])
	}
	if _, invented := body[sioUpdateProgressChildIDKey]; invented {
		t.Fatalf("plain update invented a ci: %v", body)
	}
}

func TestUpdateProgressRejectsAnUnboundSession(t *testing.T) {
	s, _ := newTestServer(t)
	session := &sioSession{sid: "0123456789abcdef01234567"}

	body := s.answerUpdateProgress(session, measuredAfterProgressSyncUpdate(25), 25)

	if body[sioUpdateProgressErrCodeKey] != sioErrNotAuthorized || body["error"] != sioErrNotAuthorized {
		t.Fatalf("unbound update was not denied: %v", body)
	}
	if body[sioUpdateProgressCommandIDKey] != "3a1089d2" || body[sioUpdateProgressChildIDKey] != "a319d868" {
		t.Fatalf("denial dropped the correlation ids and would strand the client: %v", body)
	}
}

func TestUpdateProgressIsDispatched(t *testing.T) {
	s, _ := newTestServer(t)
	session := authorisedSession("0123456789abcdef01234567", "918930694")
	answer := sioAnswerFor(sioUpdateProgressEvent)
	if answer == nil {
		t.Fatal("update_progress has no answer builder")
	}

	body := answer(s, session, measuredAfterProgressSyncUpdate(25), 25)
	if body[sioUpdateProgressCommandIDKey] != "3a1089d2" {
		t.Fatalf("update_progress still reached the generic acknowledgement: %v", body)
	}
}
