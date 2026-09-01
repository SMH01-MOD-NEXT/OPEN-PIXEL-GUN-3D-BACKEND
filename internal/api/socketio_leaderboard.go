// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"fmt"
	"math"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

const leaderboardSeasonSeconds int64 = 30 * 24 * 60 * 60

// localLeaderboardSeason gives the frozen client a stable, non-expired season
// without depending on the retired production scheduler. Both timestamps fit
// the client's exact Int32 parser through 2038.
func localLeaderboardSeason(now time.Time) (string, int) {
	window := now.UTC().Unix() / leaderboardSeasonSeconds
	end := (window + 1) * leaderboardSeasonSeconds
	if end > math.MaxInt32 {
		end = math.MaxInt32
	}
	return fmt.Sprintf("local-%d", window), int(end)
}

func profileLeaderboardScore(profile store.Profile) int {
	section, ok := profile.ProgressSlots["2"].(map[string]any)
	if !ok {
		return 0
	}
	value, ok := section["r"]
	if !ok {
		return 0
	}
	var score int64
	switch number := value.(type) {
	case float64:
		if number < 0 || number != math.Trunc(number) || number > math.MaxInt32 {
			return 0
		}
		score = int64(number)
	case int:
		score = int64(number)
	case int64:
		score = number
	default:
		return 0
	}
	if score < 0 || score > math.MaxInt32 {
		return 0
	}
	return int(score)
}

// answerLeaderboardV3 follows the exact 23.1.3 callback contract at RVA
// 0x4991394. In particular, result is a bool (not sioOK's string), season_id is
// a string, the timestamps and disable flags are integers, and all three
// collections are JSON arrays. Wrong types make the callback return before it
// publishes the leaderboard-ready state used by the result screen.
func (s *Server) answerLeaderboardV3(session *sioSession, head sioHead, reqID int) map[string]any {
	_, _, playerID := session.identity()
	if playerID == "" {
		s.log.Event("socket.leaderboard_v3_denied", map[string]any{
			"sid": session.sid, "req_id": reqID,
			"note": "get_leaderboard_v3 arrived before auth_v2 bound an account",
		})
		return map[string]any{sioRequestIDKey: reqID, "result": false, "error": sioErrNotAuthorized}
	}

	seasonID, seasonEnd := localLeaderboardSeason(time.Now())
	leaderboard := []any{}
	players := []any{}
	if profile, err := s.st.Profile(playerID); err == nil {
		leaderboard = append(leaderboard, map[string]any{
			"id": playerID, "score": profileLeaderboardScore(profile),
		})
		players = append(players, map[string]any{
			"id": playerID, "nick": profile.Nick,
		})
	}

	body := map[string]any{
		sioRequestIDKey:      reqID,
		"result":             true,
		"error":              sioErrNone,
		"season_id":          seasonID,
		"season_end":         seasonEnd,
		"current_season_end": seasonEnd,
		"disabled":           0,
		"disable_end":        0,
		"leaderboard":        leaderboard,
		"players":            players,
		"removed_players":    []any{},
	}
	s.log.Event("socket.leaderboard_v3_served", map[string]any{
		"sid": session.sid, "player_id": playerID, "req_id": reqID,
		"leaderboard_id": head.text("leaderboard_id"), "league": head.text("league"),
		"entries": len(leaderboard), "season_id": seasonID, "season_end": seasonEnd,
		"note": "served the bool/string/int/list contract consumed by the 23.1.3 callback",
	})
	return body
}
