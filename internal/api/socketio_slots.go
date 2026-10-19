// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "sort"

// Answers for the slot half of the boot handshake.
//
// The two calls sit between AuthSceneState.Authorized (2) and
// FullySynchronized (3), the state that ends the loading animation:
//
//	req_id 18  get_progress_slots  {"player_id":"918930694"}
//	req_id 19  create_slots        {"slots":{"25":{"v":{...}}, "26":{...}, ...}}
//
// Run 58 measured what happens when the reply carries an empty "slots"
// member: the client treated the query as answered, skipped create_slots, and
// regressed from 90% to 87%. Run 146 then corrected the member contract. The
// callback at RVA 0x33F145C casts "slots" to List<object>, maps every item with
// object.ToString(), and builds the HashSet<string> awaited by
// AuthSceneController. Returning the stored slot document itself fails that cast
// and throws ArgumentNullException in Enumerable.Select.
//
// Therefore an account with no stored slots still receives the bare
// acknowledgement and uploads its local set. Once the server holds a non-empty
// set, the next boot receives the sorted list of stored slot ids.

const (
	sioProgressSlotsEvent = "get_progress_slots"
	sioCreateSlotsEvent   = "create_slots"

	// sioSlotsKey is the upload document on create_slots and the string-id
	// array on get_progress_slots. The event determines the member's shape.
	sioSlotsKey = "slots"
)

// answerProgressSlots returns the ids of the account's non-empty stored slot
// set. A new account gets no slots member so it follows the measured
// create_slots path.
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

	body := sioOK(reqID)
	held := 0
	served := 0
	if profile, ok := s.sessionProfile(session); ok {
		held = len(profile.ProgressSlots)
		if held > 0 && !s.cfg.ProgressSlotsAlwaysCreate {
			slotIDs := make([]string, 0, held)
			for id := range profile.ProgressSlots {
				slotIDs = append(slotIDs, id)
			}
			sort.Strings(slotIDs)
			body[sioSlotsKey] = slotIDs
			served = len(slotIDs)
		}
	}
	// Section 56, the tutorials section the boot gates on, travels here when the
	// operator asks for it: the client uploads its own copy of that section in
	// this same document, and run 108 showed the init_progress channel does not
	// reach the tutorial manager. See socketio_tutorial_section.go.
	tutorialSeeded := false
	if s.cfg.TutorialSectionViaSlots {
		slots, _ := body[sioSlotsKey].(map[string]any)
		if slots == nil {
			slots = map[string]any{}
		}
		if tutorialSeeded = s.applyTutorialSection(slots); tutorialSeeded {
			body[sioSlotsKey] = slots
		}
	}

	note := "no stored slots; omitted slots so the client uploads its local set"
	switch {
	case served > 0:
		note = "returned the non-empty slot-id list consumed by the synchronization callback"
	case held > 0:
		note = "held a stored set but omitted it; serving it back skips the create_slots branch the boot needs"
	}
	s.log.Event("socket.progress_slots_asked", map[string]any{
		"sid":                     session.sid,
		"player_id":               playerID,
		"held":                    held,
		"served":                  served,
		"tutorial_section_seeded": tutorialSeeded,
		"note":                    note,
	})
	return body
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
		profile, err := s.st.MergeProfileProgressSlots(playerID, uploaded, session.progressWriteContext())
		if err != nil {
			// Do not report a successful durable upload after a failed write.
			s.log.Event("socket.profile_unsaved", map[string]any{
				"player_id": playerID,
				"part":      "progress_slots",
				"err":       err.Error(),
			})
			return socketStateFailure(reqID, "store_failed")
		} else {
			stored = len(profile.ProgressSlots)
		}
	}
	// The tutorials section rides back on the acknowledgement of the upload,
	// which is the one moment the client is reading a slot document without
	// also deciding whether to upload one. See socketio_tutorial_section.go.
	body := sioOK(reqID)
	tutorialSeeded := false
	if s.cfg.TutorialSectionViaCreateSlots {
		slots := map[string]any{}
		if tutorialSeeded = s.applyTutorialSection(slots); tutorialSeeded {
			body[sioSlotsKey] = slots
		}
	}

	s.log.Event("socket.progress_slots_stored", map[string]any{
		"sid":                     session.sid,
		"player_id":               playerID,
		"received":                len(uploaded),
		"stored":                  stored,
		"tutorial_section_seeded": tutorialSeeded,
	})
	return body
}
