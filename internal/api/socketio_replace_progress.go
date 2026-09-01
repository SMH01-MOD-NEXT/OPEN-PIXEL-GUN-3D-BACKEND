// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"fmt"
	"strings"
)

const (
	sioReplaceProgressIndexKey = "index"
	sioProgressSlotDataKey     = "d"
)

// replacementProgressSlots validates the snapshot before any stored state is
// touched and removes the wire-only checksum wrapper. The client sends every
// slot as {"d": <raw slot>, "h": <md5>}; ProgressSlots stores only d, just as
// create_slots does. The checksum is retained on the client side and is not an
// account selector or an authorization mechanism.
func replacementProgressSlots(head sioHead) (map[string]any, error) {
	wireSlots, ok := head[sioSlotsKey].(map[string]any)
	if !ok || len(wireSlots) == 0 {
		return nil, fmt.Errorf("%s must be a non-empty object", sioSlotsKey)
	}

	slots := make(map[string]any, len(wireSlots))
	for id, value := range wireSlots {
		if id == "" || strings.TrimSpace(id) != id {
			return nil, fmt.Errorf("slot id %q is not canonical", id)
		}
		for i := 0; i < len(id); i++ {
			if id[i] < '0' || id[i] > '9' {
				return nil, fmt.Errorf("slot id %q is not numeric", id)
			}
		}
		wrapper, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("slot %s is not an object", id)
		}
		data, present := wrapper[sioProgressSlotDataKey]
		if !present {
			return nil, fmt.Errorf("slot %s has no %s member", id, sioProgressSlotDataKey)
		}
		slots[id] = data
	}
	return slots, nil
}

// answerReplaceProgress owns the complete post-sync snapshot. The stock client
// already waits only for the normal req_id-correlated completion, so the wire
// acknowledgement stays compatible while the snapshot is now actually stored.
func (s *Server) answerReplaceProgress(session *sioSession, head sioHead, reqID int) map[string]any {
	_, _, playerID := session.identity()
	if playerID == "" {
		s.log.Event("socket.progress_replace_denied", map[string]any{
			"sid": session.sid, "req_id": reqID,
			"note": "replace_progress arrived before auth_v2 bound an account",
		})
		return sioDenied(reqID)
	}

	slots, err := replacementProgressSlots(head)
	if err != nil {
		body := sioOK(reqID)
		body["error"] = 1
		body["result"] = "invalid_progress"
		s.log.Event("socket.progress_replace_rejected", map[string]any{
			"sid": session.sid, "player_id": playerID, "req_id": reqID, "err": err.Error(),
			"note": "the old snapshot was preserved",
		})
		return body
	}

	// This upload arrives before the client has been told anything about what
	// the server holds, so a blank core slot here means "not yet informed",
	// not "reset to zero". See config.ProgressBlankUploadGuard for the device
	// run that measured an operator edit being erased 62 ms after this server
	// answered the slot handshake.
	var repaired, authoritative []string
	stored, hasStored := s.sessionProfile(session)
	if hasStored && stored.ProgressRevision != 0 {
		level, experience, coins, gems := stored.Counters()
		authoritative = enforceAuthoritativeCoreSlots(slots, level, experience, coins, gems)
	} else if s.cfg.ProgressBlankUploadGuard && hasStored {
		level, experience, coins, gems := stored.Counters()
		repaired = repairBlankCoreSlots(slots, level, experience, coins, gems)
	}

	profile, err := s.st.ReplaceProfileProgressSlots(playerID, slots)
	if err != nil {
		body := sioOK(reqID)
		body["error"] = 1
		body["result"] = "store_error"
		s.log.Event("socket.progress_replace_failed", map[string]any{
			"sid": session.sid, "player_id": playerID, "req_id": reqID, "err": err.Error(),
		})
		return body
	}

	s.log.Event("socket.progress_replaced", map[string]any{
		"sid": session.sid, "player_id": playerID, "req_id": reqID,
		"index": head.text(sioReplaceProgressIndexKey), "received": len(slots), "stored": len(profile.ProgressSlots),
		"level": profile.Level, "experience": profile.Experience, "coins": profile.Coins, "gems": profile.Gems,
		"repaired_slots":      repaired,
		"authoritative_slots": authoritative,
		"progress_revision":   profile.ProgressRevision,
		"note":                "installed the full unwrapped slot snapshot",
	})
	return sioOK(reqID)
}

// slotCounter reads one counter out of an uploaded slot document.
//
// Strings are refused for the same reason store.counterInt64 refuses them: the
// client sends its counters as JSON numbers, and accepting a quoted digit here
// would let an unrelated text field be read as a currency.
func slotCounter(value any) (int64, bool) {
	switch number := value.(type) {
	case float64:
		if float64(int64(number)) != number {
			return 0, false
		}
		return int64(number), true
	case int64:
		return number, true
	case int:
		return int64(number), true
	default:
		return 0, false
	}
}

// readSlotCounter returns the first candidate key present in the slot. The
// candidate order mirrors store.projectProfileCounters, which reads the long
// spelling before the compact one and stops at the first key it finds.
func readSlotCounter(slot map[string]any, candidates ...string) int64 {
	for _, key := range candidates {
		value, present := slot[key]
		if !present {
			continue
		}
		number, _ := slotCounter(value)
		return number
	}
	return 0
}

// writeSlotCounter fills a counter back in using the spelling the uploaded slot
// already uses, falling back to the compact key the 23.1.3 client writes. The
// value is stored as a float64 because that is what this document decodes to
// everywhere else, and a slot that round-trips through JSON differently from
// its neighbours is a bug waiting for the next reader.
func writeSlotCounter(slot map[string]any, value int64, candidates ...string) {
	for _, key := range candidates {
		if _, present := slot[key]; present {
			slot[key] = float64(value)
			return
		}
	}
	slot[candidates[len(candidates)-1]] = float64(value)
}

// enforceAuthoritativeCoreSlots overwrites the two operator-owned slots with
// the current server values. This applies to non-blank uploads too: the stock
// client can legally normalize an impossible XP value, but that local repair
// must not silently rewrite a profile an operator still owns. Missing core
// slots are created so a complete replacement cannot drop server authority.
func enforceAuthoritativeCoreSlots(slots map[string]any, level int, experience, coins, gems int64) []string {
	currencies, ok := slots["3"].(map[string]any)
	if !ok {
		currencies = map[string]any{}
		slots["3"] = currencies
	}
	writeSlotCounter(currencies, coins, "Coins")
	writeSlotCounter(currencies, gems, "GemsCurrency")

	progress, ok := slots["4"].(map[string]any)
	if !ok {
		progress = map[string]any{}
		slots["4"] = progress
	}
	writeSlotCounter(progress, int64(level), "level", "l")
	writeSlotCounter(progress, experience, "experience", "exp", "e")
	return []string{"3", "4"}
}

// repairBlankCoreSlots refills the two core slots when the upload would erase a
// non-blank stored profile, and reports which ones it touched.
//
// Only slot 3 and slot 4 are considered, because those are the only two
// store.projectReplacementSlotState projects onto the profile: slot 3 owns the
// currencies and slot 4 owns level and experience. A slot the upload omits
// entirely is left omitted - the projection does not touch a counter whose slot
// is absent, so the stored value already survives that case on its own.
func repairBlankCoreSlots(slots map[string]any, level int, experience, coins, gems int64) []string {
	var repaired []string
	if currencies, ok := slots["3"].(map[string]any); ok && (coins > 0 || gems > 0) {
		if readSlotCounter(currencies, "Coins") == 0 && readSlotCounter(currencies, "GemsCurrency") == 0 {
			writeSlotCounter(currencies, coins, "Coins")
			writeSlotCounter(currencies, gems, "GemsCurrency")
			repaired = append(repaired, "3")
		}
	}
	if progress, ok := slots["4"].(map[string]any); ok && (level > 1 || experience > 0) {
		if readSlotCounter(progress, "level", "l") <= 1 && readSlotCounter(progress, "experience", "exp", "e") == 0 {
			writeSlotCounter(progress, int64(level), "level", "l")
			writeSlotCounter(progress, experience, "experience", "exp", "e")
			repaired = append(repaired, "4")
		}
	}
	return repaired
}
