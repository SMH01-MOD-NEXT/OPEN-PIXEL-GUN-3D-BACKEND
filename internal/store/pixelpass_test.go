// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/pixelpass"
)

var passNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func passRewards(a []string) []any {
	out := []any{}
	for _, v := range a {
		out = append(out, v)
	}
	return out
}
func passApply(t *testing.T, s *Store, ctx ProgressWriteContext, commands ...any) {
	t.Helper()
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, commands, passNow); err != nil {
		t.Fatal(err)
	}
}
func passSetup(t *testing.T) (*Store, string, ProgressWriteContext) {
	t.Helper()
	s, dir, ctx := syncedGameplayStore(t)
	passApply(t, s, ctx, counterCommand(10, "seed", map[string]any{"c": pixelpass.Currency, "v": 5000}), counterCommand(322, "season", map[string]any{"i": 1, "c": "stock-config-reference"}))
	return s, dir, ctx
}
func claimCell(n int, ci string) any {
	return counterCommand(323, ci, map[string]any{"l": n, "r": passRewards(pixelpass.Levels()[n-1].Rewards)})
}
func TestPixelPassFreeClaimOnceAcrossRestartAndSnapshot(t *testing.T) {
	s, dir, ctx := passSetup(t)
	before, _ := s.Profile(socketUniqID)
	batch := []any{counterCommand(10, "echo", map[string]any{"c": "Coins", "v": 55}), claimCell(1, "claim")}
	passApply(t, s, ctx, batch...)
	p, _ := s.Profile(socketUniqID)
	if p.Coins != before.Coins+55 {
		t.Fatal("reward duplicated", p.Coins)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s = reopened
	n, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, batch, passNow)
	if n != 0 || err != nil {
		t.Fatal("durable retry", n, err)
	}
	passApply(t, s, ctx, counterCommand(10, "echo-new", map[string]any{"c": "Coins", "v": 55}), claimCell(1, "claim-new"))
	p, _ = s.Profile(socketUniqID)
	if p.Coins != before.Coins+55 {
		t.Fatal("semantic retry minted money")
	}
	for _, kind := range []string{"merge", "document", "replace"} {
		slots := map[string]any{"74": map[string]any{"pp": map[string]any{"i": 1, "e": 99999, "p": 1}}, "33": map[string]any{"5_1000": map[string]any{"p": 999}}}
		switch kind {
		case "merge":
			_, err = s.MergeProfileProgressSlots(socketUniqID, slots, ctx)
		case "document":
			_, err = s.MergeProfileProgressDocument(socketUniqID, slots, ctx)
		case "replace":
			_, err = s.ReplaceProfileProgressSlots(socketUniqID, slots, ctx)
		}
		if err != nil {
			t.Fatal(err)
		}
		p, _ = s.Profile(socketUniqID)
		for _, doc := range []map[string]any{p.ProgressSlots, p.ProgressDocument} {
			pp := objectAt(doc, "74", "pp")
			if ppInt(pp, "e") != 0 || ppInt(pp, "p") != 0 || len(pp["lr"].([]any)) != 1 {
				t.Fatal("snapshot rewrote pass", kind, pp)
			}
		}
	}
}
func TestPixelPassPremiumPackTasksAndReplay(t *testing.T) {
	s, dir, ctx := passSetup(t)
	purchase := func(suffix string) []any {
		return []any{counterCommand(66, "pay"+suffix, map[string]any{"c": pixelpass.Currency, "v": 1200}), counterCommand(326, "boost"+suffix, map[string]any{"e": 1000}), counterCommand(324, "premium"+suffix, map[string]any{})}
	}
	passApply(t, s, ctx, purchase("a")...)
	passApply(t, s, ctx, purchase("b")...)
	p, _ := s.Profile(socketUniqID)
	if ppInt(p.PixelPass.State, "p") != 1 || ppInt(p.PixelPass.State, "e") != 1000 || ppInt(objectAt(p.ProgressSlots, "3"), pixelpass.Currency) != 3800 {
		t.Fatal("premium pack replay", p.PixelPass)
	}
	passApply(t, s, ctx, claimCell(2, "premium-cell"))
	for _, task := range pixelpass.Tasks() {
		if task.StartDay != 0 || task.Type != 2 {
			continue
		}
		key := fmt.Sprintf("5_%d", task.Index)
		passApply(t, s, ctx, counterCommand(150, key+"progress", map[string]any{key: map[string]any{"p": task.Needed}}), counterCommand(151, key+"complete", map[string]any{key: map[string]any{"c": 1}}), counterCommand(337, key+"pending", map[string]any{"t": task.Index, "r": passRewards(task.Rewards)}))
		claim := func(ci string) []any {
			return []any{counterCommand(326, ci+"exp", map[string]any{"e": 60}), counterCommand(338, ci+"claim", map[string]any{"t": task.Index}), counterCommand(152, ci+"rewarded", map[string]any{key: map[string]any{"r": 1}})}
		}
		passApply(t, s, ctx, claim(key+"first")...)
		passApply(t, s, ctx, claim(key+"retry")...)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ = s.Profile(socketUniqID)
	if ppInt(p.PixelPass.State, "e") != 1180 || len(p.PixelPass.Claimed) != 3 || len(objectAt(p.PixelPass.State, "ctr")) != 0 {
		t.Fatal("task persistence", p.PixelPass)
	}
	// Buying one level uses native currency66 + crown326, no invented command.
	passApply(t, s, ctx, counterCommand(66, "level-price", map[string]any{"c": pixelpass.Currency, "v": 20}), counterCommand(326, "level-exp", map[string]any{"e": 100}))
	p, _ = s.Profile(socketUniqID)
	if ppInt(p.PixelPass.State, "e") != 1280 {
		t.Fatal("bought level")
	}
}
func TestPixelPassAll50LevelsRankOneAndElite(t *testing.T) {
	s, _, ctx := passSetup(t)
	// Keep an actual rank-one profile without using an admin revision reset.
	passApply(t, s, ctx, counterCommand(64, "rank-one", map[string]any{"l": 1}), counterCommand(324, "premium", map[string]any{}))
	// Calendar day31 unlocks sufficient quests to finish the whole pass.
	now := passNow.Add(31 * 24 * time.Hour)
	for _, task := range pixelpass.Tasks() {
		if task.StartDay > 31 {
			continue
		}
		key := fmt.Sprintf("5_%d", task.Index)
		batch := []any{counterCommand(150, key+"p", map[string]any{key: map[string]any{"p": task.Needed, "c": 1}}), counterCommand(337, key+"q", map[string]any{"t": task.Index, "r": passRewards(task.Rewards)}), counterCommand(326, key+"e", map[string]any{"e": ppRewardAmount(task.Rewards)}), counterCommand(338, key+"r", map[string]any{"t": task.Index})}
		if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, batch, now); err != nil {
			t.Fatal(task.Index, err)
		}
	}
	for n := 1; n <= 50; n++ {
		if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{claimCell(n, fmt.Sprint("cell", n))}, now); err != nil {
			t.Fatal(n, err)
		}
	}
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{counterCommand(328, "elite-complete", map[string]any{}), counterCommand(325, "elite", map[string]any{})}, now); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Profile(socketUniqID)
	if len(p.PixelPass.State["lr"].([]any)) != 50 || ppInt(p.PixelPass.State, "er") != 1 {
		t.Fatal("incomplete pass")
	}
	for _, idx := range []string{"832001", "1292001", "1277001", "1291001", "1279001"} {
		w := objectAt(p.ProgressSlots, "28", "0", idx)
		if ppInt(w, "l") != 1 {
			t.Fatal("rank-one weapon missing", idx, w)
		}
	}
}
func TestPixelPassInvalidOperationsAreAtomic(t *testing.T) {
	cases := map[string][]any{
		"locked": {claimCell(2, "locked")}, "level50": {claimCell(50, "locked")}, "close": {counterCommand(331, "close", map[string]any{})},
		"wrong-reward":    {counterCommand(323, "forged", map[string]any{"l": 1, "r": []any{"2015:999999"}})},
		"future-task":     {counterCommand(150, "future", map[string]any{"5_1006": map[string]any{"p": 3}})},
		"incomplete-task": {counterCommand(337, "task", map[string]any{"t": 1000, "r": []any{"25015:60"}})},
		"unbudgeted-exp":  {counterCommand(326, "exp", map[string]any{"e": 600})},
		"wrong-season":    {counterCommand(322, "season", map[string]any{"i": 2, "c": "x"})},
		"invalid-payload": {counterCommand(324, "premium", nil)},
	}
	for name, batch := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, ctx := passSetup(t)
			before, _ := s.Profile(socketUniqID)
			batch = append([]any{counterCommand(10, "must-rollback", map[string]any{"c": "Coins", "v": 10})}, batch...)
			if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, batch, passNow); err == nil {
				t.Fatal("accepted")
			}
			after, _ := s.Profile(socketUniqID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("partial mutation")
			}
		})
	}
}
func TestPixelPassPersistenceFailureAndConcurrentClaim(t *testing.T) {
	s, dir, ctx := passSetup(t)
	before, _ := s.Profile(socketUniqID)
	path := s.path
	s.path = filepath.Join(dir, "missing", "state.json")
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{claimCell(1, "fail")}, passNow); err == nil {
		t.Fatal("persist succeeded")
	}
	s.path = path
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed write applied")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{claimCell(1, fmt.Sprint("parallel", i))}, passNow); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	after, _ = s.Profile(socketUniqID)
	if after.Coins != before.Coins+55 {
		t.Fatal("concurrent double reward")
	}
}
func TestPixelPassMatchBudgetAndExpiry(t *testing.T) {
	s, _, ctx := passSetup(t)
	for i := 0; i < 20; i++ {
		passApply(t, s, ctx, counterCommand(326, fmt.Sprint("match", i), map[string]any{"e": 50}))
	}
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{counterCommand(326, "over-cap", map[string]any{"e": 1})}, passNow); err == nil {
		t.Fatal("daily cap")
	}
	end, _ := time.Parse(time.RFC3339, pixelpass.End)
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{claimCell(1, "expired")}, end); err == nil {
		t.Fatal("expired claim")
	}
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{counterCommand(150, "other-task", map[string]any{"1_201": map[string]any{"p": 1}})}, end); err != nil {
		t.Fatal("expired season broke unrelated tasks", err)
	}
}

func TestPixelPassInsufficientFundsPartialLevelAndClose(t *testing.T) {
	s, _, ctx := passSetup(t)
	passApply(t, s, ctx, counterCommand(66, "empty-purse", map[string]any{"c": pixelpass.Currency, "v": 5000}))
	before, _ := s.Profile(socketUniqID)
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{counterCommand(324, "unaffordable", map[string]any{})}, passNow); err == nil {
		t.Fatal("free premium")
	}
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed purchase mutated")
	}
	passApply(t, s, ctx, counterCommand(10, "fund", map[string]any{"c": pixelpass.Currency, "v": 100}), counterCommand(326, "match-progress", map[string]any{"e": 20}))
	passApply(t, s, ctx, counterCommand(66, "partial-price", map[string]any{"c": pixelpass.Currency, "v": 20}), counterCommand(326, "partial-exp", map[string]any{"e": 80}))
	after, _ = s.Profile(socketUniqID)
	if ppInt(after.PixelPass.State, "e") != 100 || ppInt(objectAt(after.ProgressSlots, "3"), pixelpass.Currency) != 80 {
		t.Fatal("partial cell purchase")
	}
	end, _ := time.Parse(time.RFC3339, pixelpass.End)
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{counterCommand(331, "ended", map[string]any{})}, end); err != nil {
		t.Fatal("cannot close ended season", err)
	}
	after, _ = s.Profile(socketUniqID)
	if !after.PixelPass.Closed || len(objectAt(after.ProgressSlots, "74", "pp")) != 0 {
		t.Fatal("ended state projected")
	}
}
func TestPixelPassEliteCompleteBeforeIndividualClaims(t *testing.T) {
	s, _, ctx := passSetup(t)
	batch := []any{}
	for _, task := range pixelpass.Tasks() {
		if task.Type != 3 {
			continue
		}
		key := fmt.Sprintf("5_%d", task.Index)
		batch = append(batch, counterCommand(150, key, map[string]any{key: map[string]any{"p": task.Needed, "c": 1}}))
	}
	batch = append(batch, counterCommand(328, "elite-finished", map[string]any{}), counterCommand(10, "elite-echo", map[string]any{"c": "GemsCurrency", "v": 100}), counterCommand(325, "elite-claim", map[string]any{}))
	before, _ := s.Profile(socketUniqID)
	passApply(t, s, ctx, batch...)
	after, _ := s.Profile(socketUniqID)
	if after.Gems != before.Gems+100 || ppInt(after.PixelPass.State, "er") != 1 {
		t.Fatal("elite rewards")
	}
}
func TestPixelPassDuplicateChildrenAreReducedOnce(t *testing.T) {
	s, _, ctx := passSetup(t)
	c := counterCommand(326, "same-child", map[string]any{"e": 20})
	passApply(t, s, ctx, c, c)
	p, _ := s.Profile(socketUniqID)
	if ppInt(p.PixelPass.State, "e") != 20 {
		t.Fatal("duplicate child reduced twice")
	}
}

func TestPixelPassTutorialUnlockAfterMatchAndRepairAcrossRestart(t *testing.T) {
	s, dir, ctx := passSetup(t)
	passApply(t, s, ctx, counterCommand(64, "intro-rank-one", map[string]any{"l": 1}))
	before, _ := s.Profile(socketUniqID)
	if len(objectAt(before.ProgressSlots, "33", "5_9999")) != 0 {
		t.Fatal("new account unlocked before match")
	}
	passApply(t, s, ctx, counterCommand(326, "intro-match", map[string]any{"e": 20}))
	p, _ := s.Profile(socketUniqID)
	for _, doc := range []map[string]any{p.ProgressSlots, p.ProgressDocument} {
		intro := objectAt(doc, "33", "5_9999")
		if ppInt(intro, "p") != 1 || ppInt(intro, "c") != 1 || ppInt(intro, "r") != 1 {
			t.Fatal("match did not unlock tutorial", intro)
		}
	}
	if p.Level != 1 || p.Coins != before.Coins || p.Gems != before.Gems || ppInt(p.PixelPass.State, "e") != 20 || ppInt(p.PixelPass.State, "p") != 0 {
		t.Fatal("unlock changed rank/economy/ownership")
	}
	beforeRepairReceipts := p.ProgressCommandReceipts
	// Recreate the deployed pre-fix ledger: match crowns exist, intro is absent.
	s.mu.Lock()
	for _, profile := range s.state.Profiles {
		if profile.PixelPass != nil {
			profile.PixelPass.TutorialVersion = 0
			delete(profile.PixelPass.Tasks, "5_9999")
			delete(objectAt(profile.ProgressSlots, "33"), "5_9999")
			delete(objectAt(profile.ProgressDocument, "33"), "5_9999")
		}
	}
	err := s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ = s.Profile(socketUniqID)
	if ppInt(objectAt(p.ProgressSlots, "33", "5_9999"), "r") != 1 || ppInt(p.PixelPass.State, "e") != 20 {
		t.Fatal("historical match not repaired before first read")
	}
	if p.ProgressRevision != before.ProgressRevision+1 || !reflect.DeepEqual(p.ProgressCommandReceipts, beforeRepairReceipts) {
		t.Fatal("repair must arm exactly one full download without clearing receipts")
	}
	ctx.Revision = p.ProgressRevision
	if _, err := s.AcknowledgeProfileProgressRevision(p.PlayerID, ctx.Device, ctx.Revision); err != nil {
		t.Fatal(err)
	}
	// Stale flags/snapshots cannot re-lock the pass or reject unrelated gameplay.
	passApply(t, s, ctx, counterCommand(150, "stale-intro", map[string]any{"5_9999": map[string]any{"p": 0, "c": 0, "r": 0}}))
	_, err = s.ReplaceProfileProgressSlots(socketUniqID, map[string]any{"33": map[string]any{"5_9999": map[string]any{"p": 0}}}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ = s.Profile(socketUniqID)
	if ppInt(objectAt(p.ProgressSlots, "33", "5_9999"), "r") != 1 || ppInt(p.PixelPass.State, "e") != 20 {
		t.Fatal("repaired unlock not durable")
	}
	if p.ProgressRevision != ctx.Revision {
		t.Fatal("repair repeated revision bump after restart")
	}
}

func TestPixelPassTutorialNativeZeroRewardClaim(t *testing.T) {
	s, _, ctx := passSetup(t)
	before, _ := s.Profile(socketUniqID)
	for _, suffix := range []string{"first", "retry"} {
		passApply(t, s, ctx,
			counterCommand(150, "intro-p"+suffix, map[string]any{"5_9999": map[string]any{"p": 1, "c": 1}}),
			counterCommand(337, "intro-prepare"+suffix, map[string]any{"t": 9999, "r": []any{"25015:0"}}),
			counterCommand(326, "intro-zero"+suffix, map[string]any{"e": 0}),
			counterCommand(338, "intro-claim"+suffix, map[string]any{"t": 9999}),
			counterCommand(152, "intro-r"+suffix, map[string]any{"5_9999": map[string]any{"r": 1}}))
	}
	p, _ := s.Profile(socketUniqID)
	if ppInt(objectAt(p.ProgressSlots, "33", "5_9999"), "r") != 1 || ppInt(p.PixelPass.State, "e") != 0 || p.Coins != before.Coins || p.Gems != before.Gems {
		t.Fatal("zero tutorial reward/replay changed economy")
	}
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{counterCommand(326, "unrelated-zero", map[string]any{"e": 0})}, passNow); err == nil {
		t.Fatal("allowed generic zero-crown report")
	}
}

func TestPixelPassBoughtCrownsDoNotRepairTutorial(t *testing.T) {
	s, dir, ctx := passSetup(t)
	passApply(t, s, ctx, counterCommand(66, "intro-pack-pay", map[string]any{"c": pixelpass.Currency, "v": 1200}), counterCommand(326, "intro-pack-exp", map[string]any{"e": 1000}), counterCommand(324, "intro-pack", map[string]any{}))
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.Profile(socketUniqID)
	if len(objectAt(p.ProgressSlots, "33", "5_9999")) != 0 {
		t.Fatal("purchased crowns counted as a match")
	}
}

func TestPixelPassTutorialCanClaimAlongsideMatchReward(t *testing.T) {
	s, _, ctx := passSetup(t)
	passApply(t, s, ctx,
		counterCommand(150, "intro-match-p", map[string]any{"5_9999": map[string]any{"p": 1, "c": 1}}),
		counterCommand(337, "intro-match-prepare", map[string]any{"t": 9999, "r": []any{"25015:0"}}),
		counterCommand(326, "intro-match-zero", map[string]any{"e": 0}),
		counterCommand(338, "intro-match-claim", map[string]any{"t": 9999}),
		counterCommand(326, "intro-real-match", map[string]any{"e": 20}),
		counterCommand(152, "intro-match-r", map[string]any{"5_9999": map[string]any{"r": 1}}))
	p, _ := s.Profile(socketUniqID)
	if ppInt(p.PixelPass.State, "e") != 20 || p.PixelPass.MatchCrowns != 20 || ppInt(objectAt(p.ProgressSlots, "33", "5_9999"), "r") != 1 {
		t.Fatal("tutorial swallowed/rejected native match reward")
	}
}
