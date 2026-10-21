// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func editFor(p Profile, typ, index int, action string) InventoryEdit {
	edit := InventoryEdit{Type: typ, Index: index, Action: action, Quantity: 7, Level: 23, Rarity: 4, Revision: p.ProgressRevision, UpdatedAt: p.UpdatedAt}
	if typ == 80 {
		edit.Quantity, edit.Level, edit.Rarity = 1, 1, 0
	}
	return edit
}
func TestInventoryAdaptersPersistGrantAndRevoke(t *testing.T) {
	for _, typ := range []int{10, 20, 30, 40, 50, 60, 65, 70, 80, 1070, 1080, 1155, 1350, 1470, 1510, 1520} {
		t.Run(catalog.Key(typ, 0), func(t *testing.T) {
			s, dir, _ := syncedGameplayStore(t)
			p, _ := s.Profile(socketUniqID)
			var item catalog.Item
			for _, v := range catalog.All() {
				if v.Type == typ && v.Editable {
					item = v
					break
				}
			}
			if item.Index == 0 {
				t.Fatal("missing test category")
			}
			p.ProgressSlots["91"] = map[string]any{"future": []any{"preserve"}}
			stored := p.clone()
			s.state.Profiles[socketUniqID] = &stored
			got, changed, err := s.EditInventory(socketUniqID, editFor(p, typ, item.Index, "grant"))
			if err != nil || !changed {
				t.Fatal(changed, err)
			}
			if got.ProgressRevision != p.ProgressRevision+1 {
				t.Fatal("revision")
			}
			q, level, r := inventoryValue(got, item)
			if q == 0 {
				t.Fatal("not owned")
			}
			if typ == 10 && (level != 23 || r != 4) {
				t.Fatal("weapon data")
			}
			if typ == 80 && level != 1 {
				t.Fatal("new pet must start at level 1", level)
			}
			if typ == 1070 || typ == 1155 || typ == 1520 {
				if q != 7 {
					t.Fatal("quantity")
				}
			}
			if !reflect.DeepEqual(got.ProgressSlots["91"], p.ProgressSlots["91"]) {
				t.Fatal("unknown slot lost")
			}
			reopened, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			saved, _ := reopened.Profile(socketUniqID)
			if q, _, _ := inventoryValue(saved, item); q == 0 {
				t.Fatal("not persisted")
			}
			removed, changed, err := reopened.EditInventory(socketUniqID, editFor(saved, typ, item.Index, "revoke"))
			if err != nil || !changed {
				t.Fatal(changed, err)
			}
			if q, _, _ := inventoryValue(removed, item); q != 0 {
				t.Fatal("not removed")
			}
			again, changed, err := reopened.EditInventory(socketUniqID, editFor(removed, typ, item.Index, "revoke"))
			if err != nil || changed || again.ProgressRevision != removed.ProgressRevision {
				t.Fatal("absent revoke must be no-op", err)
			}
		})
	}
}
func TestInventoryConflictAndEquippedProtection(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	item, _ := catalog.Find(10, 727001)
	p.ProgressSlots["28"] = map[string]any{"0": map[string]any{"727001": map[string]any{"l": 65, "r": 5}}}
	p.ProgressSlots["67"] = map[string]any{"l": []any{map[string]any{"pw": 727001}}}
	stored := p.clone()
	s.state.Profiles[socketUniqID] = &stored
	if _, _, err := s.EditInventory(socketUniqID, editFor(p, 10, item.Index, "revoke")); !errors.Is(err, ErrEquippedItem) {
		t.Fatal(err)
	}
	e := editFor(p, 10, 745001, "grant")
	e.Revision++
	if _, _, err := s.EditInventory(socketUniqID, e); !errors.Is(err, ErrInventoryConflict) {
		t.Fatal(err)
	}
	changed, _, err := s.EditInventory(socketUniqID, editFor(p, 10, 745001, "grant"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyProfileCounterCommands(socketUniqID, ctx, []any{counterCommand(132, "stale", map[string]any{"i": 743001, "u": map[string]any{"l": 1, "r": 0}})}); !errors.Is(err, ErrStaleProgressRevision) {
		t.Fatal("old session wrote", err)
	}
	current := ProgressWriteContext{Device: ctx.Device, Revision: changed.ProgressRevision}
	if n, err := s.ApplyProfileCounterCommands(socketUniqID, current, []any{counterCommand(132, "pending", map[string]any{"i": 743001, "u": map[string]any{"l": 1, "r": 0}})}); err != nil || n != 0 {
		t.Fatal("pending boot must not overwrite", n, err)
	}
}
func TestPurchaseInventoryTransactionReplayAndRollback(t *testing.T) {
	s, dir, ctx := syncedGameplayStore(t)
	batch := []any{counterCommand(66, "pay", map[string]any{"c": "Coins", "v": 100}), counterCommand(132, "own", map[string]any{"i": 727001, "u": map[string]any{"l": 65, "r": 5, "future": "kept"}})}
	if n, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, batch); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	p, _ := s.Profile(socketUniqID)
	if p.Coins != 999899 || objectAt(p.ProgressSlots, "28", "0", "727001")["future"] != "kept" {
		t.Fatal("purchase")
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := reopened.ApplyProfileCounterCommands(socketUniqID, ctx, batch); err != nil || n != 0 {
		t.Fatal("replay", n, err)
	}
	bad := []any{counterCommand(66, "pay2", map[string]any{"c": "Coins", "v": 100}), counterCommand(132, "bad", map[string]any{"i": 745001, "u": map[string]any{"l": 999, "r": 5}})}
	before, _ := reopened.Profile(socketUniqID)
	if _, err := reopened.ApplyProfileCounterCommands(socketUniqID, ctx, bad); err == nil {
		t.Fatal("accepted malformed purchase")
	}
	after, _ := reopened.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("partial purchase")
	}
}
func TestInventoryMutationPersistenceFailureRollback(t *testing.T) {
	s, dir, _ := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	// Atomic writer cannot rename onto a non-empty directory.
	path := filepath.Join(dir, "state.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(path, "block"), []byte("x"), 0600)
	if _, _, err := s.EditInventory(socketUniqID, editFor(p, 10, 727001, "grant")); err == nil {
		t.Fatal("expected persistence error")
	}
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(p, after) {
		t.Fatal("failed edit leaked")
	}
}
func TestMeasuredInventoryCommands(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	batch := []any{
		counterCommand(33, "gadget", map[string]any{"g": 12007, "u": 12}), counterCommand(191, "armor", map[string]any{"a": 75002, "u": 65}), counterCommand(84, "single", map[string]any{"i": 1028}), counterCommand(197, "armor-skin", map[string]any{"w": 75002, "s": 9023}), counterCommand(120, "parts", map[string]any{"i": 1398001, "d": 10}), counterCommand(139, "first-ups", map[string]any{"d": map[string]any{"727001": 1}}), counterCommand(25, "equip", map[string]any{"5": 11004}),
	}
	if n, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, batch); err != nil || n != len(batch) {
		t.Fatal(n, err)
	}
	p, _ := s.Profile(socketUniqID)
	for _, c := range []struct {
		path []string
		key  string
		want int64
	}{{[]string{"16"}, "12007", 12}, {[]string{"43", "0"}, "75002", 65}, {[]string{"26", "s"}, "1028", 1}, {[]string{"44"}, "75002", 9023}, {[]string{"24"}, "1398001", 10}, {[]string{"29"}, "727001", 1}, {[]string{"9"}, "5", 11004}} {
		n, ok := counterInt64(objectAt(p.ProgressSlots, c.path...)[c.key])
		if !ok || n != c.want {
			t.Fatal(c)
		}
	}
}

func TestInventoryUnknownOwnershipIsVisibleReadOnly(t *testing.T) {
	p := Profile{ProgressSlots: map[string]any{
		"28": map[string]any{"0": map[string]any{"999999001": map[string]any{"l": 5, "r": 0}}},
		"26": map[string]any{"s": map[string]any{"999999023": 1}},
		"13": map[string]any{"w": []any{999999002, 999999002}},
		"53": map[string]any{"p": []any{map[string]any{"i": int64(999999003), "c": int64(0), "l": int64(1), "n": ""}}},
	}}
	got := Inventory(p)
	if len(got) != 4 {
		t.Fatalf("unknown owned items hidden/duplicated: %d", len(got))
	}
	for _, item := range got {
		if item.Editable || item.NameSource != "technical_id" || item.Quantity != 1 {
			t.Fatalf("unsafe unknown: %+v", item)
		}
	}
}
func TestPetInventoryUsesProgressRecordsAndProtectsEquippedPet(t *testing.T) {
	s, _, _ := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	activeID, grantID := 1003, 2003
	p.ProgressSlots["53"] = map[string]any{
		"e": []any{map[string]any{"future": true}},
		"p": []any{map[string]any{"c": int64(4), "i": int64(activeID), "l": int64(7), "n": "Retained"}},
	}
	p.ProgressSlots["9"] = map[string]any{"6": int64(activeID), "future": "preserved"}
	stored := p.clone()
	s.state.Profiles[socketUniqID] = &stored

	activeItem, ok := catalog.Find(80, activeID)
	if !ok || !activeItem.Editable {
		t.Fatal("pet is not grantable in catalog", activeItem)
	}
	for _, inv := range Inventory(p) {
		if inv.Type == 80 && inv.Index == activeID && !inv.Equipped {
			t.Fatal("equipped pet was not marked equipped")
		}
	}
	if _, _, err := s.EditInventory(socketUniqID, editFor(p, 80, activeID, "revoke")); !errors.Is(err, ErrEquippedItem) {
		t.Fatal("equipped pet revoke was not rejected", err)
	}

	grantItem, ok := catalog.Find(80, grantID)
	if !ok {
		t.Fatal("missing pet test item")
	}
	invalid := editFor(p, 80, grantItem.Index, "grant")
	invalid.Level = 2
	if _, _, err := s.EditInventory(socketUniqID, invalid); err == nil {
		t.Fatal("accepted non-baseline pet grant")
	}
	got, changed, err := s.EditInventory(socketUniqID, editFor(p, 80, grantItem.Index, "grant"))
	if err != nil || !changed {
		t.Fatal("pet grant", changed, err)
	}
	record, ok := petProgressRecord(got, grantID)
	if !ok {
		t.Fatal("pet record was not added")
	}
	if record["i"] != int64(grantID) || record["c"] != int64(0) || record["l"] != int64(1) || record["n"] != "" {
		t.Fatal("unexpected new-pet defaults", record)
	}
	if objectAt(got.ProgressSlots, "53")["e"].([]any)[0].(map[string]any)["future"] != true || objectAt(got.ProgressSlots, "9")["6"] != int64(activeID) {
		t.Fatal("pet grant damaged unrelated/equipped progress")
	}
	if _, changed, err := s.EditInventory(socketUniqID, editFor(got, 80, grantID, "grant")); err != nil || changed {
		t.Fatal("duplicate pet grant must be idempotent", changed, err)
	}
	current, _ := s.Profile(socketUniqID)
	removed, changed, err := s.EditInventory(socketUniqID, editFor(current, 80, grantID, "revoke"))
	if err != nil || !changed {
		t.Fatal("pet revoke", changed, err)
	}
	if _, ok := petProgressRecord(removed, grantID); ok {
		t.Fatal("revoked pet remains")
	}
	if _, ok := petProgressRecord(removed, activeID); !ok {
		t.Fatal("revoke damaged another pet")
	}
}

func TestPocketHarpoonVariantsGrantThroughGadgetSlot(t *testing.T) {
	s, _, _ := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	p.ProgressSlots["16"] = map[string]any{"future": "preserved"}
	stored := p.clone()
	s.state.Profiles[socketUniqID] = &stored
	for _, index := range []int{63007, 76007} {
		item, ok := catalog.Find(70, index)
		if !ok || !item.Editable || item.Category != "Gadget" {
			t.Fatalf("Pocket Harpoon catalog entry is not editable: %+v", item)
		}
		var changed bool
		var err error
		p, changed, err = s.EditInventory(socketUniqID, editFor(p, 70, index, "grant"))
		if err != nil || !changed {
			t.Fatalf("grant gadget %d: changed=%v err=%v", index, changed, err)
		}
		qty, level, _ := inventoryValue(p, item)
		if qty != 1 || level != 23 || objectAt(p.ProgressSlots, "16")[strconv.Itoa(index)] != int64(23) {
			t.Fatalf("gadget %d not persisted in slot 16", index)
		}
	}
	if objectAt(p.ProgressSlots, "16")["future"] != "preserved" {
		t.Fatal("gadget grant dropped unknown slot fields")
	}
}

func TestPetInventoryMalformedListRejectedWithoutMutation(t *testing.T) {
	s, _, _ := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	p.ProgressSlots["53"] = map[string]any{"p": "not-a-list"}
	stored := p.clone()
	s.state.Profiles[socketUniqID] = &stored
	if _, _, err := s.EditInventory(socketUniqID, editFor(p, 80, 1003, "grant")); err == nil {
		t.Fatal("accepted malformed pet list")
	}
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(p, after) {
		t.Fatal("malformed pet grant mutated profile")
	}
}

func TestInventoryMirrorsOnlyExistingInitProjections(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	p.ProgressDocument = map[string]any{"28": map[string]any{"stale": true}, "extension": map[string]any{"keep": true}, "91": map[string]any{"old": true}}
	stored := p.clone()
	s.state.Profiles[socketUniqID] = &stored
	q, err := s.MergeProfileProgressSlots(socketUniqID, map[string]any{"28": map[string]any{"0": map[string]any{}}, "92": map[string]any{"new": true}}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(q.ProgressDocument["28"], q.ProgressSlots["28"]) {
		t.Fatal("stale init ownership")
	}
	if _, ok := q.ProgressDocument["92"]; ok {
		t.Fatal("raw slot injected into separate init channel")
	}
	q, err = s.ReplaceProfileProgressSlots(socketUniqID, map[string]any{"28": map[string]any{"0": map[string]any{}}}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := q.ProgressDocument["91"]; ok {
		t.Fatal("omitted numeric projection resurrected")
	}
	if q.ProgressDocument["extension"] == nil {
		t.Fatal("extension lost")
	}
}
func TestUnverifiedConsumableAddIsNotInterpreted(t *testing.T) {
	if SupportsProgressCommand(80) {
		t.Fatal("unverified schema must use raw snapshot path")
	}
}
