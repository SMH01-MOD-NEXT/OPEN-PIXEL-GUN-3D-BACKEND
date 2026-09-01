// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "encoding/json"

const (
	gameModeHubProgressSlotID   = "75"
	gameModeHubProgressRevision = 20696
)

// isJSONZero accepts the scalar forms a JSON-originated slot can have in tests
// and in a freshly decoded state file, without treating strings or booleans as
// the legacy revision.
func isJSONZero(value any) bool {
	switch n := value.(type) {
	case int:
		return n == 0
	case int8:
		return n == 0
	case int16:
		return n == 0
	case int32:
		return n == 0
	case int64:
		return n == 0
	case uint:
		return n == 0
	case uint8:
		return n == 0
	case uint16:
		return n == 0
	case uint32:
		return n == 0
	case uint64:
		return n == 0
	case float32:
		return n == 0
	case float64:
		return n == 0
	case json.Number:
		parsed, err := n.Float64()
		return err == nil && parsed == 0
	default:
		return false
	}
}

// isLegacyGameModeHubSlot recognises only the exact stale 23.1.3 record proved
// by runs 161-174. A richer record, another revision, or even one extra member
// is left untouched rather than guessed at.
func isLegacyGameModeHubSlot(value any) bool {
	slot, ok := value.(map[string]any)
	if !ok || len(slot) != 2 {
		return false
	}
	content, hasContent := slot["c"]
	revision, hasRevision := slot["d"]
	return hasContent && content == nil && hasRevision && isJSONZero(revision)
}

// repairLegacyGameModeHubSlot upgrades one exact stale slot after auth_v2 has
// selected playerID. The caller never takes an id from the repair payload, and
// MergeProfileProgressSlots replaces only slot 75, preserving every sibling.
// Re-entry sees the new shape and is therefore a no-op.
func (s *Server) repairLegacyGameModeHubSlot(playerID string) (bool, error) {
	profile, err := s.st.Profile(playerID)
	if err != nil {
		return false, err
	}
	if !isLegacyGameModeHubSlot(profile.ProgressSlots[gameModeHubProgressSlotID]) {
		return false, nil
	}
	_, err = s.st.MergeProfileProgressSlots(playerID, map[string]any{
		gameModeHubProgressSlotID: map[string]any{
			"c": []any{},
			"d": gameModeHubProgressRevision,
		},
	})
	if err != nil {
		return false, err
	}
	return true, nil
}
