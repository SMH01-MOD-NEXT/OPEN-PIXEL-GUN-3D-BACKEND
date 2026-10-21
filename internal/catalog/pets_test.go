// SPDX-License-Identifier: GPL-3.0-or-later
package catalog

import (
	"strings"
	"testing"
)

func TestPetDisplayNamesPreserveStockIdentity(t *testing.T) {
	pets := Search("", 80, true)
	if len(pets) != 94 {
		t.Fatalf("editable pets: %d", len(pets))
	}
	for _, item := range pets {
		if !strings.HasPrefix(item.ID, "pet_") || item.NameSource != "derived_codename" || item.Name == item.ID {
			t.Fatalf("invalid derived pet label: %+v", item)
		}
		if strings.Contains(item.Name, "_") || strings.HasPrefix(item.Name, "Pet ") {
			t.Fatalf("unreadable pet label: %+v", item)
		}
		found := Search(item.ID, 80, true)
		present := false
		for _, candidate := range found {
			if candidate.Index == item.Index && candidate.ID == item.ID {
				present = true
			}
		}
		if !present {
			t.Fatalf("lost stock codename lookup: %+v", item)
		}
	}
	for index, name := range map[int]string{20003: "Fox", 1003: "Alien Cat", 16003: "Dinosaur", 17003: "Dog", 18003: "Eagle", 93003: "8 Bit Boi", 99003: "Servants Right Hand"} {
		item, ok := Find(80, index)
		if !ok || item.Name != name {
			t.Fatalf("pet %d: %+v; want %q", index, item, name)
		}
	}
	for _, query := range []string{"Fox", "pet_fox", "20003"} {
		found := Search(query, 80, true)
		if len(found) != 1 || found[0].Index != 20003 || found[0].ID != "pet_fox" {
			t.Fatalf("fox search %q: %+v", query, found)
		}
	}
}
