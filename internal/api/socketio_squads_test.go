// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func squadFriends(t *testing.T, s *Server, a, b *sioSession) {
	t.Helper()
	if _, err := s.st.SocialAction(a.playerID, b.playerID, "request"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.SocialAction(b.playerID, a.playerID, "accept"); err != nil {
		t.Fatal(err)
	}
}
func squadHead(target string, n int) sioHead {
	return sioHead{"receiver_id": target, "squad_name": "room-example", "protocol": "1eu", "invite_number": n, "siege_mode": 0, "clanwar_mode": 0, "test_mode": 0, "req_id": 91}
}
func TestSquadEncryptedDeliveryAndExpiry(t *testing.T) {
	s, a, b, c, codec := socialAPIFixture(t)
	squadFriends(t, s, a, b)
	for _, ss := range []*sioSession{a, b, c} {
		socialDispatch(t, s, ss, "subscribe", sioHead{"type": "squad_invites", "req_id": 1})
		rows := socialEvent(t, socialFrames(t, ss, codec), "squad_invites")["squad_invites"].([]any)
		if len(rows) != 0 {
			t.Fatal(rows)
		}
	}
	h := squadHead(b.playerID, 17)
	h["name"] = "spoofed"
	h["id"] = c.playerID
	h["player_id"] = c.playerID
	before, _ := s.st.Profile(a.playerID)
	socialDispatch(t, s, a, "send_squad_invite", h)
	if ack := socialEvent(t, socialFrames(t, a, codec), "send_squad_invite"); ack["err_code"] != nil {
		t.Fatal(ack)
	}
	rows := socialEvent(t, socialFrames(t, b, codec), "squad_invites")["squad_invites"].([]any)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	row := rows[0].(map[string]any)
	expected := map[string]any{"id": a.playerID, "name": before.Nick, "squad": "room-example", "protocol": "1eu", "invite_number": float64(17), "siege_mode": float64(0), "clanwar_mode": float64(0), "test_mode": float64(0)}
	if !reflect.DeepEqual(row, expected) {
		t.Fatal(row)
	}
	if len(c.out) != 0 {
		t.Fatal("invite leaked to third party")
	}
	socialDispatch(t, s, a, "send_squad_invite", h)
	socialFrames(t, a, codec)
	if len(b.out) != 0 {
		t.Fatal("duplicate refresh")
	}
	after, _ := s.st.Profile(a.playerID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("ephemeral invite changed durable progress")
	}
	s.sio.squads.Lock()
	s.sio.squads.invites[0].expires = time.Now().Add(-time.Second)
	s.sio.squads.Unlock()
	s.refreshSocialAll()
	rows = socialEvent(t, socialFrames(t, b, codec), "squad_invites")["squad_invites"].([]any)
	if len(rows) != 0 {
		t.Fatal("expired invite survived")
	}
}
func TestSquadAuthorizationValidationAndRate(t *testing.T) {
	s, a, b, c, _ := socialAPIFixture(t)
	for _, event := range []string{"send_squad_invite", "squad_invites"} {
		if got := sioAnswerFor(event)(s, &sioSession{}, squadHead(b.playerID, 1), 1); got["err_code"] == nil {
			t.Fatal("anonymous", event)
		}
	}
	if s.answerSendSquadInvite(a, squadHead(b.playerID, 1), 1)["err_code"] == nil {
		t.Fatal("nonfriend")
	}
	squadFriends(t, s, a, b)
	for _, v := range []struct {
		key   string
		value any
	}{
		{"receiver_id", a.playerID}, {"receiver_id", c.playerID}, {"squad_name", ""}, {"squad_name", "bad\nroom"}, {"protocol", true}, {"invite_number", nil}, {"invite_number", -1}, {"invite_number", 1.5}, {"invite_number", "NaN"}, {"invite_number", 2147483648.0}, {"test_mode", 1}, {"clanwar_mode", 1}, {"siege_mode", 1},
	} {
		h := squadHead(b.playerID, 1)
		h[v.key] = v.value
		if s.answerSendSquadInvite(a, h, 1)["err_code"] == nil {
			t.Fatal("invalid accepted", v.key, v.value)
		}
	}
	for i := 0; i < 10; i++ {
		if got := s.answerSendSquadInvite(a, squadHead(b.playerID, i), 1); got["err_code"] != nil {
			t.Fatal(got)
		}
	}
	if got := s.answerSendSquadInvite(a, squadHead(b.playerID, 10), 1); got["err_code"] == nil {
		t.Fatal("unbounded invitations")
	}
	if len(s.sio.squads.invites) != 1 {
		t.Fatal("old pair invitation not replaced")
	}
	if _, err := s.st.SocialAction(a.playerID, b.playerID, "remove"); err != nil {
		t.Fatal(err)
	}
	if rows := s.squadInvitesBody(b.playerID, time.Now())["squad_invites"].([]any); len(rows) != 0 {
		t.Fatal("unfriended sender")
	}
}
func TestSquadRetryUnsubscribeAndConcurrentRefresh(t *testing.T) {
	s, a, b, _, codec := socialAPIFixture(t)
	squadFriends(t, s, a, b)
	s.pushSocialSubscription(b, "/sio", "test", codec, sioHead{"type": "squad_invites"})
	socialFrames(t, b, codec)
	// Insufficient room must not advance the subscription signature or enqueue partial frames.
	b.out = make(chan sioFrame, 3)
	b.out <- sioFrame{text: "occupied"}
	if got := s.answerSendSquadInvite(a, squadHead(b.playerID, 1), 1); got["err_code"] != nil {
		t.Fatal(got)
	}
	s.refreshSocialAll()
	if len(b.out) != 1 {
		t.Fatal("partial frame group")
	}
	<-b.out
	s.refreshSocialAll()
	if len(socialEvent(t, socialFrames(t, b, codec), "squad_invites")["squad_invites"].([]any)) != 1 {
		t.Fatal("failed push not retried")
	}
	s.answerUnsubscribe(b, sioHead{"type": "squad_invites"}, 2)
	if got := s.answerSendSquadInvite(a, squadHead(b.playerID, 2), 1); got["err_code"] != nil {
		t.Fatal(got)
	}
	s.refreshSocialAll()
	if len(b.out) != 0 {
		t.Fatal("unsubscribe ignored")
	}
	b.out = make(chan sioFrame, 128)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h := squadHead(b.playerID, i+3)
			h["squad_name"] = fmt.Sprint("room-", i)
			s.answerSendSquadInvite(a, h, 1)
			s.squadInvitesBody(b.playerID, time.Now())
			s.refreshSocialAll()
		}(i)
	}
	wg.Wait()
	s.pushSocialSubscription(b, "/sio", "test", codec, sioHead{"type": "squad_invites"})
	if len(socialEvent(t, socialFrames(t, b, codec), "squad_invites")["squad_invites"].([]any)) != 1 {
		t.Fatal("resubscription lost invite")
	}
}

func TestSquadCapacityAndPruning(t *testing.T) {
	s, a, b, _, _ := socialAPIFixture(t)
	squadFriends(t, s, a, b)
	now := time.Now()
	for i := 0; i < 20; i++ {
		s.sio.squads.invites = append(s.sio.squads.invites, squadInvite{sender: fmt.Sprint("other-", i), receiver: b.playerID, expires: now.Add(time.Minute)})
	}
	if s.answerSendSquadInvite(a, squadHead(b.playerID, 1), 1)["err_code"] == nil {
		t.Fatal("recipient capacity ignored")
	}
	s.sio.squads.invites[0].expires = now.Add(-time.Second)
	if got := s.answerSendSquadInvite(a, squadHead(b.playerID, 1), 1); got["err_code"] != nil {
		t.Fatal("expired invite consumed capacity", got)
	}
	if got := s.answerSendSquadInvite(a, squadHead(b.playerID, 2), 1); got["err_code"] != nil {
		t.Fatal("replacement incorrectly consumed another slot", got)
	}
	if len(s.sio.squads.invites) != 20 {
		t.Fatal("capacity count")
	}
}
