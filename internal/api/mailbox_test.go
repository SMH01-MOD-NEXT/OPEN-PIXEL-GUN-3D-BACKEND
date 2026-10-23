// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

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
