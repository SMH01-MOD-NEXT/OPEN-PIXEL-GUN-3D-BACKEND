// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
)

// botDifficultyBand is the stock 23.1.3 four-step AI scale. Player rank is
// deliberately mapped to a tier instead of being used as an AI index: the
// legacy controller accepts only 0..3, while MMBots has matching named presets.
type botDifficultyBand struct {
	legacyTier int
	matchTier  int
	preset     string
}

func botDifficultyForLevel(level int) botDifficultyBand {
	switch {
	case level >= 46:
		return botDifficultyBand{legacyTier: 3, matchTier: 4, preset: "VeryHard"}
	case level >= 26:
		return botDifficultyBand{legacyTier: 2, matchTier: 3, preset: "Hard"}
	case level >= 11:
		return botDifficultyBand{legacyTier: 1, matchTier: 2, preset: "Middle"}
	default:
		return botDifficultyBand{legacyTier: 0, matchTier: 0, preset: "Easy"}
	}
}

func (s *Server) sessionPlayerLevel(session *sioSession) int {
	if session == nil {
		return 1
	}
	_, _, playerID := session.identity()
	if playerID == "" {
		playerID, _ = session.bootstrapIdentity()
	}
	if playerID == "" {
		return 1
	}
	profile, err := s.st.Profile(playerID)
	if err != nil || profile.Level < 1 {
		return 1
	}
	return profile.Level
}

// scaleBotConfig repairs the stock fallback path without changing the client.
// In ordinary Photon matches the retired matchmaking transaction never calls
// AIBotController.SetAILevel, so the field remains zero. Each authenticated
// client therefore receives a cache document whose fallback entry represents
// that player's rank band. The room master owns the bots, making its profile
// the authoritative scale for the room.
func (s *Server) scaleBotConfig(name string, hosted hostedConfig, playerLevel int) hostedConfig {
	band := botDifficultyForLevel(playerLevel)
	changed := false

	switch name {
	case "Bots":
		changed = repeatLegacyBotTier(hosted.document, band.legacyTier)
	case "MatchmakingBots":
		changed = repeatMatchmakingBotTier(hosted.document, band)
	case "MMBots":
		changed = selectMMBotPreset(hosted.document, band.preset)
	}
	if !changed {
		return hosted
	}
	body, err := json.Marshal(hosted.document)
	if err != nil {
		return hosted
	}
	hosted.body = body
	hosted.hash = s.hostedConfigHash(body)
	return hosted
}

func repeatLegacyBotTier(document map[string]any, tier int) bool {
	weapons, ok := document["weapons"].([]any)
	if !ok || tier < 0 || tier >= len(weapons) {
		return false
	}
	for key, raw := range document {
		values, ok := raw.([]any)
		if !ok || len(values) != len(weapons) || tier >= len(values) {
			continue
		}
		selected := cloneJSONValue(values[tier])
		for i := range values {
			values[i] = cloneJSONValue(selected)
		}
		document[key] = values
	}
	return true
}

func repeatMatchmakingBotTier(document map[string]any, band botDifficultyBand) bool {
	ai, ok := document["AI"].(map[string]any)
	if !ok || !repeatLegacyBotTier(ai, band.legacyTier) {
		return false
	}
	tiers, ok := document["AI_Tiers"].([]any)
	if !ok || band.matchTier < 0 || band.matchTier >= len(tiers) {
		return false
	}
	selected := cloneJSONValue(tiers[band.matchTier])
	for i := range tiers {
		tiers[i] = cloneJSONValue(selected)
	}
	document["AI_Tiers"] = tiers
	return true
}

func selectMMBotPreset(document map[string]any, presetID string) bool {
	presets, ok := document["bpl"].([]any)
	if !ok {
		return false
	}
	var selected map[string]any
	for _, raw := range presets {
		preset, ok := raw.(map[string]any)
		if ok && preset["id"] == presetID {
			selected = preset
			break
		}
	}
	if selected == nil {
		return false
	}

	loadouts, ok := selected["el"].([]any)
	if !ok || len(loadouts) == 0 {
		return false
	}
	// dl is the path used when the retired matchmaking payload supplies no
	// equipment id. Point it at the strongest loadout in the selected stock
	// preset instead of the original starter loadout "1".
	document["dl"] = jsonNumberString(loadouts[len(loadouts)-1])
	for _, raw := range presets {
		preset, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := preset["id"]
		for _, key := range []string{"el", "t", "d", "mnkr", "mxkr", "ic", "c"} {
			preset[key] = cloneJSONValue(selected[key])
		}
		preset["id"] = id
	}
	document["bpl"] = presets
	return true
}

func jsonNumberString(value any) string {
	switch value := value.(type) {
	case json.Number:
		return value.String()
	case float64:
		body, _ := json.Marshal(value)
		return string(body)
	default:
		return "1"
	}
}

func cloneJSONValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		copy := make(map[string]any, len(value))
		for key, child := range value {
			copy[key] = cloneJSONValue(child)
		}
		return copy
	case []any:
		copy := make([]any, len(value))
		for i, child := range value {
			copy[i] = cloneJSONValue(child)
		}
		return copy
	default:
		return value
	}
}
