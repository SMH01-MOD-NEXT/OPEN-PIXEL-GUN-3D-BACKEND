// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

type socialWire struct {
	event string
	body  map[string]any
}

func socialFrames(t *testing.T, session *sioSession, codec *sio.Codec) []socialWire {
	t.Helper()
	out := []socialWire{}
	for len(session.out) > 0 {
		header := <-session.out
		if header.binary || !strings.HasPrefix(header.text, "452-/sio,") {
			t.Fatal("bad frame group", header.text)
		}
		name, _ := stripBinaryPrefix((<-session.out).data)
		arg, _ := stripBinaryPrefix((<-session.out).data)
		pn, err := codec.Decrypt(name)
		if err != nil {
			t.Fatal(err)
		}
		pa, err := codec.Decrypt(arg)
		if err != nil {
			t.Fatal(err)
		}
		var event string
		var body map[string]any
		if json.Unmarshal(pn, &event) != nil || json.Unmarshal(pa, &body) != nil {
			t.Fatal("invalid event")
		}
		out = append(out, socialWire{event, body})
	}
	return out
}
func socialEvent(t *testing.T, events []socialWire, name string) map[string]any {
	t.Helper()
	for _, e := range events {
		if e.event == name {
			return e.body
		}
	}
	t.Fatal("missing event", name, events)
	return nil
}
func socialDispatch(t *testing.T, s *Server, session *sioSession, event string, head sioHead) {
	t.Helper()
	raw, _ := json.Marshal(head)
	codec := s.sio.codec
	s.decodeBinaryEvent(session, &sioBinaryAssembly{namespace: "/sio", transport: "test", attachments: []sioAttachment{{data: codec.Encrypt([]byte(event))}, {data: codec.Encrypt(raw)}}})
}
func socialAPIFixture(t *testing.T) (*Server, *sioSession, *sioSession, *sioSession, *sio.Codec) {
	t.Helper()
	s, cfg := newTestServer(t)
	cfg.SioReply = true
	codec := codecForPinnedSid(t)
	s.sio.codec = codec
	ids := []string{"111111111", "222222222", "333333333"}
	sessions := []*sioSession{}
	for _, id := range ids {
		seedSocketAccount(t, s, id, "device-"+id)
		session := authorisedSession(id, id)
		session.out = make(chan sioFrame, 128)
		sessions = append(sessions, session)
		s.sio.sessions[id] = session
	}
	return s, sessions[0], sessions[1], sessions[2], codec
}
func TestSocialEncryptedTwoPlayerLifecycle(t *testing.T) {
	s, a, b, c, codec := socialAPIFixture(t)
	for _, session := range []*sioSession{a, b} {
		for _, topic := range []string{"friends_list", "friend_invites"} {
			socialDispatch(t, s, session, "subscribe", sioHead{"req_id": float64(1), "type": topic})
			events := socialFrames(t, session, codec)
			ack := socialEvent(t, events, "subscribe")
			if ack["err_code"] != nil {
				t.Fatal(ack)
			}
			push := socialEvent(t, events, topic)
			if push["req_id"] != nil {
				t.Fatal("uncorrelated push has req_id")
			}
			if topic == "friend_invites" && push["delta"] != float64(0) {
				t.Fatal("full replacement required", push)
			}
		}
	}
	socialDispatch(t, s, a, "friend_request", sioHead{"req_id": float64(2), "whom": b.playerID, "player_id": c.playerID, "from": c.playerID})
	events := socialFrames(t, b, codec)
	incoming := socialEvent(t, events, "friend_invites")["incoming"]
	if !reflect.DeepEqual(incoming, []any{a.playerID}) {
		t.Fatal("spoofed actor", incoming)
	}
	socialFrames(t, a, codec)
	socialDispatch(t, s, b, "accept_friend", sioHead{"req_id": float64(3), "acceptee_id": a.playerID})
	for _, session := range []*sioSession{a, b} {
		events = socialFrames(t, session, codec)
		added := socialEvent(t, events, "friend_added")
		peer := a.playerID
		if session == a {
			peer = b.playerID
		}
		if added["id"] != peer {
			t.Fatal(added)
		}
		socialEvent(t, events, "friends_list")
	}
	socialDispatch(t, s, b, "subscribe", sioHead{"req_id": float64(4), "type": "get_private_message", "list": []any{a.playerID}, "last_timeStamp": float64(0)})
	socialFrames(t, b, codec)
	const text = "PRIVATE-CONTENT-must-not-appear-in-logs"
	socialDispatch(t, s, a, "send_private_message", sioHead{"req_id": float64(5), "to": b.playerID, "from": c.playerID, "text": text, "timeStamp": float64(100), "clan": false})
	ack := socialEvent(t, socialFrames(t, a, codec), "send_private_message")
	if ack["err_code"] != nil || ack["timeStamp"] == nil {
		t.Fatal(ack)
	}
	body := socialEvent(t, socialFrames(t, b, codec), "get_private_message")
	rows := body["messages"].([]any)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	for key, value := range rows[0].(map[string]any) {
		if _, ok := socialNumber(key); !ok {
			t.Fatal("non timestamp key")
		}
		m := value.(map[string]any)
		if m["from"] != a.playerID || m["text"] != text || m["clan"] != false {
			t.Fatal(m)
		}
	}
	if len(c.out) != 0 {
		t.Fatal("third party received DM")
	}
	for _, entry := range s.log.Recent(0) {
		if strings.Contains(entry.Line, text) {
			t.Fatal("plaintext chat in diagnostic log")
		}
	}
	socialDispatch(t, s, a, "send_private_message", sioHead{"req_id": float64(6), "to": b.playerID, "text": text, "timeStamp": float64(100), "clan": false})
	socialFrames(t, a, codec)
	if len(b.out) != 0 {
		t.Fatal("retry repushed same message")
	}
	socialDispatch(t, s, a, "reject_friendship", sioHead{"req_id": float64(7), "rejectee_id": b.playerID})
	for _, session := range []*sioSession{a, b} {
		socialEvent(t, socialFrames(t, session, codec), "friend_removed")
	}
	socialDispatch(t, s, a, "send_private_message", sioHead{"req_id": float64(8), "to": b.playerID, "text": "revoked", "timeStamp": float64(101)})
	if socialEvent(t, socialFrames(t, a, codec), "send_private_message")["err_code"] == nil {
		t.Fatal("revoked chat allowed")
	}
	if len(b.out) != 0 {
		t.Fatal("revoked push")
	}
}
func TestSocialSearchPublicStatsAndBulk(t *testing.T) {
	s, a, b, _, codec := socialAPIFixture(t)
	if _, err := s.st.SetProfile(b.playerID, "Борис", 12, 30, 500, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.MergeProfileClientState(b.playerID, map[string]any{"device_id": "PRIVATE-DEVICE", "session": "PRIVATE-SESSION"}); err != nil {
		t.Fatal(err)
	}
	socialDispatch(t, s, a, "get_users_info_by_param_v3", sioHead{"req_id": float64(1), "param": "бор"})
	body := socialEvent(t, socialFrames(t, a, codec), "get_users_info_by_param_v3")
	players := body["players"].([]any)
	if len(players) != 1 {
		t.Fatal(players)
	}
	row := players[0].(map[string]any)
	if row["id"] != b.playerID || row["n"] != "Борис" || row["lv"] != float64(12) {
		t.Fatal("native search requires PlayerData dictionaries, not string IDs", row)
	}
	socialDispatch(t, s, a, "subscribe", sioHead{"req_id": float64(2), "type": "all_short_info_by_id_v3", "list_v2": []any{map[string]any{"id": b.playerID, "s_h": ""}}})
	body = socialEvent(t, socialFrames(t, a, codec), "all_short_info_by_id_v3")
	if body["info"].(map[string]any)[b.playerID] == nil {
		t.Fatal(body)
	}
	socialDispatch(t, s, a, "get_all_short_info_by_id_v3", sioHead{"req_id": float64(4), "ids_v2": []any{map[string]any{"id": b.playerID, "s_h": "", "cl_h": ""}}})
	body = socialEvent(t, socialFrames(t, a, codec), "get_all_short_info_by_id_v3")
	if body["info"].(map[string]any)[b.playerID] == nil {
		t.Fatal(body)
	}
	for _, event := range []string{"get_info_by_id", "get_info_by_id_v3"} {
		body = sioAnswerFor(event)(s, a, sioHead{"id": b.playerID}, 3)
		raw, _ := json.Marshal(body)
		for _, bad := range []string{"PRIVATE-", "coins", "gems", "device_id", "session"} {
			if strings.Contains(string(raw), bad) {
				t.Fatal("private disclosure", event, bad)
			}
		}
		if sioAnswerFor(event)(s, &sioSession{}, sioHead{"id": b.playerID}, 3)["err_code"] == nil {
			t.Fatal("anonymous profile")
		}
	}
}
func TestSocialAuthenticationInvalidRequestsAndUnsubscribe(t *testing.T) {
	s, a, b, c, codec := socialAPIFixture(t)
	for _, event := range []string{"friend_request", "accept_friend", "reject_friendship", "delete_friend_invites", "reject_invite_friendship", "send_private_message", "get_private_message", "get_users_info_by_param_v3", "get_all_short_info_by_id_v3"} {
		body := sioAnswerFor(event)(s, &sioSession{}, sioHead{}, 1)
		if body["err_code"] == nil {
			t.Fatal("anonymous", event)
		}
	}
	for _, head := range []sioHead{{"type": "get_private_message", "list": []any{12}}, {"type": "get_private_message", "list": []any{a.playerID}, "last_timeStamp": "NaN"}, {"type": "all_short_info_by_id_v3", "list_v2": []any{"wrong"}}} {
		if s.answerSubscribe(a, head, 2)["err_code"] == nil {
			t.Fatal("invalid subscription", head)
		}
	}
	if _, err := s.st.SocialAction(a.playerID, b.playerID, "request"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.SocialAction(b.playerID, a.playerID, "accept"); err != nil {
		t.Fatal(err)
	}
	s.pushSocialSubscription(b, "/sio", "test", codec, sioHead{"type": "get_private_message", "list": []any{a.playerID}})
	socialFrames(t, b, codec)
	s.answerUnsubscribe(b, sioHead{"type": "get_private_message", "list": []any{a.playerID}}, 3)
	if _, err := s.st.SendPrivateMessage(a.playerID, b.playerID, "not subscribed", "1"); err != nil {
		t.Fatal(err)
	}
	s.refreshSocialAll()
	if len(b.out) != 0 {
		t.Fatal("unsubscribe ignored")
	}
	s.pushSocialSubscription(b, "/sio", "test", codec, sioHead{"type": "get_private_message", "list": []any{a.playerID}, "last_timeStamp": float64(0)})
	rows := socialEvent(t, socialFrames(t, b, codec), "get_private_message")["messages"].([]any)
	if len(rows) != 1 {
		t.Fatal("offline restore", rows)
	}
	s.pushSocialSubscription(c, "/sio", "test", codec, sioHead{"type": "get_private_message", "list": []any{a.playerID, b.playerID}})
	rows = socialEvent(t, socialFrames(t, c, codec), "get_private_message")["messages"].([]any)
	if len(rows) != 0 {
		t.Fatal("third-party history")
	}
	for _, head := range []sioHead{{"to": b.playerID, "text": "no clan", "timeStamp": float64(2), "clan": true}, {"to": b.playerID, "text": "bad stamp", "timeStamp": "NaN"}, {"to": b.playerID, "text": "", "timeStamp": float64(2)}} {
		if s.answerSendPrivateMessage(a, head, 4)["err_code"] == nil {
			t.Fatal("invalid message", head)
		}
	}
	if err := s.st.AddBan(store.Ban{Kind: store.BanPlayer, Subject: a.playerID}); err != nil {
		t.Fatal(err)
	}
	if s.answerSearchPlayers(a, sioHead{"param": b.playerID}, 5)["err_code"] == nil {
		t.Fatal("banned actor")
	}
}
func TestSocialConcurrentSubscriptionRefresh(t *testing.T) {
	s, a, b, _, codec := socialAPIFixture(t)
	a.out = make(chan sioFrame, 1024)
	b.out = make(chan sioFrame, 1024)
	s.pushSocialSubscription(a, "/sio", "test", codec, sioHead{"type": "friends_list"})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.refreshSocialAll() }()
	}
	wg.Wait()
	events := socialFrames(t, a, codec)
	if len(events) != 1 {
		t.Fatal("duplicate unchanged pushes", len(events))
	}
}
func TestSocialClanSubscriptionDoesNotErasePrivatePeers(t *testing.T) {
	s, a, b, c, codec := socialAPIFixture(t)
	s.pushSocialSubscription(b, "/sio", "test", codec, sioHead{"type": "get_private_message", "list": []any{a.playerID}})
	socialFrames(t, b, codec)
	s.pushSocialSubscription(b, "/sio", "test", codec, sioHead{"type": "get_private_message", "list": []any{"-1"}})
	s.answerUnsubscribe(b, sioHead{"type": "get_private_message", "list": []any{"-1"}}, 1)
	b.social.Lock()
	peers := append([]string(nil), b.social.topics["get_private_message"].peers...)
	b.social.Unlock()
	if !reflect.DeepEqual(peers, []string{a.playerID}) {
		t.Fatal("clan unsub erased DMs", peers)
	}
	s.pushSocialSubscription(b, "/sio", "test", codec, sioHead{"type": "get_private_message", "list": []any{c.playerID}})
	socialFrames(t, b, codec)
	s.answerUnsubscribe(b, sioHead{"type": "get_private_message", "list": []any{a.playerID}}, 2)
	b.social.Lock()
	peers = append([]string(nil), b.social.topics["get_private_message"].peers...)
	b.social.Unlock()
	if !reflect.DeepEqual(peers, []string{c.playerID}) {
		t.Fatal("partial unsubscribe", peers)
	}
}

func TestSocialFullQueueDropsWholeEventAndRetries(t *testing.T) {
	s, a, b, _, codec := socialAPIFixture(t)
	a.out = make(chan sioFrame, 3)
	a.send(sioFrame{text: "occupied"})
	s.pushSocialSubscription(a, "/sio", "test", codec, sioHead{"type": "friends_list"})
	if len(a.out) != 1 {
		t.Fatal("partial encrypted frame group queued")
	}
	<-a.out
	s.refreshSocial(a)
	socialEvent(t, socialFrames(t, a, codec), "friends_list")
	if _, err := s.st.SocialAction(a.playerID, b.playerID, "request"); err != nil {
		t.Fatal(err)
	}
}
