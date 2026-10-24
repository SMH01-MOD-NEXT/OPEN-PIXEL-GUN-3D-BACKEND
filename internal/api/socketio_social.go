// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

// Read-only stock 23.1.3 evidence: friend_request.whom 0x235F438,
// accept_friend.acceptee_id 0x235D7B4, reject*.rejectee_id 0x2360AAC /
// 0x235C60C. List consumers: 0x3884654 / 0x235E608 / 0x235F94C.
func isSocialTopic(t string) bool {
	return t == "squad_invites" || t == "friends_list" || t == "friend_invites" || t == "get_private_message" || t == "all_short_info_by_id_v3"
}
func isSocialMutation(t string) bool {
	switch t {
	case "send_squad_invite", "friend_request", "accept_friend", "reject_invite_friendship", "delete_friend_invites", "reject_friendship", "send_private_message":
		return true
	}
	return false
}
func socialFailure(req int, err error) map[string]any {
	code := "store_failed"
	switch {
	case errors.Is(err, store.ErrSocialInvalid):
		code = "invalid_social_request"
	case errors.Is(err, store.ErrSocialForbidden):
		code = "not_friends_or_no_invite"
	case errors.Is(err, store.ErrSocialRate):
		code = "rate_limited"
	case errors.Is(err, store.ErrSocialFull):
		code = "social_limit"
	case errors.Is(err, store.ErrNotFound):
		code = "not_found"
	}
	return socketStateFailure(req, code)
}
func socialActionAnswer(action, key string) sioAnswer {
	return func(s *Server, session *sioSession, head sioHead, req int) map[string]any {
		actor, ok := s.socketPlayerID(session)
		if !ok {
			return socketStateFailure(req, "not_authorized")
		}
		target := head.text(key)
		if key != "" && !store.ValidPlayerID(strings.TrimSpace(target)) {
			return socialFailure(req, store.ErrSocialInvalid)
		}
		id, err := s.st.SocialAction(actor, target, action)
		if err != nil {
			return socialFailure(req, err)
		}
		body := sioOK(req)
		if key != "" {
			body[key] = id
		}
		return body
	}
}
func (s *Server) answerSearchPlayers(session *sioSession, head sioHead, req int) map[string]any {
	actor, ok := s.socketPlayerID(session)
	if !ok {
		return socketStateFailure(req, "not_authorized")
	}
	ids, err := s.st.SearchPlayers(actor, head.text("param"))
	if err != nil {
		return socialFailure(req, err)
	}
	body := sioOK(req)
	players := []any{}
	for _, id := range ids {
		if info, ok := s.nativePlayerInfo(id); ok {
			info["id"] = id
			players = append(players, info)
		}
	}
	body["players"] = players
	return body
}
func socialNumber(value any) (float64, bool) {
	var n float64
	var err error
	switch v := value.(type) {
	case nil:
		return 0, true
	case float64:
		n = v
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case string:
		n, err = strconv.ParseFloat(v, 64)
	default:
		return 0, false
	}
	return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 1e12
}
func (s *Server) answerSendPrivateMessage(session *sioSession, head sioHead, req int) map[string]any {
	actor, ok := s.socketPlayerID(session)
	if !ok {
		return socketStateFailure(req, "not_authorized")
	}
	// Clan messaging is a separate permission model, never silently treat it as DM.
	if clan, exists := head["clan"]; exists && clan != false {
		return socialFailure(req, store.ErrSocialInvalid)
	}
	text, ok := head["text"].(string)
	stamp, valid := socialNumber(head["timeStamp"])
	if !ok || !valid || head["timeStamp"] == nil {
		return socialFailure(req, store.ErrSocialInvalid)
	}
	token := strconv.FormatFloat(stamp, 'f', -1, 64)
	m, err := s.st.SendPrivateMessage(actor, head.text("to"), text, token)
	if err != nil {
		return socialFailure(req, err)
	}
	body := sioOK(req)
	body["timeStamp"] = float64(m.Stamp) / 1e6
	return body
}
func socialPeers(head sioHead) ([]string, bool) {
	values, ok := head["list"].([]any)
	if !ok || len(values) > store.SocialFriendLimit {
		return nil, false
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, v := range values {
		id, ok := v.(string)
		if !ok {
			return nil, false
		}
		if id == "-1" {
			continue
		}
		if !store.ValidPlayerID(id) {
			return nil, false
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids, true
}

// 0x401431C requires messages to be a LIST of DICTIONARIES keyed by numeric
// timestamp strings, each value containing from/text/clan (not a flat list).
func (s *Server) privateMessagesBody(actor string, peers []string, after float64) (map[string]any, float64, error) {
	messages, err := s.st.PrivateInbox(actor, peers, after)
	if err != nil {
		return nil, after, err
	}
	rows := []any{}
	if len(messages) > 100 {
		messages = messages[len(messages)-100:]
	}
	for _, m := range messages {
		stamp := float64(m.Stamp) / 1e6
		rows = append(rows, map[string]any{strconv.FormatFloat(stamp, 'f', 6, 64): map[string]any{"from": m.From, "text": m.Text, "clan": false}})
		if stamp > after {
			after = stamp
		}
	}
	return map[string]any{"messages": rows}, after, nil
}
func (s *Server) answerGetPrivateMessages(session *sioSession, head sioHead, req int) map[string]any {
	actor, ok := s.socketPlayerID(session)
	if !ok {
		return socketStateFailure(req, "not_authorized")
	}
	peers, ok := socialPeers(head)
	after, valid := socialNumber(head["last_timeStamp"])
	if !ok || !valid {
		return socialFailure(req, store.ErrSocialInvalid)
	}
	body, _, err := s.privateMessagesBody(actor, peers, after)
	if err != nil {
		return socialFailure(req, err)
	}
	body[sioRequestIDKey] = req
	return body
}

type socialSubscription struct {
	owner, namespace, signature string
	codec                       *sio.Codec
	peers                       []string
	after                       float64
	friends                     []string
	initialized                 bool
}

// socialMu serializes subscribe/unsubscribe and pushes; never acquire it while
// holding store.mu or session.mu. No network I/O while holding the hub lock.
type socialSubscriptions struct {
	sync.Mutex
	topics map[string]*socialSubscription
}

func (s *Server) validateSocialSubscription(session *sioSession, head sioHead, req int) map[string]any {
	if _, ok := s.socketPlayerID(session); !ok {
		return socketStateFailure(req, "not_authorized")
	}
	if head.text("type") == "get_private_message" {
		peers, ok := socialPeers(head)
		_, valid := socialNumber(head["last_timeStamp"])
		if !ok || !valid {
			return socialFailure(req, store.ErrSocialInvalid)
		}
		session.social.Lock()
		existing := session.social.topics["get_private_message"]
		if existing != nil {
			for _, id := range existing.peers {
				if !hasSocialID(peers, id) {
					peers = append(peers, id)
				}
			}
		}
		session.social.Unlock()
		if len(peers) > store.SocialFriendLimit {
			return socialFailure(req, store.ErrSocialFull)
		}
	}
	if head.text("type") == "all_short_info_by_id_v3" {
		if _, ok := socialProfileIDs(head); !ok {
			return socialFailure(req, store.ErrSocialInvalid)
		}
	}
	return nil
}
func (s *Server) pushSocialSubscription(session *sioSession, namespace, transport string, codec *sio.Codec, head sioHead) {
	topic := head.text("type")
	if !isSocialTopic(topic) {
		return
	}
	actor, ok := s.socketPlayerID(session)
	if !ok || s.validateSocialSubscription(session, head, 0) != nil {
		return
	}
	session.social.Lock()
	defer session.social.Unlock()
	if topic == "get_private_message" {
		peers, _ := socialPeers(head)
		if len(peers) == 0 && session.social.topics[topic] != nil {
			return
		} // stock clan-only subscription must not erase DMs
	}
	if session.social.topics == nil {
		session.social.topics = map[string]*socialSubscription{}
	}
	sub := &socialSubscription{owner: actor, namespace: namespace, codec: codec}
	if topic == "get_private_message" {
		sub.peers, _ = socialPeers(head)
		if existing := session.social.topics[topic]; existing != nil && existing.owner == actor {
			for _, id := range existing.peers {
				if !hasSocialID(sub.peers, id) {
					sub.peers = append(sub.peers, id)
				}
			}
			if len(sub.peers) > store.SocialFriendLimit {
				return
			}
		}
		sub.after, _ = socialNumber(head["last_timeStamp"])
	}
	if topic == "all_short_info_by_id_v3" {
		sub.peers, _ = socialProfileIDs(head)
	}
	session.social.topics[topic] = sub
	s.pushSocialLocked(session, topic, sub, transport)
}
func hasSocialID(ids []string, id string) bool {
	for _, v := range ids {
		if id == v {
			return true
		}
	}
	return false
}
func (s *Server) pushSocialLocked(session *sioSession, topic string, sub *socialSubscription, transport string) {
	actor, ok := s.socketPlayerID(session)
	if !ok || actor != sub.owner {
		return
	}
	var body map[string]any
	var friends []string
	after := sub.after
	switch topic {
	case "friends_list", "friend_invites":
		snap, err := s.st.SocialSnapshot(actor)
		if err != nil {
			return
		}
		if topic == "friends_list" {
			body = map[string]any{"friends": snap.Friends}
			friends = snap.Friends
		} else {
			body = map[string]any{"incoming": snap.Incoming, "outcoming": snap.Outgoing, "delta": 0}
		}
	case "get_private_message":
		var err error
		body, after, err = s.privateMessagesBody(actor, sub.peers, sub.after)
		if err != nil {
			return
		}
		if sub.initialized && len(body["messages"].([]any)) == 0 {
			return
		}
	case "squad_invites":
		body = s.squadInvitesBody(actor, time.Now())
	case "all_short_info_by_id_v3":
		body = map[string]any{"info": s.socialProfileMap(sub.peers)}
	default:
		return
	}
	raw, _ := json.Marshal(body)
	if sub.initialized && string(raw) == sub.signature && topic != "get_private_message" {
		return
	}
	if topic == "friends_list" && sub.initialized {
		for _, id := range sub.friends {
			if !hasSocialID(friends, id) {
				if !s.replyEncrypted(session, sub.namespace, transport, sub.codec, "friend_removed", 0, map[string]any{"id": id}) {
					return
				}
			}
		}
		for _, id := range friends {
			if !hasSocialID(sub.friends, id) {
				if !s.replyEncrypted(session, sub.namespace, transport, sub.codec, "friend_added", 0, map[string]any{"id": id}) {
					return
				}
			}
		}
	}
	if s.replyEncrypted(session, sub.namespace, transport, sub.codec, topic, 0, body) {
		sub.signature = string(raw)
		sub.initialized = true
		sub.after = after
		sub.friends = friends
	}
}
func (s *Server) refreshSocial(session *sioSession) {
	session.social.Lock()
	defer session.social.Unlock()
	for topic, sub := range session.social.topics {
		s.pushSocialLocked(session, topic, sub, "social_update")
	}
}
func (s *Server) refreshSocialAll() {
	s.sio.mu.Lock()
	sessions := make([]*sioSession, 0, len(s.sio.sessions))
	for _, v := range s.sio.sessions {
		sessions = append(sessions, v)
	}
	s.sio.mu.Unlock()
	for _, session := range sessions {
		s.refreshSocial(session)
	}
}
func (s *Server) unsubscribeSocial(session *sioSession, head sioHead) {
	if session == nil {
		return
	}
	session.social.Lock()
	defer session.social.Unlock()
	topic := head.text("type")
	sub := session.social.topics[topic]
	if sub == nil {
		return
	}
	if topic == "get_private_message" {
		if ids, ok := socialPeers(head); ok {
			if len(ids) == 0 {
				if values, yes := head["list"].([]any); yes && len(values) > 0 {
					return
				}
			}
			peers := []string{}
			for _, id := range sub.peers {
				if !hasSocialID(ids, id) {
					peers = append(peers, id)
				}
			}
			sub.peers = peers
			if len(peers) > 0 {
				return
			}
		}
	}
	delete(session.social.topics, topic)
}

// Bulk profile callback 0x2500D28: {info:{playerID:PlayerData,...}}.
func socialProfileIDs(head sioHead) ([]string, bool) {
	return socialProfileIDsKey(head, "list_v2")
}

func socialProfileIDsKey(head sioHead, key string) ([]string, bool) {
	raw, ok := head[key].([]any)
	if !ok || len(raw) > 100 {
		return nil, false
	}
	ids := []string{}
	for _, v := range raw {
		entry, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		id, ok := entry["id"].(string)
		if !ok || !store.ValidPlayerID(id) {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}
func (s *Server) socialProfileMap(ids []string) map[string]any {
	out := map[string]any{}
	for _, id := range ids {
		if info, ok := s.nativePlayerInfo(id); ok {
			out[id] = info
		}
	}
	return out
}
func (s *Server) answerShortPlayerInfo(session *sioSession, head sioHead, req int) map[string]any {
	if _, ok := s.socketPlayerID(session); !ok {
		return socketStateFailure(req, "not_authorized")
	}
	// Direct request 0x24FFA88 maps IDs through 0x2507314 into ids_v2
	// dictionaries; the subscription uses the separate list_v2 key.
	ids, ok := socialProfileIDsKey(head, "ids_v2")
	if !ok {
		return socialFailure(req, store.ErrSocialInvalid)
	}
	body := sioOK(req)
	body["info"] = s.socialProfileMap(ids)
	return body
}
