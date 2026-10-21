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
	pet, ok := Find(80, 1003)
	if !ok || !pet.Editable || pet.Category != "Pet" {
		t.Fatal("pet ownership catalog entry is not editable", pet)
	}
	foundPocketHarpoon := map[int]bool{}
	for _, item := range Search("pocket harpoon", 70, true) {
		foundPocketHarpoon[item.Index] = true
	}
	if !foundPocketHarpoon[63007] || !foundPocketHarpoon[76007] {
		t.Fatal("missing editable Pocket Harpoon variants", foundPocketHarpoon)
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

func TestSearchWhitespaceTokensCaseAndInternalID(t *testing.T) {
	for _, q := range []string{"  fUTuRe   Police  ", "police future", "727001"} {
		found := false
		for _, v := range Search(q, 10, false) {
			if v.Index == 727001 {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing weapon for %q", q)
		}
	}
	if len(Search("future nonexistent_word_please", 10, false)) != 0 {
		t.Fatal("tokens must all match")
	}
	item, _ := Find(10, 727001)
	if len(Search(item.ID, 10, false)) == 0 {
		t.Fatal("internal id search failed")
	}
}

func TestModuleCatalogLabelsAreReadableAndEditable(t *testing.T) {
	modules := Search("", 1150, false)
	if len(modules) != 42 {
		t.Fatalf("module definitions: %d", len(modules))
	}
	for _, item := range modules {
		if !item.Editable || item.NameSource != "derived_id" || item.Name == item.ID || item.Category != "Module" {
			t.Fatalf("module must have a readable derived label and an editable storage adapter: %+v", item)
		}
		if len(Search(item.ID, 1150, false)) == 0 || len(Search(item.Name, 1150, false)) == 0 {
			t.Fatalf("module is not searchable by ID and label: %+v", item)
		}
	}
	if editable := Search("", 1150, true); len(editable) != 42 {
		t.Fatalf("module editor should expose all 42 verified catalog entries: %d", len(editable))
	}
	for index, name := range map[int]string{
		304009: "Kinetic Primary",
		326009: "Armor Regeneration",
		327009: "Armor Camouflage",
		328009: "Bandolier Primary",
	} {
		item, ok := Find(1150, index)
		if !ok || !item.Editable || item.Name != name {
			t.Fatalf("module %d: %+v; want editable %q", index, item, name)
		}
	}
}
