// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"strconv"
	"strings"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
)

// moduleSectionRecords reads the client-owned module storage in progress slot 27.
// The stock 23.1.3 ModuleStorage container serializes owned v2 modules as
// `modules_v2`; levels are stored separately in ModuleInfo slot 37.
func moduleSectionRecords(p Profile) ([]any, bool) {
	section := objectAt(p.ProgressSlots, "27")
	if section == nil {
		return nil, true
	}
	raw, exists := section["modules_v2"]
	if !exists || raw == nil {
		return nil, true
	}
	records, ok := raw.([]any)
	return records, ok
}

func moduleRecordIndex(record map[string]any) (int, bool) {
	if index, ok := counterInt64(record["s"]); ok && index > 0 && index <= 2147483647 {
		return int(index), true
	}
	id, _ := record["id"].(string)
	if id == "" {
		return 0, false
	}
	for _, item := range catalog.All() {
		if item.Type == 1150 && item.ID == id {
			return item.Index, true
		}
	}
	return 0, false
}

func moduleOwnedRecord(p Profile, item catalog.Item) (map[string]any, bool) {
	records, valid := moduleSectionRecords(p)
	if !valid {
		return nil, false
	}
	for _, raw := range records {
		record, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := record["id"].(string); id == item.ID {
			return record, true
		}
		if index, ok := moduleRecordIndex(record); ok && index == item.Index {
			return record, true
		}
	}
	return nil, false
}

func moduleLevel(p Profile, item catalog.Item) int64 {
	if _, owned := moduleOwnedRecord(p, item); !owned {
		return 0
	}
	value, ok := counterInt64(objectAt(p.ProgressSlots, "37")[strconv.Itoa(item.Index)])
	if !ok || value < 1 || value > 10 {
		return 1
	}
	return value
}

func moduleInventoryValue(p Profile, item catalog.Item) (quantity, level, rarity int64) {
	if _, owned := moduleOwnedRecord(p, item); !owned {
		return 0, 0, 0
	}
	return 1, moduleLevel(p, item), 0
}

func moduleTypeAndCategory(item catalog.Item) (int64, int64, bool) {
	parts := strings.Split(item.ID, "_")
	if len(parts) < 3 || parts[0] != "mod" {
		return 0, 0, false
	}
	moduleTypes := map[string]int64{
		"kinetic": 1, "energy": 2, "magic": 3, "accelerator": 4,
		"regenerator": 5, "camouflage": 6, "bandoleer": 7, "stock": 8,
		"magazine": 9, "drummagazine": 10, "luckybullet": 11, "belt": 12,
		"alloy": 13, "astraledge": 14, "sharpening": 15, "battery": 16,
		"gears": 17, "voodoo": 18, "grip": 19, "lenses": 20,
		"muzzlebrake": 21, "shrapnel": 22, "pouch": 23, "jetfuel": 24,
	}
	categories := map[string]int64{
		"primary": 1, "backup": 2, "melee": 3, "special": 4,
		"sniper": 5, "premium": 6, "armor": 7,
	}
	moduleType, typeOK := moduleTypes[parts[1]]
	category, categoryOK := categories[parts[len(parts)-1]]
	return moduleType, category, typeOK && categoryOK
}

func newModuleStorageRecord(item catalog.Item) (map[string]any, error) {
	moduleType, category, ok := moduleTypeAndCategory(item)
	if !ok {
		return nil, errors.New("store: unsupported module identity")
	}
	// Store only the module identity fields recoverable from the stock ID and
	// enum. Point/upgrade/pricing payloads are omitted rather than guessed.
	return map[string]any{
		"id": item.ID,
		"s":  int64(item.Index),
		"t":  moduleType,
		"c":  category,
	}, nil
}

func editModuleInventorySlots(p *Profile, item catalog.Item, edit InventoryEdit) error {
	if edit.Action != "grant" {
		return errors.New("store: module revocation is not supported")
	}
	if edit.Quantity != 1 || edit.Rarity != 0 || edit.Level < 1 || edit.Level > 10 {
		return errors.New("store: module grants require quantity 1, level 1-10 and rarity 0")
	}
	section, err := ensureObject(p.ProgressSlots, "27")
	if err != nil {
		return err
	}
	var records []any
	if raw, exists := section["modules_v2"]; exists && raw != nil {
		var ok bool
		records, ok = raw.([]any)
		if !ok {
			return errors.New("store: incompatible module storage list")
		}
	}
	owned := false
	for _, raw := range records {
		record, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := record["id"].(string); id == item.ID {
			owned = true
			break
		}
		if index, ok := moduleRecordIndex(record); ok && index == item.Index {
			owned = true
			break
		}
	}
	if !owned {
		record, err := newModuleStorageRecord(item)
		if err != nil {
			return err
		}
		records = append(records, record)
	}
	section["modules_v2"] = records

	levels, err := ensureObject(p.ProgressSlots, "37")
	if err != nil {
		return err
	}
	key := strconv.Itoa(item.Index)
	if raw, exists := levels[key]; exists {
		if _, ok := counterInt64(raw); !ok {
			return errors.New("store: incompatible stored module level")
		}
	}
	levels[key] = edit.Level
	return nil
}
