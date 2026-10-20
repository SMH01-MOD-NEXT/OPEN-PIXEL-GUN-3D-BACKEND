// SPDX-License-Identifier: GPL-3.0-or-later
package catalog

import "testing"

func TestStockRegistryIdentity(t *testing.T) {
	if len(All()) != 7457 {
		t.Fatal(len(All()))
	}
	v, ok := Find(10, 727001)
	if !ok || v.Name != "Future Police Rifle" || v.ID == "" || !v.Editable {
		t.Fatal(v)
	}
	if _, ok := Find(10, 727); ok {
		t.Fatal("accepted ordinal instead of full index")
	}
	for _, v := range All() {
		if v.NameSource == "technical_id" && v.Name != v.ID {
			t.Fatal("invented label", v)
		}
		if v.NameSource == "" || v.Category == "" {
			t.Fatal("missing source/category")
		}
	}
	if len(Search("future police", 10, true)) != 1 {
		t.Fatal("name lookup")
	}
	if len(Search("727001", 10, true)) != 1 {
		t.Fatal("id lookup")
	}
}

func TestCatalogReturnedValuesCannotMutateRegistry(t *testing.T) {
	original, _ := Find(10, 727001)
	if original.WeaponCategory == nil {
		t.Fatal("missing category")
	}
	want := *original.WeaponCategory
	for _, list := range [][]Item{All(), ForIndex(727001), Search("727001", 10, false), {original}} {
		for _, v := range list {
			if v.Type == 10 && v.Index == 727001 {
				*v.WeaponCategory = 999
			}
		}
	}
	got, _ := Find(10, 727001)
	if *got.WeaponCategory != want {
		t.Fatal("caller mutated registry")
	}
}
