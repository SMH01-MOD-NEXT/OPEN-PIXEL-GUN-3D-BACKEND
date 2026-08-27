// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"testing"
	"time"
)

func TestStateSurvivesReopen(t *testing.T) {
	dir := t.TempDir()

	first, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	account, created, err := first.RegisterOrLogin("device-1", "Nick", "192.0.2.1")
	if err != nil || !created {
		t.Fatalf("register: account=%+v created=%v err=%v", account, created, err)
	}

	second, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reloaded, err := second.Account(account.PlayerID)
	if err != nil {
		t.Fatalf("account after reopen: %v", err)
	}
	if reloaded.DeviceID != "device-1" || reloaded.Nick != "Nick" {
		t.Fatalf("reloaded = %+v", reloaded)
	}
	if _, ok := second.Authenticate(account.PlayerID, account.Session, "192.0.2.1"); !ok {
		t.Fatal("session did not survive a reopen")
	}
}

func TestExpiredBanStopsMatching(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if err := s.AddBan(Ban{Kind: BanIP, Subject: "192.0.2.99", Reason: "expired", Until: &past}); err != nil {
		t.Fatalf("add ban: %v", err)
	}
	if _, hit := s.MatchBan("", "", "192.0.2.99"); hit {
		t.Fatal("expired ban still matched")
	}

	if err := s.AddBan(Ban{Kind: BanPlayer, Subject: "123456789", Reason: "permanent"}); err != nil {
		t.Fatalf("add ban: %v", err)
	}
	if _, hit := s.MatchBan("123456789", "", ""); !hit {
		t.Fatal("permanent ban did not match")
	}
}

func TestEmptyIdentifiersNeverMatchABan(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.AddBan(Ban{Kind: BanDevice, Subject: "device-x"}); err != nil {
		t.Fatalf("add ban: %v", err)
	}
	if _, hit := s.MatchBan("", "", ""); hit {
		t.Fatal("a request with no identifiers must not be banned")
	}
}

func TestBanKindIsValidated(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.AddBan(Ban{Kind: "everyone", Subject: "x"}); err == nil {
		t.Fatal("unknown ban kind was accepted")
	}
	if err := s.AddBan(Ban{Kind: BanIP, Subject: "  "}); err == nil {
		t.Fatal("empty ban subject was accepted")
	}
}
