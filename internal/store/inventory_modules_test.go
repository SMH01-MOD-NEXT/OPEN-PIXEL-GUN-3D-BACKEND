// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
)

func moduleEdit(p Profile, index int, level int64) InventoryEdit {
	e := editFor(p, 1150, index, "grant")
	e.Quantity, e.Level, e.Rarity = 1, level, 0
	return e
}

func TestModuleStorageMappingCoversCatalogAndIgnoresConfigRecords(t *testing.T) {
	seen := map[int64]bool{}
	for _, item := range catalog.Search("", 1150, false) {
		typ, ok := moduleType(item)
		if !ok || typ < 1 || typ > 24 {
			t.Fatalf("module type mapping: %+v => %d %v", item, typ, ok)
		}
		seen[typ] = true
	}
	if len(seen) != 24 {
		t.Fatalf("module type coverage: %v", seen)
	}
	item, _ := catalog.Find(1150, 326009)
	p := Profile{ProgressSlots: map[string]any{
		"27": map[string]any{"modules_v2": []any{map[string]any{"id": item.ID, "s": item.Index}}},
		"37": map[string]any{strconv.Itoa(item.Index): int64(4)},
	}}
	if q, l, _ := inventoryValue(p, item); q != 0 || l != 0 {
		t.Fatal("configuration record or level alone claimed ownership", q, l)
	}
	p.ProgressSlots["27"].(map[string]any)["5"] = []any{int64(item.Index)}
	if q, l, _ := inventoryValue(p, item); q != 1 || l != 4 {
		t.Fatal("canonical ModuleStorage record not recognized", q, l)
	}
}

func TestModuleGrantModuleStorageAllCatalogAndLevels(t *testing.T) {
	for _, item := range catalog.Search("", 1150, false) {
		t.Run(item.ID, func(t *testing.T) {
			s, dir, _ := syncedGameplayStore(t)
			p, _ := s.Profile(socketUniqID)
			p.ProgressSlots["27"] = map[string]any{"future": "keep", "modules_v2": []any{map[string]any{"future": true}}}
			p.ProgressSlots["26"] = map[string]any{"u": map[string]any{"999": map[string]any{"41": int64(123)}}, "g": int64(2), "future": "keep", "s": map[string]any{"9023": int64(1)}}
			p.ProgressSlots["37"] = map[string]any{"future": int64(7)}
			p.ProgressDocument["26"] = cloneAny(p.ProgressSlots["26"])
			p.ProgressDocument["27"] = cloneAny(p.ProgressSlots["27"])
			stored := p.clone()
			s.state.Profiles[socketUniqID] = &stored
			typ, _ := moduleType(item)
			group := strconv.FormatInt(typ, 10)
			for level := int64(1); level <= 10; level++ {
				before := p.clone()
				got, changed, err := s.EditInventory(socketUniqID, moduleEdit(p, item.Index, level))
				if err != nil || !changed {
					t.Fatal(level, changed, err)
				}
				indices, ok := objectAt(got.ProgressSlots, "27")[group].([]any)
				storedIndex, valid := counterInt64(indices[0])
				if !ok || len(indices) != 1 || !valid || storedIndex != int64(item.Index) {
					t.Fatal("bad ModuleStorage ownership", objectAt(got.ProgressSlots, "27"))
				}
				if q, l, _ := inventoryValue(got, item); q != 1 || l != level {
					t.Fatal("ownership/level", q, l)
				}
				if !reflect.DeepEqual(got.ProgressSlots["26"], before.ProgressSlots["26"]) || objectAt(got.ProgressSlots, "27")["future"] != "keep" || !reflect.DeepEqual(objectAt(got.ProgressSlots, "27")["modules_v2"], objectAt(before.ProgressSlots, "27")["modules_v2"]) {
					t.Fatal("unrelated inventory changed")
				}
				if !reflect.DeepEqual(got.ProgressDocument["27"], got.ProgressSlots["27"]) {
					t.Fatal("full sync missing ModuleStorage ownership")
				}
				retry, changed, err := s.EditInventory(socketUniqID, moduleEdit(got, item.Index, level))
				if err != nil || changed || retry.ProgressRevision != got.ProgressRevision {
					t.Fatal("retry", changed, err)
				}
				s, err = Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				p, err = s.Profile(socketUniqID)
				if err != nil {
					t.Fatal(err)
				}
				if q, l, _ := inventoryValue(p, item); q != 1 || l != level {
					t.Fatal("not persisted", q, l)
				}
			}
		})
	}
}

func TestModuleGrantValidationAndAtomicRollback(t *testing.T) {
	badSlots := []map[string]any{
		{"27": "bad"}, {"27": map[string]any{"1": "bad"}},
		{"37": "bad"}, {"37": map[string]any{"304009": "bad"}},
	}
	for i, slots := range badSlots {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			s, _, _ := syncedGameplayStore(t)
			p, _ := s.Profile(socketUniqID)
			for key, v := range slots {
				p.ProgressSlots[key] = v
			}
			stored := p.clone()
			s.state.Profiles[socketUniqID] = &stored
			before := stored.clone()
			if _, _, err := s.EditInventory(socketUniqID, moduleEdit(p, 304009, 3)); err == nil {
				t.Fatal("malformed record accepted")
			}
			got, _ := s.Profile(socketUniqID)
			if !reflect.DeepEqual(got, before) {
				t.Fatal("rejected grant mutated profile")
			}
		})
	}
	s, dir, _ := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	for _, level := range []int64{0, 11} {
		if _, _, err := s.EditInventory(socketUniqID, moduleEdit(p, 304009, level)); err == nil {
			t.Fatal("bad level", level)
		}
	}
	e := moduleEdit(p, 304009, 1)
	e.Action = "revoke"
	if _, _, err := s.EditInventory(socketUniqID, e); err == nil {
		t.Fatal("unsupported revoke")
	}
	if err := os.Remove(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "state.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EditInventory(socketUniqID, moduleEdit(p, 304009, 3)); !errors.Is(err, ErrInventoryPersistence) {
		t.Fatal("disk failure", err)
	}
	got, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(got, p) {
		t.Fatal("disk failure mutated profile")
	}
}

func TestModuleUpgradeCommandPersistenceRetryAndStaleSnapshot(t *testing.T) {
	s, dir, ctx := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	p.ProgressSlots["27"] = map[string]any{"5": []any{int64(326009)}}
	p.ProgressSlots["37"] = map[string]any{"326009": int64(1)}
	p.ProgressDocument["27"] = cloneAny(p.ProgressSlots["27"])
	p.ProgressDocument["37"] = cloneAny(p.ProgressSlots["37"])
	stored := p.clone()
	s.state.Profiles[socketUniqID] = &stored
	commands := []any{counterCommand(87, "module-up", map[string]any{"i": 326009})}
	n, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, commands)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	n, err = s.ApplyProfileCounterCommands(socketUniqID, ctx, commands)
	if err != nil || n != 0 {
		t.Fatal("replay", n, err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ = s.Profile(socketUniqID)
	item, _ := catalog.Find(1150, 326009)
	if q, l, _ := inventoryValue(p, item); q != 1 || l != 2 {
		t.Fatal("command persistence", q, l)
	}
	got, _, err := s.EditInventory(socketUniqID, moduleEdit(p, 326009, 7))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MergeProfileProgressSlots(socketUniqID, map[string]any{"27": map[string]any{"5": []any{}}, "37": map[string]any{}}, ctx); !errors.Is(err, ErrStaleProgressRevision) {
		t.Fatal("stale upload", err)
	}
	if _, err = s.ApplyProfileCounterCommands(socketUniqID, ctx, commands); !errors.Is(err, ErrStaleProgressRevision) {
		t.Fatal("stale commands", err)
	}
	current := ProgressWriteContext{Device: ctx.Device, Revision: got.ProgressRevision}
	blocked, err := s.MergeProfileProgressSlots(socketUniqID, map[string]any{"27": map[string]any{"5": []any{}}}, current)
	if err != nil {
		t.Fatal(err)
	}
	if !moduleOwned(blocked, item) {
		t.Fatal("pending sync erased grant")
	}
}

func TestModuleCommandsAtomicChargeAndUpgrade(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	before := p.clone()
	if _, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, []any{counterCommand(10, "reward", map[string]any{"c": "Coins", "v": 1}), counterCommand(87, "unowned", map[string]any{"i": 326009})}); err == nil {
		t.Fatal("unowned upgrade accepted")
	}
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(after, before) {
		t.Fatal("failed upgrade batch partially applied")
	}
}

func TestModuleStorageMembershipUsesIndices(t *testing.T) {
	item, _ := catalog.Find(1150, 326009)
	for _, index := range []any{int64(326009), float64(326009)} {
		p := Profile{ProgressSlots: map[string]any{"27": map[string]any{"5": []any{index}}}}
		if !moduleOwned(p, item) || moduleLevel(p, item) != 1 {
			t.Fatal("ModuleStorage index ignored", index)
		}
	}
	p := Profile{ProgressSlots: map[string]any{"27": map[string]any{"6": []any{int64(326009)}}}}
	if moduleOwned(p, item) {
		t.Fatal("wrong module type group claimed ownership")
	}
}

func TestModuleUpgradeRejectsMaximumAndMalformedLevel(t *testing.T) {
	for _, raw := range []any{int64(10), int64(0), "bad"} {
		s, _, ctx := syncedGameplayStore(t)
		p, _ := s.Profile(socketUniqID)
		p.ProgressSlots["27"] = map[string]any{"5": []any{int64(326009)}}
		p.ProgressSlots["37"] = map[string]any{"326009": raw}
		stored := p.clone()
		s.state.Profiles[socketUniqID] = &stored
		_, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, []any{counterCommand(87, "bad-upgrade", map[string]any{"i": 326009})})
		if err == nil {
			t.Fatal("invalid upgrade accepted", raw)
		}
		got, _ := s.Profile(socketUniqID)
		if !reflect.DeepEqual(got, p) {
			t.Fatal("rejected upgrade changed profile")
		}
	}
}
