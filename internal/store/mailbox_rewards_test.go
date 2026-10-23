// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func rewardNews(t *testing.T, s *Store, at *time.Time) MailboxNews {
	t.Helper()
	d := mailboxDraft()
	d.Type = "DeveloperMessage"
	d.PublishAt = at
	d.Rewards = MailboxRewards{Coins: 1000, Gems: 500, Weapon: 203001}
	n, e := s.CreateMailboxNews(d)
	if e != nil {
		t.Fatal(e)
	}
	return n
}
func mailClaim(n MailboxNews, ci string) []any {
	return []any{
		counterCommand(10, ci+"c", map[string]any{"c": "Coins", "v": 1000, "ca": 62}),
		counterCommand(10, ci+"g", map[string]any{"c": "GemsCurrency", "v": 500, "ca": 62}),
		counterCommand(132, ci+"w", map[string]any{"i": 203001, "u": map[string]any{"l": 65, "r": 3}}),
		counterCommand(187, ci+"r", map[string]any{"records": []any{n.ID}}),
	}
}
func TestMailboxRewardAtomicPersistentReplayAndProjection(t *testing.T) {
	s, dir, ctx := syncedGameplayStore(t)
	n := rewardNews(t, s, nil)
	if count, e := s.ApplyProfileCounterCommands(socketUniqID, ctx, mailClaim(n, "first")); e != nil || count != 4 {
		t.Fatal(count, e)
	}
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	p, _ := s.Profile(socketUniqID)
	requireCoherentCounters(t, p, 500, 1000999, 1000499)
	if !p.MailboxClaims[fmt.Sprint(n.ID)] || len(objectAt(p.ProgressSlots, "28", "0")) != 1 {
		t.Fatal("missing rewards", p.MailboxClaims)
	}
	for _, ci := range []string{"first", "different"} {
		if _, e := s.ApplyProfileCounterCommands(socketUniqID, ctx, mailClaim(n, ci)); e != nil {
			t.Fatal(e)
		}
	}
	p, _ = s.Profile(socketUniqID)
	requireCoherentCounters(t, p, 500, 1000999, 1000499)
	if _, e := s.ApplyProfileCounterCommands(socketUniqID, ctx, []any{counterCommand(188, "remove", map[string]any{"records": []any{n.ID}})}); e != nil {
		t.Fatal(e)
	}
	p, _ = s.Profile(socketUniqID)
	slots := p.ProgressSlots
	slots["42"] = []any{999}
	p, e = s.ReplaceProfileProgressSlots(socketUniqID, slots, ctx)
	if e != nil {
		t.Fatal(e)
	}
	got, _ := json.Marshal(p.ProgressSlots["42"])
	if string(got) != "[1]" {
		t.Fatal("claim projection overwritten", string(got))
	}
	// Operator revision resets transport receipts, not mailbox entitlements.
	p, e = s.SetProfile(socketUniqID, "Admin", 65, 500, p.Coins, p.Gems)
	if e != nil {
		t.Fatal(e)
	}
	ctx.Revision = p.ProgressRevision
	s.AcknowledgeProfileProgressRevision(socketUniqID, ctx.Device, ctx.Revision)
	if _, e = s.ApplyProfileCounterCommands(socketUniqID, ctx, mailClaim(n, "afteradmin")); e != nil {
		t.Fatal(e)
	}
	p, _ = s.Profile(socketUniqID)
	requireCoherentCounters(t, p, 500, 1000999, 1000499)
}
func TestMailboxRewardScheduleArchiveAndNewAccount(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	n := rewardNews(t, s, &at)
	if n.VisibleAt(at.Add(-time.Nanosecond)) || !n.VisibleAt(at) {
		t.Fatal("wrong boundary")
	}
	before, _ := s.Profile(socketUniqID)
	if _, e := s.applyProfileCounterCommandsAt(socketUniqID, ctx, mailClaim(n, "early"), at.Add(-time.Second)); !errors.Is(e, ErrMailboxClaim) {
		t.Fatal(e)
	}
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("early grant mutated account")
	}
	if _, e := s.applyProfileCounterCommandsAt(socketUniqID, ctx, mailClaim(n, "ontime"), at); e != nil {
		t.Fatal(e)
	}
	a, _, e := s.RegisterOrLogin("new-mail-account", "New", "127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	other := ProgressWriteContext{Device: "another-mail-device"}
	if _, e = s.applyProfileCounterCommandsAt(a.PlayerID, other, mailClaim(n, "new"), at); e != nil {
		t.Fatal("new account excluded", e)
	}
	if e = s.ArchiveMailboxNews(n.ID); e != nil {
		t.Fatal(e)
	}
	archived := s.MailboxNews()[0]
	if archived.VisibleAt(at.Add(time.Hour)) {
		t.Fatal("archived visible")
	}
	a, _, _ = s.RegisterOrLogin("after-archive", "New", "127.0.0.1")
	if _, e = s.applyProfileCounterCommandsAt(a.PlayerID, other, mailClaim(n, "removed"), at.Add(time.Hour)); !errors.Is(e, ErrMailboxClaim) {
		t.Fatal(e)
	}
	n2 := rewardNews(t, s, nil)
	if n2.ID != 2 {
		t.Fatal("ID reused", n2.ID)
	}
}
func TestMailboxRewardRejectsForgedEchoesAndRollsBack(t *testing.T) {
	for _, kind := range []string{"coins", "gem", "weapon", "futureid", "orphan", "twice", "badrecords", "disk"} {
		t.Run(kind, func(t *testing.T) {
			s, _, ctx := syncedGameplayStore(t)
			n := rewardNews(t, s, nil)
			batch := mailClaim(n, kind)
			switch kind {
			case "coins":
				batch[0].(map[string]any)["p"].(map[string]any)["v"] = 1001
			case "gem":
				batch[1].(map[string]any)["p"].(map[string]any)["c"] = "Unknown"
			case "weapon":
				batch[2].(map[string]any)["p"].(map[string]any)["i"] = 143001
			case "futureid":
				batch[3].(map[string]any)["p"].(map[string]any)["records"] = []any{999}
			case "orphan":
				batch = batch[:1]
			case "twice":
				batch = append(batch, counterCommand(10, "extra", map[string]any{"c": "Coins", "v": 1000, "ca": 62}))
			case "badrecords":
				batch[3].(map[string]any)["p"].(map[string]any)["records"] = []any{1.5}
			case "disk":
				s.path = filepath.Join(t.TempDir(), "absent", "state.json")
			}
			before, _ := s.Profile(socketUniqID)
			if _, e := s.ApplyProfileCounterCommands(socketUniqID, ctx, batch); e == nil {
				t.Fatal("invalid accepted")
			}
			after, _ := s.Profile(socketUniqID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("partial grant/receipt escaped rollback")
			}
		})
	}
}
func TestMailboxRewardConcurrentClaimsOnceAndOwnedWeaponPreserved(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	n := rewardNews(t, s, nil)
	if e := applyInventoryCommand(s.state.Profiles[socketUniqID], 132, map[string]any{"i": 203001, "u": map[string]any{"l": 65, "r": 5}}); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, e := s.ApplyProfileCounterCommands(socketUniqID, ctx, mailClaim(n, fmt.Sprint(i))); e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	p, _ := s.Profile(socketUniqID)
	requireCoherentCounters(t, p, 500, 1000999, 1000499)
	weapon := objectAt(p.ProgressSlots, "28", "0")["203001"].(map[string]any)
	if ppInt(weapon, "r") != 5 {
		t.Fatal("owned weapon downgraded", weapon)
	}
}
func TestMailboxClaimTransferUnionAndIDChange(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	n := rewardNews(t, s, nil)
	n2 := rewardNews(t, s, nil)
	if _, e := s.ApplyProfileCounterCommands(socketUniqID, ctx, mailClaim(n, "source")); e != nil {
		t.Fatal(e)
	}
	a, _, _ := s.RegisterOrLogin("mail-transfer-target", "Target", "127.0.0.1")
	if _, e := s.ApplyProfileCounterCommands(a.PlayerID, ProgressWriteContext{Device: "target"}, mailClaim(n2, "target")); e != nil {
		t.Fatal(e)
	}
	p, e := s.TransferProgress(socketUniqID, a.PlayerID)
	if e != nil {
		t.Fatal(e)
	}
	if !p.MailboxClaims["1"] || !p.MailboxClaims["2"] {
		t.Fatal("transfer reset target claims", p.MailboxClaims)
	}
	p, _ = s.Profile(socketUniqID)
	if !p.MailboxClaims["1"] {
		t.Fatal("transfer reset source claims")
	}
	changed, e := s.ChangePlayerID(a.PlayerID, "999001234")
	if e != nil {
		t.Fatal(e)
	}
	p, _ = s.Profile(changed.PlayerID)
	if !p.MailboxClaims["1"] || !p.MailboxClaims["2"] {
		t.Fatal("rename reset ledger")
	}
}

func TestMailboxGiftWireMigrationInvalidatesCacheOnceWithoutRegrant(t *testing.T) {
	s, dir, ctx := syncedGameplayStore(t)
	n := rewardNews(t, s, nil)
	if _, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, mailClaim(n, "claimed-before-wire-fix")); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	future := rewardNews(t, s, &at)
	text, err := s.CreateMailboxNews(mailboxDraft())
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the existing version with items but no id_reward. Also exercise
	// monotonic invalidation when ctime_change is already ahead of the clock.
	s.state.MailboxNews[0].RewardWireVersion = 0
	s.state.MailboxNews[1].RewardWireVersion = 0
	s.state.MailboxNews[1].Changed = int(time.Now().Add(time.Hour).Unix())
	futureChanged := s.state.MailboxNews[1].Changed
	if err = s.persistLocked(); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Profile(socketUniqID)
	beforeJSON, _ := json.Marshal(before)
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	news := reopened.MailboxNews()
	if news[0].ID != n.ID || news[0].Changed <= n.Changed || news[0].RewardWireVersion != mailboxRewardWireVersion {
		t.Fatal("cached gift not invalidated", news[0])
	}
	if news[1].ID != future.ID || news[1].Changed <= futureChanged || !news[1].PublishAt.Equal(at) || news[1].Date != future.Date {
		t.Fatal("future letter altered", news[1])
	}
	if !reflect.DeepEqual(news[2], text) {
		t.Fatal("text-only news changed")
	}
	after, _ := reopened.Profile(socketUniqID)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("repair changed account progress/claims")
	}
	if _, err = reopened.ApplyProfileCounterCommands(socketUniqID, ctx, mailClaim(n, "after-wire-fix")); err != nil {
		t.Fatal(err)
	}
	after, _ = reopened.Profile(socketUniqID)
	if after.Coins != before.Coins || after.Gems != before.Gems {
		t.Fatal("repair rearmed claim")
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(news, again.MailboxNews()) {
		t.Fatal("wire fix invalidates cache on every restart")
	}
}
func TestMailboxNewGiftsAlreadyCarryCurrentWireVersion(t *testing.T) {
	s, dir, _ := syncedGameplayStore(t)
	n := rewardNews(t, s, nil)
	if n.RewardWireVersion != mailboxRewardWireVersion {
		t.Fatal("new gift missing wire version")
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(n, again.MailboxNews()[0]) {
		t.Fatal("new gift unnecessarily invalidated on restart")
	}
}
