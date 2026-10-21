// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"math"
	"strconv"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
)

// SupportsProgressCommand is deliberately a measured-contract allowlist. Unknown
// stock commands remain losslessly stored through full slot snapshots.
func SupportsProgressCommand(id int64) bool {
	if ppCommand(id) || id >= 150 && id <= 155 {
		return true
	}
	switch id {
	case 10, 25, 33, 64, 65, 66, 81, 84, 85, 87, 120, 132, 139, 191, 197:
		return true
	}
	return false
}
func commandIndex(d map[string]any, key string) (string, error) {
	n, ok := counterInt64(d[key])
	if !ok || n <= 0 || n > math.MaxInt32 {
		return "", errors.New("store: invalid item index")
	}
	return strconv.FormatInt(n, 10), nil
}
func applyInventoryCommand(p *Profile, id int64, d map[string]any) error {
	bad := errors.New("store: invalid inventory command payload")
	put := func(path []string, key string, value any) error {
		m, err := ensureObject(p.ProgressSlots, path...)
		if err != nil {
			return err
		}
		m[key] = cloneAny(value)
		return nil
	}
	switch id {
	case 87:
		// Stock 0x1B54F8C carries only i; increment an owned module's level.
		key, err := commandIndex(d, "i")
		if err != nil {
			return err
		}
		index, _ := strconv.Atoi(key)
		item, ok := catalog.Find(1150, index)
		if !ok || !moduleOwned(*p, item) {
			return errors.New("store: cannot upgrade an unowned module")
		}
		level := moduleLevel(*p, item)
		if level >= 10 {
			return errors.New("store: module already at maximum level")
		}
		levels, err := ensureObject(p.ProgressSlots, "37")
		if err != nil {
			return err
		}
		if raw, exists := levels[key]; exists {
			n, valid := counterInt64(raw)
			if !valid || n < 1 || n > 10 {
				return bad
			}
		}
		levels[key] = level + 1
	case 25:
		// Measured equipment payloads contain a partial slot-9 document. Preserve
		// other categories and members within an equipment category.
		if len(d) == 0 || len(d) > 32 {
			return bad
		}
		m, err := ensureObject(p.ProgressSlots, "9")
		if err != nil {
			return err
		}
		for k, v := range d {
			n, err := strconv.Atoi(k)
			if err != nil || n < 0 || n > 32 {
				return bad
			}
			if obj, ok := v.(map[string]any); ok {
				dst, err := ensureObject(m, k)
				if err != nil {
					return err
				}
				for a, b := range obj {
					dst[a] = cloneAny(b)
				}
			} else {
				m[k] = cloneAny(v)
			}
		}
	case 33, 191:
		key := "g"
		path := []string{"16"}
		if id == 191 {
			key = "a"
			path = []string{"43", "0"}
		}
		i, err := commandIndex(d, key)
		if err != nil {
			return err
		}
		level, ok := counterInt64(d["u"])
		if !ok || level < 1 || level > 65 {
			return bad
		}
		return put(path, i, level)
	case 132:
		i, err := commandIndex(d, "i")
		if err != nil {
			return err
		}
		u, ok := d["u"].(map[string]any)
		if !ok {
			return bad
		}
		l, ok := counterInt64(u["l"])
		r, rok := counterInt64(u["r"])
		if !ok || !rok || l < 1 || l > 65 || r > 5 {
			return bad
		}
		// Preserve optional stock weapon record fields instead of flattening them.
		return put([]string{"28", "0"}, i, u)
	case 139:
		values, ok := d["d"].(map[string]any)
		if !ok || len(values) > 10000 {
			return bad
		}
		m, err := ensureObject(p.ProgressSlots, "29")
		if err != nil {
			return err
		}
		for k, v := range values {
			n, e := strconv.Atoi(k)
			c, valid := counterInt64(v)
			if e != nil || n <= 0 || !valid || c > 1 {
				return bad
			}
			m[k] = cloneAny(v)
		}
	case 84, 85:
		i, err := commandIndex(d, "i")
		if err != nil {
			return err
		}
		m, err := ensureObject(p.ProgressSlots, "26", "s")
		if err != nil {
			return err
		}
		if id == 84 {
			m[i] = int64(1)
		} else {
			delete(m, i)
		}
	case 81, 120:
		i, err := commandIndex(d, "i")
		if err != nil {
			return err
		}
		path := []string{"24"}
		amountKey := "d"
		if id != 120 {
			typ, ok := counterInt64(d["t"])
			if !ok || typ <= 0 || typ > 10000 {
				return bad
			}
			path = []string{"26", "c", strconv.FormatInt(typ, 10)}
			amountKey = "c"
		}
		amount, ok := counterInt64(d[amountKey])
		if !ok {
			return bad
		}
		m, err := ensureObject(p.ProgressSlots, path...)
		if err != nil {
			return err
		}
		value := int64(0)
		if raw, exists := m[i]; exists {
			var valid bool
			value, valid = counterInt64(raw)
			if !valid {
				return bad
			}
		}
		if id == 81 {
			if amount > value {
				return errors.New("store: insufficient consumables")
			}
			value -= amount
		} else {
			if amount > math.MaxInt64-value {
				return errors.New("store: item quantity overflow")
			}
			value += amount
		}
		m[i] = value
	case 197:
		w, err := commandIndex(d, "w")
		if err != nil {
			return err
		}
		skin, err := commandIndex(d, "s")
		if err != nil {
			return err
		}
		v, _ := strconv.ParseInt(skin, 10, 64)
		return put([]string{"44"}, w, v)
	default:
		return errors.New("store: unsupported inventory command")
	}
	return nil
}
