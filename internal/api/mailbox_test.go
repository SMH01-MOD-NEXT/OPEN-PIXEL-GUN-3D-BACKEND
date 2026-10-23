// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

func mailboxTestDraft() store.MailboxNewsDraft {
	return store.MailboxNewsDraft{Type: "Update", Header: store.MailboxText{Russian: "Тест", English: "Test"}, ShortDescription: store.MailboxText{English: "Summary"}, Description: store.MailboxText{English: "Body"}}
}
func TestAdminMailboxAuthValidationAndCreation(t *testing.T) {
	s, cfg := newTestServer(t)
	path := "/api/v1/admin/mailbox/news"
	auth := map[string]string{"Authorization": "Bearer admin-secret"}
	for _, method := range []string{"GET", "POST"} {
		if r := do(t, s, method, path, mailboxTestDraft(), nil); r.Code != 401 {
			t.Fatal("open admin endpoint", r.Code)
		}
	}
	for _, payload := range []string{`{}`, `{"type":"None"}`, `{"id":1}`, `{"header":{"Russian":"Title","secret":"x"}}`, `{} {}`, strings.Repeat("x", 65537)} {
		req := httptest.NewRequest("POST", path, strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer admin-secret")
		r := httptest.NewRecorder()
		s.ServeHTTP(r, req)
		if r.Code != 400 {
			t.Fatal("invalid request accepted", r.Code)
		}
	}
	if r := do(t, s, "POST", path, mailboxTestDraft(), auth); r.Code != 201 {
		t.Fatal(r.Code, r.Body)
	}
	b := decode(t, do(t, s, "GET", path, nil, auth))
	news := b["news"].([]any)
	if len(news) != 1 || b["limit"] != float64(store.MailboxNewsLimit) {
		t.Fatal(b)
	}
	if strings.Contains(mustJSON(t, b), "session") {
		t.Fatal("secret leaked")
	}
	cfg.AdminToken = ""
	if r := do(t, s, "GET", path, nil, auth); r.Code != 503 {
		t.Fatal("unset admin token not closed", r.Code)
	}
}
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestMailboxStockMessageSchemaAndAuth(t *testing.T) {
	s, _ := newTestServer(t)
	seedSocketAccount(t, s, "918930694", "mail-owner")
	session := authorisedSession("mail-sid", "918930694")
	n, err := s.st.CreateMailboxNews(mailboxTestDraft())
	if err != nil {
		t.Fatal(err)
	}
	head := sioHead{"ids": []any{float64(n.ID), "1", float64(999)}, "player_id": "other"}
	b := sioAnswerFor("mailbox_get_messages")(s, session, head, 42)
	if b["req_id"] != 42 || b["error"] != sioErrNone {
		t.Fatal(b)
	}
	news := b["news"].(map[string]any)
	if len(news) != 1 {
		t.Fatal(news)
	}
	wire := news["1"].(map[string]any)
	if wire["type"] != "Update" || wire["header"].(store.MailboxText).English != "Test" || wire["date"] != n.Date {
		t.Fatal(wire)
	}
	for _, field := range []string{"items", "id_reward", "reward_type", "reward_cnt", "toId", "session", "player_id", "isReaded"} {
		if _, ok := wire[field]; ok {
			t.Fatal("unexpected field", field)
		}
	}
	for _, ids := range []any{nil, "1", []any{-1.0}, []any{1.5}, []any{"abc"}, []any{true}, []any{2147483648.0}, make([]any, 501)} {
		if b := s.answerMailboxMessages(session, sioHead{"ids": ids}, 43); b["err_code"] == nil {
			t.Fatal("invalid ids accepted", ids)
		}
	}
	if b := s.answerMailboxMessages(session, sioHead{"ids": []any{}}, 44); len(b["news"].(map[string]any)) != 0 {
		t.Fatal(b)
	}
	if b := s.answerMailboxMessages(&sioSession{}, head, 45); b["err_code"] == nil {
		t.Fatal("anonymous read")
	}
	for _, topic := range []string{mailboxUpdated, mailboxPersonalUpdated} {
		if b := s.answerSubscribe(&sioSession{}, sioHead{"type": topic}, 46); b["err_code"] == nil {
			t.Fatal("anonymous subscribe")
		}
	}
	err = s.st.AddBan(store.Ban{Kind: store.BanPlayer, Subject: "918930694", Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if b := s.answerMailboxMessages(session, head, 47); b["err_code"] == nil {
		t.Fatal("banned read")
	}
}
func TestMailboxEncryptedSubscriptionsAndFetchFlow(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.SioReply = true
	seedSocketAccount(t, s, "918930694", "mail-push")
	session := authorisedSession("mail-push", "918930694")
	session.out = make(chan sioFrame, 32)
	n, err := s.st.CreateMailboxNews(mailboxTestDraft())
	if err != nil {
		t.Fatal(err)
	}
	codec := codecForPinnedSid(t)
	readEvent := func(event string) map[string]any {
		t.Helper()
		if len(session.out) < 3 {
			t.Fatal("missing encrypted event")
		}
		<-session.out
		name, _ := stripBinaryPrefix((<-session.out).data)
		plain, err := codec.Decrypt(name)
		if err != nil || string(plain) != `"`+event+`"` {
			t.Fatal("wrong event", string(plain), err)
		}
		arg, _ := stripBinaryPrefix((<-session.out).data)
		plain, err = codec.Decrypt(arg)
		if err != nil {
			t.Fatal(err)
		}
		var b map[string]any
		if err = json.Unmarshal(plain, &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, topic := range []string{mailboxUpdated, mailboxPersonalUpdated} {
		head := `{"req_id":51,"type":"` + topic + `","platform":1}`
		s.answerDecodedEvent(session, &sioBinaryAssembly{namespace: "/sio", transport: "websocket"}, codec, [][]byte{[]byte("subscribe"), []byte(head)})
		ack := readEvent("subscribe")
		if ack["req_id"] != float64(51) {
			t.Fatal(ack)
		}
		push := readEvent(topic)
		if topic == mailboxUpdated {
			if len(push) != 1 || push["1"] != float64(n.Changed) {
				t.Fatal("stock id/version map", push)
			}
		} else if len(push) != 0 {
			t.Fatal("invented personal news", push)
		}
	}
	s.answerDecodedEvent(session, &sioBinaryAssembly{namespace: "/sio", transport: "websocket"}, codec, [][]byte{[]byte("mailbox_get_messages"), []byte(`{"req_id":52,"ids":[1]}`)})
	b := readEvent("mailbox_get_messages")
	if b["req_id"] != float64(52) || b["news"].(map[string]any)["1"] == nil {
		t.Fatal(b)
	}
	if len(session.out) != 0 {
		t.Fatal("unexpected pushes")
	}
	s.pushMailboxSubscription(&sioSession{out: session.out}, "/sio", "websocket", codec, sioHead{"type": mailboxUpdated})
	s.pushMailboxSubscription(session, "/sio", "websocket", codec, sioHead{"type": "friends_list"})
	if len(session.out) != 0 {
		t.Fatal("unauthorized or unrelated subscription pushed news")
	}
}

func TestMailboxScheduleGiftWireAndArchiveKeepalive(t *testing.T) {
	s, _ := newTestServer(t)
	seedSocketAccount(t, s, "918930694", "schedule-mail")
	session := authorisedSession("schedule-mail", "918930694")
	session.out = make(chan sioFrame, 64)
	codec := codecForPinnedSid(t)
	d := mailboxTestDraft()
	d.Type = "DeveloperMessage"
	d.Rewards = store.MailboxRewards{Coins: 2626, Gems: 1010}
	at := time.Now().Add(time.Hour)
	d.PublishAt = &at
	future, err := s.st.CreateMailboxNews(d)
	if err != nil {
		t.Fatal(err)
	}
	if b := s.answerMailboxMessages(session, sioHead{"ids": []any{float64(future.ID)}}, 1); len(b["news"].(map[string]any)) != 0 {
		t.Fatal("scheduled content leaked", b)
	}
	s.pushMailboxSubscription(session, "/sio", "websocket", codec, sioHead{"type": mailboxUpdated})
	for len(session.out) > 0 {
		<-session.out
	}
	d.PublishAt = nil
	n, err := s.st.CreateMailboxNews(d)
	if err != nil {
		t.Fatal(err)
	}
	s.refreshMailbox(session)
	if len(session.out) != 3 {
		t.Fatal("new letter not pushed on keepalive")
	}
	for len(session.out) > 0 {
		<-session.out
	}
	s.refreshMailbox(session)
	if len(session.out) != 0 {
		t.Fatal("unchanged map re-sent")
	}
	b := s.answerMailboxMessages(session, sioHead{"ids": []any{float64(n.ID)}}, 2)
	wire := b["news"].(map[string]any)["2"].(map[string]any)
	items := wire["items"].([]string)
	if len(items) != 2 || items[0] != "Coins::2626" || items[1] != "Gems::1010" {
		t.Fatal(items)
	}
	endpoint := "/api/v1/admin/mailbox/news/2"
	if r := do(t, s, "DELETE", endpoint, nil, nil); r.Code != 401 {
		t.Fatal("archive not protected", r.Code)
	}
	if r := do(t, s, "DELETE", endpoint, nil, map[string]string{"Authorization": "Bearer admin-secret"}); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	s.refreshMailbox(session)
	if len(session.out) != 3 {
		t.Fatal("archive not pushed")
	}
	if b := s.answerMailboxMessages(session, sioHead{"ids": []any{float64(n.ID)}}, 3); len(b["news"].(map[string]any)) != 0 {
		t.Fatal("archived letter fetched")
	}
}
func TestMailboxRewardStockUpdateProgressDispatch(t *testing.T) {
	s, _ := newTestServer(t)
	seedSocketAccount(t, s, "918930694", "mail-claim")
	session := authorisedSession("mail-claim", "918930694")
	d := mailboxTestDraft()
	d.Type = "DeveloperMessage"
	d.Rewards = store.MailboxRewards{Coins: 1000, Gems: 500, Weapon: 203001}
	n, e := s.st.CreateMailboxNews(d)
	if e != nil {
		t.Fatal(e)
	}
	before, _ := s.st.Profile("918930694")
	head := sioHead{"i": "mail-batch", "p": map[string]any{"c": []any{
		map[string]any{"id": float64(10), "ci": "coin", "p": map[string]any{"c": "Coins", "v": float64(1000), "ca": float64(62)}},
		map[string]any{"id": float64(10), "ci": "gem", "p": map[string]any{"c": "GemsCurrency", "v": float64(500), "ca": float64(62)}},
		map[string]any{"id": float64(132), "ci": "gun", "p": map[string]any{"i": float64(203001), "u": map[string]any{"l": float64(1), "r": float64(3)}}},
		map[string]any{"id": float64(187), "ci": "claim", "p": map[string]any{"records": []any{float64(n.ID)}}},
	}}}
	for i := 0; i < 2; i++ {
		if b := s.answerUpdateProgress(session, head, 91); b["err_code"] != sioErrNone {
			t.Fatal(b)
		}
	}
	after, _ := s.st.Profile("918930694")
	if after.Coins != before.Coins+1000 || after.Gems != before.Gems+500 || !after.MailboxClaims["1"] {
		t.Fatal("claim did not reach durable reducer", after.Coins, after.Gems)
	}
}

// Regression: the stock UI's 0x33BA980 gate is items.Count > 0 AND
// idReward >= 0. Merely asserting that items exists missed the hidden gifts.
func TestMailboxGiftWireCarriesStockRewardIdentity(t *testing.T) {
	for _, tc := range []struct {
		rewards store.MailboxRewards
		items   []any
	}{
		{store.MailboxRewards{Coins: 1000, Gems: 500, Weapon: 203001}, []any{"Coins::1000", "Gems::500", "203001:1"}},
		{store.MailboxRewards{Coins: 2626, Gems: 1010}, []any{"Coins::2626", "Gems::1010"}},
		{store.MailboxRewards{Coins: 3131, Gems: 1010, Weapon: 143001}, []any{"Coins::3131", "Gems::1010", "143001:1"}},
	} {
		r := tc.rewards
		n := store.MailboxNews{ID: 27, MailboxNewsDraft: store.MailboxNewsDraft{Type: "DeveloperMessage", Rewards: r}}
		encoded, err := json.Marshal(mailboxWireNews(n))
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err = json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		id, ok := wire["id_reward"].(float64)
		if !ok || id < 0 || id != float64(n.ID) {
			t.Fatal("stock hides gifts without stable id_reward", wire)
		}
		items, ok := wire["items"].([]any)
		// Do not derive expectations from Tokens(): that let invalid numeric
		// currency IDs pass while the stock currency grid stayed empty.
		if !ok || !reflect.DeepEqual(items, tc.items) || wire["type"] != "DeveloperMessage" {
			t.Fatal("invalid stock gift list", wire)
		}
		if wire["r_message"] != nil || wire["reward_type"] != nil || wire["reward_cnt"] != nil {
			t.Fatal("unexpected alternate reward path", wire)
		}
	}
}
