// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

// TestSioHubStableSidRepeats covers the second half of the OPG3D_STABLE_SESSIONS
// diagnostic.
//
// The /sio payload codec encrypts deterministically and its key is built in
// SocketManager.get_Extras from two static arrays, with no session input on that
// path at all. The client's auth_v2 attachment 0 is Encrypt("auth_v2"), a fixed
// plaintext, so it should be byte-identical on every run. It is not. Some codec
// input therefore varies per session.
//
// The first run of this diagnostic pinned only the account session token and
// left the socket.io id random, so both candidate inputs moved together and the
// differing ciphertext ruled out neither. The experiment is only decisive once
// every per-session value this server originates is frozen, which requires
// successive sessions to reuse a single id.
func TestSioHubStableSidRepeats(t *testing.T) {
	hub := newSioHub(true)

	first, err := hub.create(eioV4)
	if err != nil {
		t.Fatalf("create first session: %v", err)
	}
	second, err := hub.create(eioV4)
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}

	if first.sid != fixedSid {
		t.Errorf("first sid = %q, want the pinned %q", first.sid, fixedSid)
	}
	if second.sid != first.sid {
		t.Errorf("second sid = %q, want it to repeat %q", second.sid, first.sid)
	}

	// The registered session must be the newer one. A stale entry here would
	// aim the next reply at a websocket that is already closed.
	if got := hub.get(fixedSid); got != second {
		t.Error("hub.get returned a stale session for the pinned sid")
	}
}

// TestSioHubRandomSidByDefault guards the default. A predictable socket.io id is
// a guessable credential, so the diagnostic must not leak into normal operation.
func TestSioHubRandomSidByDefault(t *testing.T) {
	hub := newSioHub(false)

	first, err := hub.create(eioV4)
	if err != nil {
		t.Fatalf("create first session: %v", err)
	}
	second, err := hub.create(eioV4)
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}

	if first.sid == second.sid {
		t.Fatalf("two sessions share sid %q with the diagnostic off", first.sid)
	}
	if first.sid == fixedSid || second.sid == fixedSid {
		t.Error("the pinned diagnostic sid was issued with the diagnostic off")
	}
	if len(first.sid) != len(fixedSid) {
		t.Errorf("random sid %q is %d chars, want %d to match the pinned shape",
			first.sid, len(first.sid), len(fixedSid))
	}
}
