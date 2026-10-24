// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func socialTestStore(t *testing.T) (*Store, string, string, string) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, name := range []string{"Alice", "Борис", "Carol"} {
		a, _, err := s.RegisterOrLogin(name, name, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.EnsureProfile(a.PlayerID, name); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.PlayerID)
	}
	return s, ids[0], ids[1], ids[2]
}
func socialDo(t *testing.T, s *Store, a, b, action string) {
	t.Helper()
	if _, err := s.SocialAction(a, b, action); err != nil {
		t.Fatal(action, err)
	}
}
func socialConnect(t *testing.T, s *Store, a, b string) {
	t.Helper()
	socialDo(t, s, a, b, "request")
	socialDo(t, s, b, a, "accept")
}
func TestSocialLifecycleAndDurability(t *testing.T) {
	s, a, b, c := socialTestStore(t)
	if _, err := s.SocialAction(a, b, "accept"); !errors.Is(err, ErrSocialForbidden) {
		t.Fatal("unsolicited accept", err)
	}
	if _, err := s.SocialAction(a, a, "request"); !errors.Is(err, ErrSocialInvalid) {
		t.Fatal("self", err)
	}
	if _, err := s.SocialAction(a, "999999999", "request"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing", err)
	}
	socialDo(t, s, a, b, "request")
	socialDo(t, s, a, b, "request")
	snap, _ := s.SocialSnapshot(b)
	if !reflect.DeepEqual(snap.Incoming, []string{a}) || len(snap.Friends) != 0 {
		t.Fatal(snap)
	}
	if _, err := s.SocialAction(c, a, "accept"); !errors.Is(err, ErrSocialForbidden) {
		t.Fatal("third party accepted", err)
	}
	socialDo(t, s, b, a, "accept")
	socialDo(t, s, b, a, "accept")
	for _, id := range []string{a, b} {
		snap, _ = s.SocialSnapshot(id)
		if len(snap.Friends) != 1 || len(snap.Incoming)+len(snap.Outgoing) != 0 {
			t.Fatal(snap)
		}
	}
	m, err := s.SendPrivateMessage(a, b, "Привет! 🎮", "100")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.SendPrivateMessage(a, b, "Привет! 🎮", "100")
	if err != nil || again.Stamp != m.Stamp {
		t.Fatal("retry duplicated", err)
	}
	msgs, err := s.PrivateInbox(b, []string{a}, 0)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "Привет! 🎮" {
		t.Fatal(msgs, err)
	}
	for _, id := range []string{a, c} {
		msgs, err = s.PrivateInbox(id, []string{a, b}, 0)
		if err != nil || len(msgs) != 0 {
			t.Fatal("other inbox leaked", id, msgs, err)
		}
	}
	if _, err = s.SendPrivateMessage(c, b, "spoof", "1"); !errors.Is(err, ErrSocialForbidden) {
		t.Fatal(err)
	}
	reopened, err := Open(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	snap, _ = reopened.SocialSnapshot(a)
	msgs, _ = reopened.PrivateInbox(b, []string{a}, 0)
	if len(snap.Friends) != 1 || len(msgs) != 1 {
		t.Fatal("restart lost state")
	}
	socialDo(t, reopened, b, a, "remove")
	msgs, _ = reopened.PrivateInbox(b, []string{a}, 0)
	if len(msgs) != 0 {
		t.Fatal("revocation")
	}
	if _, err = reopened.SendPrivateMessage(a, b, "no", "2"); !errors.Is(err, ErrSocialForbidden) {
		t.Fatal(err)
	}
	socialConnect(t, reopened, a, b)
	msgs, _ = reopened.PrivateInbox(b, []string{a}, 0)
	if len(msgs) != 0 {
		t.Fatal("removed history resurrected")
	}
}
func TestSocialCrossedRejectClearAndCopies(t *testing.T) {
	s, a, b, c := socialTestStore(t)
	socialDo(t, s, a, b, "request")
	socialDo(t, s, b, a, "request")
	snap, _ := s.SocialSnapshot(a)
	if len(snap.Friends) != 0 || len(snap.Incoming) != 1 || len(snap.Outgoing) != 1 {
		t.Fatal(snap)
	}
	socialDo(t, s, a, b, "reject")
	snap, _ = s.SocialSnapshot(a)
	if len(snap.Incoming) != 0 || len(snap.Outgoing) != 1 {
		t.Fatal(snap)
	}
	socialDo(t, s, c, b, "request")
	socialDo(t, s, b, "", "clear")
	snap, _ = s.SocialSnapshot(b)
	if len(snap.Incoming) != 0 {
		t.Fatal(snap)
	}
	socialConnect(t, s, a, b)
	snap, _ = s.SocialSnapshot(a)
	snap.Friends[0] = "bad"
	again, _ := s.SocialSnapshot(a)
	if again.Friends[0] != b {
		t.Fatal("interior pointer")
	}
}
func TestSocialFailureAtomicity(t *testing.T) {
	s, a, b, _ := socialTestStore(t)
	socialDo(t, s, a, b, "request")
	path := s.path
	before, _ := os.ReadFile(path)
	s.path = filepath.Join(t.TempDir(), "missing", "state.json")
	original, _ := json.Marshal(s.state.Social)
	if _, err := s.SocialAction(b, a, "accept"); err == nil {
		t.Fatal("expected disk failure")
	}
	after, _ := json.Marshal(s.state.Social)
	if string(original) != string(after) {
		t.Fatal("half friendship")
	}
	s.path = path
	socialDo(t, s, b, a, "accept")
	s.path = filepath.Join(t.TempDir(), "missing", "state.json")
	if _, err := s.SendPrivateMessage(a, b, "lost", "1"); err == nil {
		t.Fatal("expected failure")
	}
	messages, _ := s.PrivateInbox(b, []string{a}, 0)
	if len(messages) != 0 {
		t.Fatal("phantom message")
	}
	s.path = path
	disk, _ := os.ReadFile(path)
	if string(before) == string(disk) {
		t.Fatal("successful accept was not persisted")
	}
	originalState, _ := json.Marshal(s.state)
	s.path = filepath.Join(t.TempDir(), "missing", "state.json")
	if _, err := s.ChangePlayerID(a, "123456789"); err == nil {
		t.Fatal("rename failure")
	}
	afterState, _ := json.Marshal(s.state)
	if string(originalState) != string(afterState) {
		t.Fatal("rename rollback incomplete")
	}
	if err := s.DeleteAccount(a); err == nil {
		t.Fatal("delete failure")
	}
	afterState, _ = json.Marshal(s.state)
	if string(originalState) != string(afterState) {
		t.Fatal("delete rollback incomplete")
	}
}
func TestSocialLimitsExpiryAndValidation(t *testing.T) {
	s, a, b, _ := socialTestStore(t)
	socialConnect(t, s, a, b)
	for _, text := range []string{"", "  ", string([]byte{255}), "bad\x00text", strings.Repeat("я", 513)} {
		if _, err := s.SendPrivateMessage(a, b, text, "x"); !errors.Is(err, ErrSocialInvalid) {
			t.Fatal("accepted invalid text", err)
		}
	}
	for i := 0; i < 30; i++ {
		if _, err := s.SendPrivateMessage(a, b, "hi", ""); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := s.SendPrivateMessage(a, b, "limited", ""); !errors.Is(err, ErrSocialRate) {
		t.Fatal(err)
	}
	for i := range s.state.Social.Messages {
		s.state.Social.Messages[i].Stamp = time.Now().Add(-31 * 24 * time.Hour).UnixMicro()
	}
	msgs, _ := s.PrivateInbox(b, []string{a}, 0)
	if len(msgs) != 0 {
		t.Fatal("expired messages")
	}
	s.state.Social.Requests = append(s.state.Social.Requests, FriendRequest{a, b, time.Now().Add(-15 * 24 * time.Hour).Unix()})
	snap, _ := s.SocialSnapshot(b)
	if len(snap.Incoming) != 0 {
		t.Fatal("expired invite")
	}
	s.socialRates = nil
	for i := 0; i < 120; i++ {
		if _, err := s.SearchPlayers(a, "БОР"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SearchPlayers(a, "БОР"); !errors.Is(err, ErrSocialRate) {
		t.Fatal("search unlimited", err)
	}
}
func TestSocialConcurrentAcceptAndRetry(t *testing.T) {
	s, a, b, _ := socialTestStore(t)
	socialDo(t, s, a, b, "request")
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.SocialAction(b, a, "accept")
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(s.state.Social.Friends) != 1 {
		t.Fatal("duplicate edges")
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.SendPrivateMessage(a, b, "once", "123")
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(s.state.Social.Messages) != 1 {
		t.Fatal("duplicate retries")
	}
}
func TestSocialRenameDeleteAndSearch(t *testing.T) {
	s, a, b, c := socialTestStore(t)
	socialConnect(t, s, a, b)
	if _, err := s.SendPrivateMessage(a, b, "before rename", "1"); err != nil {
		t.Fatal(err)
	}
	socialDo(t, s, c, a, "request")
	if _, err := s.ChangePlayerID(a, "123456789"); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.SocialSnapshot(b)
	msgs, _ := s.PrivateInbox(b, []string{"123456789"}, 0)
	if !reflect.DeepEqual(snap.Friends, []string{"123456789"}) || len(msgs) != 1 || msgs[0].From != "123456789" {
		t.Fatal("rename lost ownership", snap, msgs)
	}
	ids, err := s.SearchPlayers(b, "alice")
	if err != nil || !reflect.DeepEqual(ids, []string{"123456789"}) {
		t.Fatal(ids, err)
	}
	if err := s.DeleteAccount("123456789"); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.SocialSnapshot(b)
	if len(snap.Friends) != 0 || len(s.state.Social.Messages) != 0 || len(s.state.Social.Requests) != 0 {
		t.Fatal("orphan social data")
	}
}
func TestSocialFriendAndPendingCaps(t *testing.T) {
	s, a, b, c := socialTestStore(t)
	for i := 0; i < SocialPendingLimit; i++ {
		s.state.Social.Requests = append(s.state.Social.Requests, FriendRequest{a, c, time.Now().Unix()})
	} // normalized duplicates must not exhaust a real user's cap.
	socialDo(t, s, a, b, "request")
	if len(s.state.Social.Requests) != 2 {
		t.Fatal("normalization")
	}
	// Fill with real accounts to exercise the public limit.
	for i := 0; i < SocialFriendLimit; i++ {
		acct, _, err := s.RegisterOrLogin("cap"+string(rune(1000+i)), "cap", "")
		if err != nil {
			t.Fatal(err)
		}
		s.state.Social.Friends = append(s.state.Social.Friends, Friendship{b, acct.PlayerID})
	}
	if _, err := s.SocialAction(b, a, "accept"); !errors.Is(err, ErrSocialFull) {
		t.Fatal("friend cap", err)
	}
}
func TestSocialAliasReconciliation(t *testing.T) {
	s, a, b, c := socialTestStore(t)
	socialConnect(t, s, a, b)
	if _, err := s.SendPrivateMessage(a, b, "alias", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileAccounts(a, c); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.SocialSnapshot(c)
	msgs, _ := s.PrivateInbox(b, []string{c}, 0)
	if !reflect.DeepEqual(snap.Friends, []string{b}) || len(msgs) != 1 || msgs[0].From != c {
		t.Fatal("alias lost relation", snap, msgs)
	}
	socialDo(t, s, c, b, "remove")
	snap, _ = s.SocialSnapshot(b)
	if len(snap.Friends) != 0 {
		t.Fatal(snap)
	}
}
func TestSocialInboxCursorRetentionAndBound(t *testing.T) {
	s, a, b, _ := socialTestStore(t)
	socialConnect(t, s, a, b)
	now := time.Now().UnixMicro()
	for i := 0; i < 120; i++ {
		s.state.Social.Messages = append(s.state.Social.Messages, PrivateMessage{From: a, To: b, Text: "message", Stamp: now - int64(120-i)*1000})
	}
	s.normalizeSocialLocked(time.Now())
	if len(s.state.Social.Messages) != socialHistoryLimit {
		t.Fatal("per-pair retention", len(s.state.Social.Messages))
	}
	msgs, err := s.PrivateInbox(b, []string{a}, float64(now-2000)/1e6)
	if err != nil || len(msgs) != 1 {
		t.Fatal("cursor", len(msgs), err)
	}
	msgs[0].Text = "corrupted"
	again, _ := s.PrivateInbox(b, []string{a}, float64(now-2000)/1e6)
	if again[0].Text != "message" {
		t.Fatal("interior message reference")
	}
}
