// SPDX-License-Identifier: GPL-3.0-or-later

package api

// Answers for the slot half of the boot handshake.
//
// The two calls sit between AuthSceneState.Authorized (2) and
// FullySynchronized (3), the state that ends the loading animation:
//
//	req_id 18  get_progress_slots  {"player_id":"918930694"}
//	req_id 19  create_slots        {"slots":{"25":{"v":{...}}, "26":{...}, ...}}
//
// Run 58 measured what happens when the reply carries the member the client
// uploads its slots under. Answering get_progress_slots with an empty "slots"
// dictionary made the boot worse rather than better: the client never sent
// create_slots at all, none of the subscribe and update_progress traffic that
// normally follows it appeared, and the bar fell back from 90% to 87%. The
// bare acknowledgement of run 57 is what makes the client upload its slots and
// carry on, so that is what this server sends.
//
// The upload is still stored. The client cannot tell the difference - the
// reply is byte for byte the fallback's - and it is state a real server owns.
// Serving it back is a separate question, and run 58 answered "not as an empty
// dictionary"; the shape that a client with slots expects has to be read out
// of the client before it is put on the wire again.

const (
	sioProgressSlotsEvent = "get_progress_slots"
	sioCreateSlotsEvent   = "create_slots"

	// sioSlotsKey is the member the client uploads under. It is read here and
	// deliberately not written back; see the note above.
	sioSlotsKey = "slots"
)

// answerProgressSlots acknowledges the slot request and records what this
// server holds for the account, so the gap between stored and served state
// stays visible in the log.
//
// SECURITY: the payload names the player itself, so the id it claims is
// checked against the one this socket authenticated as.
func (s *Server) answerProgressSlots(session *sioSession, head sioHead, reqID int) map[string]any {
	_, _, playerID := session.identity()
	if playerID == "" {
		s.log.Event("socket.progress_slots_unowned", map[string]any{
			"sid":  session.sid,
			"note": "get_progress_slots arrived before auth_v2 bound an account",
		})
		return sioOK(reqID)
	}
	if claimed := head.text("player_id"); claimed != "" && claimed != playerID {
		s.log.Event("socket.progress_slots_rejected", map[string]any{
			"sid":               session.sid,
			"session_player_id": playerID,
			"claimed_player_id": claimed,
			"note":              "a socket asked for the slots of an id it did not authenticate as",
		})
		return sioDenied(reqID)
	}

	held := 0
	if profile, ok := s.sessionProfile(session); ok {
		held = len(profile.ProgressSlots)
	}
	s.log.Event("socket.progress_slots_asked", map[string]any{
		"sid":       session.sid,
		"player_id": playerID,
		"held":      held,
		"note":      "acknowledged without a slots member; run 58 showed an empty one stops the client from uploading its own",
	})
	return sioOK(reqID)
}

// answerCreateSlots stores the slot document the client uploads and answers
// with the same acknowledgement the fallback sent through run 57.
func (s *Server) answerCreateSlots(session *sioSession, head sioHead, reqID int) map[string]any {
	uploaded, _ := head[sioSlotsKey].(map[string]any)

	_, _, playerID := session.identity()
	if playerID == "" {
		s.log.Event("socket.progress_slots_unowned", map[string]any{
			"sid":  session.sid,
			"note": "create_slots arrived before auth_v2 bound an account; nothing was stored",
		})
		return sioOK(reqID)
	}
	if claimed := head.text("player_id"); claimed != "" && claimed != playerID {
		s.log.Event("socket.progress_slots_rejected", map[string]any{
			"sid":               session.sid,
			"session_player_id": playerID,
			"claimed_player_id": claimed,
			"note":              "a socket uploaded slots for an id it did not authenticate as",
		})
		return sioDenied(reqID)
	}

	stored := 0
	if len(uploaded) > 0 {
		profile, err := s.st.MergeProfileProgressSlots(playerID, uploaded)
		if err != nil {
			// The client is waiting on an acknowledgement, not on this
			// server's bookkeeping, so a failed write is recorded and the
			// reply still goes out.
			s.log.Event("socket.profile_unsaved", map[string]any{
				"player_id": playerID,
				"part":      "progress_slots",
				"err":       err.Error(),
			})
		} else {
			stored = len(profile.ProgressSlots)
		}
	}
	s.log.Event("socket.progress_slots_stored", map[string]any{
		"sid":       session.sid,
		"player_id": playerID,
		"received":  len(uploaded),
		"stored":    stored,
	})
	return sioOK(reqID)
}
