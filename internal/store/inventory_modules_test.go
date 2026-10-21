// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
)

func TestModuleStorageMappingCoversAllCatalogEntries(t *testing.T) {
	modules := catalog.Search("", 1150, false)
	if len(modules) != 42 {
		t.Fatalf("module definitions: %d", len(modules))
	}
	seen := map[int]bool{}
	for _, item := range modules {
		moduleType, category, ok := moduleTypeAndCategory(item)
		if !ok || moduleType < 1 || moduleType > 24 || category < 1 || category > 7 || seen[item.Index] {
			t.Fatalf("invalid module identity mapping: %+v type=%d category=%d", item, moduleType, category)
		}
		seen[item.Index] = true
	}
}

func TestUnownedModuleGrantWritesStockOwnershipAndMigratesLegacy(t *testing.T) {
	s, dir, _ := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	item, ok := catalog.Find(1150, 304009)
	if !ok || !item.Editable {
		t.Fatal("module missing from editable catalog", item)
	}
	legacy, ok := catalog.Find(1150, 343009)
	if !ok {
		t.Fatal("legacy fixture module missing")
	}
	if quantity, _, _ := inventoryValue(p, item); quantity != 0 {
		t.Fatalf("fixture module must genuinely start unowned, got quantity %d", quantity)
	}
	legacyRecord := map[string]any{"id": legacy.ID, "s": int64(legacy.Index), "t": int64(1), "c": int64(1), "future": "keep"}
	p.ProgressSlots["27"] = map[string]any{"future": "keep", "modules_v2": []any{legacyRecord}}
	p.ProgressSlots["37"] = map[string]any{strconv.Itoa(legacy.Index): int64(4), "future": int64(7)}
	stored := p.clone()
	s.state.Profiles[socketUniqID] = &stored

	edit := editFor(p, 1150, item.Index, "grant")
	edit.Quantity, edit.Level, edit.Rarity = 1, 3, 0
	got, changed, err := s.EditInventory(socketUniqID, edit)
	if err != nil || !changed {
		t.Fatalf("grant: changed=%v err=%v", changed, err)
	}
	section := objectAt(got.ProgressSlots, "27")
	moduleType, _, _ := moduleTypeAndCategory(item)
	indices, ok := section[strconv.FormatInt(moduleType, 10)].([]any)
	legacyType, _, _ := moduleTypeAndCategory(legacy)
	legacyIndices, legacyOK := section[strconv.FormatInt(legacyType, 10)].([]any)
	if !ok || !containsModuleIndex(indices, item.Index) || !legacyOK || !containsModuleIndex(legacyIndices, legacy.Index) {
		t.Fatalf("stock ownership indices = %#v", section)
	}
	if section["future"] != "keep" || !reflect.DeepEqual(section["modules_v2"], []any{legacyRecord}) {
		t.Fatal("migration did not preserve unrelated and legacy data", section)
	}
	if objectAt(got.ProgressSlots, "37")[strconv.Itoa(legacy.Index)] != int64(4) || objectAt(got.ProgressSlots, "37")["future"] != int64(7) {
		t.Fatal("module grant changed unrelated levels")
	}
	if level := objectAt(got.ProgressSlots, "37")[strconv.Itoa(item.Index)]; level != int64(3) {
		t.Fatalf("stored module level = %#v", level)
	}
	qty, level, _ := inventoryValue(got, item)
	if qty != 1 || level != 3 {
		t.Fatalf("new module projection = quantity %d level %d", qty, level)
	}
	if qty, level, _ := inventoryValue(got, legacy); qty != 1 || level != 4 {
		t.Fatalf("migrated legacy module projection = quantity %d level %d", qty, level)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := reopened.Profile(socketUniqID)
	if err != nil {
		t.Fatal(err)
	}
	if qty, level, _ := inventoryValue(saved, item); qty != 1 || level != 3 {
		t.Fatalf("module not persisted: quantity %d level %d", qty, level)
	}
	edit = editFor(saved, 1150, item.Index, "grant")
	edit.Quantity, edit.Level, edit.Rarity = 1, 10, 0
	updated, changed, err := reopened.EditInventory(socketUniqID, edit)
	if err != nil || !changed {
		t.Fatalf("level update: changed=%v err=%v", changed, err)
	}
	indices, ok = objectAt(updated.ProgressSlots, "27")[strconv.FormatInt(moduleType, 10)].([]any)
	if !ok || len(indices) != 1 || moduleLevel(updated, item) != 10 {
		t.Fatal("level update duplicated ownership or missed level", indices, moduleLevel(updated, item))
	}
}

func TestModuleInventoryValidationAndNoOp(t *testing.T) {
	s, _, _ := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	item, _ := catalog.Find(1150, 326009)
	edit := editFor(p, 1150, item.Index, "grant")
	edit.Quantity, edit.Rarity = 1, 0
	edit.Level = 11
	if _, _, err := s.EditInventory(socketUniqID, edit); err == nil {
		t.Fatal("module level above 10 was accepted")
	}
	edit.Level = 1
	got, changed, err := s.EditInventory(socketUniqID, edit)
	if err != nil || !changed {
		t.Fatal("module grant", changed, err)
	}
	edit = editFor(got, 1150, item.Index, "grant")
	edit.Quantity, edit.Level, edit.Rarity = 1, 1, 0
	again, changed, err := s.EditInventory(socketUniqID, edit)
	if err != nil || changed || again.ProgressRevision != got.ProgressRevision {
		t.Fatal("same module level should be a no-op", changed, err)
	}
	edit = editFor(got, 1150, item.Index, "revoke")
	edit.Quantity, edit.Level, edit.Rarity = 1, 1, 0
	if _, _, err := s.EditInventory(socketUniqID, edit); err == nil {
		t.Fatal("unsupported module revoke was accepted")
	}
}

func TestModuleInventoryRejectsMalformedStorageWithoutMutation(t *testing.T) {
	for _, slot := range []map[string]any{
		{"modules_v2": "not-a-list"},
		{"1": "not-an-index-list"},
	} {
		t.Run("malformed", func(t *testing.T) {
			s, _, _ := syncedGameplayStore(t)
			p, _ := s.Profile(socketUniqID)
			p.ProgressSlots["27"] = slot
			stored := p.clone()
			s.state.Profiles[socketUniqID] = &stored
			item, _ := catalog.Find(1150, 304009)
			edit := editFor(p, 1150, item.Index, "grant")
			edit.Quantity, edit.Level, edit.Rarity = 1, 1, 0
			before := stored.clone()
			if _, _, err := s.EditInventory(socketUniqID, edit); err == nil {
				t.Fatal("malformed module container was accepted")
			}
			after, _ := s.Profile(socketUniqID)
			if !reflect.DeepEqual(after, before) {
				t.Fatal("failed module edit mutated the profile")
			}
		})
	}
}

func containsModuleIndex(indices []any, target int) bool {
	for _, raw := range indices {
		if index, ok := counterInt64(raw); ok && index == int64(target) {
			return true
		}
	}
	return false
}
