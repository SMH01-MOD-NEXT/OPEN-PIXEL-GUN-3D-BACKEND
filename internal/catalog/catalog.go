// SPDX-License-Identifier: GPL-3.0-or-later
// Package catalog contains identifiers extracted from the stock 23.1.3 registry.
package catalog

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed stock2313.json
var files embed.FS

type Item struct {
	Type           int    `json:"type"`
	Index          int    `json:"index"`
	ID             string `json:"id"`
	Name           string `json:"name"`
	NameSource     string `json:"name_source"`
	Category       string `json:"category"`
	WeaponCategory *int   `json:"weapon_category,omitempty"`
	Editable       bool   `json:"editable"`
}

var items []Item
var byKey map[string]Item
var byIndex map[int][]Item

func init() {
	raw, err := files.ReadFile("stock2313.json")
	if err != nil {
		panic(err)
	}
	if err = json.Unmarshal(raw, &items); err != nil {
		panic(err)
	}
	byKey = map[string]Item{}
	byIndex = map[int][]Item{}
	for _, v := range items {
		if v.Index <= 0 || v.ID == "" || v.Name == "" {
			panic("invalid stock catalog record")
		}
		key := Key(v.Type, v.Index)
		if _, ok := byKey[key]; ok {
			panic("duplicate catalog key: " + key)
		}
		byKey[key] = v
		byIndex[v.Index] = append(byIndex[v.Index], v)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Type != items[j].Type {
			return items[i].Type < items[j].Type
		}
		return items[i].Index < items[j].Index
	})
}
func Key(t, i int) string        { return fmt.Sprintf("%d:%d", t, i) }
func Find(t, i int) (Item, bool) { v, ok := byKey[Key(t, i)]; return copyItem(v), ok }

// All returns a copy; callers cannot mutate the embedded registry.
func copyItem(v Item) Item {
	if v.WeaponCategory != nil {
		n := *v.WeaponCategory
		v.WeaponCategory = &n
	}
	return v
}
func copyItems(src []Item) []Item {
	out := make([]Item, len(src))
	for i, v := range src {
		out[i] = copyItem(v)
	}
	return out
}
func All() []Item           { return copyItems(items) }
func ForIndex(i int) []Item { return copyItems(byIndex[i]) }
func Search(q string, t int, editable bool) []Item {
	q = strings.ToLower(strings.TrimSpace(q))
	out := []Item{}
	for _, v := range items {
		if t != 0 && t != v.Type || editable && !v.Editable {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(v.Name+" "+v.ID+" "+strconv.Itoa(v.Index)), q) {
			continue
		}
		out = append(out, copyItem(v))
	}
	return out
}
