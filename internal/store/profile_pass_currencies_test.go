// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"reflect"
	"testing"
)

func TestAdminPassCurrencyEditPreservesPassAndPersists(t *testing.T) {
	s, dir, ctx := passSetup(t)
	passApply(t, s, ctx, counterCommand(326, "currency-test-match", map[string]any{"e": 20}), claimCell(1, "currency-test-free"))
	before, _ := s.Profile(socketUniqID)
	pixel, battle := int64(600), int64(345)
	p, err := s.SetProfile(socketUniqID, before.Nick, before.Level, before.Experience, before.Coins, before.Gems, PassCurrencyEdit{&pixel, &battle})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.PixelPass, before.PixelPass) {
		t.Fatal("currency edit changed pass crowns/premium/tasks/claims")
	}
	if p.ProgressRevision != before.ProgressRevision+1 {
		t.Fatal("currency-only edit did not advance revision")
	}
	if p.Coins != before.Coins || p.Gems != before.Gems {
		t.Fatal("currency edit changed coins/gems")
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ = s.Profile(socketUniqID)
	for _, doc := range []map[string]any{p.ProgressSlots, p.ProgressDocument} {
		if ppInt(objectAt(doc, "3"), "PixelPassCurrency") != 600 || ppInt(objectAt(doc, "3"), "BattlePassCurrency") != 345 {
			t.Fatal("balances not durable")
		}
	}
	// Before the correlated download is acknowledged, stale snapshots are ignored.
	_, err = s.ReplaceProfileProgressSlots(socketUniqID, map[string]any{"3": map[string]any{"PixelPassCurrency": 0, "BattlePassCurrency": 0}}, ProgressWriteContext{Device: ctx.Device, Revision: p.ProgressRevision})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(p, after) {
		t.Fatal("pre-sync upload overwrote currency balances")
	}
	// Omitted values preserve both currencies; explicit zero is not omission.
	same, err := s.SetProfile(socketUniqID, p.Nick, p.Level, p.Experience, p.Coins, p.Gems)
	if err != nil || same.ProgressRevision != p.ProgressRevision {
		t.Fatal("legacy save changed revision", err)
	}
	zero := int64(0)
	after, err = s.SetProfile(socketUniqID, p.Nick, p.Level, p.Experience, p.Coins, p.Gems, PassCurrencyEdit{PixelPass: &zero})
	if err != nil || ppInt(objectAt(after.ProgressSlots, "3"), "PixelPassCurrency") != 0 || ppInt(objectAt(after.ProgressSlots, "3"), "BattlePassCurrency") != 345 {
		t.Fatal("explicit zero/omission", err)
	}
	for _, v := range []int64{-1, 2147483648} {
		if _, err = s.SetProfile(socketUniqID, "Invalid", 1, 0, 0, 0, PassCurrencyEdit{BattlePass: &v}); err == nil {
			t.Fatal("store accepted invalid balance")
		}
		got, _ := s.Profile(socketUniqID)
		if !reflect.DeepEqual(after, got) {
			t.Fatal("invalid currency changed profile")
		}
	}
}

func TestAdminPassCurrencyPersistenceFailureRollsBack(t *testing.T) {
	s, dir, _ := passSetup(t)
	before, _ := s.Profile(socketUniqID)
	accountBefore, _ := s.Account(socketUniqID)
	path := s.path
	s.path = dir + "/missing/state.json"
	pixel, battle := int64(123), int64(456)
	if _, err := s.SetProfile(socketUniqID, "Must rollback", 9, 100, 200, 300, PassCurrencyEdit{&pixel, &battle}); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	s.path = path
	after, _ := s.Profile(socketUniqID)
	accountAfter, _ := s.Account(socketUniqID)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accountBefore, accountAfter) {
		t.Fatal("failed save leaked currency/profile/account mutation")
	}
}
