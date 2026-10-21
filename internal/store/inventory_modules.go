// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"strconv"
	"strings"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
)

// Stock 23.1.3 represents a usable module with three linked records: its index
// in ModuleStorage slot 27 grouped by ModuleData.ModuleType, a positive linked
// ModulePoint count in 26.c[1155], and its level in ModulesInfo slot 37. Writing
// a module index directly to generic 26.s/26.u reaches an incompatible inventory
// item cast and can leave the lobby loading overlay stuck at 90%.
func moduleType(item catalog.Item) (int64, bool) {
	parts := strings.Split(item.ID, "_")
	if len(parts) < 3 || parts[0] != "mod" {
		return 0, false
	}
	types := map[string]int64{
		"kinetic": 1, "energy": 2, "magic": 3, "accelerator": 4,
		"regenerator": 5, "camouflage": 6, "bandoleer": 7, "stock": 8,
		"magazine": 9, "drummagazine": 10, "luckybullet": 11, "belt": 12,
		"alloy": 13, "astraledge": 14, "sharpening": 15, "battery": 16,
		"gears": 17, "voodoo": 18, "grip": 19, "lenses": 20,
		"muzzlebrake": 21, "shrapnel": 22, "pouch": 23, "jetfuel": 24,
	}
	v, ok := types[parts[1]]
	return v, ok
}

func moduleStorageIndices(p Profile, typ int64) ([]any, bool) {
	section := objectAt(p.ProgressSlots, "27")
	if section == nil {
		return nil, true
	}
	raw, exists := section[strconv.FormatInt(typ, 10)]
	if !exists || raw == nil {
		return nil, true
	}
	indices, ok := raw.([]any)
	return indices, ok
}

func modulePointItem(item catalog.Item) (catalog.Item, bool) {
	point, ok := catalog.Find(1155, item.Index+42000)
	if !ok || point.ID != strings.Replace(item.ID, "mod_", "modp_", 1) {
		return catalog.Item{}, false
	}
	return point, true
}

func modulePointQuantity(p Profile, item catalog.Item) (int64, bool) {
	point, ok := modulePointItem(item)
	if !ok {
		return 0, false
	}
	raw, exists := objectAt(p.ProgressSlots, "26", "c", "1155")[strconv.Itoa(point.Index)]
	if !exists {
		return 0, true
	}
	quantity, valid := counterInt64(raw)
	return quantity, valid
}

func moduleOwned(p Profile, item catalog.Item) bool {
	quantity, valid := modulePointQuantity(p, item)
	return valid && quantity > 0
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

func editModuleInventorySlots(p *Profile, item catalog.Item, edit InventoryEdit) error {
	if edit.Action != "grant" {
		return errors.New("store: module revocation is not supported")
	}
	if edit.Quantity != 1 || edit.Rarity != 0 || edit.Level < 1 || edit.Level > 10 {
		return errors.New("store: module grants require quantity 1, level 1-10 and rarity 0")
	}
	typ, ok := moduleType(item)
	if !ok {
		return errors.New("store: unsupported module identity")
	}
	point, ok := modulePointItem(item)
	if !ok {
		return errors.New("store: unsupported module point identity")
	}
	section, err := ensureObject(p.ProgressSlots, "27")
	if err != nil {
		return err
	}
	key := strconv.FormatInt(typ, 10)
	indices, valid := moduleStorageIndices(*p, typ)
	if !valid {
		return errors.New("store: incompatible module storage list")
	}
	owned := false
	for _, raw := range indices {
		if index, ok := counterInt64(raw); ok && index == int64(item.Index) {
			owned = true
			break
		}
	}
	if !owned {
		section[key] = append(indices, int64(item.Index))
	}

	points, err := ensureObject(p.ProgressSlots, "26", "c", "1155")
	if err != nil {
		return err
	}
	pointKey := strconv.Itoa(point.Index)
	quantity := int64(0)
	if raw, exists := points[pointKey]; exists {
		var valid bool
		quantity, valid = counterInt64(raw)
		if !valid || quantity < 0 {
			return errors.New("store: incompatible module point quantity")
		}
	}
	if quantity < 1 {
		points[pointKey] = int64(1)
	}

	levels, err := ensureObject(p.ProgressSlots, "37")
	if err != nil {
		return err
	}
	levelKey := strconv.Itoa(item.Index)
	if raw, exists := levels[levelKey]; exists {
		if _, ok := counterInt64(raw); !ok {
			return errors.New("store: incompatible stored module level")
		}
	}
	levels[levelKey] = edit.Level
	return nil
}
