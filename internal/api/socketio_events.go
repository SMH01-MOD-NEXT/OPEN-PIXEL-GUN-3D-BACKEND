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
	"strconv"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

// Event names the client emits after authorisation.
const (
	sioSubscribeEvent         = "subscribe"
	sioProgressEvent          = "progress_status"
	sioPrefsEvent             = "player_prefs"
	sioLastCommandEvent       = "get_last_command"
	sioRoomEvent              = "mmc_get_room"
	sioOnlineEvent            = "update_player_online"
	sioBindedIDsEvent         = "get_binded_ids"
	sioCheckBindedGCEvent     = "check_binded_gc"
	sioSocialIDKey            = "social_id"
	sioCheckVersionEvent      = "full_check_version"
	sioPlayerVersionEvent     = "get_player_version"
	sioRegisterPushEvent      = "register_push"
	sioInitProgressEvent      = "init_progress"
	sioGetInappsEvent         = "get_inapps"
	sioUpdateProgressEvent    = "update_progress"
	sioPlayerSuccessInitEvent = "player_success_init"
	sioReplaceProgressEvent   = "replace_progress"
	sioLeaderboardV3Event     = "get_leaderboard_v3"
)

// setBootstrapIdentity keeps the validated bootstrap id and source address,
// but never the reusable session credential that proved it.
func (s *sioSession) setBootstrapIdentity(playerID, ip string) {
	s.mu.Lock()
	s.bootstrapPlayerID = playerID
	s.remoteIP = ip
	s.mu.Unlock()
}

func (s *sioSession) setRemoteIP(ip string) {
	s.mu.Lock()
	s.remoteIP = ip
	s.mu.Unlock()
}

func (s *sioSession) bootstrapIdentity() (playerID, ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bootstrapPlayerID, s.remoteIP
}

// setIdentity records who authorised this session.
func (s *sioSession) setIdentity(uniqID, device, playerID string) {
	s.mu.Lock()
	s.uniqID = uniqID
	s.device = device
	if s.playerID != playerID {
		s.mapPresence = stockMapPresence{}
	}
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

// setAuthHash records the account hash the client presented in auth_v2.
func (s *sioSession) setAuthHash(hash string) {
	s.mu.Lock()
	s.authHash = hash
	s.mu.Unlock()
}

// identityHash returns the hash this session authorised with. It is empty on a
// first run, where the client has no account to name yet.
func (s *sioSession) identityHash() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authHash
}

func (s *sioSession) setPendingProgressRevision(revision uint64) {
	s.mu.Lock()
	s.pendingProgressRevision = revision
	s.progressSyncServed = false
	s.progressRevisionEchoed = false
	s.mu.Unlock()
}

func (s *sioSession) pendingProgressState() (revision uint64, syncServed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingProgressRevision, s.progressSyncServed
}

func (s *sioSession) progressRevisionEvidence() (revision uint64, syncServed, echoed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingProgressRevision, s.progressSyncServed, s.progressRevisionEchoed
}

func (s *sioSession) markProgressSyncServed(revision uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision == 0 || s.pendingProgressRevision != revision {
		return false
	}
	s.progressSyncServed = true
	return true
}

func (s *sioSession) resetProgressRevisionEvidence(revision uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision == 0 || s.pendingProgressRevision != revision {
		return false
	}
	s.progressSyncServed = false
	s.progressRevisionEchoed = false
	return true
}

// confirmProgressRevisionEcho records the second half of delivery proof only
// after a correlated sync_full returned this exact revision in the current boot
// or in the process-local staged boot immediately before it. The comparison is
// performed on the raw replace_progress upload before server authority repairs
// either core slot.
func (s *sioSession) confirmProgressRevisionEcho(revision uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision == 0 || s.pendingProgressRevision != revision || !s.progressSyncServed {
		return false
	}
	s.progressRevisionEchoed = true
	return true
}

func (s *sioSession) progressRevisionConfirmed(revision uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return revision != 0 && s.pendingProgressRevision == revision &&
		s.progressSyncServed && s.progressRevisionEchoed
}

func (s *sioSession) clearPendingProgressRevision(revision uint64) {
	s.mu.Lock()
	if revision == 0 || s.pendingProgressRevision == revision {
		s.pendingProgressRevision = 0
		s.progressSyncServed = false
		s.progressRevisionEchoed = false
	}
	s.mu.Unlock()
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
	case sioPlayerSuccessInitEvent:
		return (*Server).answerPlayerSuccessInit
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
	case sioCheckBindedGCEvent:
		return (*Server).answerCheckBindedGC
	case sioCheckVersionEvent:
		return (*Server).answerCheckVersion
	case sioPlayerVersionEvent:
		return (*Server).answerPlayerVersion
	case sioRegisterPushEvent:
		return (*Server).answerRegisterPush
	case sioInitProgressEvent:
		return (*Server).answerInitProgress
	case sioProgressSlotsEvent:
		return (*Server).answerProgressSlots
	case sioGetProgressEvent:
		return (*Server).answerGetProgress
	case sioGetProgressHashesEvent:
		return (*Server).answerGetProgressHashes
	case sioSyncFullEvent:
		return (*Server).answerSyncFull
	case sioUpdateProgressEvent:
		return (*Server).answerUpdateProgress
	case sioReplaceProgressEvent:
		return (*Server).answerReplaceProgress
	case sioLeaderboardV3Event:
		return (*Server).answerLeaderboardV3
	case sioGetInappsEvent:
		return (*Server).answerGetInapps
	case sioCreateSlotsEvent:
		return (*Server).answerCreateSlots
	case "send_squad_invite":
		return (*Server).answerSendSquadInvite
	case "squad_invites":
		return (*Server).answerSquadInvites
	case "friend_request":
		return socialActionAnswer("request", "whom")
	case "accept_friend":
		return socialActionAnswer("accept", "acceptee_id")
	case "reject_invite_friendship":
		return socialActionAnswer("reject", "rejectee_id")
	case "reject_friendship":
		return socialActionAnswer("remove", "rejectee_id")
	case "delete_friend_invites":
		return socialActionAnswer("clear", "")
	case "send_private_message":
		return (*Server).answerSendPrivateMessage
	case "get_private_message":
		return (*Server).answerGetPrivateMessages
	case "get_users_info_by_param_v3":
		return (*Server).answerSearchPlayers
	case "get_all_short_info_by_id_v3":
		return (*Server).answerShortPlayerInfo
	case "get_info_by_id_v3":
		return (*Server).answerNativePlayerInfo
	case "update_player_modes_stat":
		return (*Server).answerUpdatePlayerModesStat
	case "get_info_by_id", "get_info_by_id_v2":
		return (*Server).answerGetInfoByID
	case "update_player":
		return (*Server).answerUpdatePlayer
	case "update_accessories":
		return (*Server).answerUpdateAccessories
	case "update_loadout":
		return (*Server).answerUpdateLoadout
	case "update_character_target_ad_consent":
		return (*Server).answerUpdateTargetAdConsent
	case "refund_get":
		return (*Server).answerRefundGet
	case "get_time":
		return (*Server).answerGetTime
	case "check_binded_id":
		return (*Server).answerCheckBindedID
	case "mailbox_get_messages":
		return (*Server).answerMailboxMessages
	case "get_map_popularity":
		return (*Server).answerMapPopularity
	case "get_leader_clan":
		return (*Server).answerLeaderClan
	case "unsubscribe":
		return (*Server).answerUnsubscribe
	default:
		// Anything else gets the minimal acknowledgement rather than silence.
		//
		// Every request this client makes carries a req_id and parks a
		// TaskCompletionSource on it, and a request that is never completed is
		// what held the loading bar at 78% for so long. The client emits events
		// conditionally, so the set is not knowable up front - register_push and
		// full_check_version both turned up only after earlier steps started
		// working. An empty ok is the smallest answer that can release the wait,
		// and it is logged separately so it is visible which events are still
		// running on the fallback rather than a real handler.
		return func(s *Server, session *sioSession, head sioHead, reqID int) map[string]any {
			return s.answerGenericAck(event, session, head, reqID)
		}
	}
}

// sioOK is the base every reply shares: the correlation id the client matches
// on, plus the result spelling the retired HTTP contract used.
//
// Two consumers read `err_code` before anything else, and each threw when it
// was absent. Run 32 found the version checker index
// `info` on the way out; run 35 found the Google Play bind check index
// `social_id` the same way, at 0x3DCFC04:
//
//	ContainsKey(answer, "err_code") ? (int)answer["err_code"]
//	                        : answer["social_id"]  <- KeyNotFoundException
//
// The guard is the whole answer: a present err_code returns early and the
// consumer never reaches the key it does not have. The code is not put here
// on every reply, though - run 36 tried exactly that and the boot went quiet
// instead, so the answers that need it carry it themselves.
func sioOK(reqID int) map[string]any {
	return map[string]any{
		sioRequestIDKey: reqID,
		"result":        "ok",
		"error":         sioErrNone,
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
	// Kept for get_binded_ids, which is answered with id/hash pairs.
	session.setAuthHash(head.text("hash"))

	bootstrapID, remoteIP := session.bootstrapIdentity()
	prior, priorErr := s.st.Account(uniqID)
	matchedExistingDevice := priorErr == nil && device != "" && (prior.SocketDevice == device || (prior.SocketDevice == "" && prior.DeviceID == device))
	session.mu.Lock()
	session.mailboxOwnerFingerprint = ""
	nativeFingerprint := session.bootstrapOwnerFingerprint
	session.mu.Unlock()
	nativeProof := false
	nativeAccount, nativeErr := s.st.Account(bootstrapID)
	if nativeErr == nil && nativeFingerprint != "" {
		nativeProof = nativeFingerprint == webOwnerFingerprint(nativeAccount)
	}
	if bootstrapID != "" && nativeFingerprint != "" && !nativeProof {
		session.setIdentity(uniqID, device, "")
		body := sioDenied(reqID)
		body["err_code"] = sioErrNotAuthorized
		return body
	}

	if _, banned := s.st.MatchBan(uniqID, device, remoteIP); banned {
		session.setIdentity(uniqID, device, "")
		return socketBanReply(reqID)
	}
	if account, err := s.st.Account(bootstrapID); err == nil {
		if _, banned := s.accountBan(account, remoteIP); banned {
			session.setIdentity(uniqID, device, "")
			return socketBanReply(reqID)
		}
	}
	if account, err := s.st.Account(uniqID); err == nil {
		if _, banned := s.accountBan(account, remoteIP); banned {
			session.setIdentity(uniqID, device, "")
			return socketBanReply(reqID)
		}
	}

	var account store.Account
	var created bool
	var err error
	var reconciliation store.IdentityReconciliation
	if bootstrapID != "" {
		reconciliation, err = s.st.AdoptSocketIdentityForAccount(bootstrapID, uniqID, device, remoteIP, nativeAccount)
		account = reconciliation.Account
	} else {
		// The stock 23.1.3 client does not preserve custom query parameters on
		// its direct WebSocket handshake. Correlate only one unbound native
		// account recently seen on this address; zero or multiple candidates
		// are deliberately left untouched rather than risking a wrong merge.
		var candidates int
		reconciliation, candidates, err = s.st.ReconcileRecentHTTPIdentity(uniqID, device, remoteIP, 45*time.Second)
		if err == nil && candidates == 1 {
			account = reconciliation.Account
			bootstrapID = account.PlayerID
			s.log.Event("socket.bootstrap_identity_correlated", map[string]any{
				"sid": session.sid, "player_id": account.PlayerID, "ip": remoteIP,
				"note": "one recently seen HTTP account on this address had no socket binding",
			})
		} else if err == nil && candidates > 1 {
			s.log.Event("socket.bootstrap_identity_ambiguous", map[string]any{
				"sid": session.sid, "ip": remoteIP, "candidates": candidates,
				"note": "automatic reconciliation skipped; use the support merge operation",
			})
		}
		if err == nil && candidates != 1 {
			account, created, err = s.st.AdoptSocketIdentity(uniqID, device, remoteIP)
		}
	}
	if err == nil && reconciliation.Changed {
		s.log.Event("socket.identity_reconciled", map[string]any{
			"sid": session.sid, "canonical_player": account.PlayerID, "retired_alias": reconciliation.AliasAdded,
		})
	}
	if err != nil {
		s.log.Event("socket.identity_unbound", map[string]any{"sid": session.sid, "err": err.Error()})
		session.setIdentity(uniqID, device, "")
		body := sioDenied(reqID)
		body["err_code"] = sioErrNotAuthorized
		return body
	}

	if _, banned := s.accountBan(account, remoteIP); banned {
		session.setIdentity(uniqID, device, "")
		return socketBanReply(reqID)
	}
	session.setIdentity(uniqID, device, account.PlayerID)
	// Recent-IP correlation alone is deliberately NOT a mail credential.
	// A newly bound legacy socket must reconnect with its stored device proof.
	if nativeProof || matchedExistingDevice || created {
		session.mu.Lock()
		session.mailboxOwnerFingerprint = webOwnerFingerprint(account)
		if nativeProof {
			session.bootstrapOwnerFingerprint = webOwnerFingerprint(account)
		}
		session.mu.Unlock()
	}
	if nativeProof || matchedExistingDevice || created {
		if err := s.st.ObserveDevice(account, store.DeviceObservation{IP: remoteIP, Model: firstNonEmpty(head.text("device_model"), head.text("model")), OS: firstNonEmpty(head.text("os_version"), head.text("operating_system")), Platform: head.text("platform"), Version: head.text("app_version"), Source: "authenticated_game_client"}); err != nil {
			s.log.Event("support.evidence_write_failed", nil)
		}
	}
	s.retireOlderMapPresence(session)
	if profile, err := s.st.Profile(account.PlayerID); err == nil {
		session.setProgressWriteRevision(profile.ProgressRevision)
	}
	s.log.Event("socket.identity_bound", map[string]any{
		"sid":       session.sid,
		"player_id": account.PlayerID,
		"nick":      account.Nick,
		"device":    device,
		"created":   created,
	})

	if repaired, err := s.repairLegacyGameModeHubSlot(account.PlayerID); err != nil {
		s.log.Event("socket.progress_slot_repair_failed", map[string]any{
			"sid":       session.sid,
			"player_id": account.PlayerID,
			"slot":      gameModeHubProgressSlotID,
			"err":       err.Error(),
		})
	} else if repaired {
		s.log.Event("socket.progress_slot_repaired", map[string]any{
			"sid":       session.sid,
			"player_id": account.PlayerID,
			"slot":      gameModeHubProgressSlotID,
			"from":      "c=null,d=0",
			"to":        "c=[],d=20696",
			"note":      "version-specific repair applied only to the account authenticated on this socket",
		})
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

// The client's config system speaks three keys per document: "h" is the hash of
// the copy it already holds, "c" is the document body, and "v" is the document
// version. A poll carries "h" for every config the client has cached and asks
// this server to say which of those copies are stale.
const (
	sioConfigHashKey    = "h"
	sioConfigBodyKey    = "c"
	sioConfigVersionKey = "v"
)

// sioCachedConfigHash reads the hash the client reports for one polled config.
//
// An empty hash is a value, not a defect: it is the client saying it holds no
// usable copy of that document. Measured on 2026-08-29 in run 25, 86 of the
// 153 polled configs arrive that way, ClanCommon among them, and treating them
// as unreadable dropped all 86 names out of the answer: the client heard back
// about only the 67 documents it already had, logged Start wait config response
// and then waited for the rest forever.
//
// An entry that carries no hash key at all is still reported rather than
// guessed at, because acknowledging a hash that was never read would tell the
// client a lie about its own cache.
func sioCachedConfigHash(entry any) (string, bool) {
	fields, ok := entry.(map[string]any)
	if !ok {
		return "", false
	}
	if _, present := fields[sioConfigHashKey]; !present {
		return "", false
	}
	return sioHead(fields).text(sioConfigHashKey), true
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
// answer's job is to say which of them changed. Echoing a hash back asserts
// that the copy the client holds is current, and naming a config as changed
// without supplying a body would strand the client waiting for content that
// never arrives - so a config is named as changed only when this server can
// hand over the whole document in the same reply.
//
// Exactly one document is hosted that way today: CommandsWithFullSlots, the
// stale embedded copy of which is what held the loading bar at 79%. See
// slot_config.go for the device measurement that established its shape.
// Everything else is echoed, which is the truthful answer for a document this
// server does not host.
func (s *Server) answerSubscribe(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)

	if list, ok := head["list"].(map[string]any); ok {
		acknowledged := make(map[string]any, len(list))
		unreadable := 0
		uncached := 0
		served := 0
		playerLevel := s.sessionPlayerLevel(session)
		for name, entry := range list {
			hash, ok := sioCachedConfigHash(entry)
			if !ok {
				unreadable++
				continue
			}
			if hash == "" {
				uncached++
			}
			if reply, changed := s.hostedConfigReplyForLevel(name, hash, playerLevel); reply != nil {
				acknowledged[name] = reply
				if changed {
					served++
				}
				continue
			}
			acknowledged[name] = map[string]any{sioConfigHashKey: hash}
		}
		s.log.Event("socket.config_polled", map[string]any{
			"sid":          session.sid,
			"req_id":       reqID,
			"configs":      len(list),
			"acknowledged": len(acknowledged),
			"unreadable":   unreadable,
			"uncached":     uncached,
			"served":       served,
			"player_level": playerLevel,
			"note":         "every polled name is answered; an empty hash means the client holds no copy of that document, which is answered rather than dropped",
		})
		body["list"] = acknowledged
		return body
	}

	if topic := head.text("type"); topic != "" {
		if isSocialTopic(topic) {
			if failed := s.validateSocialSubscription(session, head, reqID); failed != nil {
				return failed
			}
		}
		if isMailboxTopic(topic) {
			if _, ok := s.socketPlayerID(session); !ok {
				return socketStateFailure(reqID, "not_authorized")
			}
		}
		body["type"] = topic
		if topic == "get_player_info_subscription" {
			body["status"] = "ok"
		}
	}
	return body
}

// sioProgressEnabledKey names the single integer the auth scene reads out of
// this answer. Key, meaning and polarity were all measured on a rooted device,
// and the polarity had to be measured in both directions, one run each, because
// reading the disassembly alone got it wrong twice.
//
// The reply handler at libil2cpp.so+0x17A9980 does this with the value:
//
//	ContainsKey(answer, "err_code")  -> present means failure, so it stays out
//	ContainsKey(answer, "enabled")   -> absent leaves the flag unset
//	Convert.ToInt32(answer["enabled"])
//	flag = (value == 0)             -> cset w1, eq at 0x17A9B40
//	statics+0x00 = Nullable<bool>(flag)  strh at 0x17A9BA4
//	statics+0x02 = (value == 2)          strb at 0x17A9BF0
//	statics+0x04 = Nullable<int>(value)  stur at 0x17A9C3C
//
// Run 142 sent zero with the flag traced live on the device. The flag did turn
// true, and eighty percent into the ladder the scene applied state 15,
// TechnicalWorks, from 0x3DC49D4, raised the information window and cleared the
// flag again through the reset writer at 0x17A9934. A true flag is how this
// build says "technical works", and zero is the code that produces it. Run 141
// sent one, the flag stayed false, and the ladder ran to its last step.
//
// The third code is not another polarity for that flag. Value two keeps the
// maintenance Nullable false and sets the distinct byte at statics+0x02. The
// outer auth iterator reads exactly that byte at 0x3DC78D4: false takes the
// create-slots path, while true starts TypeDefIndex 2029, whose stock
// continuation reaches SynchronizeProgress/state 9 and emits get_progress.
// The server therefore sends two only when it already owns progress worth
// synchronising; an empty profile remains the ordinary open code one.
//
// Two warnings for the next reader of the startup state machine:
//
//	0x3DC9854 does start the login coroutine when the flag is true, but that
//	arm is shared with the local multiplayer shortcut and it is the technical
//	works route. Reaching it is not reaching the main menu.
//	0x17A9934 looks unreferenced to a scan for direct calls. It is reached
//	through a delegate from 0x11EF5F0, and run 142 caught it running.
//
// The online route into Login is the other caller of the same coroutine factory
// at 0x3DC6214: the small method at 0x3DC70F8 starts it without consulting this
// flag at all. That is the path this server still has to earn.
const (
	sioProgressEnabledKey = "enabled"

	// All three numbers are client wire codes, not booleans. Maintenance wins
	// over the profile-derived mode so an account with stored progress still
	// sees TechnicalWorks while the operator switch is on.
	sioProgressOpen        = 1
	sioProgressSynchronize = 2
	sioProgressMaintenance = 0
)

// progressStatusCode maps the operator's maintenance switch onto the code the
// auth scene reads. It is the socket half of the flag the liveness endpoint
// already reports over HTTP: one switch, announced on both channels.
func (s *Server) progressStatusCode() int {
	if s.settings.snapshot().Maintenance {
		return sioProgressMaintenance
	}
	return sioProgressOpen
}

// profileNeedsSynchronization reports whether the server has any remote
// progress for the client to reconcile. These documents are deliberately kept
// separate in storage because they travel through different socket events, but
// any one of them means this is no longer a blank server-side profile.
func profileNeedsSynchronization(profile store.Profile) bool {
	return len(profile.Progress) != 0 ||
		len(profile.ProgressDocument) != 0 ||
		len(profile.ProgressSlots) != 0
}

// answerProgressStatus hands back the progress this server holds for the
// player. A fresh profile answers with an empty object, which is the truthful
// answer for an account that has never synchronised.
func (s *Server) answerProgressStatus(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body["progress"] = map[string]any{}

	profile, ok := s.sessionProfile(session)
	status := s.progressStatusCode()
	if status == sioProgressOpen && ok {
		_, device, _ := session.identity()
		_, operatorRevisionPending := profile.PendingProgressRevision(device)
		switch {
		case operatorRevisionPending:
			// Every pending revision uses the stock state-9 download. Login
			// after this session's full download is the durable boundary.
			status = sioProgressOpen
		case s.cfg.ProgressSynchronizeMode && profileNeedsSynchronization(profile):
			status = sioProgressSynchronize
		}
	}
	body[sioProgressEnabledKey] = status
	if !ok {
		return body
	}
	if profile.Progress != nil {
		body["progress"] = profile.Progress
	}
	// The flat counters have to agree with the document already in this reply:
	// the client reads both out of one object and cannot choose between them.
	// See store.Profile.Counters for the run that measured them disagreeing and
	// for how a blank client snapshot erased an operator edit because of it.
	level, experience, coins, gems := profile.Counters()
	body["level"] = level
	body["experience"] = experience
	body["coins"] = coins
	body["gems"] = gems
	// A stock lobby reconnect does not instantiate AuthSceneController. The
	// exact-build frontend bridge uses this authenticated, per-device pending
	// revision to re-enter the stock authorization scene on Unity's main thread.
	// Cold boot needs no bridge. Only correlated sync_full + Login clears it.
	_, device, _ := session.identity()
	if revision, pending := profile.PendingProgressRevision(device); pending {
		body["opg3d_admin_resync"] = profile.PlayerID + ":" + strconv.FormatUint(revision, 10)
	}
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

// answerLastCommand supplies the two strings the stock 23.1.3 handler actually
// reads. The state-8 wait at 0x3DC7584 tests whether the parsed index object is
// non-null, so omitting the key stalls forever even though an empty string is a
// valid local cursor. Both values stay empty: a non-empty mismatching device_id
// is the external-profile route that opens ChooseProgress (state 7). With this
// safe local shape, state 8 can finish and its stock callback starts the
// correlated state-9 sync_full request.
func (s *Server) answerLastCommand(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body["command"] = ""
	body["command_id"] = 0
	body["index"] = ""
	body["device_id"] = ""

	_, device, playerID := session.identity()
	if playerID == "" {
		return sioDenied(reqID)
	}
	profile, ok := s.sessionProfile(session)
	if !ok || device == "" {
		session.clearPendingProgressRevision(0)
		return body
	}
	revision, pending := profile.PendingProgressRevision(device)
	if !pending {
		session.clearPendingProgressRevision(0)
		return body
	}

	session.setPendingProgressRevision(revision)
	s.log.Event("socket.progress_revision_armed", map[string]any{
		"sid": session.sid, "player_id": playerID, "req_id": reqID,
		"revision": revision, "delivery_event": sioSyncFullEvent,
		"index": "", "device_id": "", "route": "state9_download",
		"note": "used a non-null empty local cursor without advertising a foreign device or ChooseProgress",
	})
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
	if _, ok := s.socketPlayerID(session); !ok {
		return sioDenied(reqID)
	}
	s.retireOlderMapPresence(session)
	session.mergeStockMapPresence(head)
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

// answerBindedIDs answers the client's request for the accounts bound to this
// player.
//
// The `ids` list is not what the client reads. Its parser for this answer looks
// for `err_code` first and treats any value of one or more as a failure, and
// only when that key is absent does it look for a `user_characters` dictionary
// and build the bound-account object out of it. An answer carrying neither key
// leaves that object unset, and the login scene waits on it forever - that wait
// is the stall at 79%.
//
// The dictionary is not a list of external logins either. The builder that
// turns it into that object was read out of the running client: it walks the
// dictionary's own `ids` list and, for every entry, takes exactly two values -
// `id` and `hash` - and adds that pair to a set. It then hands back the built
// object only if the set ended up with at least one member; otherwise it
// returns nothing at all and the login scene waits forever. An entry missing
// either half never reaches the set, which is why a list of {id, user_id}
// entries counted for nothing.
//
// The accurate answer is therefore this player themselves, named the way the
// builder reads it: this deployment gives every account exactly one identity,
// its own, so one entry carrying the id this socket authenticated as and the
// hash it authorised with is what the client is asking for. The outer `ids`
// list stays empty because no external identity is bound.
//
// The same builder also accepts `info`, `mobile_pay_shown`, `target_ad`,
// `inhouse_log_level`, `update_banner_version`, `hub_version` and
// `change_log_data`, each behind its own presence check. They are left out:
// this server has nothing true to say about them yet, and their absence only
// leaves the object's defaults in place.
func (s *Server) answerBindedIDs(session *sioSession, head sioHead, reqID int) map[string]any {
	_, _, playerID := session.identity()
	if playerID == "" {
		return sioDenied(reqID)
	}
	hash := session.identityHash()
	if hash == "" {
		// A first run authorises without a hash, so the id this server just
		// assigned is the only name it can give the identity. What the builder
		// requires is that both halves are there.
		hash = playerID
	}
	body := sioOK(reqID)
	body["ids"] = []any{}
	body["user_characters"] = map[string]any{
		"id":  playerID,
		"ids": []any{map[string]any{"id": playerID, "hash": hash}},
	}
	return body
}

// answerCheckBindedGC answers the Google Play bind check.
//
// The consumer at 0x3DCFC04 is not the error reader it looked like. Reading
// the whole method, rather than its first branch, shows what it really does:
//
//	if answer has err_code -> return (int)answer[err_code]   // and nothing else
//	else                   -> store answer[social_id] into the class static
//	                          at +0x30, look up a second key into +0x38,
//	                          return 0
//
// That static at +0x30 is exactly what the auth scene polls: the predicate at
// 0x3DC739C loads the same class, reads statics+0x30 and returns true when it
// is non-null. The client prints `Start wait info about Game Center` and waits
// for the store, so an error code answers the question and strands the boot -
// run 37 polled that predicate 2880 times and never got past it.
//
// So the bind check has to be answered with the field, not with a code. The
// empty string is the honest value: the store is what the scene waits for, and
// an empty id is how a client reads `no account is bound here`.
func (s *Server) answerCheckBindedGC(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body[sioSocialIDKey] = ""
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
	// The client reads this answer in UpdatesChecker directly: it takes
	// err_code when the key is present, and otherwise indexes head[info],
	// which throws KeyNotFoundException inside the socket event dispatch and
	// tears down the rest of that dispatch. Answering with an explicit zero
	// keeps the client on the branch that needs no update document.
	body["err_code"] = 0
	return body
}

// answerRegisterPush records what the client reports about itself.
//
// The event is nominally a push-token registration, but it carries the client's
// own view of the account - level, coins, gems, language, whether the player has
// paid - and is the first point at which this server learns any of it. That
// makes it the client half of profile synchronisation.
//
// The values are merged rather than assigned. This is the client reporting its
// state, not an operator setting it, and the client sends only the keys it
// currently cares about, so a full assignment would zero whatever it left out.
//
// SECURITY: the payload's own player_id is checked against the id this socket
// actually authenticated as. The client supplies that field itself, so taking
// it at face value would let any authenticated socket rewrite another player's
// profile.
func (s *Server) answerRegisterPush(session *sioSession, head sioHead, reqID int) map[string]any {
	_, _, playerID := session.identity()
	if playerID == "" {
		return sioDenied(reqID)
	}
	if claimed := head.text("player_id"); claimed != "" && claimed != playerID {
		s.log.Event("socket.profile_rejected", map[string]any{
			"sid":               session.sid,
			"session_player_id": playerID,
			"claimed_player_id": claimed,
			"note":              "a socket pushed a profile for an id it did not authenticate as",
		})
		return sioDenied(reqID)
	}

	progress := map[string]any{}
	for _, key := range []string{"level", "coins", "gems"} {
		if value, ok := head.number(key); ok {
			progress[key] = value
		}
	}
	prefs := map[string]string{}
	for _, key := range []string{"language", "app_version", "os_version", "device_token", "utc_shift", "allow_invites", "is_paying"} {
		if value := head.text(key); value != "" {
			prefs[key] = value
		}
	}

	if len(progress) > 0 {
		if _, err := s.st.MergeProfileProgress(playerID, progress, session.progressWriteContext()); err != nil {
			s.log.Event("socket.profile_unsaved", map[string]any{
				"player_id": playerID, "part": "progress", "err": err.Error(),
			})
		}
	}
	if len(prefs) > 0 {
		if _, err := s.st.MergeProfilePrefs(playerID, prefs, session.progressWriteContext()); err != nil {
			s.log.Event("socket.profile_unsaved", map[string]any{
				"player_id": playerID, "part": "prefs", "err": err.Error(),
			})
		}
	}
	s.log.Event("socket.profile_pushed", map[string]any{
		"sid":       session.sid,
		"player_id": playerID,
		"progress":  len(progress),
		"prefs":     len(prefs),
	})
	return sioOK(reqID)
}

// sioPlayerVersionKey is the only event-specific member the live 23.1.3
// callback reads from get_player_version. Run72 traced its exact ContainsKey
// call at FriendsController+0x1D8F740; when absent the callback returns before
// applying anything. The following cast and consumer both take System.String,
// so this member must remain a string rather than a numeric protocol version.
const sioPlayerVersionKey = "version"

// answerPlayerVersion returns the version belonging to the authorised profile.
// init_progress arrives earlier in the same boot and carries the client's own
// version_slot, so that stored value is preferred. A fresh or incomplete
// profile falls back to the one client build this backend currently serves.
func (s *Server) answerPlayerVersion(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	version := sioClientVersion
	if profile, ok := s.sessionProfile(session); ok {
		if stored, ok := profile.ProgressDocument["version_slot"].(string); ok && stored != "" {
			version = stored
		}
	}
	body[sioPlayerVersionKey] = version
	s.log.Event("socket.player_version_answered", map[string]any{
		"sid":     session.sid,
		"req_id":  reqID,
		"version": version,
		"note":    "version is a string because the stock FriendsController callback casts it to System.String",
	})
	return body
}

// answerGenericAck is the fallback for an event that has no handler of its own.
// It is logged under its own kind so it stays visible which events are running
// on the fallback instead of a real answer.
func (s *Server) answerGenericAck(event string, session *sioSession, head sioHead, reqID int) map[string]any {
	s.log.Event("socket.event_generic_ack", map[string]any{
		"event":  event,
		"sid":    session.sid,
		"req_id": reqID,
		"note":   "acknowledged by the fallback; no dedicated handler exists for this event yet",
	})
	return sioOK(reqID)
}

// sioDenied is the refusal the client's own error enum expects.
func sioDenied(reqID int) map[string]any {
	body := sioOK(reqID)
	body["result"] = "not_authorized"
	body["error"] = sioErrNotAuthorized
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
// frame group is reserved atomically; a full queue drops the entire event.
func (s *Server) replyEncrypted(session *sioSession, namespace, transport string, codec *sio.Codec, event string, reqID int, body map[string]any) bool {
	session.eventMu.Lock()
	defer session.eventMu.Unlock()
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

	if !session.sendFrames(frames) {
		s.log.Event("socket.reply_undelivered", map[string]any{
			"sid": session.sid, "event": event, "req_id": reqID,
			"frames": len(frames), "note": "send queue full; complete event dropped",
		})
		return false
	}

	sent, err := json.Marshal(body)
	if err != nil {
		// body already survived json.Marshal inside encodeEncryptedEvent.
		sent = nil
	}
	traceBody := s.log.Body(sent)
	if event == "get_private_message" || event == "send_private_message" {
		traceBody = "<private chat redacted>"
	}
	if event == "mailbox_get_messages" || event == mailboxPersonalUpdated {
		traceBody = "<personal mailbox body redacted>"
	}
	s.log.Event("socket.event_answered", map[string]any{
		"sid":         session.sid,
		"transport":   transport,
		"namespace":   namespace,
		"event":       event,
		"req_id":      reqID,
		"attachments": sioReplyAttachments,
		"mode":        codec.Mode().String(),
		"body":        traceBody,
	})
	return true
}

// sioProgressDocumentKey is the member of the init_progress reply the client
// casts to a dictionary and applies.
//
// Run 52 pinned the caller down and it is not the Progress method runs 50 to
// 52 assumed. The applier has exactly two callers; the return address its own
// device trace printed, libil2cpp.so+0x3E753B8, is the instruction after the
// call at 0x3E753B4 inside AuthSceneController's boot coroutine. That
// coroutine awaits this request, takes the response object the task produces,
// and hands the applier the object's member at +0x18 without checking it.
//
// So the name that matters is the one the client's socket layer turns into
// that member, and run 54 read it out of the client rather than guessing.
// The request keeps the document in a field of its own, filled in exactly one
// place: the reply reader at libil2cpp.so+0x17A8B40, which looks the answer up
// by a single key, casts it to Dictionary<string, object>, and keeps a null
// when the cast does not hold. A device probe named the two literals that
// reader loads:
//
//	key holding the progress document = "progress"
//	key holding the error code        = "err_code"
//
// The same probe listed the reply as the client saw it, "error" and "req_id"
// as doubles and the document member as a dictionary, so the shape was never
// the problem: "slots" and "result" were simply the wrong members. The client
// sends its own snapshot under this name too and expects the server's
// document back under it.
const sioProgressDocumentKey = "progress"

// sioProgressRejected is the client's own SlotNotMatches code. The server does
// not put it on the wire directly: the sync_full callback derives it from the
// measured _unknown_slots marker before dispatching the reason to AuthScene.
const sioProgressRejected = 1602

// answerInitProgress stores the boot-time progress snapshot and answers with
// the authoritative document the client applies slot by slot.
//
// Run 48 answered this event with the bare acknowledgement and the boot died
// on the spot, which is what holds the loading bar at 87%:
//
//	NullReferenceException
//	  at Progress.<applier> (System.Collections.Generic.Dictionary`2[TKey,TValue])
//	  at AuthSceneController+<boot coroutine>[T].MoveNext ()
//
// The coroutine reads one member of the response object and passes it straight
// to the applier, which throws on null at its first instruction, 0x2701904.
// The applier is therefore entered whether that member is missing or merely
// the wrong type, and a device trace confirmed it arrives null while the reply
// itself decodes intact.
//
// The applier enumerates the document and parses each key as an integer slot
// id, with a second path for the names that are not numeric, which is how the
// client's own version_slot entry travels. That is the shape the client sent
// up, so the stored snapshot goes back as it came instead of being reshaped
// into a schema this server invented.
func (s *Server) answerInitProgress(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	// An empty document is still a document: it survives the cast, so the
	// applier runs to a no-op instead of dereferencing null. Every early
	// return below leaves this in place for that reason.
	body[sioProgressDocumentKey] = map[string]any{}

	_, _, playerID := session.identity()
	if playerID == "" {
		s.log.Event("socket.progress_unowned", map[string]any{
			"sid":  session.sid,
			"note": "init_progress arrived before auth_v2 bound an account; answered with an empty document",
		})
		return body
	}

	// The client uploads under the same member it reads the answer from.
	snapshot, _ := head[sioProgressDocumentKey].(map[string]any)
	if len(snapshot) > 0 {
		if _, err := s.st.MergeProfileProgressDocument(playerID, snapshot, session.progressWriteContext()); err != nil {
			// The client is waiting on a document, not on this server's
			// bookkeeping, so a failed write is recorded and the reply still
			// goes out with whatever was stored before.
			s.log.Event("socket.profile_unsaved", map[string]any{
				"player_id": playerID,
				"part":      "progress_document",
				"err":       err.Error(),
			})
		}
	}

	if profile, ok := s.sessionProfile(session); ok && len(profile.ProgressDocument) > 0 {
		body[sioProgressDocumentKey] = profile.ProgressDocument
	}

	document, _ := body[sioProgressDocumentKey].(map[string]any)
	// Section 56 is the tutorials section the boot gates on. It travels with
	// the rest of the document because the applier enumerates every member and
	// parses its key as a section id, so an authored section needs no separate
	// channel. See socketio_tutorial_section.go for why this server authors it.
	// Run 108 measured that a tutorials section in this document never reaches
	// the client's tutorial manager, so this channel stays behind a switch and
	// the create_slots acknowledgement carries it instead.
	tutorialSeeded := false
	if s.cfg.TutorialSectionViaProgressDocument {
		tutorialSeeded = s.applyTutorialSection(document)
	}
	s.log.Event("socket.progress_initialised", map[string]any{
		"sid":                     session.sid,
		"player_id":               playerID,
		"received":                len(snapshot),
		"answered":                len(document),
		"tutorial_section_seeded": tutorialSeeded,
		"hash":                    head.text("hash"),
		"note":                    "the document travels in progress, the member the reply reader casts to a dictionary before the boot coroutine applies it",
	})
	return body
}
