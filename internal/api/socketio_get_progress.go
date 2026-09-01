// SPDX-License-Identifier: GPL-3.0-or-later

package api

// The download half of progress synchronisation.
//
// Every other progress path in this server is an upload: the client sends what
// it has and this server stores it. The 2026-09-01 device measurement showed
// why that is not enough. On the synchronize path the client asked
// get_progress_slots, received the 87 stored slot ids, and uploaded its own
// blank snapshot through replace_progress 62 ms later, erasing an operator
// edit. On the open path the edit survived and the boot never finished. Neither
// arm emitted init_progress, so a value this server holds had no channel to
// reach the client at all.
//
// The 23.3.2 reference protocol has that channel, and this server had no case
// for either half of it:
//
//	["get_progress", {"player_id": "918930694", "slots": [3, 4]}]
//	["get_progress_hashes_v2", {}]
//
// (PGWSD TestScript55.py:1237 and :639.) Those are request shapes. The answer
// shape is not in the reference, which is a client-side scripting harness: it
// builds requests and never describes replies.
//
// So the reply below follows this server's own measured contract instead of an
// invented one. The client's applier reads the member named progress and casts
// it to a dictionary without checking - that unchecked cast is what threw a
// NullReferenceException in run 48 - so the member is always present and always
// a map. The flat counters ride along for the reason they do on
// progress_status: the client reads the document and the scalars out of one
// object and cannot choose between them when they disagree.
//
// Until a device run catches the client asking, this handler is inert. It does
// not change the boot by itself. It means that when the stock
// SynchronizeProgress continuation does ask, there is an answer waiting other
// than the bare acknowledgement.
const (
	sioGetProgressEvent       = "get_progress"
	sioGetProgressHashesEvent = "get_progress_hashes_v2"
)

// requestedProgressSlots reads the optional slots filter. The reference sends
// it as an array of JSON numbers while every slot id in storage is a string, so
// the ids go through the same coercion the rest of the wire uses rather than a
// second, subtly different one written here.
func requestedProgressSlots(head sioHead) map[string]bool {
	raw, ok := head[sioSlotsKey].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	wanted := make(map[string]bool, len(raw))
	for _, value := range raw {
		if id := (sioHead{"id": value}).text("id"); id != "" {
			wanted[id] = true
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	return wanted
}

// answerGetProgress serves the stored progress document back to the client.
//
// SECURITY: the payload names the player itself, so the id it claims is checked
// against the one this socket authenticated as, exactly as the slot handshake
// does. Without that check any authenticated socket could read another
// account's progress by asking for it by id.
func (s *Server) answerGetProgress(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	// An empty document is still a document: it survives the client's cast and
	// the applier runs to a no-op instead of dereferencing null. Every early
	// return below leaves this in place for that reason.
	body[sioProgressDocumentKey] = map[string]any{}
	body[sioSlotsKey] = map[string]any{}

	_, _, playerID := session.identity()
	if playerID == "" {
		s.log.Event("socket.progress_download_unowned", map[string]any{
			"sid":  session.sid,
			"note": "get_progress arrived before auth_v2 bound an account; answered with an empty document",
		})
		return body
	}
	if claimed := head.text("player_id"); claimed != "" && claimed != playerID {
		s.log.Event("socket.progress_download_rejected", map[string]any{
			"sid":               session.sid,
			"session_player_id": playerID,
			"claimed_player_id": claimed,
			"note":              "a socket asked for the progress of an id it did not authenticate as",
		})
		return sioDenied(reqID)
	}

	profile, ok := s.sessionProfile(session)
	if !ok {
		s.log.Event("socket.progress_download_served", map[string]any{
			"sid": session.sid, "player_id": playerID, "req_id": reqID, "served": 0,
			"note": "no stored profile; answered with the empty document",
		})
		return body
	}

	// ProgressSlots is the set get_progress_slots enumerates ids for, so a
	// download asked for by slot id is answered from the same document. A
	// profile that only ever synchronised through init_progress has its copy in
	// ProgressDocument instead, and answering nothing in that case would be a
	// lie about what this server holds.
	source := profile.ProgressSlots
	origin := "progress_slots"
	if len(source) == 0 {
		source = profile.ProgressDocument
		origin = "progress_document"
	}

	wanted := requestedProgressSlots(head)
	document := make(map[string]any, len(source))
	for id, value := range source {
		if wanted != nil && !wanted[id] {
			continue
		}
		document[id] = value
	}
	// The live 23.1.3 handler reads the document from slots. Keep progress as
	// a compatibility alias for the later reference protocol and older tests.
	body[sioProgressDocumentKey] = document
	body[sioSlotsKey] = document

	level, experience, coins, gems := profile.Counters()
	body["level"] = level
	body["experience"] = experience
	body["coins"] = coins
	body["gems"] = gems

	s.log.Event("socket.progress_download_served", map[string]any{
		"sid": session.sid, "player_id": playerID, "req_id": reqID,
		"origin": origin, "held": len(source), "requested": len(wanted), "served": len(document),
		"level": level, "experience": experience, "coins": coins, "gems": gems,
		"note": "served the stored progress document on the download channel the 23.3.2 protocol defines",
	})
	return body
}

// answerGetProgressHashes names the checksum query instead of answering it.
//
// The client pairs every slot with an md5 when it uploads through
// replace_progress, so a per-slot checksum plainly exists. What is not known is
// the member this reply is read from or how the digest is computed, and neither
// can be derived from the reference harness, which only builds the request. A
// wrong member here is not a harmless guess: the applier casts what it finds
// without checking, and that is the failure that killed the boot coroutine in
// run 48.
//
// So this answers the bare acknowledgement, which is what the fallback did
// anyway, and logs the request under its own name. The moment a device run
// shows the client asking, the log says so and the answer can be measured from
// what the client does next instead of guessed now.
func (s *Server) answerGetProgressHashes(session *sioSession, head sioHead, reqID int) map[string]any {
	_, _, playerID := session.identity()
	s.log.Event("socket.progress_hashes_asked", map[string]any{
		"sid": session.sid, "player_id": playerID, "req_id": reqID,
		"note": "answered the bare acknowledgement: the reply member and digest form are unmeasured, and a wrong cast here kills the boot coroutine",
	})
	return sioOK(reqID)
}
