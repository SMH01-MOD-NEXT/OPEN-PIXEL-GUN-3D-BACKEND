// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func counterCommand(id int, ci string, data map[string]any) any {
	return map[string]any{"id": id, "ci": ci, "p": data}
}
func syncedGameplayStore(t *testing.T) (*Store, string, ProgressWriteContext) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, ""); err != nil {
		t.Fatal(err)
	}
	profile, err := s.SetProfile(socketUniqID, "Veteran", 65, 500, 999999, 999999)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AcknowledgeProfileProgressRevision(socketUniqID, socketDevice, profile.ProgressRevision); err != nil || !ok {
		t.Fatal("ack", err)
	}
	return s, dir, ProgressWriteContext{Device: socketDevice, Revision: profile.ProgressRevision}
}
func requireCoherentCounters(t *testing.T, p Profile, xp, coins, gems int64) {
	t.Helper()
	_, e, c, g := p.Counters()
	if e != xp || c != coins || g != gems {
		t.Fatalf("counters XP/coins/gems=%d/%d/%d", e, c, g)
	}
	if p.Experience != xp || p.Coins != coins || p.Gems != gems {
		t.Fatal("scalar projection mismatch")
	}
	for name, doc := range map[string]map[string]any{"init": p.ProgressDocument, "slots": p.ProgressSlots} {
		currency, _ := doc["3"].(map[string]any)
		progress, _ := doc["4"].(map[string]any)
		cv, _ := counterInt64(currency["Coins"])
		gv, _ := counterInt64(currency["GemsCurrency"])
		ev, _ := counterInt64(progress["e"])
		if cv != coins || gv != gems || ev != xp {
			t.Fatalf("%s core mismatch", name)
		}
	}
}
func TestGameplayCountersPersistAfterAdminSyncWithoutRegrant(t *testing.T) {
	s, dir, ctx := syncedGameplayStore(t)
	commands := []any{
		counterCommand(66, "coins1", map[string]any{"c": "Coins", "v": 2265}),
		counterCommand(66, "gems1", map[string]any{"c": "GemsCurrency", "v": 900}),
		counterCommand(65, "chest1", map[string]any{"e": 0}),
		counterCommand(10, "reward1", map[string]any{"c": "GemsCurrency", "v": 12}),
	}
	n, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, commands)
	if err != nil || n != 4 {
		t.Fatal(n, err)
	}
	p, _ := s.Profile(socketUniqID)
	requireCoherentCounters(t, p, 0, 997734, 999111)
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	n, err = reopened.ApplyProfileCounterCommands(socketUniqID, ctx, commands)
	if err != nil || n != 0 {
		t.Fatalf("replay = %d/%v", n, err)
	}
	p, _ = reopened.Profile(socketUniqID)
	requireCoherentCounters(t, p, 0, 997734, 999111)
	// Absolute client snapshot is not another delta. Inventory survives alongside
	// the updated money and every download channel agrees after another reopen.
	slots := p.ProgressSlots
	slots["28"] = map[string]any{"weapon": 141001}
	if _, err := reopened.ReplaceProfileProgressSlots(socketUniqID, slots, ctx); err != nil {
		t.Fatal(err)
	}
	reopened, _ = Open(dir)
	p, _ = reopened.Profile(socketUniqID)
	requireCoherentCounters(t, p, 0, 997734, 999111)
	if p.ProgressSlots["28"].(map[string]any)["weapon"] != float64(141001) {
		t.Fatal("weapon lost")
	}
	if _, pending := p.PendingProgressRevision(socketDevice); pending {
		t.Fatal("gameplay rearmed admin override")
	}
}
func TestPostSyncFullSnapshotUpdatesEveryCounterRepresentation(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	slots := p.ProgressSlots
	slots["3"] = map[string]any{"Coins": 999099, "GemsCurrency": 999999}
	slots["4"] = map[string]any{"l": 65, "e": 20}
	after, err := s.ReplaceProfileProgressSlots(socketUniqID, slots, ctx)
	if err != nil {
		t.Fatal(err)
	}
	requireCoherentCounters(t, after, 20, 999099, 999999)
}
func TestPendingAndStaleDevicesCannotOverwriteAnyAdminSlot(t *testing.T) {
	s, dir, old := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	slots := p.ProgressSlots
	slots["28"] = map[string]any{"admin": "kept"}
	admin, err := s.ReplaceProfileRawState(socketUniqID, p.Prefs, p.Progress, p.ProgressDocument, slots, p.ClientState)
	if err != nil {
		t.Fatal(err)
	}
	reopened, _ := Open(dir)
	before, _ := reopened.Profile(socketUniqID)
	if _, pending := before.PendingProgressRevision(socketDevice); !pending {
		t.Fatal("offline flag not durable")
	}
	if _, err := reopened.ReplaceProfileProgressSlots(socketUniqID, map[string]any{"28": map[string]any{"stale": true}}, old); !errors.Is(err, ErrStaleProgressRevision) {
		t.Fatal("stale session accepted", err)
	}
	pending := ProgressWriteContext{Device: socketDevice, Revision: admin.ProgressRevision}
	after, err := reopened.ReplaceProfileProgressSlots(socketUniqID, map[string]any{"28": map[string]any{"stale": true}}, pending)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("pre-download upload changed full admin snapshot", err)
	}
	_, err = reopened.MergeProfilePrefs(socketUniqID, map[string]string{"skin": "stale"}, pending)
	if err != nil {
		t.Fatal(err)
	}
	after, _ = reopened.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("pre-download prefs changed admin snapshot")
	}
	// Acknowledging another device never unlocks this one.
	reopened.AcknowledgeProfileProgressRevision(socketUniqID, "other-device", admin.ProgressRevision)
	after, err = reopened.ReplaceProfileProgressSlots(socketUniqID, map[string]any{"28": map[string]any{"stale": true}}, pending)
	if err != nil || after.ProgressSlots["28"].(map[string]any)["admin"] != "kept" {
		t.Fatal("foreign device unlocked upload")
	}
}
func TestCounterBatchRollbackAndValidation(t *testing.T) {
	cases := map[string][]any{
		"underflow":     {counterCommand(66, "spend", map[string]any{"c": "Coins", "v": 1000000})},
		"fraction":      {counterCommand(10, "bad", map[string]any{"c": "Coins", "v": 0.5})},
		"negative":      {counterCommand(10, "bad", map[string]any{"c": "Coins", "v": -1})},
		"missing-ci":    {counterCommand(65, "", map[string]any{"e": 0})},
		"partial-batch": {counterCommand(10, "good", map[string]any{"c": "Coins", "v": 1}), counterCommand(65, "bad", map[string]any{"e": -1})},
	}
	for name, commands := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, ctx := syncedGameplayStore(t)
			before, _ := s.Profile(socketUniqID)
			if _, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, commands); err == nil {
				t.Fatal("accepted invalid batch")
			}
			after, _ := s.Profile(socketUniqID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid batch partially mutated profile")
			}
		})
	}
	t.Run("disk failure", func(t *testing.T) {
		s, _, ctx := syncedGameplayStore(t)
		before, _ := s.Profile(socketUniqID)
		s.path = filepath.Join(t.TempDir(), "missing", "state.json")
		if _, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, []any{counterCommand(65, "xp", map[string]any{"e": 0})}); err == nil {
			t.Fatal("persist unexpectedly succeeded")
		}
		after, _ := s.Profile(socketUniqID)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("failed disk write left mutation/receipt")
		}
	})
}
func TestConcurrentCommandRetriesAreAppliedOnce(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	command := []any{counterCommand(66, "one", map[string]any{"c": "Coins", "v": 100})}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, command); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	p, _ := s.Profile(socketUniqID)
	requireCoherentCounters(t, p, 500, 999899, 999999)
}

func TestFirstGameplayCommandsWithoutAdminEditInitializeCoreChannels(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnsureProfile("678095418", "New"); err != nil {
		t.Fatal(err)
	}
	ctx := ProgressWriteContext{Device: "first-device", Revision: 0}
	n, err := s.ApplyProfileCounterCommands("678095418", ctx, []any{
		counterCommand(10, "first-reward", map[string]any{"c": "Coins", "v": 8}),
		counterCommand(65, "first-xp", map[string]any{"e": 20}),
	})
	if n != 2 || err != nil {
		t.Fatal(n, err)
	}
	p, _ := s.Profile("678095418")
	requireCoherentCounters(t, p, 20, 8, 0)
}

func TestStaleReplaceDoesNotLeaveNewProfile(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ReplaceProfileProgressSlots("678095418", map[string]any{}, ProgressWriteContext{Device: "device", Revision: 9})
	if !errors.Is(err, ErrStaleProgressRevision) {
		t.Fatal(err)
	}
	if _, err = s.Profile("678095418"); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed write created profile", err)
	}
}
