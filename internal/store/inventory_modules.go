// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"strconv"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
)

// Stock modules are non-unique inventory items: slot 26/s[index] = 1.
// The 23.1.3 loader casts 26/u records to unique-item subclasses; a module
// there throws InvalidCastException. Slot 27 is placement, not ownership.
// Evidence: Inventory load 0x171BBB0/write 0x171CE90, module owned-count
// 0x24B0C38 and level gate 0x24B0BB0. Never infer ownership from slot 27/37.
func moduleOwned(p Profile, item catalog.Item) bool {
	_, exists := objectAt(p.ProgressSlots, "26", "s")[strconv.Itoa(item.Index)]
	return exists
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
	modules, err := ensureObject(p.ProgressSlots, "26", "s")
	if err != nil {
		return err
	}
	modules[strconv.Itoa(item.Index)] = int64(1)
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
