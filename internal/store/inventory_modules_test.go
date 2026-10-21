// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"reflect"
	"testing"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
)

func TestModuleStorageIdentityAdapterCoversAllCatalogEntries(t *testing.T) {
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
		record, err := newModuleStorageRecord(item)
		if err != nil || record["id"] != item.ID || record["s"] != int64(item.Index) || record["t"] != moduleType || record["c"] != category {
			t.Fatalf("invalid storage record for %s: %#v err=%v", item.ID, record, err)
		}
	}
}

func TestModuleInventoryGrantStoresOwnershipAndLevelSeparately(t *testing.T) {
	s, dir, _ := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	item, ok := catalog.Find(1150, 304009)
	if !ok || !item.Editable {
		t.Fatal("module missing from editable catalog", item)
	}
	p.ProgressSlots["27"] = map[string]any{"future": "keep"}
	p.ProgressSlots["37"] = map[string]any{"343009": int64(4), "future": int64(7)}
	stored := p.clone()
	s.state.Profiles[socketUniqID] = &stored

	edit := editFor(p, 1150, item.Index, "grant")
	edit.Quantity, edit.Level, edit.Rarity = 1, 3, 0
	got, changed, err := s.EditInventory(socketUniqID, edit)
	if err != nil || !changed {
		t.Fatalf("grant: changed=%v err=%v", changed, err)
	}
	section := objectAt(got.ProgressSlots, "27")
	records, ok := section["modules_v2"].([]any)
	if !ok || len(records) != 1 {
		t.Fatalf("module storage: %#v", section)
	}
	record, ok := records[0].(map[string]any)
	if !ok || record["id"] != item.ID || record["s"] != int64(item.Index) || record["t"] != int64(1) || record["c"] != int64(1) {
		t.Fatalf("module identity record: %#v", records[0])
	}
	if section["future"] != "keep" || objectAt(got.ProgressSlots, "37")["343009"] != int64(4) || objectAt(got.ProgressSlots, "37")["future"] != int64(7) {
		t.Fatal("module grant changed unrelated data")
	}
	if level := objectAt(got.ProgressSlots, "37")["304009"]; level != int64(3) {
		t.Fatalf("stored module level = %#v", level)
	}
	qty, level, _ := inventoryValue(got, item)
	if qty != 1 || level != 3 {
		t.Fatalf("inventory projection = quantity %d level %d", qty, level)
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
	records, ok = objectAt(updated.ProgressSlots, "27")["modules_v2"].([]any)
	if !ok || len(records) != 1 || moduleLevel(updated, item) != 10 {
		t.Fatal("level update duplicated ownership or missed level", records, moduleLevel(updated, item))
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
	s, _, _ := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	item, _ := catalog.Find(1150, 304009)
	p.ProgressSlots["27"] = map[string]any{"modules_v2": "not-a-list"}
	stored := p.clone()
	s.state.Profiles[socketUniqID] = &stored
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
}
