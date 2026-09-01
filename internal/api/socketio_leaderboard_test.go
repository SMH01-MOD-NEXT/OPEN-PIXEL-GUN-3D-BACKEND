// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLocalLeaderboardSeasonIsStableAndInt32Safe(t *testing.T) {
	now := time.Date(2026, time.September, 1, 14, 13, 42, 0, time.UTC)
	id1, end1 := localLeaderboardSeason(now)
	id2, end2 := localLeaderboardSeason(now.Add(24 * time.Hour))
	if id1 == "" || id1 != id2 || end1 != end2 {
		t.Fatalf("season changed inside one window: (%q,%d) then (%q,%d)", id1, end1, id2, end2)
	}
	if int64(end1) <= now.Unix() || int64(end1) > int64(^uint32(0)>>1) {
		t.Fatalf("season end %d is expired or outside the callback's Int32 range", end1)
	}
}

func TestLeaderboardV3UsesTheExactClientFieldTypes(t *testing.T) {
	s, _ := newTestServer(t)
	const playerID = "918930694"
	if _, err := s.st.ReplaceProfileProgressSlots(playerID, map[string]any{
		"1": map[string]any{"n": "Leaderboard Player"},
		"2": map[string]any{"r": float64(42)},
	}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	answer := sioAnswerFor(sioLeaderboardV3Event)
	if answer == nil {
		t.Fatal("get_leaderboard_v3 has no answer builder")
	}
	body := answer(s, authorisedSession("0123456789abcdef01234567", playerID), sioHead{
		sioRequestIDKey:  float64(74),
		"leaderboard_id": "",
		"league":         float64(0),
		"lb_time":        float64(0),
		"dt_time":        float64(0),
		"platform":       float64(1),
	}, 74)

	if result, ok := body["result"].(bool); !ok || !result {
		t.Fatalf("result is %T(%v), want the callback's true bool", body["result"], body["result"])
	}
	if _, ok := body["season_id"].(string); !ok {
		t.Fatalf("season_id is %T, want string", body["season_id"])
	}
	for _, key := range []string{"season_end", "current_season_end", "disabled", "disable_end"} {
		if _, ok := body[key].(int); !ok {
			t.Fatalf("%s is %T, want an integer accepted by Convert.ToInt32", key, body[key])
		}
	}
	leaderboard, ok := body["leaderboard"].([]any)
	if !ok || len(leaderboard) != 1 {
		t.Fatalf("leaderboard is %T(%v), want one-entry JSON list", body["leaderboard"], body["leaderboard"])
	}
	entry, ok := leaderboard[0].(map[string]any)
	if !ok || entry["id"] != playerID || entry["score"] != 42 {
		t.Fatalf("leaderboard entry has the wrong exact fields: %#v", leaderboard[0])
	}
	players, ok := body["players"].([]any)
	if !ok || len(players) != 1 {
		t.Fatalf("players is %T(%v), want one-entry JSON list", body["players"], body["players"])
	}
	player, ok := players[0].(map[string]any)
	if !ok || player["id"] != playerID || player["nick"] != "Leaderboard Player" {
		t.Fatalf("player data has the wrong exact fields: %#v", players[0])
	}
	removed, ok := body["removed_players"].([]any)
	if !ok || len(removed) != 0 {
		t.Fatalf("removed_players is %T(%v), want an empty JSON list", body["removed_players"], body["removed_players"])
	}

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode reply as the client does: %v", err)
	}
	if _, ok := decoded["result"].(bool); !ok {
		t.Fatalf("result lost its bool type on the wire: %s", raw)
	}
	for _, key := range []string{"leaderboard", "players", "removed_players"} {
		if _, ok := decoded[key].([]any); !ok {
			t.Fatalf("%s lost its list type on the wire: %s", key, raw)
		}
	}
}

func TestLeaderboardV3UnboundReplyStillUsesABooleanResult(t *testing.T) {
	s, _ := newTestServer(t)
	body := s.answerLeaderboardV3(&sioSession{sid: "0123456789abcdef01234567"}, sioHead{}, 100)
	if result, ok := body["result"].(bool); !ok || result {
		t.Fatalf("denied result is %T(%v), want false bool so the callback exits cleanly", body["result"], body["result"])
	}
	if body["error"] != sioErrNotAuthorized || body[sioRequestIDKey] != 100 {
		t.Fatalf("denied reply is not correlated or authorized correctly: %v", body)
	}
}
