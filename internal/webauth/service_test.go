// SPDX-License-Identifier: GPL-3.0-or-later
package webauth

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testService(t *testing.T) (*Service, *time.Time) {
	t.Helper()
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, &now
}
func TestOwnerExpirySingleUseAndWebSessions(t *testing.T) {
	s, now := testService(t)
	owner := Identity{"1", "owner"}
	other := Identity{"2", "other"}
	c, err := s.Start("ip", "1", owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Letters(other, nil)) != 0 {
		t.Fatal("cross-account letter")
	}
	letters := s.Letters(owner, nil)
	if len(letters) != 1 || len(letters[0].Code) != 8 || letters[0].ID < 1000000000 {
		t.Fatal("missing bounded stock letter")
	}
	code := letters[0].Code
	valid := func(i Identity) bool { return i == owner }
	if _, e := s.Verify("ip", "invalid", code, valid); e == nil {
		t.Fatal("unbound browser")
	}
	if _, e := s.Verify("ip", c.Token, "x", valid); e == nil {
		t.Fatal("bad code")
	}
	login, e := s.Verify("ip", c.Token, code, valid)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := s.Verify("ip", c.Token, code, valid); e == nil {
		t.Fatal("replay")
	}
	if len(s.Letters(owner, nil)) != 0 {
		t.Fatal("finished fresh subscription")
	}
	tombstone := s.Letters(owner, map[int]bool{letters[0].ID: true})
	if len(tombstone) != 1 || tombstone[0].Active || tombstone[0].Code != "" || tombstone[0].Changed <= letters[0].Changed {
		t.Fatal("unsafe cached tombstone")
	}
	current, e := s.Current(login.Token, valid)
	if e != nil || current.Identity != owner {
		t.Fatal("lost session")
	}
	if s.Logout(login.Token, "wrong") {
		t.Fatal("missing csrf")
	}
	*now = now.Add(IdleTTL)
	if _, e := s.Current(login.Token, valid); e == nil {
		t.Fatal("idle session")
	}
	c, e = s.Start("ip", "1", owner)
	if e != nil {
		t.Fatal(e)
	}
	code = s.Letters(owner, nil)[0].Code
	*now = now.Add(ChallengeTTL)
	if _, e := s.Verify("ip", c.Token, code, valid); e == nil {
		t.Fatal("expired code")
	}
}
func TestAttemptsConcurrencyBindingAndRestart(t *testing.T) {
	s, _ := testService(t)
	id := Identity{"1", "owner"}
	valid := func(i Identity) bool { return i == id }
	c, _ := s.Start("ip", "1", id)
	code := s.Letters(id, nil)[0].Code
	for i := 0; i < 5; i++ {
		s.Verify("ip", c.Token, "invalid", valid)
	}
	if _, e := s.Verify("ip", c.Token, code, valid); e == nil {
		t.Fatal("attempt exhaustion")
	}
	c, _ = s.Start("ip", "1", id)
	code = s.Letters(id, nil)[0].Code
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.Verify("ip", c.Token, code, valid); e == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("non atomic consumption", wins.Load())
	}
	c, _ = s.Start("ip", "1", id)
	code = s.Letters(id, nil)[0].Code
	if _, e := s.Verify("ip", c.Token, code, func(Identity) bool { return false }); e == nil {
		t.Fatal("owner changed")
	}
	login, e := s.Verify("ip", c.Token, code, valid)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := s.Current(login.Token, func(Identity) bool { return false }); e == nil {
		t.Fatal("rebound web session")
	}
	fresh, _ := testService(t)
	if _, e := fresh.Verify("ip", c.Token, code, valid); e == nil {
		t.Fatal("restart retained challenge")
	}
	if _, e := fresh.Current(login.Token, valid); e == nil {
		t.Fatal("restart retained session")
	}
}
func TestDummyRateBoundsAndPrune(t *testing.T) {
	s, now := testService(t)
	c, _ := s.Start("ip", "unknown", Identity{})
	if len(s.Letters(Identity{}, nil)) != 0 {
		t.Fatal("dummy mail")
	}
	k, _ := tokenKey(c.Token)
	if _, e := s.Verify("ip", c.Token, s.code(k), func(Identity) bool { return true }); e == nil {
		t.Fatal("dummy login")
	}
	for i := 0; i < 2; i++ {
		if _, e := s.Start("ip", "unknown", Identity{}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Start("other-ip", "unknown", Identity{}); e != ErrLimited {
		t.Fatal("account limit")
	}
	for i := 0; i < 7; i++ {
		s.Start("ip", fmt.Sprint(i), Identity{})
	}
	if _, e := s.Start("ip", "more", Identity{}); e != ErrLimited {
		t.Fatal("IP limit")
	}
	*now = now.Add(16 * time.Minute)
	if _, e := s.Start("ip", "unknown", Identity{}); e != nil {
		t.Fatal("limit did not expire")
	}
	if len(s.challenges) != 1 {
		t.Fatal("unbounded stale records")
	}
	for i := len(s.buckets); i < maxBuckets; i++ {
		s.buckets[fmt.Sprint(i)] = bucket{since: *now}
	}
	if _, e := s.Start("new-ip", "new-account", Identity{}); e != ErrLimited {
		t.Fatal("unbounded rate buckets")
	}
}
func TestAbsoluteSessionExpiryAndCancel(t *testing.T) {
	s, now := testService(t)
	id := Identity{"1", "owner"}
	valid := func(i Identity) bool { return i == id }
	c, _ := s.Start("ip", "1", id)
	code := s.Letters(id, nil)[0].Code
	s.Cancel(c.Token)
	if _, e := s.Verify("ip", c.Token, code, valid); e == nil {
		t.Fatal("cancelled code")
	}
	c, _ = s.Start("ip", "1", id)
	code = s.Letters(id, nil)[0].Code
	login, e := s.Verify("ip", c.Token, code, valid)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 47; i++ {
		*now = now.Add(30 * time.Minute)
		if _, e := s.Current(login.Token, valid); e != nil {
			t.Fatal(e)
		}
	}
	*now = now.Add(30 * time.Minute)
	if _, e := s.Current(login.Token, valid); e == nil {
		t.Fatal("absolute expiry")
	}
}
