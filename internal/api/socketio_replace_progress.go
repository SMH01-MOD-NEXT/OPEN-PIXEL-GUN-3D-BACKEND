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
		"note": "installed the full unwrapped slot snapshot",
	})
	return sioOK(reqID)
}
