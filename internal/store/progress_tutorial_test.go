// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"reflect"
	"testing"
)

// measuredFirstMatchBatch is the update_progress batch the stock client sends
// when its first battle levels the account from 1 to 2, reduced to the children
// the 2026-10-26 AWS analysis names: 64, 777, 74, 139 and 253. Child shapes,
// including the h/v/u members, are copied from a logged 23.1.3 batch; the 139
// payload keeps a slice of the 168 weapon entries (all 2) it carried, and 253
// names FirstMatch (1300).
func measuredFirstMatchBatch() []any {
	child := func(id int, ci string, p, digest map[string]any) map[string]any {
		return map[string]any{"id": float64(id), "ci": ci, "p": p, "h": digest, "v": "23.1.3"}
	}
	weapons := map[string]any{}
	for _, index := range []string{"102001", "105001", "116001", "16001", "25001", "45001", "48001", "61001", "77001", "84001", "99001", "968001"} {
		weapons[index] = float64(2)
	}
	tutorial := child(253, "adc3e4ec", map[string]any{"t": float64(1300)}, map[string]any{"56": "0ada6bbf2ef5ca9603de372cd30c3d3a"})
	tutorial["u"] = []any{float64(140)}
	return []any{
		child(64, "e5efb632", map[string]any{"l": float64(2)}, map[string]any{"4": "88a4f560d58a9a4b63d567b245c6a3fb"}),
		child(777, "7550aba3", map[string]any{"eid": float64(52), "params": map[string]any{}}, map[string]any{}),
		child(74, "02579b35", map[string]any{"1": "", "2": ""}, map[string]any{"21": "580a25530d12e36794cf0b6dbc2cba9b"}),
		child(74, "629012d0", map[string]any{"1": "Coins", "2": "Gems", "72": "royale_hat_aquarium", "75": "avatar_blogger"}, map[string]any{"21": "f50a663265f87bd1671983fb57aaa538"}),
		child(139, "15972246", map[string]any{"d": weapons}, map[string]any{"29": "1a47ed16808039ad17ff70ef7d6ee286"}),
		tutorial,
	}
}

// newFirstMatchAccount stores the create_slots upload of a brand-new account:
// level 1 and the empty tutorials section the affected AWS profiles hold.
func newFirstMatchAccount(t *testing.T) (*Store, string, ProgressWriteContext) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ProgressWriteContext{Device: socketDevice}
	upload := map[string]any{
		"3":  map[string]any{"Coins": float64(0), "GemsCurrency": float64(0)},
		"4":  map[string]any{"l": float64(1), "e": float64(0)},
		"29": map[string]any{},
		"56": map[string]any{"t": []any{}},
	}
	if _, err := s.MergeProfileProgressSlots(socketUniqID, upload, ctx); err != nil {
		t.Fatal(err)
	}
	return s, dir, ctx
}

func storedTutorialIDs(t *testing.T, p Profile) []int64 {
	t.Helper()
	section, _ := p.ProgressSlots[tutorialSectionSlot].(map[string]any)
	list, _ := section[tutorialSectionList].([]any)
	ids := make([]int64, 0, len(list))
	for _, value := range list {
		id, ok := heldTutorialID(value)
		if !ok {
			t.Fatalf("unreadable stored tutorial id %#v", value)
		}
		ids = append(ids, id)
	}
	return ids
}

// TestMeasuredFirstMatchBatchPersistsLevelAndTutorial is the 2026-10-26 reset:
// the 139 payload used to fail the whole atomic batch, so the level-up and the
// tutorial completion were lost together.
func TestMeasuredFirstMatchBatchPersistsLevelAndTutorial(t *testing.T) {
	s, dir, ctx := newFirstMatchAccount(t)
	// 64, 139 and 253 have reducers; 777 and 74 keep their snapshot path.
	if n, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, measuredFirstMatchBatch()); err != nil || n != 3 {
		t.Fatalf("first-match batch applied %d commands (err %v), want 64, 139 and 253", n, err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := reopened.Profile(socketUniqID)
	if err != nil {
		t.Fatal(err)
	}
	if level, _ := counterInt64(objectAt(p.ProgressSlots, "4")["l"]); p.Level != 2 || level != 2 {
		t.Fatalf("level is %d (slot 4: %d), want the level-up to 2 persisted", p.Level, level)
	}
	if got, _ := counterInt64(objectAt(p.ProgressSlots, "29")["968001"]); got != 2 {
		t.Fatalf("slot 29 records %d, want the level the client reported", got)
	}
	if got := storedTutorialIDs(t, p); !reflect.DeepEqual(got, []int64{1300}) {
		t.Fatalf("stored tutorial history is %v, want the completed first match", got)
	}
	// The client retries the same batch after a lost acknowledgement.
	if n, err := reopened.ApplyProfileCounterCommands(socketUniqID, ctx, measuredFirstMatchBatch()); err != nil || n != 0 {
		t.Fatalf("retry applied %d commands (err %v), want receipts to absorb it", n, err)
	}
	again, _ := reopened.Profile(socketUniqID)
	if !reflect.DeepEqual(again.ProgressSlots, p.ProgressSlots) || again.Level != 2 {
		t.Fatal("retry changed the stored progress")
	}
}

func TestSlot29AcceptsStockPlayerLevelsOnly(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	accepted := map[string]any{"16001": float64(1), "25001": float64(2), "45001": float64(10), "48001": float64(11), "61001": float64(32), "77001": float64(65), "84001": 2}
	if n, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, []any{counterCommand(139, "levels", map[string]any{"d": accepted})}); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	p, _ := s.Profile(socketUniqID)
	for index, value := range accepted {
		want, _ := counterInt64(value)
		if got, ok := counterInt64(objectAt(p.ProgressSlots, "29")[index]); !ok || got != want {
			t.Fatalf("slot 29[%s] is %d, want %d", index, got, want)
		}
	}
	reject := func(name string, levels map[string]any) {
		t.Helper()
		before, _ := s.Profile(socketUniqID)
		batch := []any{counterCommand(64, "level-"+name, map[string]any{"l": 3}), counterCommand(139, "bad-"+name, map[string]any{"d": levels})}
		if _, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, batch); err == nil {
			t.Fatalf("slot-29 payload %s %v was accepted", name, levels)
		}
		if after, _ := s.Profile(socketUniqID); !reflect.DeepEqual(before, after) {
			t.Fatalf("rejected %s batch was partially applied", name)
		}
	}
	for name, value := range map[string]any{"above-cap": float64(66), "negative": float64(-1), "fraction": 1.5, "string": "2", "null": nil} {
		reject(name, map[string]any{"99001": value})
	}
	for _, index := range []string{"0", "-1", "weapon"} {
		reject("index-"+index, map[string]any{index: float64(2)})
	}
}

func TestTutorialCompletionOnlyAddsIDs(t *testing.T) {
	s, dir, ctx := syncedGameplayStore(t)
	// A stored history can mix the encodings JSON, Go and older documents use.
	held := map[string]any{"56": map[string]any{"t": []any{float64(1100), int64(1500), "1200"}, "future": "kept"}}
	if _, err := s.MergeProfileProgressSlots(socketUniqID, held, ctx); err != nil {
		t.Fatal(err)
	}
	batch := []any{
		counterCommand(253, "first-match", map[string]any{"t": float64(1300)}),
		counterCommand(253, "rancho-again", map[string]any{"t": 1100}),
		counterCommand(253, "string-held", map[string]any{"t": float64(1200)}),
	}
	if n, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, batch); err != nil || n != len(batch) {
		t.Fatal(n, err)
	}
	p, _ := s.Profile(socketUniqID)
	if got := storedTutorialIDs(t, p); !reflect.DeepEqual(got, []int64{1100, 1500, 1200, 1300}) {
		t.Fatalf("tutorial history is %v, want held ids kept in order and 1300 appended once", got)
	}
	if objectAt(p.ProgressSlots, "56")["future"] != "kept" {
		t.Fatal("another section member was dropped")
	}
	// After a restart the held ids are JSON numbers; a new child id for an
	// already completed step is still a no-op.
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	next := []any{counterCommand(253, "first-match-new-ci", map[string]any{"t": 1300}), counterCommand(253, "second-match", map[string]any{"t": float64(1400)})}
	if n, err := reopened.ApplyProfileCounterCommands(socketUniqID, ctx, next); err != nil || n != len(next) {
		t.Fatal(n, err)
	}
	p, _ = reopened.Profile(socketUniqID)
	if got := storedTutorialIDs(t, p); !reflect.DeepEqual(got, []int64{1100, 1500, 1200, 1300, 1400}) {
		t.Fatalf("tutorial history after restart is %v", got)
	}
}

func TestTutorialCompletionRejectsMalformedPayloads(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	for name, payload := range map[string]map[string]any{
		"missing":      {},
		"zero":         {"t": float64(0)},
		"negative":     {"t": float64(-1300)},
		"fraction":     {"t": 1300.5},
		"string":       {"t": "1300"},
		"list":         {"t": []any{float64(1300)}},
		"beyond-int32": {"t": float64(1 << 31)},
	} {
		before, _ := s.Profile(socketUniqID)
		batch := []any{counterCommand(64, "level-"+name, map[string]any{"l": 2}), counterCommand(253, "tutorial-"+name, payload)}
		if _, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, batch); err == nil {
			t.Fatalf("%s tutorial payload %v was accepted", name, payload)
		}
		if after, _ := s.Profile(socketUniqID); !reflect.DeepEqual(before, after) {
			t.Fatalf("rejected %s batch was partially applied", name)
		}
	}
}

func TestTutorialCompletionCreatesSectionButNeverOverwritesForeignShapes(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	if n, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, []any{counterCommand(253, "fresh", map[string]any{"t": 1300})}); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	p, _ := s.Profile(socketUniqID)
	if got := storedTutorialIDs(t, p); !reflect.DeepEqual(got, []int64{1300}) {
		t.Fatalf("missing section became %v, want [1300]", got)
	}
	for name, section := range map[string]any{"scalar-section": "x", "scalar-list": map[string]any{"t": "x"}} {
		if _, err := s.MergeProfileProgressSlots(socketUniqID, map[string]any{"56": section}, ctx); err != nil {
			t.Fatal(err)
		}
		before, _ := s.Profile(socketUniqID)
		if _, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, []any{counterCommand(253, "over-"+name, map[string]any{"t": 1400})}); err == nil {
			t.Fatalf("%s history was overwritten", name)
		}
		if after, _ := s.Profile(socketUniqID); !reflect.DeepEqual(before, after) {
			t.Fatalf("%s history changed", name)
		}
	}
}

func TestTutorialHistoryIsBounded(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	full := make([]any, 0, maxTutorialSectionIDs)
	for id := 1; id <= maxTutorialSectionIDs; id++ {
		full = append(full, float64(id))
	}
	if _, err := s.MergeProfileProgressSlots(socketUniqID, map[string]any{"56": map[string]any{"t": full}}, ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, []any{counterCommand(253, "held", map[string]any{"t": 1})}); err != nil || n != 1 {
		t.Fatalf("re-completing a held id failed: %d %v", n, err)
	}
	if _, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, []any{counterCommand(253, "overflow", map[string]any{"t": maxTutorialSectionIDs + 1})}); err == nil {
		t.Fatal("tutorial history grew past its bound")
	}
}
