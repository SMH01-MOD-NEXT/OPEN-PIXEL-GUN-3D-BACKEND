// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
)

func TestBotWeaponBandsHaveExactStockCategoryIDs(t *testing.T) {
	last := 0
	for _, band := range botWeaponBands {
		if band.minLevel <= last {
			t.Fatal("weapon bands are not strictly ordered")
		}
		last = band.minLevel
		for slot, pool := range band.pools {
			if len(pool) == 0 || len(pool) > 4 {
				t.Fatalf("level %d slot %d invalid pool size", band.minLevel, slot)
			}
			seen := map[int]bool{}
			for _, id := range pool {
				item, ok := catalog.Find(10, id*1000+1)
				if !ok || item.WeaponCategory == nil || *item.WeaponCategory != slot || seen[id] {
					t.Fatalf("level %d slot %d invalid/duplicate stock weapon %d: %+v", band.minLevel, slot, id, item)
				}
				seen[id] = true
			}
		}
	}
}

func TestBotWeaponBandBoundaries(t *testing.T) {
	for _, tc := range []struct{ level, want int }{{-1, 1}, {0, 1}, {1, 1}, {5, 1}, {6, 6}, {10, 6}, {11, 11}, {20, 11}, {21, 21}, {30, 21}, {31, 31}, {45, 31}, {46, 46}, {55, 46}, {56, 56}, {64, 56}, {65, 65}, {66, 65}} {
		if got := botWeaponsForLevel(tc.level).minLevel; got != tc.want {
			t.Fatalf("level %d band %d want %d", tc.level, got, tc.want)
		}
	}
}

func TestAllPlayerLevelsUseValidArsenalsAcrossEveryConfig(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")
	keys := []string{"p", "b", "m", "sp", "sn", "h"}
	for level := 1; level <= 65; level++ {
		t.Run(fmt.Sprint(level), func(t *testing.T) {
			band := botWeaponsForLevel(level)
			check := func(raw []any, full bool) {
				t.Helper()
				if len(raw) != 6 {
					t.Fatalf("set size %d", len(raw))
				}
				for slot, value := range raw {
					id := int(value.(float64))
					if full {
						if id%1000 != 1 {
							t.Fatalf("bad full index %d", id)
						}
						id /= 1000
					}
					allowed := false
					for _, candidate := range band.pools[slot] {
						if id == candidate {
							allowed = true
						}
					}
					if !allowed {
						t.Fatalf("level %d slot %d got forbidden weapon %d", level, slot, id)
					}
				}
			}
			for _, name := range []string{"Bots", "MatchmakingBots", "MMBots"} {
				d := scaledBotDocument(t, s, name, level)
				if name == "MMBots" {
					base, _ := s.hostedConfigDocument(name)
					equipment := d["el"].(map[string]any)
					original := base.document["el"].(map[string]any)
					presets := d["bpl"].([]any)
					ids := presets[0].(map[string]any)["el"].([]any)
					seen := [6]map[int]bool{}
					for j := range seen {
						seen[j] = map[int]bool{}
					}
					active := map[string]bool{}
					for _, raw := range ids {
						id := jsonNumberString(raw)
						active[id] = true
						set := equipment[id].(map[string]any)
						before := original[id].(map[string]any)
						values := make([]any, 6)
						for slot, key := range keys {
							values[slot] = set[key]
							seen[slot][int(set[key].(float64))/1000] = true
						}
						check(values, true)
						for key, value := range before {
							if !strings.Contains("|p|b|m|sp|sn|h|", "|"+key+"|") && !reflect.DeepEqual(value, set[key]) {
								t.Fatalf("changed non-weapon %s.%s", id, key)
							}
						}
					}
					for id, set := range original {
						if !active[id] && !reflect.DeepEqual(set, equipment[id]) {
							t.Fatalf("inactive loadout %s changed", id)
						}
					}
					for slot, pool := range band.pools {
						for _, id := range pool {
							if !seen[slot][id] {
								t.Fatalf("MMBots missing pool weapon %d", id)
							}
						}
					}
					fallback := equipment[d["dl"].(string)].(map[string]any)
					if level == 65 && (fallback["p"] != float64(834001) || fallback["sp"] != float64(1175001) || fallback["h"] != float64(94001)) {
						t.Fatal("lost requested fallback")
					}
					continue
				}
				legacy := d
				if name == "MatchmakingBots" {
					legacy = d["AI"].(map[string]any)
					for _, raw := range d["AI_Tiers"].([]any) {
						sets := raw.(map[string]any)["AI_WeaponSets"].([]any)
						seen := [6]map[int]bool{}
						for j := range seen {
							seen[j] = map[int]bool{}
						}
						for _, rawSet := range sets {
							values := rawSet.([]any)
							check(values, false)
							for slot, v := range values {
								seen[slot][int(v.(float64))] = true
							}
						}
						for slot, pool := range band.pools {
							for _, id := range pool {
								if !seen[slot][id] {
									t.Fatalf("MatchmakingBots missing weapon %d", id)
								}
							}
						}
					}
				}
				for _, raw := range legacy["weapons"].([]any) {
					parts := strings.Split(raw.(string), ",")
					values := make([]any, len(parts))
					for i, p := range parts {
						v, err := strconv.Atoi(p)
						if err != nil {
							t.Fatal(err)
						}
						values[i] = float64(v)
					}
					check(values, false)
				}
			}
		})
	}
}

func TestBotWeaponProgressionHashAndIsolation(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")
	for _, name := range []string{"Bots", "MatchmakingBots", "MMBots"} {
		base, _ := s.hostedConfigDocument(name)
		before := append([]byte(nil), base.body...)
		hashes := map[string]bool{}
		for _, band := range botWeaponBands {
			hosted, _ := s.hostedConfigDocument(name)
			got := s.scaleBotConfig(name, hosted, band.minLevel)
			if hashes[got.hash] {
				t.Fatalf("%s band %d hash collision", name, band.minLevel)
			}
			hashes[got.hash] = true
			again, _ := s.hostedConfigDocument(name)
			same := s.scaleBotConfig(name, again, band.minLevel)
			if got.hash != same.hash || string(got.body) != string(same.body) {
				t.Fatal("nondeterministic scaling")
			}
		}
		after, _ := s.hostedConfigDocument(name)
		if !reflect.DeepEqual(before, after.body) {
			t.Fatal("mutated shared document")
		}
		encoded, _ := json.Marshal(after.document)
		if string(encoded) != string(before) {
			t.Fatal("shared document/body diverged")
		}
	}
}
