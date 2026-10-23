// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

const mailboxUpdated = "mailbox_updated_messages"

// Preserve the stock client's typo, not an invented corrected event name.
const mailboxPersonalUpdated = "mailbox_udpated_me_messages"

func isMailboxTopic(topic string) bool {
	return topic == mailboxUpdated || topic == mailboxPersonalUpdated
}

// The subscription callback RVA 0x33B8090 iterates the entire argument as
// id -> ctime_change. No req_id/result/type or wrapping 'news' belongs here.
func (s *Server) pushMailboxSubscription(session *sioSession, namespace, transport string, codec *sio.Codec, head sioHead) {
	topic := head.text("type")
	if !isMailboxTopic(topic) {
		return
	}
	if _, ok := s.socketPlayerID(session); !ok {
		return
	}
	body := map[string]any{}
	if topic == mailboxUpdated {
		for _, n := range s.st.MailboxNews() {
			body[strconv.Itoa(n.ID)] = n.Changed
		}
	}
	// Personal/reward messages are intentionally empty, never forged grants.
	s.replyEncrypted(session, namespace, transport, codec, topic, 0, body)
}

// Stock RVA 0x33B65E8: news must be a dictionary keyed by decimal message ID.
// Its message constructor 0x33B699C parses type as a string enum name, text as
// language dictionaries and date as Unix seconds. No reward fields are emitted.
func mailboxWireNews(n store.MailboxNews) map[string]any {
	return map[string]any{
		"short_header": n.Header, "header": n.Header, "type": n.Type,
		"short_description": n.ShortDescription, "description": n.Description,
		"date": n.Date, "ctime_change": n.Changed,
	}
}
func (s *Server) answerMailboxMessages(session *sioSession, head sioHead, reqID int) map[string]any {
	if _, ok := s.socketPlayerID(session); !ok {
		return socketStateFailure(reqID, "not_authorized")
	}
	raw, ok := head["ids"].([]any)
	if !ok || len(raw) > store.MailboxNewsLimit {
		return socketStateFailure(reqID, "invalid_ids")
	}
	wanted := make(map[int]bool, len(raw))
	for _, v := range raw {
		var id int
		switch x := v.(type) {
		case float64:
			if x < 1 || x > 2147483647 || x != float64(int(x)) {
				return socketStateFailure(reqID, "invalid_ids")
			}
			id = int(x)
		case string:
			n, err := strconv.Atoi(x)
			if err != nil || n < 1 || n > 2147483647 {
				return socketStateFailure(reqID, "invalid_ids")
			}
			id = n
		default:
			return socketStateFailure(reqID, "invalid_ids")
		}
		wanted[id] = true
	}
	news := map[string]any{}
	for _, n := range s.st.MailboxNews() {
		if wanted[n.ID] {
			news[strconv.Itoa(n.ID)] = mailboxWireNews(n)
		}
	}
	body := sioOK(reqID)
	body["news"] = news
	return body
}
func (s *Server) handleAdminMailboxNews(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		s.writeJSON(w, 200, map[string]any{"news": s.st.MailboxNews(), "limit": store.MailboxNewsLimit})
		return
	}
	var draft store.MailboxNewsDraft
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&draft); err != nil || dec.Decode(&struct{}{}) != io.EOF {
		s.writeError(w, 400, "invalid_news", "")
		return
	}
	n, err := s.st.CreateMailboxNews(draft)
	if errors.Is(err, store.ErrInvalidMailboxNews) {
		s.writeError(w, 400, "invalid_news", "")
		return
	}
	if errors.Is(err, store.ErrMailboxFull) {
		s.writeError(w, 409, "mailbox_full", "")
		return
	}
	if err != nil {
		s.log.Event("admin.mailbox_failed", map[string]any{"err": err.Error()})
		s.writeError(w, 503, "mailbox_unavailable", "")
		return
	}
	s.log.Event("admin.mailbox_news_created", map[string]any{"id": n.ID, "type": n.Type})
	s.writeJSON(w, http.StatusCreated, map[string]any{"news": n})
}
