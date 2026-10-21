// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "strings"

// Compact stock IDs, NOT gallery numbers. Slot order matches the client's
// Primary, Backup, Melee, Special, Sniper, Heavy contract. These are balance
// unlocks for bots, not claims about the armory's purchase-level requirements.
type botWeaponBand struct {
	minLevel int
	pools    [6][]int
}

var botWeaponBands = []botWeaponBand{
	{1, [6][]int{
		{10, 53}, {1, 17}, {9}, {333}, {67}, {162},
	}},
	{6, [6][]int{
		{84, 142, 163}, {17, 380}, {131, 130}, {333}, {67, 77}, {75, 79},
	}},
	{11, [6][]int{
		{220, 167, 598}, {203, 293}, {615, 130}, {399, 704}, {374, 458, 77}, {444, 75},
	}},
	{21, [6][]int{
		{727, 720, 767}, {640, 1069, 293}, {1110, 615}, {769, 399, 895}, {687, 1068, 458}, {755, 706, 444},
	}},
	{31, [6][]int{
		{795, 987, 711, 720}, {1069, 1380, 640}, {1110, 1121}, {895, 769, 704}, {687, 1068, 1167}, {755, 1216, 706},
	}},
	{46, [6][]int{
		{1449, 795, 987, 711}, {1380, 1069, 640}, {1110, 1121}, {895, 769, 704}, {705, 1197, 1167, 687}, {1416, 1216, 755, 706},
	}},
	{56, [6][]int{
		{1449, 795, 987, 767}, {1380, 1069, 640, 293}, {1121, 1110}, {895, 769, 399, 704}, {1197, 705, 1167, 1068}, {1416, 1216, 706, 755},
	}},
	{65, [6][]int{
		{level65UltimatumID, 1449, 795, 987}, {1380, 1069, 640, 293}, {1121, 1110},
		{level65VerticalGripDeviceID, 895, 769, 704}, {1197, 705, 1167, 687}, {level65BigBuddyID, 1416, 1216, 706},
	}},
}

func botWeaponsForLevel(level int) botWeaponBand {
	selected := botWeaponBands[0]
	for _, band := range botWeaponBands {
		if level < band.minLevel {
			break
		}
		selected = band
	}
	return selected
}

func botWeaponSet(band botWeaponBand, index int, fallback bool) [6]int {
	var set [6]int
	for slot, pool := range band.pools {
		set[slot] = pool[index%len(pool)]
	}
	// Keep the three previously requested weapons available together on the
	// unrandomized maximum-rank fallback; other rows still rotate all pools.
	if fallback && band.minLevel == 65 {
		set[0], set[3], set[5] = level65UltimatumID, level65VerticalGripDeviceID, level65BigBuddyID
	}
	return set
}

func applyBotWeaponProgression(name string, document map[string]any, level int) {
	band := botWeaponsForLevel(level)
	switch name {
	case "Bots":
		applyLegacyBotWeapons(document, band)
	case "MatchmakingBots":
		if ai, ok := document["AI"].(map[string]any); ok {
			applyLegacyBotWeapons(ai, band)
		}
		tiers, _ := document["AI_Tiers"].([]any)
		for _, raw := range tiers {
			tier, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			sets, _ := tier["AI_WeaponSets"].([]any)
			for i, rawSet := range sets {
				values, ok := rawSet.([]any)
				if !ok || len(values) < 6 {
					continue
				}
				set := botWeaponSet(band, i, i == len(sets)-1)
				for slot, id := range set {
					values[slot] = float64(id)
				}
			}
		}
	case "MMBots":
		equipment, ok := document["el"].(map[string]any)
		if !ok {
			return
		}
		presets, _ := document["bpl"].([]any)
		// Presets were already materialized from the selected stock AI band.
		// Rewrite only that active equipment range, retaining gadgets/unknown fields.
		if len(presets) == 0 {
			return
		}
		preset, ok := presets[0].(map[string]any)
		if !ok {
			return
		}
		ids, _ := preset["el"].([]any)
		keys := [6]string{"p", "b", "m", "sp", "sn", "h"}
		for i, rawID := range ids {
			id := jsonNumberString(rawID)
			loadout, ok := equipment[id].(map[string]any)
			if !ok {
				continue
			}
			set := botWeaponSet(band, i, id == document["dl"])
			for slot, weapon := range set {
				loadout[keys[slot]] = float64(weapon*1000 + 1)
			}
		}
	}
}

func applyLegacyBotWeapons(document map[string]any, band botWeaponBand) {
	weapons, _ := document["weapons"].([]any)
	for i := range weapons {
		// AI tier zero is the common legacy fallback, so preserve all three
		// requested maximum-rank weapons there, not just in the last row.
		set := botWeaponSet(band, i, i == 0 || i == len(weapons)-1)
		values := make([]string, 6)
		for slot, id := range set {
			values[slot] = jsonNumberString(float64(id))
		}
		weapons[i] = strings.Join(values, ",")
	}
}
