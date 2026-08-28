// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

// TestSioHubStableSidRepeats covers the second half of the OPG3D_STABLE_SESSIONS
// diagnostic.
//
// The /sio payload codec encrypts deterministically, so the client's auth_v2
// attachment 0 - Encrypt("auth_v2"), a fixed plaintext - should be identical on
// every run. It was not, so some codec input varied per session.
//
// The first run of this diagnostic pinned only the account session token and
// left the socket.io id random, so both candidate inputs moved together and the
// differing ciphertext ruled out neither. Pinning the id as well settled it: the
// same sid now reproduces the same attachment across boots, so the socket.io id
// is what the key follows, and the account token is not involved.
//
// That makes this diagnostic a prerequisite rather than a curiosity. A key
// captured from a traced launch is only reusable while the sid it was captured
// with keeps being issued, which is exactly what this pinning provides.
func TestSioHubStableSidRepeats(t *testing.T) {
	hub := newSioHub(true, nil)

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
	hub := newSioHub(false, nil)

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
