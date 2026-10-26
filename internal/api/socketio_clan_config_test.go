// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
)

// clanItemParams checks one ItemParams string ("index:count") against the
// stock catalog and returns the item types found for its index.
func clanItemParams(t *testing.T, value any) (int, int, []int) {
	t.Helper()
	text, ok := value.(string)
	if !ok {
		t.Fatalf("ItemParams %#v is not an index:count string", value)
	}
	parts := strings.Split(text, ":")
	if len(parts) != 2 {
		t.Fatalf("ItemParams %q is not index:count", text)
	}
	index, err1 := strconv.Atoi(parts[0])
	count, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || count < 0 {
		t.Fatalf("ItemParams %q is not numeric", text)
	}
	var types []int
	for _, item := range catalog.ForIndex(index) {
		types = append(types, item.Type)
	}
	if len(types) == 0 {
		t.Fatalf("ItemParams %q names no stock item", text)
	}
	return index, count, types
}

func clanHasType(types []int, want int) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}

const clanSilverIndex = 8015

// ClanBuildings (84) is a Dictionary<int, building>: every key must parse
// as an int (a label key would fail the whole document) and name a stock
// ClanBuilding (1360) item.
func TestShippingClanBuildingsConfig(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")
	hosted, ok := s.hostedConfigDocument("ClanBuildings")
	if !ok {
		t.Fatal("ClanBuildings must be hosted")
	}
	if !configPushUsesInlineBody("ClanBuildings") {
		t.Fatal("ClanBuildings is read through GetText and must travel inline")
	}
	if len(hosted.document) == 0 {
		t.Fatal("no buildings")
	}
	for key, raw := range hosted.document {
		id, err := strconv.Atoi(key)
		if err != nil {
			t.Fatalf("key %q is not an int building id", key)
		}
		found := false
		for _, item := range catalog.ForIndex(id) {
			found = found || item.Type == 1360
		}
		if !found {
			t.Fatalf("building %d is not a stock ClanBuilding", id)
		}
		building := raw.(map[string]any)
		levels, ok := building["s"].([]any)
		if !ok || len(levels) == 0 {
			t.Fatalf("building %d has no levels", id)
		}
		lastPoints := -1.0
		for _, rawLevel := range levels {
			level := rawLevel.(map[string]any)
			for _, k := range []string{"fl", "tl", "p"} {
				v, ok := level[k].(float64)
				if !ok || v < 0 || v != float64(int(v)) {
					t.Fatalf("building %d level %s=%#v", id, k, level[k])
				}
			}
			if points := level["p"].(float64); points <= lastPoints {
				t.Fatalf("building %d level costs must grow", id)
			} else {
				lastPoints = points
			}
			if index, count, _ := clanItemParams(t, level["pr"]); index != clanSilverIndex || count <= 0 {
				t.Fatalf("building %d price %v", id, level["pr"])
			}
		}
		pbp, ok := building["pbp"].(map[string]any)
		if !ok || len(pbp) == 0 {
			t.Fatalf("building %d has no point price", id)
		}
		for currency, amount := range pbp {
			if _, err := strconv.Atoi(currency); err != nil {
				t.Fatalf("pbp key %q", currency)
			}
			if v, ok := amount.(float64); !ok || v <= 0 {
				t.Fatalf("pbp amount %#v", amount)
			}
		}
	}
}

// ClanCommon (69) is ClanCommonData. Every member is present and non-null
// so the store controller never meets a null list, tabs use ClanStoreTab
// names, and every store row names a stock item priced in ClanSilver.
func TestShippingClanCommonConfig(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")
	hosted, ok := s.hostedConfigDocument("ClanCommon")
	if !ok {
		t.Fatal("ClanCommon must be hosted")
	}
	if !configPushUsesInlineBody("ClanCommon") {
		t.Fatal("ClanCommon is read through GetText and must travel inline")
	}
	doc := hosted.document
	if _, ok := doc["p"].([]any); !ok {
		t.Fatal("p must be a list")
	}
	for _, k := range []string{"tsi", "o", "cs", "tsp", "rs"} {
		if _, ok := doc[k].(map[string]any); !ok {
			t.Fatalf("%s must be an object", k)
		}
	}
	offers := doc["o"].(map[string]any)
	for _, k := range []string{"sls", "sts", "pgs"} {
		if _, ok := offers[k].([]any); !ok {
			t.Fatalf("o.%s must be a list", k)
		}
	}
	storeData := doc["cs"].(map[string]any)
	for _, k := range []string{"krs", "krp", "grs", "grp"} {
		if _, ok := storeData[k].([]any); !ok {
			t.Fatalf("cs.%s must be a list", k)
		}
	}
	tabs := map[string]bool{"Clan": true, "Offer": true, "Weapon": true, "Gadget": true, "Clothes": true, "Gift": true}
	rows := 0
	seen := map[int]bool{}
	for tab, raw := range doc["tsi"].(map[string]any) {
		if !tabs[tab] {
			t.Fatalf("unknown ClanStoreTab %q", tab)
		}
		for _, rawRow := range raw.([]any) {
			row := rawRow.(map[string]any)
			index, count, types := clanItemParams(t, row["ip"])
			if count != 1 || seen[index] {
				t.Fatalf("store row %v", row)
			}
			seen[index] = true
			if tab == "Weapon" && !clanHasType(types, 10) {
				t.Fatalf("%d is not a weapon", index)
			}
			if tab == "Clothes" && !clanHasType(types, 20) && !clanHasType(types, 65) {
				t.Fatalf("%d is not armor or a skin", index)
			}
			if price, amount, _ := clanItemParams(t, row["pp"]); price != clanSilverIndex || amount <= 0 {
				t.Fatalf("store row price %v", row["pp"])
			}
			if d, ok := row["d"].(float64); !ok || d < -1 || d > 7 {
				t.Fatalf("division %#v", row["d"])
			}
			if _, ok := row["ups"].(map[string]any); !ok {
				t.Fatalf("ups must be an object: %v", row)
			}
			rows++
		}
	}
	if rows == 0 {
		t.Fatal("the clan store sells nothing")
	}
	// The parked LootBoxesProviderClan stays unhosted (see docs/CLANS_23_1_3.md).
	if _, ok := s.hostedConfigDocument("LootBoxesProviderClan"); ok {
		t.Fatal("LootBoxesProviderClan must not be hosted without measured content")
	}
}

func TestClanWarAnswersStateNoSeason(t *testing.T) {
	s, a, _, _, codec := socialAPIFixture(t)
	socialDispatch(t, s, a, "get_info_clan_warV2", sioHead{"req_id": float64(1), "clan_id": "1"})
	info := socialEvent(t, socialFrames(t, a, codec), "get_info_clan_warV2")
	if info["enable"] != false || info["reg"] != false || info["war"] != float64(0) || info["season_id"] != float64(0) {
		t.Fatal("v2 info", info)
	}
	if _, ok := info["reward"]; ok {
		t.Fatal("no season has no reward", info)
	}
	if _, ok := info["err_code"]; ok {
		t.Fatal("a successful answer must omit err_code", info)
	}
	socialDispatch(t, s, a, "get_info_clan_war", sioHead{"req_id": float64(2), "clan_id": "1"})
	info = socialEvent(t, socialFrames(t, a, codec), "get_info_clan_war")
	if _, ok := info["enable"]; ok || info["reg"] != false || info["start"] != float64(0) {
		t.Fatal("v1 info", info)
	}
	for i, event := range []string{"register_clan_war", "register_clan_warV2", "update_war_progress_v2", "clear_war_reward"} {
		socialDispatch(t, s, a, event, sioHead{"req_id": float64(10 + i)})
		fail := socialEvent(t, socialFrames(t, a, codec), event)
		if fail["err_code"] != float64(clanErrGeneric) || fail["result"] != clanWarUnavailable {
			t.Fatal(event, fail)
		}
	}
	socialDispatch(t, s, a, "get_auto_tribute", sioHead{"req_id": float64(20)})
	if read := socialEvent(t, socialFrames(t, a, codec), "get_auto_tribute"); read["err_code"] != nil {
		t.Fatal("tribute read", read)
	}
}
