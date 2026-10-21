// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"strconv"
	"strings"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
)

// moduleTypeAndCategory derives the stock ModuleStorage grouping from a module
// ID. The category is only used to recognize records written by the former
// modules_v2 adapter; stock ownership is stored as type -> item-index lists.
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

func moduleItemForLegacyRecord(record map[string]any) (catalog.Item, bool) {
	id, _ := record["id"].(string)
	var byID catalog.Item
	idOK := false
	if id != "" {
		for _, item := range catalog.All() {
			if item.Type == 1150 && item.ID == id {
				byID, idOK = item, true
				break
			}
		}
	}
	index, indexOK := counterInt64(record["s"])
	var byIndex catalog.Item
	if indexOK && index > 0 && index <= 2147483647 {
		byIndex, indexOK = catalog.Find(1150, int(index))
	} else {
		indexOK = false
	}
	if idOK && indexOK {
		if byID.Index != byIndex.Index {
			return catalog.Item{}, false
		}
		return byID, true
	}
	if idOK {
		return byID, true
	}
	if indexOK {
		return byIndex, true
	}
	return catalog.Item{}, false
}

// moduleLegacyItems recognizes only valid identities from the retired
// modules_v2 adapter. The original records are never removed or rewritten.
func moduleLegacyItems(p Profile) ([]catalog.Item, bool) {
	section := objectAt(p.ProgressSlots, "27")
	if section == nil {
		return nil, true
	}
	raw, exists := section["modules_v2"]
	if !exists || raw == nil {
		return nil, true
	}
	records, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	items := make([]catalog.Item, 0, len(records))
	seen := make(map[int]bool, len(records))
	for _, raw := range records {
		record, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		item, ok := moduleItemForLegacyRecord(record)
		if ok && !seen[item.Index] {
			items = append(items, item)
			seen[item.Index] = true
		}
	}
	return items, true
}

func moduleStorageIndices(p Profile, moduleType int64) ([]any, bool) {
	section := objectAt(p.ProgressSlots, "27")
	if section == nil {
		return nil, true
	}
	raw, exists := section[strconv.FormatInt(moduleType, 10)]
	if !exists || raw == nil {
		return nil, true
	}
	indices, ok := raw.([]any)
	return indices, ok
}

func moduleOwned(p Profile, item catalog.Item) bool {
	moduleType, _, ok := moduleTypeAndCategory(item)
	if !ok {
		return false
	}
	if indices, valid := moduleStorageIndices(p, moduleType); valid {
		for _, raw := range indices {
			if index, ok := counterInt64(raw); ok && index == int64(item.Index) {
				return true
			}
		}
	}
	legacy, valid := moduleLegacyItems(p)
	if !valid {
		return false
	}
	for _, old := range legacy {
		if old.Index == item.Index {
			return true
		}
	}
	return false
}

func moduleLevel(p Profile, item catalog.Item) int64 {
	if !moduleOwned(p, item) {
		return 0
	}
	value, ok := counterInt64(objectAt(p.ProgressSlots, "37")[strconv.Itoa(item.Index)])
	if !ok || value < 1 || value > 10 {
		return 1
	}
	return value
}

func moduleInventoryValue(p Profile, item catalog.Item) (quantity, level, rarity int64) {
	if !moduleOwned(p, item) {
		return 0, 0, 0
	}
	return 1, moduleLevel(p, item), 0
}

func addModuleIndex(section map[string]any, item catalog.Item) error {
	moduleType, _, ok := moduleTypeAndCategory(item)
	if !ok {
		return errors.New("store: unsupported module identity")
	}
	key := strconv.FormatInt(moduleType, 10)
	var indices []any
	if raw, exists := section[key]; exists && raw != nil {
		var valid bool
		indices, valid = raw.([]any)
		if !valid {
			return errors.New("store: incompatible module index list")
		}
	}
	for _, raw := range indices {
		if index, ok := counterInt64(raw); ok && index == int64(item.Index) {
			return nil
		}
	}
	section[key] = append(indices, int64(item.Index))
	return nil
}

func editModuleInventorySlots(p *Profile, item catalog.Item, edit InventoryEdit) error {
	if edit.Action != "grant" {
		return errors.New("store: module revocation is not supported")
	}
	if edit.Quantity != 1 || edit.Rarity != 0 || edit.Level < 1 || edit.Level > 10 {
		return errors.New("store: module grants require quantity 1, level 1-10 and rarity 0")
	}
	if _, valid := moduleLegacyItems(*p); !valid {
		return errors.New("store: incompatible legacy module storage")
	}
	section, err := ensureObject(p.ProgressSlots, "27")
	if err != nil {
		return err
	}
	// A prior version wrote identity objects under modules_v2. Promote every
	// recognized legacy identity into the stock type -> index-list structure,
	// while retaining the old records byte-for-byte for lossless compatibility.
	legacy, _ := moduleLegacyItems(*p)
	for _, old := range legacy {
		if err := addModuleIndex(section, old); err != nil {
			return err
		}
	}
	if err := addModuleIndex(section, item); err != nil {
		return err
	}

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
