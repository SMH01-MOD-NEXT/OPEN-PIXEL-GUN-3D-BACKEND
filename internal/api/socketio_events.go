// SPDX-License-Identifier: GPL-3.0-or-later

package api

// Answers to the events the client sends once auth_v2 has succeeded.
//
// The envelope is settled and documented in socketio_reply.go; this file is
// only about which reply each event gets. Every event name and every argument
// below was read off the wire from a real session, not guessed from the stock
// socket.io protocol:
//
//	req_id  1  auth_v2               uniq_id, hash, device, token, ...
//	req_id  2  subscribe             list: 40+ configs, each with its hash
//	req_id  3  progress_status       -
//	req_id  4  player_prefs          -
//	req_id  5  subscribe             type: mailbox_updated_messages
//	req_id  6  subscribe             type: mailbox_udpated_me_messages
//	req_id  7  subscribe             type: friends_list
//	req_id  8  get_last_command      -
//	req_id  9  subscribe             type: lobby_likes_v2
//	req_id 10  subscribe             type: mm_status
//	req_id 11  mmc_get_room          -
//	req_id 12  update_player_online  game_mode, version, map, tier, rank, ...
//	req_id 13  get_binded_ids        with_info: 1
//
// The spelling of req_id 6 is the client's own; it is matched verbatim rather
// than corrected, because the client is frozen and its typo is the protocol.
//
// Where the right answer is not yet known from the client's code, the reply is
// the smallest well-typed acknowledgement that carries req_id, and the event is
// logged with what was sent. Inventing rich payloads would be worse than
// useless: a field of the wrong type throws inside the client's handler
// (Convert.ToInt32 on a string) and aborts processing, which looks exactly like
// no answer at all.

import (
	"encoding/json"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

// Event names the client emits after authorisation.
const (
	sioSubscribeEvent    = "subscribe"
	sioProgressEvent     = "progress_status"
	sioPrefsEvent        = "player_prefs"
	sioLastCommandEvent  = "get_last_command"
	sioRoomEvent         = "mmc_get_room"
	sioOnlineEvent       = "update_player_online"
	sioBindedIDsEvent    = "get_binded_ids"
	sioCheckVersionEvent = "full_check_version"
)

// setIdentity records who authorised this session.
func (s *sioSession) setIdentity(uniqID, device, playerID string) {
	s.mu.Lock()
	s.uniqID = uniqID
	s.device = device
	s.playerID = playerID
	s.mu.Unlock()
}

// identity returns who authorised this session. The player id is empty until
// auth_v2 has been answered.
func (s *sioSession) identity() (uniqID, device, playerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.uniqID, s.device, s.playerID
}

// sioAnswer builds the single argument of a reply, or returns nil to send
// nothing. Returning nil is for the cases where answering would be a guess
// that could hang the client worse than silence; the caller logs the event
// either way.
type sioAnswer func(s *Server, session *sioSession, head sioHead, reqID int) map[string]any

// sioAnswerFor returns the answer builder for an event, or nil when this
// server has nothing to say about it yet.
func sioAnswerFor(event string) sioAnswer {
	switch event {
	case sioAuthEvent:
		return (*Server).answerAuth
	case sioSubscribeEvent:
		return (*Server).answerSubscribe
	case sioProgressEvent:
		return (*Server).answerProgressStatus
	case sioPrefsEvent:
		return (*Server).answerPlayerPrefs
	case sioLastCommandEvent:
		return (*Server).answerLastCommand
	case sioRoomEvent:
		return (*Server).answerRoom
	case sioOnlineEvent:
		return (*Server).answerPlayerOnline
	case sioBindedIDsEvent:
		return (*Server).answerBindedIDs
	case sioCheckVersionEvent:
		return (*Server).answerCheckVersion
	default:
		return nil
	}
}

// sioOK is the base every reply shares: the correlation id the client matches
// on, plus the result spelling the retired HTTP contract used.
func sioOK(reqID int) map[string]any {
	return map[string]any{
		sioRequestIDKey: reqID,
		"result":        "ok",
		"error":         0,
	}
}

// Client-visible socket error codes, from the client's own enum
// 专丏三一上专世丕七 (TypeDefIndex 9370).
const (
	sioErrNone          = 0
	sioErrNotAuthorized = 2
)

// answerAuth answers auth_v2 and binds the session to an account.
//
// This is where a ban becomes visible to a player: the identity is known for
// the first time here, and the client has a code for it, so a banned account is
// told so instead of being left to hang.
func (s *Server) answerAuth(session *sioSession, head sioHead, reqID int) map[string]any {
	uniqID := head.text("uniq_id")
	device := head.text("device")

	account, created, err := s.st.AdoptSocketIdentity(uniqID, device, "")
	if err != nil {
		// Identity is not a precondition for the reply the client waits on, so
		// a store failure is recorded and authorisation still completes. The
		// alternative is a hung client over a bookkeeping problem.
		s.log.Event("socket.identity_unbound", map[string]any{
			"sid":     session.sid,
			"uniq_id": uniqID,
			"err":     err.Error(),
		})
		session.setIdentity(uniqID, device, "")
		return authReplyBody(session, head, reqID)
	}

	session.setIdentity(uniqID, device, account.PlayerID)
	s.log.Event("socket.identity_bound", map[string]any{
		"sid":       session.sid,
		"player_id": account.PlayerID,
		"nick":      account.Nick,
		"device":    device,
		"created":   created,
	})

	if ban, banned := s.st.MatchBan(account.PlayerID, device, ""); banned {
		s.log.Event("socket.auth_banned", map[string]any{
			"sid":       session.sid,
			"player_id": account.PlayerID,
			"kind":      ban.Kind,
			"subject":   ban.Subject,
			"reason":    ban.Reason,
		})
		body := authReplyBody(session, head, reqID)
		body["result"] = "banned"
		body["error"] = sioErrNotAuthorized
		body["reason"] = ban.Reason
		return body
	}

	body := authReplyBody(session, head, reqID)
	// On a first run the client sent uniq_id empty and is waiting to be told
	// which player it is, so the assigned id replaces the echo. The aliases are
	// the names the retired /auth_v2/ HTTP contract used for this same value -
	// see handleLegacyAuth - because a JSON consumer ignores members it does not
	// know and it is not yet established which one the socket handler reads.
	// They are all the same string, so no alias can surprise a handler with a
	// type it cannot convert.
	body["uniq_id"] = account.PlayerID
	body["id_player"] = account.PlayerID
	body["player_id"] = account.PlayerID
	body["id"] = account.PlayerID
	body["user_id"] = account.PlayerID
	body["nick"] = account.Nick
	body["name"] = account.Nick
	return body
}

// answerSubscribe answers both shapes the client sends under this one name.
//
// A topic subscription names a feed: {"type":"friends_list", ...}. Only an
// acknowledgement is owed; the feed's contents arrive later as server pushes.
//
// A config poll instead carries a list of every config the client holds along
// with the hash of its cached copy:
//
//	{"list":{"Ads":{"h":"aec53fac26135f4c","b":0}, "CommandsWithFullSlots":{...}}}
//
// The client's own encrypted config cache stores plaintext shaped
// h_<16 hex>_c_<json>, so h is the hash of the copy it already has and the
// answer's job is to say which of them changed. An empty change set is the
// honest answer from this server: it does not host the 23.1.3 config bodies,
// and naming a config as changed without supplying one would strand the client
// waiting for content that never arrives. With nothing offered, the client
// keeps using the cache it already has on disk.
func (s *Server) answerSubscribe(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)

	if list, ok := head["list"].(map[string]any); ok {
		names := make([]string, 0, len(list))
		for name := range list {
			names = append(names, name)
		}
		s.log.Event("socket.config_polled", map[string]any{
			"sid":     session.sid,
			"req_id":  reqID,
			"configs": len(names),
			"note":    "answered with an empty change set; the client keeps its cached copies",
		})
		body["list"] = map[string]any{}
		return body
	}

	if topic := head.text("type"); topic != "" {
		body["type"] = topic
	}
	return body
}

// answerProgressStatus hands back the progress this server holds for the
// player. A fresh profile answers with an empty object, which is the truthful
// answer for an account that has never synchronised.
func (s *Server) answerProgressStatus(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body["progress"] = map[string]any{}

	profile, ok := s.sessionProfile(session)
	if !ok {
		return body
	}
	if profile.Progress != nil {
		body["progress"] = profile.Progress
	}
	body["level"] = profile.Level
	body["experience"] = profile.Experience
	return body
}

// answerPlayerPrefs hands back the client-owned key/value state this server
// holds, which is the other half of profile synchronisation.
func (s *Server) answerPlayerPrefs(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body["prefs"] = map[string]string{}

	profile, ok := s.sessionProfile(session)
	if !ok {
		return body
	}
	if profile.Prefs != nil {
		body["prefs"] = profile.Prefs
	}
	return body
}

// answerLastCommand reports that no operator command is queued for this player.
//
// This is the channel a live server uses to tell a client something happened to
// its account out of band. Nothing is queued on a fresh server, and saying so
// is what lets the client move on.
func (s *Server) answerLastCommand(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body["command"] = ""
	body["command_id"] = 0
	return body
}

// answerRoom reports that the player is not in a matchmaking room. Photon owns
// rooms in this deployment, so there is never one to report here.
func (s *Server) answerRoom(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body["room_name"] = ""
	body["map"] = "-1"
	return body
}

// answerPlayerOnline acknowledges the client's presence report and records what
// it said, which is where the operator's "who is online" view comes from.
func (s *Server) answerPlayerOnline(session *sioSession, head sioHead, reqID int) map[string]any {
	_, _, playerID := session.identity()
	s.log.Event("socket.player_online", map[string]any{
		"sid":       session.sid,
		"player_id": playerID,
		"game_mode": head.text("game_mode"),
		"version":   head.text("version"),
		"map":       head.text("map"),
		"tier":      head.text("tier"),
		"rank":      head.text("rank"),
	})
	return sioOK(reqID)
}

// answerBindedIDs reports no linked external accounts. Nothing in this
// deployment binds a Google or Facebook identity, so an empty list is the
// accurate answer rather than a placeholder.
func (s *Server) answerBindedIDs(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body["ids"] = []any{}
	return body
}

// answerCheckVersion answers the client's version gate.
//
// The client sends {"app_version":"1:23.1.3"} - platform and version joined by
// a colon - and waits to be told whether that build may proceed. This
// deployment exists to serve exactly that frozen build, so it is always
// allowed. The version is echoed rather than restated, so the client sees its
// own string come back instead of one this server invented.
func (s *Server) answerCheckVersion(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	if version := head.text("app_version"); version != "" {
		body["app_version"] = version
	}
	return body
}

// sessionProfile returns the stored profile for an authorised session.
func (s *Server) sessionProfile(session *sioSession) (store.Profile, bool) {
	_, _, playerID := session.identity()
	if playerID == "" {
		return store.Profile{}, false
	}
	profile, err := s.st.Profile(playerID)
	if err != nil {
		return store.Profile{}, false
	}
	return profile, true
}

// replyEncrypted writes one encrypted reply and reports whether all of its
// frames were queued.
//
// A partially written binary event is worse than none: the client would hold
// the text header and wait forever for attachments that never come. So a
// dropped frame is reported as the failure it is rather than retried into a
// worse state.
func (s *Server) replyEncrypted(session *sioSession, namespace, transport string, codec *sio.Codec, event string, reqID int, body map[string]any) bool {
	frames, err := encodeEncryptedEvent(codec, namespace, event, body)
	if err != nil {
		s.log.Event("socket.reply_unencodable", map[string]any{
			"sid":    session.sid,
			"event":  event,
			"req_id": reqID,
			"err":    err.Error(),
		})
		return false
	}

	for i, frame := range frames {
		if session.send(frame) {
			continue
		}
		s.log.Event("socket.reply_undelivered", map[string]any{
			"sid":    session.sid,
			"event":  event,
			"req_id": reqID,
			"frame":  i,
			"frames": len(frames),
			"note":   "the session send queue is full; the reply is incomplete",
		})
		return false
	}

	sent, err := json.Marshal(body)
	if err != nil {
		// body already survived json.Marshal inside encodeEncryptedEvent.
		sent = nil
	}
	s.log.Event("socket.event_answered", map[string]any{
		"sid":         session.sid,
		"transport":   transport,
		"namespace":   namespace,
		"event":       event,
		"req_id":      reqID,
		"attachments": sioReplyAttachments,
		"mode":        codec.Mode().String(),
		"body":        s.log.Body(sent),
	})
	return true
}
