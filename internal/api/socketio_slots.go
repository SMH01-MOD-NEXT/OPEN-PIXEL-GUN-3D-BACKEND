// SPDX-License-Identifier: GPL-3.0-or-later

package api

// Answers for the slot half of the boot handshake.
//
// Run 57 left the client parked in AuthSceneState.Authorized (2). The auth
// scene never dispatched FullySynchronized (3) - the state that ends the
// loading animation - and the bar stayed at 90% with its spinner running. The
// session log names the two calls that sit between those states, and this
// server answered both with the bare fallback acknowledgement:
//
//	req_id 18  get_progress_slots  {"player_id":"918930694"}
//	req_id 19  create_slots        {"slots":{"25":{"v":{...}}, "26":{...}, ...}}
//
// The member name is the client's own rather than this server's invention:
// create_slots uploads the document under "slots", and a client that uploads
// its slots is a client that was just told the server holds none of them.
//
// Two earlier stalls in this same boot had the identical shape - a consumer
// indexing a member the reply did not carry, throwing inside the client's own
// handler, and leaving behind a pending request nothing ever completes. So the
// member is always answered, even when it is empty: an empty dictionary
// survives both the lookup and the cast, and a client with no slots yet is a
// legitimate first run, not an error.

const (
	sioProgressSlotsEvent = "get_progress_slots"
	sioCreateSlotsEvent   = "create_slots"

	// sioSlotsKey is the member both calls travel under, read off the wire
	// from the client's own create_slots upload.
	sioSlotsKey = "slots"
)

// answerProgressSlots hands back the slot document this server holds for the
// authorised account.
//
// SECURITY: the payload names the player itself, so the id it claims is
// checked against the one this socket authenticated as. Without that check any
// authorised socket could read another player's slots by asking for them.
func (s *Server) answerProgressSlots(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body[sioSlotsKey] = map[string]any{}

	_, _, playerID := session.identity()
	if playerID == "" {
		s.log.Event("socket.progress_slots_unowned", map[string]any{
			"sid":  session.sid,
			"note": "get_progress_slots arrived before auth_v2 bound an account; answered with an empty document",
		})
		return body
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

	if profile, ok := s.sessionProfile(session); ok && len(profile.ProgressSlots) > 0 {
		body[sioSlotsKey] = profile.ProgressSlots
	}

	slots, _ := body[sioSlotsKey].(map[string]any)
	s.log.Event("socket.progress_slots_served", map[string]any{
		"sid":       session.sid,
		"player_id": playerID,
		"slots":     len(slots),
		"note":      "the document travels in slots, the member create_slots uploads it under",
	})
	return body
}

// answerCreateSlots stores the slot document the client uploads and answers
// with the document as this server now holds it.
//
// The echo is deliberate. This is the client's first write of its own state
// and the reply is the only place it can learn what the server accepted; the
// boot document is synchronised the same way, and answering create_slots with
// less than get_progress_slots would return is a disagreement the client has
// no way to resolve.
func (s *Server) answerCreateSlots(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body[sioSlotsKey] = map[string]any{}

	uploaded, _ := head[sioSlotsKey].(map[string]any)

	_, _, playerID := session.identity()
	if playerID == "" {
		s.log.Event("socket.progress_slots_unowned", map[string]any{
			"sid":  session.sid,
			"note": "create_slots arrived before auth_v2 bound an account; nothing was stored",
		})
		return body
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
			// The client is waiting on its slots, not on this server's
			// bookkeeping, so a failed write is recorded and the reply still
			// carries whatever was stored before.
			s.log.Event("socket.profile_unsaved", map[string]any{
				"player_id": playerID,
				"part":      "progress_slots",
				"err":       err.Error(),
			})
		} else if len(profile.ProgressSlots) > 0 {
			body[sioSlotsKey] = profile.ProgressSlots
			stored = len(profile.ProgressSlots)
		}
	}
	if stored == 0 {
		if profile, ok := s.sessionProfile(session); ok && len(profile.ProgressSlots) > 0 {
			body[sioSlotsKey] = profile.ProgressSlots
			stored = len(profile.ProgressSlots)
		}
	}

	s.log.Event("socket.progress_slots_created", map[string]any{
		"sid":       session.sid,
		"player_id": playerID,
		"received":  len(uploaded),
		"answered":  stored,
	})
	return body
}
