// SPDX-License-Identifier: GPL-3.0-or-later

package api

// Wire contract for the stored-profile reconciliation step.
//
// Run 151 read these names from the stock 23.1.3 callback at 0x17AB704. The
// callback accepts an optional Dictionary<string, object> under "slots" and
// applies it through the stock progress-slot updater at 0x2701830 before
// invoking the ordinary completion callback. Presence of _unknown_slots is a
// different path: it makes the client derive err_code=1602/SlotNotMatches.
const (
	sioSyncFullEvent   = "sync_full"
	sioUnknownSlotsKey = "_unknown_slots"
)

// answerSyncFull makes the server copy authoritative when an authenticated
// account already has stored slots. The request contains only per-slot client
// digests; the response therefore returns the actual slot document under the
// exact optional key measured in the stock callback. Returning a generic ack
// skipped this application step and produced the measured state-9 -> state-13
// dead end at 90%. Returning _unknown_slots produced state 4 (Empty) and
// removed the loading UI, so that marker is not used as the normal login path.
func (s *Server) answerSyncFull(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	clientDigests, _ := head[sioSlotsKey].(map[string]any)

	_, _, playerID := session.identity()
	if playerID == "" {
		s.log.Event("socket.progress_sync_unowned", map[string]any{
			"sid":  session.sid,
			"note": "sync_full arrived before auth_v2 bound an account",
		})
		return body
	}

	profile, ok := s.sessionProfile(session)
	if !ok || len(profile.ProgressSlots) == 0 {
		s.log.Event("socket.progress_sync_accepted", map[string]any{
			"sid":            session.sid,
			"player_id":      playerID,
			"client_digests": len(clientDigests),
			"stored_slots":   0,
			"note":           "the server has no remote slot document to apply",
		})
		return body
	}

	body[sioSlotsKey] = profile.ProgressSlots
	pendingRevision, _ := session.pendingProgressState()
	revisionServed := pendingRevision != 0 && pendingRevision == profile.ProgressRevision &&
		session.progressWriteContext().Revision == profile.ProgressRevision && session.markProgressSyncServed(pendingRevision)
	s.log.Event("socket.progress_sync_server_slots", map[string]any{
		"sid":              session.sid,
		"player_id":        playerID,
		"client_digests":   len(clientDigests),
		"stored_slots":     len(profile.ProgressSlots),
		"revision":         profile.ProgressRevision,
		"pending_revision": pendingRevision,
		"revision_served":  revisionServed,
		"prop":             head.text("prop"),
		"note":             "returned authoritative slots through correlated sync_full; Login will durably acknowledge this exact revision",
	})
	return body
}
