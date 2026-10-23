// SPDX-License-Identifier: GPL-3.0-or-later
package support

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Store, Actor, Create) {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "support.enc"), bytes.Repeat([]byte{1}, 32))
	if e != nil {
		t.Fatal(e)
	}
	raw, _, e := s.Browser("")
	if e != nil {
		t.Fatal(e)
	}
	return s, Actor{Browser: raw}, Create{Kind: "recovery", TargetID: "123", Subject: "Account recovery", Details: "private test detail", Answers: map[string]string{"old_device": "yes", "changed_device": "unknown"}, Operation: fmt.Sprintf("%032x", 1)}
}
func TestGuestDurabilityIsolationAndSecret(t *testing.T) {
	s, a, c := fixture(t)
	ticket, code, e := s.Create(a, c, "192.0.2.1")
	if e != nil {
		t.Fatal(e)
	}
	again, code2, e := s.Create(a, c, "other")
	if e != nil || again.ID != ticket.ID || code != code2 {
		t.Fatal("replay", e)
	}
	c.Subject = "changed"
	if _, _, e = s.Create(a, c, ""); !errors.Is(e, ErrConflict) {
		t.Fatal("conflict", e)
	}
	raw, csrf, _ := s.Browser("")
	other := Actor{PlayerID: "123", Fingerprint: "claimed account not owner", Browser: raw}
	if _, e = s.Get(ticket.ID, other, false); !errors.Is(e, ErrDenied) {
		t.Fatal("target granted ownership")
	}
	if s.Unlock(other, ticket.ID, "incorrect") == nil {
		t.Fatal("wrong code")
	}
	if e = s.Unlock(other, ticket.ID, code); e != nil {
		t.Fatal(e)
	}
	if !s.CSRF(raw, csrf) || s.CSRF(raw, "wrong") {
		t.Fatal("csrf")
	}
	disk, _ := os.ReadFile(s.path)
	for _, v := range []string{code, c.Details, a.Browser} {
		if bytes.Contains(disk, []byte(v)) {
			t.Fatal("plaintext")
		}
	}
	s, e = Open(s.path, s.key)
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Get(ticket.ID, other, false)
	if e != nil || got.Subject != ticket.Subject {
		t.Fatal("restart", e)
	}
	wire, _ := json.Marshal(got)
	for _, v := range []string{code, "192.0.2.1", got.SecretHash, got.CreatorBrowser} {
		if bytes.Contains(wire, []byte(v)) {
			t.Fatal("private wire field")
		}
	}
	got.Answers["old_device"] = "no"
	again, _, _ = s.Create(a, Create{Kind: "recovery", TargetID: "999", Subject: "Other ticket", Answers: map[string]string{"old_device": "unknown", "changed_device": "unknown"}, Operation: fmt.Sprintf("%032x", 2)}, "")
	if again.ID == "" {
		t.Fatal("optional details")
	}
	original, _ := s.Get(ticket.ID, a, false)
	if original.Answers["old_device"] != "yes" {
		t.Fatal("alias")
	}
	if e = s.Forget(raw); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(ticket.ID, other, false); e == nil {
		t.Fatal("forget")
	}
	s.now = func() time.Time { return time.Now().Add(TTL) }
	if _, e = s.Get(ticket.ID, a, false); e == nil {
		t.Fatal("guest expiry")
	}
}
func TestAuthenticatedTicketsRolesAndLifecycle(t *testing.T) {
	s, a, c := fixture(t)
	a.PlayerID = "123"
	a.Fingerprint = "owner"
	c.Kind = "question"
	c.Answers = nil
	ticket, code, e := s.Create(a, c, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(ticket.ID, Actor{Browser: a.Browser}, false); e == nil {
		t.Fatal("logout guest access")
	}
	if s.Unlock(Actor{Browser: a.Browser}, ticket.ID, code) == nil {
		t.Fatal("authed ticket code")
	}
	if _, e = s.Get(ticket.ID, Actor{PlayerID: "123", Fingerprint: "new"}, false); e == nil {
		t.Fatal("incarnation")
	}
	admin := Actor{PlayerID: "456", Fingerprint: "staff"}
	if s.Admin(admin) {
		t.Fatal("implicit admin")
	}
	if e = s.SetAdmin(admin.PlayerID, admin.Fingerprint, true, "operator"); e != nil {
		t.Fatal(e)
	}
	if s.Admin(Actor{PlayerID: "456", Fingerprint: "other"}) {
		t.Fatal("role incarnation")
	}
	if _, e = s.Get(ticket.ID, admin, false); e == nil {
		t.Fatal("staff must use admin route")
	}
	u := Update{Status: "needs_info", Text: "Please clarify", Revision: 1, Operation: fmt.Sprintf("%032x", 3)}
	ticket, e = s.Update(ticket.ID, admin, true, u)
	if e != nil || ticket.Status != "needs_info" {
		t.Fatal(e)
	}
	replay, e := s.Update(ticket.ID, admin, true, u)
	if e != nil || replay.Revision != ticket.Revision {
		t.Fatal("update replay")
	}
	if _, e = s.Update(ticket.ID, a, false, Update{Text: "more", Revision: 1, Operation: fmt.Sprintf("%032x", 4)}); !errors.Is(e, ErrConflict) {
		t.Fatal("revision", e)
	}
	ticket, e = s.Update(ticket.ID, a, false, Update{Text: "more information", Revision: ticket.Revision, Operation: fmt.Sprintf("%032x", 5)})
	if e != nil || ticket.Status != "open" {
		t.Fatal("auto reopen", e)
	}
	for n, status := range []string{"resolved", "closed", "open"} {
		ticket, e = s.Update(ticket.ID, admin, true, Update{Status: status, Revision: ticket.Revision, Operation: fmt.Sprintf("%032x", 10+n)})
		if e != nil {
			t.Fatal(e)
		}
		if status != "open" {
			if _, e = s.Update(ticket.ID, a, false, Update{Text: "reply", Revision: ticket.Revision, Operation: fmt.Sprintf("%032x", 20+n)}); e == nil {
				t.Fatal("closed player reply")
			}
		}
	}
	if e = s.SetAdmin(admin.PlayerID, admin.Fingerprint, false, "operator"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(ticket.ID, admin, true); e == nil {
		t.Fatal("revoked role")
	}
}
func TestAtomicFailureAndConcurrentRevision(t *testing.T) {
	s, a, c := fixture(t)
	ticket, _, _ := s.Create(a, c, "")
	path := s.path
	blocked := filepath.Join(t.TempDir(), "block")
	os.WriteFile(blocked, []byte("x"), 0600)
	s.path = filepath.Join(blocked, "fail")
	u := Update{Text: "message", Revision: 1, Operation: fmt.Sprintf("%032x", 3)}
	if _, e := s.Update(ticket.ID, a, false, u); e == nil {
		t.Fatal("write succeeded")
	}
	s.path = path
	got, _ := s.Get(ticket.ID, a, false)
	if got.Revision != 1 || len(got.Messages) != 1 || len(got.Operations) != 0 {
		t.Fatal("partial memory mutation")
	}
	if _, e := s.Update(ticket.ID, a, false, u); e != nil {
		t.Fatal("retry", e)
	}
	var applied atomic.Int32
	var wg sync.WaitGroup
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, e := s.Update(ticket.ID, a, false, Update{Text: "concurrent", Revision: 2, Operation: fmt.Sprintf("%032x", 100+n)})
			if e == nil {
				applied.Add(1)
			} else if !errors.Is(e, ErrConflict) {
				t.Error(e)
			}
		}(n)
	}
	wg.Wait()
	if applied.Load() != 1 {
		t.Fatal("lost CAS", applied.Load())
	}
}
func TestValidation(t *testing.T) {
	_, _, c := fixture(t)
	c.Answers["old_device"] = "maybe"
	if Validate(c) {
		t.Fatal("unknown choice")
	}
	c.Answers["old_device"] = "yes"
	c.Answers["unexpected"] = "x"
	if Validate(c) {
		t.Fatal("unknown answer")
	}
	delete(c.Answers, "unexpected")
	c.Operation = "bad"
	if Validate(c) {
		t.Fatal("operation")
	}
}

func TestPlayerClosureOwnershipLifecycleAndDurability(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		for _, initial := range []string{"open", "needs_info"} {
			t.Run(fmt.Sprint(authenticated, initial), func(t *testing.T) {
				s, a, c := fixture(t)
				if authenticated {
					a.PlayerID = "123"
					a.Fingerprint = "owner"
				}
				ticket, _, err := s.Create(a, c, "")
				if err != nil {
					t.Fatal(err)
				}
				if initial != "open" {
					ticket, err = s.Update(ticket.ID, Actor{Operator: true}, true, Update{Status: initial, Revision: ticket.Revision, Operation: fmt.Sprintf("%032x", 2)})
					if err != nil {
						t.Fatal(err)
					}
				}
				request := Update{Status: "closed", Revision: ticket.Revision, Operation: fmt.Sprintf("%032x", 3)}
				raw, _, _ := s.Browser("")
				other := Actor{PlayerID: "123", Fingerprint: "not-owner", Browser: raw}
				if _, err = s.Update(ticket.ID, other, false, request); !errors.Is(err, ErrDenied) {
					t.Fatal("foreign closure", err)
				}
				for _, status := range []string{"open", "resolved", "needs_info"} {
					u := request
					u.Status = status
					if _, err = s.Update(ticket.ID, a, false, u); !errors.Is(err, ErrDenied) {
						t.Fatal("owner status escalation", status, err)
					}
				}
				mixed := request
				mixed.Text = "unexpected reply"
				if _, err = s.Update(ticket.ID, a, false, mixed); !errors.Is(err, ErrDenied) {
					t.Fatal("mixed closure", err)
				}
				// Failed persistence must not consume the operation or change the ticket.
				path := s.path
				s.path = t.TempDir()
				if _, err = s.Update(ticket.ID, a, false, request); err == nil {
					t.Fatal("expected write failure")
				}
				s.path = path
				got, _ := s.Get(ticket.ID, a, false)
				if got.Status != initial || got.Revision != ticket.Revision {
					t.Fatal("failed write mutated ticket")
				}
				got, err = s.Update(ticket.ID, a, false, request)
				if err != nil || got.Status != "closed" || got.Revision != ticket.Revision+1 || len(got.Messages) != len(ticket.Messages) || got.Audit[len(got.Audit)-1].Action != "closed_by_player" {
					t.Fatal("closure", err)
				}
				again, err := s.Update(ticket.ID, a, false, request)
				if err != nil || again.Revision != got.Revision {
					t.Fatal("retry", err)
				}
				s, err = Open(path, s.key)
				if err != nil {
					t.Fatal(err)
				}
				got, err = s.Get(ticket.ID, a, false)
				if err != nil || got.Status != "closed" {
					t.Fatal("restart", err)
				}
				for _, u := range []Update{{Text: "reply"}, {Status: "open"}, {Status: "closed"}} {
					u.Revision = got.Revision
					u.Operation = fmt.Sprintf("%032x", 4)
					if _, err = s.Update(ticket.ID, a, false, u); !errors.Is(err, ErrDenied) {
						t.Fatal("completed mutation", err)
					}
				}
			})
		}
	}
}
func TestPlayerClosureConflictAndResolvedGuard(t *testing.T) {
	s, a, c := fixture(t)
	ticket, _, _ := s.Create(a, c, "")
	request := Update{Status: "closed", Revision: 1, Operation: fmt.Sprintf("%032x", 3)}
	updated, err := s.Update(ticket.ID, Actor{Operator: true}, true, Update{Text: "staff reply", Status: "resolved", Revision: 1, Operation: fmt.Sprintf("%032x", 2)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(ticket.ID, a, false, request); !errors.Is(err, ErrConflict) {
		t.Fatal("stale closure", err)
	}
	request.Revision = updated.Revision
	if _, err = s.Update(ticket.ID, a, false, request); !errors.Is(err, ErrDenied) {
		t.Fatal("resolved closure", err)
	}
}
