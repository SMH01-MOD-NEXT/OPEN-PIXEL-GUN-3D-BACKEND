// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"fmt"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrInventoryConflict = errors.New("store: inventory changed; reload before editing")
var ErrInventoryPersistence = errors.New("store: inventory could not be saved")

var ErrEquippedItem = errors.New("store: item is equipped in equipment or a loadout; unequip it before revoking")

type InventoryItem struct {
	catalog.Item
	Quantity int64 `json:"quantity"`
	Level    int64 `json:"level,omitempty"`
	Rarity   int64 `json:"rarity,omitempty"`
	Equipped bool  `json:"equipped"`
}
type InventoryEdit struct {
	Type      int       `json:"type"`
	Index     int       `json:"index"`
	Action    string    `json:"action"`
	Quantity  int64     `json:"quantity"`
	Level     int64     `json:"level"`
	Rarity    int64     `json:"rarity"`
	Revision  uint64    `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
}

func objectAt(root map[string]any, path ...string) map[string]any {
	for _, k := range path {
		v, _ := root[k].(map[string]any)
		if v == nil {
			return nil
		}
		root = v
	}
	return root
}
func ensureObject(root map[string]any, path ...string) (map[string]any, error) {
	for _, k := range path {
		v, exists := root[k]
		if !exists || v == nil {
			n := map[string]any{}
			root[k] = n
			root = n
			continue
		}
		n, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("store: incompatible inventory slot shape")
		}
		root = n
	}
	return root, nil
}
func itemStorage(t int) ([]string, bool) {
	switch t {
	case 10:
		return []string{"28", "0"}, true
	case 20:
		return []string{"43", "0"}, true
	case 70:
		return []string{"16"}, true
	case 1070:
		return []string{"24"}, true
	case 1170, 1350, 1470, 1510:
		return []string{"26", "s"}, true
	case 1155, 1520:
		return []string{"26", "c", strconv.Itoa(t)}, true
	}
	return nil, false
}
func listStorage(t int) []string {
	switch t {
	case 30, 40, 50, 60:
		return []string{"13", "w"}
	case 65:
		return []string{"10"}
	case 1080:
		return []string{"11"}
	}
	return nil
}

// petProgressRecords returns the measured stock list without materializing a
// missing slot or coercing malformed data into an editable shape.
func petProgressRecords(p Profile) []any {
	slot := objectAt(p.ProgressSlots, "53")
	pets, _ := slot["p"].([]any)
	return pets
}

// petProgressRecord locates an owned pet in the 23.1.3 slot 53/p record list.
func petProgressRecord(p Profile, index int) (map[string]any, bool) {
	for _, raw := range petProgressRecords(p) {
		record, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := counterInt64(record["i"]); ok && id == int64(index) {
			return record, true
		}
	}
	return nil, false
}

func listValue(root map[string]any, path []string) ([]any, error) {
	parent := root
	if len(path) > 1 {
		var err error
		parent, err = ensureObject(root, path[:len(path)-1]...)
		if err != nil {
			return nil, err
		}
	}
	v, exists := parent[path[len(path)-1]]
	if !exists || v == nil {
		return []any{}, nil
	}
	a, ok := v.([]any)
	if !ok {
		return nil, errors.New("store: incompatible inventory list")
	}
	return a, nil
}
func setList(root map[string]any, path []string, a []any) error {
	parent := root
	var err error
	if len(path) > 1 {
		parent, err = ensureObject(root, path[:len(path)-1]...)
		if err != nil {
			return err
		}
	}
	parent[path[len(path)-1]] = a
	return nil
}
func inventoryValue(p Profile, item catalog.Item) (qty, level, rarity int64) {
	key := strconv.Itoa(item.Index)
	if item.Type == 80 {
		record, ok := petProgressRecord(p, item.Index)
		if !ok {
			return 0, 0, 0
		}
		level, _ = counterInt64(record["l"])
		return 1, level, 0
	}
	if path, ok := itemStorage(item.Type); ok {
		value := objectAt(p.ProgressSlots, path...)[key]
		if item.Type == 10 {
			if v, ok := value.(map[string]any); ok {
				qty = 1
				level, _ = counterInt64(v["l"])
				rarity, _ = counterInt64(v["r"])
			}
			return
		}
		n, ok := counterInt64(value)
		if !ok || n <= 0 {
			return 0, 0, 0
		}
		switch item.Type {
		case 20, 70:
			return 1, n, 0
		default:
			return n, 0, 0
		}
	}
	if path := listStorage(item.Type); len(path) > 0 {
		// Read without materializing absent slots.
		root := p.ProgressSlots
		if len(path) > 1 {
			root = objectAt(root, path[:len(path)-1]...)
		}
		a, _ := root[path[len(path)-1]].([]any)
		for _, v := range a {
			if n, ok := counterInt64(v); ok && n == int64(item.Index) {
				return 1, 0, 0
			}
		}
	}
	return
}
func Inventory(p Profile) []InventoryItem {
	out := []InventoryItem{}
	for _, item := range catalog.All() {
		q, l, r := inventoryValue(p, item)
		if q > 0 {
			out = append(out, InventoryItem{Item: item, Quantity: q, Level: l, Rarity: r, Equipped: equippedInventoryItem(p, item)})
		}
	}
	// Surface unrecognized IDs in verified ownership containers as read-only.
	// Unknown slots and values are still retained verbatim in the full snapshot.
	addUnknown := func(typ int, index int, quantity int64, expected []int) {
		if index <= 0 {
			return
		}
		for _, candidate := range catalog.ForIndex(index) {
			for _, t := range expected {
				if candidate.Type == t {
					return
				}
			}
		}
		if quantity <= 0 {
			return
		}
		out = append(out, InventoryItem{Item: catalog.Item{Type: typ, Index: index, ID: strconv.Itoa(index), Name: strconv.Itoa(index), NameSource: "technical_id", Category: "Unknown", Editable: false}, Quantity: quantity})
	}
	for _, group := range []struct {
		typ      int
		path     []string
		expected []int
	}{
		{10, []string{"28", "0"}, []int{10}}, {20, []string{"43", "0"}, []int{20}},
		{70, []string{"16"}, []int{70}}, {1070, []string{"24"}, []int{1070}},
		{0, []string{"26", "s"}, []int{1170, 1350, 1470, 1510}},
		{1155, []string{"26", "c", "1155"}, []int{1155}}, {1520, []string{"26", "c", "1520"}, []int{1520}},
	} {
		for key, value := range objectAt(p.ProgressSlots, group.path...) {
			index, err := strconv.Atoi(key)
			if err != nil {
				continue
			}
			quantity, valid := counterInt64(value)
			if group.typ == 10 {
				if _, ok := value.(map[string]any); ok {
					quantity = 1
				}
			} else if !valid {
				continue
			}
			if (group.typ == 20 || group.typ == 70) && quantity > 0 {
				quantity = 1
			}
			addUnknown(group.typ, index, quantity, group.expected)
		}
	}
	// Pet records are dictionaries in slot 53/p, not numeric list entries.
	for _, raw := range petProgressRecords(p) {
		record, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		index, ok := counterInt64(record["i"])
		if !ok || index <= 0 || index > 2147483647 {
			continue
		}
		addUnknown(80, int(index), 1, []int{80})
	}
	for _, group := range []struct {
		typ      int
		path     []string
		expected []int
	}{
		{65, []string{"10"}, []int{65}}, {1080, []string{"11"}, []int{1080}},
		{0, []string{"13", "w"}, []int{30, 40, 50, 60}},
	} {
		parent := p.ProgressSlots
		if len(group.path) > 1 {
			parent = objectAt(parent, group.path[:len(group.path)-1]...)
		}
		values, _ := parent[group.path[len(group.path)-1]].([]any)
		seen := map[int64]bool{}
		for _, value := range values {
			index, ok := counterInt64(value)
			if ok && !seen[index] {
				seen[index] = true
				addUnknown(group.typ, int(index), 1, group.expected)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Index < out[j].Index
	})
	return out
}

// Only known equipment references are inspected, never arbitrary numeric prefs.
func equippedInventoryItem(p Profile, item catalog.Item) bool {
	id := int64(item.Index)
	fields := map[int][]string{10: {"pw", "bw", "mw", "spw", "snw", "prw"}, 20: {"ar"}, 30: {"ms"}, 40: {"ht"}, 50: {"bt"}, 60: {"cp"}, 65: {"sk"}, 70: {"thg", "tlg"}, 1080: {"sk", "vh"}}
	for _, raw := range func() []any { a, _ := objectAt(p.ProgressSlots, "67")["l"].([]any); return a }() {
		loadout, _ := raw.(map[string]any)
		for _, k := range fields[item.Type] {
			if n, ok := counterInt64(loadout[k]); ok && n == id {
				return true
			}
		}
	}
	eq := objectAt(p.ProgressSlots, "9")
	switch item.Type {
	case 80:
		if n, ok := counterInt64(eq["6"]); ok && n == id {
			return true
		}
	case 10:
		for _, v := range objectAt(eq, "2") {
			text, _ := v.(string)
			for _, part := range strings.Split(text, "#") {
				if part == strconv.Itoa(item.Index/1000) {
					return true
				}
			}
		}
	case 30, 40, 50, 60:
		for k, v := range objectAt(p.ProgressSlots, "13") {
			if k == "w" || k == "t" {
				continue
			}
			if n, ok := counterInt64(v); ok && n == id {
				return true
			}
		}
	case 20:
		for _, v := range objectAt(eq, "3") {
			if n, ok := counterInt64(v); ok && n == id {
				return true
			}
		}
	case 70:
		for _, v := range objectAt(eq, "1") {
			if n, ok := counterInt64(v); ok && n == id {
				return true
			}
		}
	case 65:
		if n, ok := counterInt64(eq["5"]); ok && n == id {
			return true
		}
	case 1080:
		for _, v := range objectAt(eq, "4") {
			if n, ok := counterInt64(v); ok && n == id {
				return true
			}
		}
	case 1350:
		for _, v := range objectAt(p.ProgressSlots, "44") {
			if n, ok := counterInt64(v); ok && n == id {
				return true
			}
		}
	case 1170:
		for _, v := range objectAt(p.ProgressSlots, "32") {
			if n, ok := counterInt64(v); ok && n == id {
				return true
			}
		}
	case 1510:
		for _, v := range objectAt(p.ProgressSlots, "82") {
			if n, ok := counterInt64(v); ok && n == id {
				return true
			}
		}
	}
	return false
}
func (s *Store) EditInventory(playerID string, edit InventoryEdit) (Profile, bool, error) {
	item, ok := catalog.Find(edit.Type, edit.Index)
	if !ok || !item.Editable {
		return Profile{}, false, errors.New("store: unknown or unsupported catalog item")
	}
	if edit.Action != "grant" && edit.Action != "revoke" {
		return Profile{}, false, errors.New("store: invalid inventory action")
	}
	if edit.Quantity < 1 || edit.Quantity > 1000000 || edit.Level < 1 || edit.Level > 65 || edit.Rarity < 0 || edit.Rarity > 5 {
		return Profile{}, false, errors.New("store: invalid quantity, level or rarity")
	}
	if item.Type == 80 && edit.Action == "grant" && (edit.Quantity != 1 || edit.Level != 1 || edit.Rarity != 0) {
		return Profile{}, false, errors.New("store: pet grants require quantity 1, level 1 and rarity 0")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(strings.TrimSpace(playerID))
	p, ok := s.state.Profiles[playerID]
	if !ok {
		return Profile{}, false, ErrNotFound
	}
	if p.ProgressRevision != edit.Revision || !p.UpdatedAt.Equal(edit.UpdatedAt) {
		return Profile{}, false, ErrInventoryConflict
	}
	q, _, _ := inventoryValue(*p, item)
	if edit.Action == "revoke" && q == 0 {
		return p.clone(), false, nil
	}
	if edit.Action == "revoke" && q > 0 && equippedInventoryItem(*p, item) {
		return Profile{}, false, ErrEquippedItem
	}
	candidate := p.clone()
	if candidate.ProgressSlots == nil {
		candidate.ProgressSlots = map[string]any{}
	}
	if err := editInventorySlots(&candidate, item, edit); err != nil {
		return Profile{}, false, err
	}
	if reflect.DeepEqual(candidate.ProgressSlots, p.ProgressSlots) {
		return p.clone(), false, nil
	}
	// The raw stock slots are canonical; no separate ownership database can drift.
	mirrorRawSlots(&candidate)
	advanceProfileProgressRevision(&candidate)
	candidate.UpdatedAt = time.Now().UTC()
	before := *p
	*p = candidate
	if err := s.persistLocked(); err != nil {
		*p = before
		return Profile{}, false, fmt.Errorf("%w: %v", ErrInventoryPersistence, err)
	}
	return p.clone(), true, nil
}
func editPetInventorySlots(p *Profile, item catalog.Item, action string) error {
	slot, err := ensureObject(p.ProgressSlots, "53")
	if err != nil {
		return err
	}
	var pets []any
	if raw, exists := slot["p"]; exists && raw != nil {
		var ok bool
		pets, ok = raw.([]any)
		if !ok {
			return errors.New("store: incompatible pet inventory list")
		}
	}
	out := make([]any, 0, len(pets)+1)
	found := false
	for _, raw := range pets {
		record, ok := raw.(map[string]any)
		if ok {
			if id, valid := counterInt64(record["i"]); valid && id == int64(item.Index) {
				found = true
				if action == "revoke" {
					continue
				}
			}
		}
		out = append(out, raw)
	}
	if action == "grant" && !found {
		// These are the observed slot 53/p fields: id, cards, level and custom name.
		// A newly granted pet starts at level 1 with no cards or custom nickname.
		out = append(out, map[string]any{
			"c": int64(0), "i": int64(item.Index), "l": int64(1), "n": "",
		})
	}
	slot["p"] = out
	return nil
}

func editInventorySlots(p *Profile, item catalog.Item, e InventoryEdit) error {
	if item.Type == 80 {
		return editPetInventorySlots(p, item, e.Action)
	}
	key := strconv.Itoa(item.Index)
	if path, ok := itemStorage(item.Type); ok {
		m, err := ensureObject(p.ProgressSlots, path...)
		if err != nil {
			return err
		}
		if e.Action == "revoke" {
			delete(m, key)
			if item.Type == 10 {
				delete(objectAt(p.ProgressSlots, "29"), key)
			}
			return nil
		}
		switch item.Type {
		case 10:
			v, ok := m[key].(map[string]any)
			if !ok {
				v = map[string]any{}
			}
			v["l"], v["r"] = e.Level, e.Rarity
			m[key] = v
		case 20, 70:
			m[key] = e.Level
		case 1070, 1155, 1520:
			m[key] = e.Quantity
		default:
			m[key] = int64(1)
		}
		return nil
	}
	if path := listStorage(item.Type); len(path) > 0 {
		a, err := listValue(p.ProgressSlots, path)
		if err != nil {
			return err
		}
		out := []any{}
		found := false
		for _, v := range a {
			n, ok := counterInt64(v)
			if ok && n == int64(item.Index) {
				found = true
				if e.Action == "revoke" {
					continue
				}
			}
			out = append(out, v)
		}
		if e.Action == "grant" && !found {
			out = append(out, int64(item.Index))
		}
		return setList(p.ProgressSlots, path, out)
	}
	return errors.New("store: item has no verified storage adapter")
}
func mirrorRawSlots(p *Profile) {
	if p.ProgressDocument == nil {
		p.ProgressDocument = map[string]any{}
	}
	// init_progress is a separate legacy channel. Update existing projections,
	// but do not inject an entire raw snapshot: stock applies its slots at boot.
	for k := range p.ProgressDocument {
		if v, ok := p.ProgressSlots[k]; ok {
			p.ProgressDocument[k] = cloneAny(v)
		}
	}
}
