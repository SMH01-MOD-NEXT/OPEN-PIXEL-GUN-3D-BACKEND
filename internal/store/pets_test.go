// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
)

func TestEveryPetGrantsPersistsAndRevokes(t *testing.T) {
	s, dir, _ := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	baseline := p.clone()
	pets := catalog.Search("", 80, true)
	levels := make(map[int]int64, len(pets))
	for _, pet := range pets {
		level := int64(pet.Index%65 + 1)
		levels[pet.Index] = level
		edit := editFor(p, 80, pet.Index, "grant")
		edit.Level = level
		next, changed, err := s.EditInventory(socketUniqID, edit)
		if err != nil || !changed {
			t.Fatalf("grant %s: %v %v", pet.ID, changed, err)
		}
		p = next
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ = reopened.Profile(socketUniqID)
	for _, pet := range pets {
		record, ok := petProgressRecord(p, pet.Index)
		if !ok || record["n"] != "" {
			t.Fatal("display label leaked into nickname", pet, record)
		}
		qty, level, _ := inventoryValue(p, pet)
		if qty != 1 || level != levels[pet.Index] {
			t.Fatal("incorrect pet ownership", pet, qty, level, levels[pet.Index])
		}
		next, changed, err := reopened.EditInventory(socketUniqID, editFor(p, 80, pet.Index, "revoke"))
		if err != nil || !changed {
			t.Fatalf("revoke %s: %v %v", pet.ID, changed, err)
		}
		p = next
	}
	for key, value := range baseline.ProgressSlots {
		want, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(p.ProgressSlots[key])
		if err != nil {
			t.Fatal(err)
		}
		if key != "53" && !bytes.Equal(want, got) {
			t.Fatal("unrelated slot changed", key)
		}
	}
	if p.Coins != baseline.Coins || p.Gems != baseline.Gems {
		t.Fatal("pet operations changed currency")
	}
}

func TestExistingPetLevelUpdatePreservesCustomProgress(t *testing.T) {
	s, _, _ := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	p.ProgressSlots["53"] = map[string]any{"p": []any{map[string]any{"i": int64(20003), "l": int64(27), "c": int64(15), "n": "My Fox", "future": true}}}
	stored := p.clone()
	s.state.Profiles[socketUniqID] = &stored
	edit := editFor(p, 80, 20003, "grant")
	edit.Level = 42
	got, changed, err := s.EditInventory(socketUniqID, edit)
	if err != nil || !changed || got.ProgressRevision != p.ProgressRevision+1 {
		t.Fatal("existing pet level was not updated", changed, err)
	}
	record, ok := petProgressRecord(got, 20003)
	if !ok || record["l"] != int64(42) || record["c"] != int64(15) || record["n"] != "My Fox" || record["future"] != true {
		t.Fatal("pet level update reset custom progress", record)
	}
}

func TestPetWritePersistenceFailureRollsBack(t *testing.T) {
	s, dir, _ := syncedGameplayStore(t)
	p, _ := s.Profile(socketUniqID)
	path := filepath.Join(dir, "state.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "blocked"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := s.EditInventory(socketUniqID, editFor(p, 80, 20003, "grant")); changed || !errors.Is(err, ErrInventoryPersistence) {
		t.Fatal(changed, err)
	}
	got, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(got, p) {
		t.Fatal("failed pet write mutated profile")
	}
}
