// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"encoding/json"
	"errors"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func bulkFor(p Profile, action string) InventoryBulkEdit {
	return InventoryBulkEdit{Action: action, Level: 65, Rarity: 5, Revision: p.ProgressRevision, UpdatedAt: p.UpdatedAt}
}
func confirmedBulk(t *testing.T, s *Store, p Profile, action string) InventoryBulkEdit {
	t.Helper()
	e := bulkFor(p, action)
	if action == "grant_wear" || action == "grant_pets" {
		e.Level, e.Rarity = 1, 0
	}
	if action == "grant_modules" {
		e.Level, e.Rarity = 10, 0
	}
	plan, err := s.PreviewInventoryBulk(p.PlayerID, e)
	if err != nil {
		t.Fatal(err)
	}
	e.PlanToken = plan.PlanToken
	e.ConfirmPlayerID = plan.PlayerID
	return e
}
func TestInventoryBulkGrantAtomicPreservesAndPersists(t *testing.T) {
	s, dir, _ := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	p.ProgressSlots["28"] = map[string]any{"0": map[string]any{"727001": map[string]any{"l": 12, "r": 2}, "999999001": map[string]any{"l": 8}}}
	p.ProgressSlots["91"] = map[string]any{"future": "keep"}
	stored := p.clone()
	s.state.Profiles[p.PlayerID] = &stored
	other, _, err := s.AdoptSocketIdentity("612672164", "other-bulk-device", "")
	if err != nil {
		t.Fatal(err)
	}
	beforeOther, _ := s.Profile(other.PlayerID)
	e := bulkFor(p, "grant_weapons")
	plan, err := s.PreviewInventoryBulk(p.PlayerID, e)
	if err != nil || plan.Changes < 100 || plan.PreservedOwned != 1 || len(plan.Sample) != 8 {
		t.Fatal(plan, err)
	}
	afterPreview, _ := s.Profile(p.PlayerID)
	if !reflect.DeepEqual(p, afterPreview) {
		t.Fatal("preview mutated")
	}
	if _, _, _, err = s.EditInventoryBulk(p.PlayerID, e); err == nil {
		t.Fatal("missing confirmation accepted")
	}
	e.PlanToken = plan.PlanToken
	e.ConfirmPlayerID = other.PlayerID
	if _, _, _, err = s.EditInventoryBulk(p.PlayerID, e); err == nil {
		t.Fatal("wrong target confirmation accepted")
	}
	e.ConfirmPlayerID = p.PlayerID
	e.PlanToken = "bad"
	if _, _, _, err = s.EditInventoryBulk(p.PlayerID, e); err == nil {
		t.Fatal("invalid preview accepted")
	}
	e.PlanToken = plan.PlanToken
	got, applied, changed, err := s.EditInventoryBulk(p.PlayerID, e)
	if err != nil || !changed || applied.Changes != plan.Changes || got.ProgressRevision != p.ProgressRevision+1 {
		t.Fatal(changed, err)
	}
	item, _ := catalog.Find(10, 727001)
	_, level, rarity := inventoryValue(got, item)
	if level != 12 || rarity != 2 {
		t.Fatal("owned weapon overwritten")
	}
	for _, v := range catalog.All() {
		if v.Type == 10 && v.Editable && v.WeaponCategory != nil {
			q, _, _ := inventoryValue(got, v)
			if q < 1 {
				t.Fatal("missing granted weapon", v.Index)
			}
		}
	}
	if !reflect.DeepEqual(got.ProgressSlots["91"], p.ProgressSlots["91"]) || !reflect.DeepEqual(objectAt(got.ProgressSlots, "28", "0")["999999001"], objectAt(p.ProgressSlots, "28", "0")["999999001"]) {
		t.Fatal("unknown data lost")
	}
	afterOther, _ := s.Profile(other.PlayerID)
	if !reflect.DeepEqual(beforeOther, afterOther) {
		t.Fatal("other player changed")
	}
	if _, _, _, err = s.EditInventoryBulk(p.PlayerID, e); !errors.Is(err, ErrInventoryConflict) {
		t.Fatal("stale retry accepted", err)
	}
	fresh := confirmedBulk(t, s, got, "grant_weapons")
	noop, np, changed, err := s.EditInventoryBulk(p.PlayerID, fresh)
	if err != nil || changed || np.Changes != 0 || noop.ProgressRevision != got.ProgressRevision {
		t.Fatal("duplicate bulk changed state", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := reopened.Profile(p.PlayerID)
	a, _ := json.Marshal(saved.ProgressSlots)
	b, _ := json.Marshal(got.ProgressSlots)
	if string(a) != string(b) {
		t.Fatal("bulk not durable")
	}
}
func TestInventoryBulkGrantWearArmorGadgetsPetsAndModules(t *testing.T) {
	cases := []struct {
		action string
		types  map[int]bool
		level  int64
	}{
		{"grant_wear", map[int]bool{30: true, 40: true, 50: true, 60: true}, 1},
		{"grant_armor", map[int]bool{20: true}, 3},
		{"grant_gadgets", map[int]bool{70: true}, 3},
		{"grant_pets", map[int]bool{80: true}, 1},
		{"grant_modules", map[int]bool{1150: true}, 10},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			s, _, _ := syncedGameplayStore(t)
			p, _ := s.Profile(socketUniqID)
			p.ProgressSlots = map[string]any{}
			var selected catalog.Item
			total := 0
			for _, item := range catalog.All() {
				if tc.types[item.Type] && item.Editable {
					total++
					if selected.Index == 0 {
						selected = item
					}
				}
			}
			if selected.Index == 0 || total < 2 {
				t.Fatal("bulk category unexpectedly empty", tc.action, total)
			}
			preservedLevel := int64(1)
			if selected.Type == 20 || selected.Type == 70 {
				preservedLevel = 12
			}
			if selected.Type == 1150 {
				preservedLevel = 4
			}
			if err := editInventorySlots(&p, selected, InventoryEdit{Action: "grant", Quantity: 1, Level: preservedLevel}); err != nil {
				t.Fatal("seed existing item", err)
			}
			stored := p.clone()
			s.state.Profiles[p.PlayerID] = &stored

			e := bulkFor(p, tc.action)
			e.Level, e.Rarity = tc.level, 0
			plan, err := s.PreviewInventoryBulk(p.PlayerID, e)
			if err != nil || plan.Changes != total-1 || plan.PreservedOwned != 1 {
				t.Fatalf("preview: %+v, err=%v, total=%d", plan, err, total)
			}
			e.PlanToken, e.ConfirmPlayerID = plan.PlanToken, p.PlayerID
			got, applied, changed, err := s.EditInventoryBulk(p.PlayerID, e)
			if err != nil || !changed || applied.Changes != total-1 || got.ProgressRevision != p.ProgressRevision+1 {
				t.Fatalf("apply: changed=%v plan=%+v err=%v", changed, applied, err)
			}
			for _, item := range catalog.All() {
				if tc.types[item.Type] && item.Editable {
					if quantity, _, _ := inventoryValue(got, item); quantity == 0 {
						t.Fatalf("missing granted item %d:%d", item.Type, item.Index)
					}
					if item.Type == 1150 {
						typ, ok := moduleType(item)
						if !ok {
							t.Fatalf("missing module type for %d", item.Index)
						}
						indices, valid := moduleStorageIndices(got, typ)
						stored := false
						for _, raw := range indices {
							index, ok := counterInt64(raw)
							stored = stored || ok && index == int64(item.Index)
						}
						points, pointValid := modulePointQuantity(got, item)
						if !valid || !stored || !pointValid || points < 1 {
							t.Fatalf("incomplete module linkage for %d", item.Index)
						}
					}
				}
			}
			if tc.action == "grant_modules" && total != 42 {
				t.Fatalf("unexpected module catalog size: %d", total)
			}
			if selected.Type == 20 || selected.Type == 70 || selected.Type == 80 || selected.Type == 1150 {
				if _, level, _ := inventoryValue(got, selected); level != preservedLevel {
					t.Fatalf("already-owned item level changed: got %d want %d", level, preservedLevel)
				}
			}
			fresh := confirmedBulk(t, s, got, tc.action)
			noop, duplicate, changed, err := s.EditInventoryBulk(p.PlayerID, fresh)
			if err != nil || changed || duplicate.Changes != 0 || noop.ProgressRevision != got.ProgressRevision {
				t.Fatalf("duplicate bulk grant was not idempotent: changed=%v plan=%+v err=%v", changed, duplicate, err)
			}
		})
	}
}

func TestInventoryBulkRevokeSkipsEquippedAndUnknown(t *testing.T) {
	for _, action := range []string{"revoke_weapons", "revoke_all"} {
		t.Run(action, func(t *testing.T) {
			s, _, _ := syncedGameplayStore(t)
			p, _ := s.Profile(socketUniqID)
			p.ProgressSlots["28"] = map[string]any{"0": map[string]any{"727001": map[string]any{"l": 65}, "745001": map[string]any{"l": 10}, "999999001": map[string]any{"l": 8}}}
			p.ProgressSlots["67"] = map[string]any{"l": []any{map[string]any{"pw": 727001}}}
			p.ProgressSlots["13"] = map[string]any{"w": []any{20002}}
			stored := p.clone()
			s.state.Profiles[p.PlayerID] = &stored
			e := confirmedBulk(t, s, p, action)
			got, plan, changed, err := s.EditInventoryBulk(p.PlayerID, e)
			if err != nil || !changed || plan.SkippedEquipped != 1 || plan.SkippedUnsupported != 1 {
				t.Fatal(plan, changed, err)
			}
			if objectAt(got.ProgressSlots, "28", "0")["745001"] != nil || objectAt(got.ProgressSlots, "28", "0")["727001"] == nil || objectAt(got.ProgressSlots, "28", "0")["999999001"] == nil {
				t.Fatal("unsafe removal")
			}
			hat, _ := catalog.Find(40, 20002)
			q, _, _ := inventoryValue(got, hat)
			if action == "revoke_weapons" && q != 1 || action == "revoke_all" && q != 0 {
				t.Fatal("scope", q)
			}
			if got.Coins != p.Coins || got.Gems != p.Gems || got.Experience != p.Experience {
				t.Fatal("counters changed")
			}
		})
	}
}
func TestInventoryBulkRollbackAndValidation(t *testing.T) {
	s, dir, _ := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	e := confirmedBulk(t, s, p, "grant_weapons")
	altered := e
	altered.Level = 64
	if _, _, _, err := s.EditInventoryBulk(p.PlayerID, altered); err == nil {
		t.Fatal("parameters not bound to preview")
	}
	for _, invalid := range []InventoryBulkEdit{{Action: "unknown", Level: 65}, {Action: "grant_weapons", Level: 0}, {Action: "grant_weapons", Level: 66}, {Action: "grant_weapons", Level: 65, Rarity: 6}, {Action: "grant_modules", Level: 11}, {Action: "grant_modules", Level: 10, Rarity: 1}} {
		if _, err := s.PreviewInventoryBulk(p.PlayerID, invalid); err == nil {
			t.Fatal("invalid accepted")
		}
	}
	if _, err := s.PreviewInventoryBulk("missing", e); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "block"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.EditInventoryBulk(p.PlayerID, e); !errors.Is(err, ErrInventoryPersistence) {
		t.Fatal(err)
	}
	after, _ := s.Profile(p.PlayerID)
	if !reflect.DeepEqual(p, after) {
		t.Fatal("failed bulk leaked changes")
	}
}
