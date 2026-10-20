// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"reflect"
	"testing"
	"time"
)

func TestBanBatchAtomicDurableDetachedAndUpsert(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(time.Hour)
	bans := []Ban{{Kind: BanPlayer, Subject: "123456789", Until: &deadline}, {Kind: BanDevice, Subject: "native"}, {Kind: BanDevice, Subject: "socket"}}
	if err := s.AddBans(bans); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(-time.Hour)
	if _, hit := s.MatchBan("123456789", "", ""); !hit {
		t.Fatal("input deadline aliases stored pointer")
	}
	copy := s.Bans()
	*copy[0].Until = deadline
	b, hit := s.MatchBan("123456789", "", "")
	if !hit {
		t.Fatal("returned deadline aliases store")
	}
	*b.Until = deadline
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Bans()) != 3 {
		t.Fatal("batch did not survive restart")
	}
	if err := s.AddBan(Ban{Kind: BanPlayer, Subject: "123456789", Until: &deadline}); err != nil {
		t.Fatal(err)
	}
	if len(s.Bans()) != 3 {
		t.Fatal("upsert left duplicate")
	}
	if _, hit := s.MatchBan("123456789", "", ""); hit {
		t.Fatal("old permanent/active record survived upsert")
	}
	snapshot := s.Bans()
	path := s.path
	s.path = t.TempDir() // rename onto a directory must fail
	if err := s.AddBans([]Ban{{Kind: BanDevice, Subject: "new"}}); err == nil {
		t.Fatal("expected persist failure")
	}
	if !reflect.DeepEqual(snapshot, s.Bans()) {
		t.Fatal("failed ban changed memory")
	}
	if n, err := s.RemoveBanSubjects(bans); err == nil || n != 0 {
		t.Fatal("failed unban reported success")
	}
	if !reflect.DeepEqual(snapshot, s.Bans()) {
		t.Fatal("failed unban changed memory")
	}
	s.path = path
	if n, err := s.RemoveBanSubjects(bans); err != nil || n != 3 {
		t.Fatalf("remove n=%d err=%v", n, err)
	}
	reopened, err = Open(dir)
	if err != nil || len(reopened.Bans()) != 0 {
		t.Fatal("unban did not survive restart")
	}
}
func TestBanBatchValidationBeforeMutation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Ban{{Kind: "typo", Subject: "x"}, {Kind: BanDevice, Subject: "  "}} {
		if err := s.AddBans([]Ban{{Kind: BanPlayer, Subject: "123456789"}, bad}); err == nil {
			t.Fatal("invalid batch accepted")
		}
		if len(s.Bans()) != 0 {
			t.Fatal("partial ban saved")
		}
		if _, err := s.RemoveBanSubjects([]Ban{bad}); err == nil {
			t.Fatal("invalid unban accepted")
		}
	}
}
func TestBanAliasUpsertAndRemove(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := s.RegisterOrLogin("native", "Test", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangePlayerID(a.PlayerID, "123456789"); err != nil {
		t.Fatal(err)
	}
	s.state.Aliases[a.PlayerID] = "123456789"
	if err := s.AddBan(Ban{Kind: BanPlayer, Subject: a.PlayerID}); err != nil {
		t.Fatal(err)
	}
	if b, hit := s.MatchBan("123456789", "", ""); !hit || b.Subject != "123456789" {
		t.Fatal("alias ban did not resolve")
	}
	if n, err := s.RemoveBans(BanPlayer, a.PlayerID); err != nil || n != 1 {
		t.Fatal("alias unban failed")
	}
}
