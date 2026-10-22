// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func battleFixture(id string, timestamp int64) map[string]any {
	return map[string]any{"m": 2, "r": 1, "c": 0, "t": timestamp, "p": []any{map[string]any{"i": id, "t": 0, "n": "Test", "p": 0, "s": 500, "k": 10, "c": -1}}}
}
func dailyFixture(day string) map[string]any {
	return map[string]any{"k": 10, "d": 2, "w": true, "hds": 3, "h": 30, "s": 25, "ks": 5, "c": false, "tw": 4, "sw": 0, "sid": "", "dk": day}
}
func battleBatch(tick int64) []any {
	return []any{
		counterCommand(192, "rate", map[string]any{"k": 10, "d": 2, "w": true}),
		counterCommand(254, "history", map[string]any{"m": battleFixture(socketUniqID, tick)}),
		counterCommand(336, "end", map[string]any{"u": tick, "w": 1}),
		counterCommand(370, "daily", dailyFixture("10-21-2026")),
	}
}
func TestBattleStatisticsPersistReplayAndSnapshot(t *testing.T) {
	s, dir, ctx := passSetup(t)
	before, _ := s.Profile(socketUniqID)
	batch := battleBatch(passNow.Unix())
	passApply(t, s, ctx, batch...)
	p, _ := s.Profile(socketUniqID)
	if ppInt(objectAt(p.ProgressSlots, "84", "ds", "10-21-2026"), "m") != 1 || ppInt(p.BattleWins(), "2") != 1 || len(battleArray(objectAt(p.ProgressSlots, "57")["h"])) != 1 {
		t.Fatal("missing stats", p.BattleStatistics)
	}
	if p.Coins != before.Coins || p.Gems != before.Gems || !reflect.DeepEqual(p.PixelPass, before.PixelPass) {
		t.Fatal("combat stats changed wallet/pass")
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, append(batch, batch...), passNow)
	if n != 0 || err != nil {
		t.Fatal("replayed", n, err)
	}
	for _, kind := range []string{"merge", "document", "replace"} {
		slots := map[string]any{"57": map[string]any{"h": []any{}}, "84": map[string]any{"ds": map[string]any{}}}
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
		got, _ := s.Profile(socketUniqID)
		if !sameBattleJSON(got.BattleStatistics, p.BattleStatistics) {
			t.Fatal("snapshot changed ledger", kind)
		}
		for _, doc := range []map[string]any{got.ProgressSlots, got.ProgressDocument} {
			if ppInt(objectAt(doc, "84", "ds", "10-21-2026"), "m") != 1 {
				t.Fatal("stale projection", kind)
			}
		}
	}
	// A resent history entry with a different ci is still the same match.
	passApply(t, s, ctx, counterCommand(254, "history-again", map[string]any{"m": battleFixture(socketUniqID, passNow.Unix())}), counterCommand(336, "end-again", map[string]any{"u": passNow.Unix(), "w": 1}))
	got, _ := s.Profile(socketUniqID)
	if ppInt(got.BattleWins(), "2") != 1 || ppInt(objectAt(got.ProgressSlots, "77", "t"), "w") != 1 {
		t.Fatal("semantic retry")
	}
}
func TestBattleStatisticsValidationAndDiskFailureAreAtomic(t *testing.T) {
	for _, bad := range []string{"other-owner", "negative", "bad-day", "bad-bool", "overflow", "fractional", "future", "disk", "missing-ci", "bad-deaths"} {
		t.Run(bad, func(t *testing.T) {
			s, _, ctx := passSetup(t)
			before, _ := s.Profile(socketUniqID)
			batch := battleBatch(passNow.Unix())
			d := batch[3].(map[string]any)["p"].(map[string]any)
			switch bad {
			case "other-owner":
				batch[1].(map[string]any)["p"].(map[string]any)["m"] = battleFixture("999999999", passNow.Unix())
			case "negative":
				d["k"] = -1
			case "bad-day":
				d["dk"] = "2026-15-44"
			case "bad-bool":
				d["w"] = "true"
			case "overflow":
				d["s"] = math.MaxInt64
			case "fractional":
				d["k"] = 0.5
			case "future":
				batch[2].(map[string]any)["p"].(map[string]any)["u"] = passNow.Add(72 * time.Hour).Unix()
			case "disk":
				s.path = filepath.Join(t.TempDir(), "missing", "state.json")
			case "missing-ci":
				delete(batch[3].(map[string]any), "ci")
			case "bad-deaths":
				batch[0].(map[string]any)["p"].(map[string]any)["d"] = "bad"
			}
			if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, batch, passNow); err == nil {
				t.Fatal("accepted invalid stats")
			}
			after, _ := s.Profile(socketUniqID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("partial mutation")
			}
		})
	}
}
func TestBattleStatisticsNativeModeReportsDoNotDoubleCount(t *testing.T) {
	for _, first := range []bool{true, false} {
		s, dir, ctx := passSetup(t)
		mods := map[string]any{"2": int64(1), "t": int64(3)}
		if first {
			if err := s.MergeBattleModes(socketUniqID, mods, ctx); err != nil {
				t.Fatal(err)
			}
		}
		passApply(t, s, ctx, battleBatch(passNow.Unix())...)
		if err := s.MergeBattleModes(socketUniqID, mods, ctx); err != nil {
			t.Fatal(err)
		}
		p, _ := s.Profile(socketUniqID)
		if ppInt(p.BattleWins(), "2") != 1 {
			t.Fatal("double counted modes", first, p.BattleWins())
		}
		summary := p.NativePlayerStatistics(passNow)
		if ppInt(summary, "tws") != 1 || ppInt(summary, "ms") != 1 || summary["ac"] != float64(120) {
			t.Fatal("native stats", summary)
		}
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		p, _ = s.Profile(socketUniqID)
		if ppInt(p.BattleWins(), "2") != 1 {
			t.Fatal("lost modes on restart")
		}
	}
}
func TestBattleStatisticsImportV2WindowAndHistoryLimit(t *testing.T) {
	s, _, ctx := passSetup(t)
	base := map[string]any{"84": map[string]any{"ds": map[string]any{"10-21-2026": map[string]any{"m": 2, "w": 1, "h": 50, "s": 100, "k": map[string]any{"t": map[string]any{"k": 20, "d": 4}}, "ws": 2}, "9-1-2026": map[string]any{"m": 500}}}, "80": map[string]any{"ds": map[string]any{"10-21-2026": map[string]any{"m": 999}}}}
	if _, err := s.MergeProfileProgressSlots(socketUniqID, base, ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		passApply(t, s, ctx, counterCommand(254, fmt.Sprint("h", i), map[string]any{"m": battleFixture(socketUniqID, passNow.Unix()-int64(i))}))
	}
	p, _ := s.Profile(socketUniqID)
	got := p.NativePlayerStatistics(passNow)
	if ppInt(got, "ms") != 2 || ppInt(got, "tws") != 12 || got["klr"] != float64(5) {
		t.Fatal(got)
	}
	if len(battleArray(objectAt(p.ProgressSlots, "57")["h"])) != 10 {
		t.Fatal("history must be native last10")
	}
}
func TestBattleStatisticsConcurrentRetriesAndIsolation(t *testing.T) {
	s, _, ctx := passSetup(t)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, battleBatch(passNow.Unix()), passNow); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	p, _ := s.Profile(socketUniqID)
	if ppInt(p.BattleWins(), "2") != 1 || ppInt(objectAt(p.ProgressSlots, "84", "ds", "10-21-2026"), "m") != 1 {
		t.Fatal("concurrent replay")
	}
	if err := s.MergeBattleModes(socketUniqID, map[string]any{}, ProgressWriteContext{Device: ctx.Device, Revision: ctx.Revision + 1}); err != ErrStaleProgressRevision {
		t.Fatal("missing revision fence", err)
	}
}
func TestBattleStatisticsSeasonUsesNativeIDMap(t *testing.T) {
	s, _, ctx := passSetup(t)
	d := dailyFixture("10-21-2026")
	d["c"] = true
	d["sid"] = "season-1"
	d["sw"] = 7
	passApply(t, s, ctx, counterCommand(370, "season-stat", d))
	p, _ := s.Profile(socketUniqID)
	if ppInt(objectAt(p.ProgressSlots, "84", "ss", "season-1"), "ws") != 7 {
		t.Fatal("invalid native season envelope")
	}
}

func sameBattleJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func TestBattleModeMigrationAliases(t *testing.T) {
	s, _, ctx := passSetup(t)
	if err := s.MergeBattleModes(socketUniqID, map[string]any{"br": 3, "re": 2, "rm": 1, "rh": 4}, ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Profile(socketUniqID)
	wins := p.BattleWins()
	for key, want := range map[string]int64{"20": 3, "22e": 2, "22m": 1, "22h": 4} {
		if ppInt(wins, key) != want {
			t.Fatal("unmapped stock migration", wins)
		}
	}
}
func TestBattleDailyWindowPrunesOldestCalendarDate(t *testing.T) {
	s, _, ctx := passSetup(t)
	for i := 31; i >= 0; i-- {
		day := passNow.AddDate(0, 0, -i).Format("1-2-2006")
		passApply(t, s, ctx, counterCommand(370, fmt.Sprint("day", i), dailyFixture(day)))
	}
	p, _ := s.Profile(socketUniqID)
	days := objectAt(p.ProgressSlots, "84", "ds")
	if len(days) != 30 {
		t.Fatal("daily retention", len(days))
	}
	if _, exists := days[passNow.AddDate(0, 0, -31).Format("1-2-2006")]; exists {
		t.Fatal("lexical pruning instead of calendar date")
	}
}
func TestBattleModesDiskFailureRollsBack(t *testing.T) {
	s, _, ctx := passSetup(t)
	before, _ := s.Profile(socketUniqID)
	s.path = filepath.Join(t.TempDir(), "missing", "state.json")
	if s.MergeBattleModes(socketUniqID, map[string]any{"2": 5}, ctx) == nil {
		t.Fatal("missing write failure")
	}
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("partial mode mutation")
	}
}

// Captured stock end-of-match batch: 153 carries accumulated task fields, not
// a request to reset tasks. A future calendar entry must not discard the match.
func TestBattleStatisticsWithNativeInBattleTaskFlush(t *testing.T) {
	s, dir, ctx := passSetup(t)
	before, _ := s.Profile(socketUniqID)
	batch := battleBatch(passNow.Unix())
	batch = append(batch, counterCommand(153, "native-task-flush", map[string]any{
		"5_1000": map[string]any{"p": 1}, "5_2000": map[string]any{"p": 1},
		"5_1003": map[string]any{"p": 1}, "5_1006": map[string]any{"p": 1},
	}))
	passApply(t, s, ctx, batch...)
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := reopened.Profile(socketUniqID)
	if len(battleArray(objectAt(p.ProgressSlots, "57")["h"])) != 1 || ppInt(objectAt(p.PixelPass.Tasks, "5_1000"), "p") != 1 {
		t.Fatal("match/task flush lost")
	}
	if ppInt(objectAt(p.PixelPass.Tasks, "5_1003"), "p") != 0 || ppInt(objectAt(before.ProgressSlots, "74", "pp"), "e") != ppInt(objectAt(p.ProgressSlots, "74", "pp"), "e") {
		t.Fatal("future progress or crowns changed")
	}
	for _, bad := range []any{
		counterCommand(154, "reset", map[string]any{"5_1000": map[string]any{"p": 0}}),
		counterCommand(153, "bad-field", map[string]any{"5_1003": map[string]any{"r": 2}}),
	} {
		if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{bad}, passNow); err == nil {
			t.Fatal("accepted invalid reset/flush")
		}
	}
}

func TestNativeInBattleTaskFlushCannotEraseCompletedClaims(t *testing.T) {
	s, _, ctx := passSetup(t)
	passApply(t, s, ctx, counterCommand(150, "complete-task-state", map[string]any{"5_1000": map[string]any{"p": 3, "c": 1, "r": 1}}))
	before, _ := s.Profile(socketUniqID)
	passApply(t, s, ctx, counterCommand(153, "stale-task-flush", map[string]any{"5_1000": map[string]any{"p": 0, "c": 0, "r": 0}}))
	after, _ := s.Profile(socketUniqID)
	if !sameBattleJSON(before.PixelPass, after.PixelPass) {
		t.Fatal("stale in-battle flush erased pass state")
	}
}

func TestBattleStatisticsWithNativeCompletedTaskBatch(t *testing.T) {
	s, _, ctx := passSetup(t)
	batch := battleBatch(passNow.Unix())
	batch = append(batch,
		counterCommand(151, "completed-open", map[string]any{"5_1000": map[string]any{"p": 3, "c": 1}}),
		counterCommand(151, "completed-cached", map[string]any{"5_2000": map[string]any{"p": 3}, "5_1003": map[string]any{"p": 3, "c": 1}}),
		counterCommand(151, "completed-cached-2", map[string]any{"5_1006": map[string]any{"p": 3, "c": 1}}))
	passApply(t, s, ctx, batch...)
	p, _ := s.Profile(socketUniqID)
	if ppInt(p.BattleWins(), "2") != 1 || ppInt(objectAt(p.PixelPass.Tasks, "5_1000"), "c") != 1 {
		t.Fatal("native completion discarded match")
	}
	if ppInt(objectAt(p.PixelPass.Tasks, "5_1003"), "c") != 0 || ppInt(objectAt(p.PixelPass.Tasks, "5_1006"), "c") != 0 {
		t.Fatal("future tasks unlocked")
	}
}
