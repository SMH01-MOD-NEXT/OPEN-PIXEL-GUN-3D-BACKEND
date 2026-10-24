// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

func TestFriendNicknamePresenceEncryptedRefresh(t *testing.T) {
	s, a, b, c, codec := socialAPIFixture(t)
	squadFriends(t, s, a, b)
	for _, session := range []*sioSession{a, b, c} {
		session.touch()
	}
	socialDispatch(t, s, a, "subscribe", sioHead{"req_id": 1, "type": "player_online", "list": []any{b.playerID, c.playerID}})
	body := socialEvent(t, socialFrames(t, a, codec), "player_online")
	if body[b.playerID] == nil || body[c.playerID] != nil || body["req_id"] != nil {
		t.Fatal("native presence map or privacy", body)
	}
	// Native subscriptions add peers incrementally, not replace the old set.
	for i, id := range []string{b.playerID, c.playerID} {
		socialDispatch(t, s, a, "subscribe", sioHead{"req_id": i + 2, "type": "all_short_info_by_id_v3", "list_v2": []any{map[string]any{"id": id}}})
		socialFrames(t, a, codec)
	}
	socialDispatch(t, s, b, "update_player", sioHead{"req_id": 4, "nick": "Updated friend", "player_id": c.playerID})
	socialFrames(t, b, codec)
	info := socialEvent(t, socialFrames(t, a, codec), "all_short_info_by_id_v3")["info"].(map[string]any)
	if info[b.playerID].(map[string]any)["n"] != "Updated friend" || info[c.playerID] == nil {
		t.Fatal("nickname subscription lost or spoofed")
	}
	socialDispatch(t, s, b, "update_player_online", sioHead{"req_id": 5, "game_mode": "110002", "protocol": "16.6.0mm_v2", "map": "1001", "rank": "invalid", "room_name": "friend-room", "device_id": "PRIVATE-DEVICE", "hash": "PRIVATE-HASH", "ip": "PRIVATE-IP"})
	socialFrames(t, b, codec)
	events := socialFrames(t, a, codec)
	online := socialEvent(t, events, "player_online")[b.playerID].(map[string]any)
	if online["game_mode"] != "110002" || online["protocol"] != "16.6.0mm_v2" || online["rank"] != "0" {
		t.Fatal("room presence missing", online)
	}
	payloads := make([]map[string]any, 0, len(events))
	for _, event := range events {
		payloads = append(payloads, event.body)
	}
	raw, _ := json.Marshal(payloads)
	if strings.Contains(string(raw), "PRIVATE-") {
		t.Fatal("presence leaked private data")
	}
	account, _ := s.st.Account(b.playerID)
	if account.GameLastSeenAt.IsZero() {
		t.Fatal("game activity not recorded")
	}
	before := account.GameLastSeenAt.Unix()
	// Clean closure pushes a null tombstone immediately and retains last activity.
	s.handleEnginePacket(b, string(eioClose), "test")
	events = socialFrames(t, a, codec)
	if socialEvent(t, events, "player_online")[b.playerID] != nil {
		t.Fatal("offline cache not cleared")
	}
	// No duplicate short-profile push is needed when its timestamp is unchanged.
	profile, ok := s.nativePlayerInfo(b.playerID)
	if !ok {
		t.Fatal("offline profile missing")
	}
	if got := profile["ldt"].(int64); got < before || got > time.Now().Unix() {
		t.Fatal("invalid last-seen seconds", got)
	}
	s.refreshSocialAll()
	if len(a.out) != 0 {
		t.Fatal("unchanged state repushed")
	}
	// A fresh game connection restores online, an old socket stays retired.
	fresh := authorisedSession("new-b", b.playerID)
	fresh.created = time.Now()
	fresh.touch()
	fresh.out = make(chan sioFrame, 128)
	s.sio.mu.Lock()
	s.sio.sessions[fresh.sid] = fresh
	s.sio.mu.Unlock()
	s.refreshSocialAll()
	if socialEvent(t, socialFrames(t, a, codec), "player_online")[b.playerID] == nil {
		t.Fatal("reconnect not online")
	}
	fresh.mu.Lock()
	fresh.lastSeen = time.Now().Add(-stockMapPresenceLiveness - time.Second)
	fresh.mu.Unlock()
	s.refreshSocialAll()
	if socialEvent(t, socialFrames(t, a, codec), "player_online")[b.playerID] != nil {
		t.Fatal("stale socket remained online")
	}
	// Presence permission is re-evaluated, not captured at subscribe time.
	fresh.touch()
	if _, err := s.st.SocialAction(a.playerID, b.playerID, "remove"); err != nil {
		t.Fatal(err)
	}
	s.refreshSocialAll()
	socialFrames(t, a, codec)
	if s.friendsOnlineBody(a.playerID, []string{b.playerID})[b.playerID] != nil {
		t.Fatal("former friend location disclosed")
	}
}

func TestFriendPresenceSubscriptionValidationAndPartialUnsubscribe(t *testing.T) {
	s, a, b, c, codec := socialAPIFixture(t)
	squadFriends(t, s, a, b)
	squadFriends(t, s, a, c)
	b.touch()
	c.touch()
	if s.answerSubscribe(&sioSession{}, sioHead{"type": "player_online", "list": []any{b.playerID}}, 1)["err_code"] == nil {
		t.Fatal("anonymous subscription")
	}
	for _, list := range []any{[]any{42}, []any{"bad"}, "bad"} {
		if s.answerSubscribe(a, sioHead{"type": "player_online", "list": list}, 1)["err_code"] == nil {
			t.Fatal("invalid peers accepted")
		}
	}
	for _, id := range []string{b.playerID, c.playerID} {
		s.pushSocialSubscription(a, "/sio", "test", codec, sioHead{"type": "player_online", "list": []any{id}})
		socialFrames(t, a, codec)
	}
	s.answerUnsubscribe(a, sioHead{"type": "player_online", "list": []any{b.playerID}}, 2)
	a.social.Lock()
	peers := append([]string{}, a.social.topics["player_online"].peers...)
	a.social.Unlock()
	if len(peers) != 1 || peers[0] != c.playerID {
		t.Fatal("partial unsubscribe lost other peer", peers)
	}
	if err := s.st.AddBan(store.Ban{Kind: store.BanPlayer, Subject: c.playerID}); err != nil {
		t.Fatal(err)
	}
	s.refreshSocialAll()
	if socialEvent(t, socialFrames(t, a, codec), "player_online")[c.playerID] != nil {
		t.Fatal("banned socket shown online")
	}
}

func TestFriendPresenceSafeDefaultsAndRetiredSockets(t *testing.T) {
	s, _, b, _, _ := socialAPIFixture(t)
	now := time.Now()
	if s.friendPresence(b.playerID, now) != nil {
		t.Fatal("unobserved socket online")
	}
	b.touch()
	b.mergeStockMapPresence(sioHead{"game_mode": "not-an-integer", "room_name": "must-not-leak"})
	info := s.friendPresence(b.playerID, time.Now())
	if info["game_mode"] != "100006" || info["protocol"] != "" || info["room_name"] != "" {
		t.Fatal("unsafe native parser defaults", info)
	}
	b.mu.Lock()
	b.lastSeen = time.Now().Add(time.Hour)
	b.mu.Unlock()
	if s.friendPresence(b.playerID, time.Now()) != nil {
		t.Fatal("future activity online")
	}
	b.touch()
	fresh := authorisedSession("replacement-b", b.playerID)
	fresh.created = time.Now()
	fresh.touch()
	s.sio.mu.Lock()
	s.sio.sessions[fresh.sid] = fresh
	s.sio.mu.Unlock()
	s.retireOlderMapPresence(fresh)
	s.endSocketGamePresence(fresh)
	b.touch()
	if s.friendPresence(b.playerID, time.Now()) != nil {
		t.Fatal("retired connection resurrected")
	}
}

func TestFriendSubscriptionBoundsAndShortPartialUnsubscribe(t *testing.T) {
	s, a, b, c, codec := socialAPIFixture(t)
	for _, id := range []string{b.playerID, c.playerID} {
		s.pushSocialSubscription(a, "/sio", "test", codec, sioHead{"type": "all_short_info_by_id_v3", "list_v2": []any{map[string]any{"id": id}}})
		socialFrames(t, a, codec)
	}
	s.answerUnsubscribe(a, sioHead{"type": "all_short_info_by_id_v3", "list_v2": []any{map[string]any{"id": b.playerID}}}, 1)
	a.social.Lock()
	peers := append([]string{}, a.social.topics["all_short_info_by_id_v3"].peers...)
	a.social.Unlock()
	if len(peers) != 1 || peers[0] != c.playerID {
		t.Fatal("short unsubscribe lost peer", peers)
	}
	list := []any{}
	profiles := []any{}
	for i := 0; i < store.SocialFriendLimit; i++ {
		id := fmt.Sprint(500000000 + i)
		list = append(list, id)
		profiles = append(profiles, map[string]any{"id": id})
	}
	for _, topic := range []string{"player_online", "all_short_info_by_id_v3"} {
		head := sioHead{"type": topic, "list": list, "list_v2": profiles}
		// Clear the earlier short subscription before filling to the exact limit.
		s.answerUnsubscribe(a, sioHead{"type": topic}, 2)
		if err := s.validateSocialSubscription(a, head, 3); err != nil {
			t.Fatal("valid bounded subscription rejected", err)
		}
		s.pushSocialSubscription(a, "/sio", "test", codec, head)
		socialFrames(t, a, codec)
		extra := sioHead{"type": topic, "list": []any{b.playerID}, "list_v2": []any{map[string]any{"id": b.playerID}}}
		if err := s.validateSocialSubscription(a, extra, 4); err == nil {
			t.Fatal("incremental subscription exceeded limit")
		}
		s.answerUnsubscribe(a, sioHead{"type": topic}, 5)
		a.social.Lock()
		_, exists := a.social.topics[topic]
		a.social.Unlock()
		if exists {
			t.Fatal("full unsubscribe retained topic")
		}
	}
}
