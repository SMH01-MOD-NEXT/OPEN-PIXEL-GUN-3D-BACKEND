// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestGameSeenDurableCoalescedIndependentOfWebAndProgress(t *testing.T) {
	s, a, _, _ := socialTestStore(t)
	profile, _ := s.Profile(a)
	now := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	if err := s.RecordGameSeen(a, now, false); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordGameSeen(a, now.Add(20*time.Second), false); err != nil {
		t.Fatal(err)
	}
	account, _ := s.Account(a)
	if !account.GameLastSeenAt.Equal(now) {
		t.Fatal("heartbeat not coalesced")
	}
	if _, ok := s.Authenticate(a, account.Session, ""); !ok {
		t.Fatal("web auth failed")
	}
	account, _ = s.Account(a)
	if !account.GameLastSeenAt.Equal(now) {
		t.Fatal("web login advanced game last-seen")
	}
	if err := s.RecordGameSeen(a, now.Add(20*time.Second), true); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	account, _ = reopened.Account(a)
	if !account.GameLastSeenAt.Equal(now.Add(20 * time.Second)) {
		t.Fatal("disconnect flush not durable")
	}
	if err := s.RecordGameSeen(a, now.Add(-time.Hour), true); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Profile(a)
	if !reflect.DeepEqual(profile, after) {
		t.Fatal("presence changed progress")
	}
	if err := s.RecordGameSeen(a, time.Now().Add(time.Hour), true); err == nil {
		t.Fatal("future time accepted")
	}
	// Failed persistence rolls back the in-memory timestamp as well.
	oldPath := s.path
	block := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(block, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(block, "state.json")
	if err := s.RecordGameSeen(a, now.Add(time.Minute), true); err == nil {
		t.Fatal("expected write failure")
	}
	s.path = oldPath
	account, _ = s.Account(a)
	if !account.GameLastSeenAt.Equal(now.Add(20 * time.Second)) {
		t.Fatal("failed write advanced activity")
	}
}
