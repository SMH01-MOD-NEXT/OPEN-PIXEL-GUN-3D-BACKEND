// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

func mailboxEditPayload(n store.MailboxNews) map[string]any {
	b, _ := json.Marshal(n.MailboxNewsDraft)
	var d map[string]any
	_ = json.Unmarshal(b, &d)
	d["expected_ctime_change"] = n.Changed
	return d
}
func TestAdminMailboxEditContractAuthAndConflicts(t *testing.T) {
	s, _ := newTestServer(t)
	auth := map[string]string{"Authorization": "Bearer admin-secret"}
	d := mailboxTestDraft()
	d.Type = "DeveloperMessage"
	at := time.Now().Add(24 * time.Hour)
	d.PublishAt = &at
	d.Rewards = store.MailboxRewards{Coins: 2626, Gems: 1010}
	n, err := s.st.CreateMailboxNews(d)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/mailbox/news/1"
	payload := mailboxEditPayload(n)
	if res := do(t, s, "PUT", path, payload, nil); res.Code != 401 {
		t.Fatal("edit not protected", res.Code)
	}
	for _, raw := range []string{`{}`, `{"id":1}`, `{"expected_ctime_change":1,"archived":false}`, `{} {}`, strings.Repeat("x", 65537)} {
		req := httptest.NewRequest("PUT", path, strings.NewReader(raw))
		req.Header.Set("Authorization", "Bearer admin-secret")
		res := httptest.NewRecorder()
		s.ServeHTTP(res, req)
		if res.Code != 400 {
			t.Fatal("malformed accepted", res.Code)
		}
	}
	for _, bad := range []string{"0", "abc", "2147483648"} {
		if res := do(t, s, "PUT", "/api/v1/admin/mailbox/news/"+bad, payload, auth); res.Code != 400 {
			t.Fatal(res.Code)
		}
	}
	if res := do(t, s, "PUT", "/api/v1/admin/mailbox/news/99", payload, auth); res.Code != 404 {
		t.Fatal(res.Code)
	}
	payload["header"] = map[string]string{"Russian": "Новое имя", "English": "New title"}
	res := do(t, s, "PUT", path, payload, auth)
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body)
	}
	current := s.st.MailboxNews()[0]
	if current.Changed <= n.Changed || current.ID != n.ID || current.Date != n.Date {
		t.Fatal(current)
	}
	if res = do(t, s, "PUT", path, payload, auth); res.Code != 409 || !strings.Contains(res.Body.String(), "mailbox_conflict") {
		t.Fatal("stale form accepted", res.Code, res.Body)
	}
	list := decode(t, do(t, s, "GET", "/api/v1/admin/mailbox/news", nil, auth))["news"].([]any)
	if list[0].(map[string]any)["delivery_editable"] != true {
		t.Fatal(list)
	}
	if err = s.st.ArchiveMailboxNews(n.ID); err != nil {
		t.Fatal(err)
	}
	payload = mailboxEditPayload(s.st.MailboxNews()[0])
	if res = do(t, s, "PUT", path, payload, auth); res.Code != 409 || !strings.Contains(res.Body.String(), "mailbox_archived") {
		t.Fatal(res.Code, res.Body)
	}
}
func TestAdminMailboxEditCacheRefreshAndPublishedDeliveryLock(t *testing.T) {
	s, _ := newTestServer(t)
	seedSocketAccount(t, s, "918930694", "mail-edit")
	session := authorisedSession("mail-edit", "918930694")
	session.out = make(chan sioFrame, 64)
	codec := codecForPinnedSid(t)
	d := mailboxTestDraft()
	d.Type = "DeveloperMessage"
	d.Rewards = store.MailboxRewards{Coins: 1000, Gems: 500, Weapon: 203001}
	n, err := s.st.CreateMailboxNews(d)
	if err != nil {
		t.Fatal(err)
	}
	s.pushMailboxSubscription(session, "/sio", "websocket", codec, sioHead{"type": mailboxUpdated})
	for len(session.out) > 0 {
		<-session.out
	}
	auth := map[string]string{"Authorization": "Bearer admin-secret"}
	payload := mailboxEditPayload(n)
	payload["description"] = map[string]string{"Russian": "Исправлено", "English": "Corrected"}
	if res := do(t, s, "PUT", "/api/v1/admin/mailbox/news/1", payload, auth); res.Code != 200 {
		t.Fatal(res.Code, res.Body)
	}
	s.refreshMailbox(session)
	if len(session.out) != 3 {
		t.Fatal("edited ctime not pushed on keepalive", len(session.out))
	}
	for len(session.out) > 0 {
		<-session.out
	}
	s.refreshMailbox(session)
	if len(session.out) != 0 {
		t.Fatal("unchanged repeated")
	}
	wire := s.answerMailboxMessages(session, sioHead{"ids": []any{float64(n.ID)}}, 9)["news"].(map[string]any)["1"].(map[string]any)
	if wire["description"].(store.MailboxText).English != "Corrected" || wire["id_reward"] != n.ID {
		t.Fatal(wire)
	}
	current := s.st.MailboxNews()[0]
	payload = mailboxEditPayload(current)
	payload["rewards"] = store.MailboxRewards{Coins: 2000}
	if res := do(t, s, "PUT", "/api/v1/admin/mailbox/news/1", payload, auth); res.Code != 409 || !strings.Contains(res.Body.String(), "mailbox_delivery_locked") {
		t.Fatal(res.Code, res.Body)
	}
}
