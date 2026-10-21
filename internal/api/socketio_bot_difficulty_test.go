// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBotDifficultyBandsFollowPlayerLevel(t *testing.T) {
	tests := []struct {
		level      int
		legacyTier int
		matchTier  int
		preset     string
	}{
		{1, 0, 0, "Easy"},
		{10, 0, 0, "Easy"},
		{11, 1, 2, "Middle"},
		{25, 1, 2, "Middle"},
		{26, 2, 3, "Hard"},
		{45, 2, 3, "Hard"},
		{46, 3, 4, "VeryHard"},
		{65, 3, 4, "VeryHard"},
	}
	for _, test := range tests {
		got := botDifficultyForLevel(test.level)
		if got.legacyTier != test.legacyTier || got.matchTier != test.matchTier || got.preset != test.preset {
			t.Fatalf("level %d = %#v, want legacy=%d match=%d preset=%s", test.level, got, test.legacyTier, test.matchTier, test.preset)
		}
	}
}

func TestLevel65BotConfigsReplaceEveryFallbackWithStockHighRankData(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")

	bots := scaledBotDocument(t, s, "Bots", 65)
	if got := bots["weapons"].([]any)[0]; got != "834,1380,1121,1175,1197,94" {
		t.Fatalf("Bots fallback = %v", got)
	}
	for i, dodge := range bots["dodge"].([]any) {
		if dodge != float64(30) {
			t.Fatalf("Bots dodge[%d] = %v, want stock tier-3 value 30", i, dodge)
		}
	}

	matchmaking := scaledBotDocument(t, s, "MatchmakingBots", 65)
	ai := matchmaking["AI"].(map[string]any)
	if got := ai["weapons"].([]any)[0]; got != "834,1380,1121,1175,1197,94" {
		t.Fatalf("MatchmakingBots fallback = %v", got)
	}
	for i, raw := range matchmaking["AI_Tiers"].([]any) {
		tier := raw.(map[string]any)
		if tier["AI_DefaultAI"] != float64(3) {
			t.Fatalf("AI_Tiers[%d].AI_DefaultAI = %v, want 3", i, tier["AI_DefaultAI"])
		}
		firstSet := tier["AI_WeaponSets"].([]any)[0].([]any)
		if firstSet[0] != float64(level65UltimatumID) {
			t.Fatalf("AI_Tiers[%d] starts with weapon %v, want Ultimatum", i, firstSet[0])
		}
	}

	mmBots := scaledBotDocument(t, s, "MMBots", 65)
	if mmBots["dl"] != "47" {
		t.Fatalf("MMBots fallback loadout = %v, want strongest stock VeryHard fallback 47", mmBots["dl"])
	}
	for i, raw := range mmBots["bpl"].([]any) {
		preset := raw.(map[string]any)
		if preset["d"] != float64(3) {
			t.Fatalf("bpl[%d].d = %v, want stock VeryHard default tier 3", i, preset["d"])
		}
		if !reflect.DeepEqual(preset["t"], []any{float64(2), float64(3), float64(4)}) {
			t.Fatalf("bpl[%d].t = %#v, want stock VeryHard tiers", i, preset["t"])
		}
		loadouts := preset["el"].([]any)
		if loadouts[0] != float64(33) || loadouts[len(loadouts)-1] != float64(47) {
			t.Fatalf("bpl[%d].el = %#v, want stock VeryHard loadouts 33..47", i, loadouts)
		}
	}
}

func TestLevel65BotConfigsIncludeRequestedWeapons(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")

	for _, name := range []string{"Bots", "MatchmakingBots"} {
		document := scaledBotDocument(t, s, name, 65)
		if name == "MatchmakingBots" {
			document = document["AI"].(map[string]any)
		}
		for _, i := range []int{0, len(document["weapons"].([]any)) - 1} {
			got := document["weapons"].([]any)[i].(string)
			values := strings.Split(got, ",")
			if values[0] != "834" || values[3] != "1175" || values[5] != "94" {
				t.Fatalf("%s fallback[%d] = %v", name, i, got)
			}
		}
	}

	matchmaking := scaledBotDocument(t, s, "MatchmakingBots", 65)
	for tierIndex, rawTier := range matchmaking["AI_Tiers"].([]any) {
		sets := rawTier.(map[string]any)["AI_WeaponSets"].([]any)
		last := sets[len(sets)-1].([]any)
		if last[0] != float64(level65UltimatumID) || last[3] != float64(level65VerticalGripDeviceID) || last[5] != float64(level65BigBuddyID) {
			t.Fatalf("AI_Tiers[%d] last set = %#v", tierIndex, last)
		}
	}

	mmBots := scaledBotDocument(t, s, "MMBots", 65)
	loadout := mmBots["el"].(map[string]any)["47"].(map[string]any)
	if loadout["p"] != float64(834001) || loadout["sp"] != float64(1175001) || loadout["h"] != float64(94001) {
		t.Fatalf("MMBots loadout 47 = %#v", loadout)
	}
}

func TestRequestedWeaponsAreExclusiveToLevel65(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")

	level64 := scaledBotDocument(t, s, "Bots", 64)
	for i, got := range level64["weapons"].([]any) {
		if strings.Contains(","+got.(string)+",", ",834,") || strings.Contains(","+got.(string)+",", ",1175,") || strings.Contains(","+got.(string)+",", ",94,") {
			t.Fatalf("level-64 weapons[%d] = %v, contains a level-65 exclusive weapon", i, got)
		}
	}
}

func TestLevelScaledBotPushUsesMeasuredDeliveryContracts(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")
	list := map[string]any{
		"Bots":            map[string]any{sioConfigHashKey: ""},
		"MatchmakingBots": map[string]any{sioConfigHashKey: ""},
		"MMBots":          map[string]any{sioConfigHashKey: ""},
	}
	batches := s.configPushBatchesForLevel(list, 65)
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want one", len(batches))
	}
	batch := batches[0]
	if len(batch.entries) != 3 || len(batch.documents) != 1 {
		t.Fatalf("entries=%d documents=%d, want 3 and 1", len(batch.entries), len(batch.documents))
	}
	for _, name := range []string{"Bots", "MatchmakingBots"} {
		entry := batch.entries[name].(map[string]any)
		if _, ok := entry[sioPushBodyKey].(string); !ok {
			t.Fatalf("%s val is %T, want inline JSON text", name, entry[sioPushBodyKey])
		}
	}
	if _, ok := batch.entries["MMBots"].(map[string]any)[sioPushBodyKey].(int); !ok {
		t.Fatalf("MMBots must retain the binary attachment path: %#v", batch.entries["MMBots"])
	}
}

func TestBotScalingDoesNotMutateShippingDocuments(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")
	base, ok := s.hostedConfigDocument("MMBots")
	if !ok {
		t.Fatal("shipping MMBots is not hosted")
	}
	before := append([]byte(nil), base.body...)
	_ = s.scaleBotConfig("MMBots", base, 65)
	again, ok := s.hostedConfigDocument("MMBots")
	if !ok || !reflect.DeepEqual(before, again.body) {
		t.Fatal("per-player scaling changed the shared shipping document")
	}
}

func scaledBotDocument(t *testing.T, s *Server, name string, level int) map[string]any {
	t.Helper()
	hosted, ok := s.hostedConfigDocument(name)
	if !ok {
		t.Fatalf("shipping %s is not hosted", name)
	}
	scaled := s.scaleBotConfig(name, hosted, level)
	var document map[string]any
	if err := json.Unmarshal(scaled.body, &document); err != nil {
		t.Fatalf("decode scaled %s: %v", name, err)
	}
	if scaled.hash == hosted.hash {
		t.Fatalf("scaled %s retained the baseline hash", name)
	}
	return document
}

func TestBotPollAndPushUseTheSamePlayerScaledFingerprint(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = filepath.Join("..", "..", "content")
	reply, changed := s.hostedConfigReplyForLevel("MMBots", "f9d49789271aa02d", 65)
	if !changed {
		t.Fatal("level-65 MMBots was confirmed as the embedded starter document")
	}
	batches := s.configPushBatchesForLevel(map[string]any{
		"MMBots": map[string]any{sioConfigHashKey: "f9d49789271aa02d"},
	}, 65)
	if len(batches) != 1 {
		t.Fatalf("got %d push batches, want one", len(batches))
	}
	entry := batches[0].entries["MMBots"].(map[string]any)
	if reply[sioConfigHashKey] != entry[sioPushHashKey] {
		t.Fatalf("poll hash %v != push hash %v", reply[sioConfigHashKey], entry[sioPushHashKey])
	}
	body, ok := reply[sioConfigBodyKey].(string)
	if !ok {
		t.Fatalf("poll body is %T, want JSON text", reply[sioConfigBodyKey])
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		t.Fatal(err)
	}
	if document["dl"] != "47" {
		t.Fatalf("poll fallback loadout = %v, want 47", document["dl"])
	}
}
