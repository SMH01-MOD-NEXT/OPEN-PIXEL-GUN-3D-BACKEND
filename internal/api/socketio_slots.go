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
// Run 58 measured what happens when the reply carries an empty "slots"
// dictionary: the client treated the query as answered, skipped create_slots,
// and regressed from 90% to 87%. Run 75 then identified the live callback at
// RVA 0x33F145C. It tests that same top-level member, casts it to the same
// Dictionary<string, object> shape create_slots uploads, and turns its keys
// into the remote-slot set awaited by AuthSceneController.
//
// Therefore an account with no stored slots still receives the bare
// acknowledgement and uploads its local set. Once the server holds a non-empty
// set, the next boot receives that actual set rather than the empty sentinel
// that caused the run-58 regression.

const (
	sioProgressSlotsEvent = "get_progress_slots"
	sioCreateSlotsEvent   = "create_slots"

	// sioSlotsKey is both the member the client uploads under and the member
	// the get_progress_slots callback reads back as Dictionary<string, object>.
	sioSlotsKey = "slots"
)

// answerProgressSlots returns the account's non-empty stored slot set. A new
// account gets no slots member so it follows the measured create_slots path.
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
			slots := make(map[string]any, held)
			for id, value := range profile.ProgressSlots {
				slots[id] = value
			}
			body[sioSlotsKey] = slots
			served = len(slots)
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
		note = "returned the non-empty slot dictionary consumed by the run-75 callback"
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
