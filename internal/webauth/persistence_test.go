// SPDX-License-Identifier: GPL-3.0-or-later
package webauth

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistentSessionRestartNinetyDaysAndLogout(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	path := filepath.Join(t.TempDir(), "sessions.enc")
	s, e := Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	start := time.Now().UTC()
	s.now = func() time.Time { return start }
	identity := Identity{PlayerID: "123", Fingerprint: "owner", Epoch: 2}
	issued, e := s.Issue(identity)
	if e != nil {
		t.Fatal(e)
	}
	disk, _ := os.ReadFile(path)
	if bytes.Contains(disk, []byte(issued.Token)) || bytes.Contains(disk, []byte(identity.Fingerprint)) {
		t.Fatal("plaintext session")
	}
	s, e = Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return start.Add(89 * 24 * time.Hour) }
	cur, e := s.Current(issued.Token, func(i Identity) bool { return i == identity })
	if e != nil || cur.CSRF != issued.CSRF || !cur.ExpiresAt.Equal(start.Add(SessionTTL)) || !cur.AuthenticatedAt.Equal(start) {
		t.Fatal("restart/idle/expiry", e)
	}
	if s.Logout(issued.Token, "invalid") {
		t.Fatal("bad csrf")
	}
	if !s.Logout(issued.Token, issued.CSRF) {
		t.Fatal("logout")
	}
	s, e = Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Current(issued.Token, func(Identity) bool { return true }); e == nil {
		t.Fatal("logout survived restart")
	}
	s.now = func() time.Time { return start }
	issued, e = s.Issue(identity)
	if e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return start.Add(SessionTTL) }
	if _, e = s.Current(issued.Token, func(Identity) bool { return true }); e == nil {
		t.Fatal("90 day expiry")
	}
}
func TestPersistentRevokeAndWriteFailure(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	dir := t.TempDir()
	path := filepath.Join(dir, "s.enc")
	s, _ := Open(path, key)
	i := Identity{PlayerID: "123", Fingerprint: "owner"}
	issued, _ := s.Issue(i)
	s.RevokeIdentity(i)
	s, _ = Open(path, key)
	if _, e := s.Current(issued.Token, func(Identity) bool { return true }); e == nil {
		t.Fatal("revocation lost")
	}
	blocker := filepath.Join(dir, "blocked")
	os.WriteFile(blocker, []byte("x"), 0600)
	s.path = filepath.Join(blocker, "s.enc")
	if _, e := s.Issue(i); e == nil || len(s.sessions) != 0 {
		t.Fatal("failed write granted session")
	}
	s.path = path
	issued, _ = s.Issue(i)
	s.path = filepath.Join(blocker, "s.enc")
	if s.Logout(issued.Token, issued.CSRF) {
		t.Fatal("failed durable logout claimed success")
	}
	if _, e := s.Current(issued.Token, func(Identity) bool { return true }); e == nil {
		t.Fatal("failed logout not fail-closed")
	}
}
