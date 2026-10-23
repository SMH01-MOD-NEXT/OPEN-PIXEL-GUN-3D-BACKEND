// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

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
	if topic == mailboxPersonalUpdated {
		body = s.personalMailboxMap(session)
	}
	if topic == mailboxUpdated {
		for _, n := range s.st.MailboxNews() {
			if n.VisibleAt(time.Now()) {
				body[strconv.Itoa(n.ID)] = n.Changed
			}
		}
	}
	// The stock client subscribes once. Remember the encrypted subscription to
	// refresh publication/archival on its ordinary keepalive, without new threads.
	if s.replyEncrypted(session, namespace, transport, codec, topic, 0, body) {
		signature, _ := json.Marshal(body)
		session.mu.Lock()
		if topic == mailboxUpdated {
			session.mailboxNamespace = namespace
			session.mailboxCodec = codec
			session.mailboxSignature = string(signature)
		} else {
			session.personalMailboxNamespace = namespace
			session.personalMailboxCodec = codec
			session.personalMailboxSignature = string(signature)
			session.personalMailboxSeen = map[int]bool{}
			for id := range body {
				n, _ := strconv.Atoi(id)
				session.personalMailboxSeen[n] = true
			}
		}
		session.mu.Unlock()
	}
}

// Stock RVA 0x33B65E8: news must be a dictionary keyed by decimal message ID.
// Its message constructor 0x33B699C parses type as a string enum name, text as
// language dictionaries and date as Unix seconds. Gift items use stock string tokens.
func mailboxWireNews(n store.MailboxNews) map[string]any {
	out := map[string]any{
		"short_header": n.Header, "header": n.Header, "type": n.Type,
		"short_description": n.ShortDescription, "description": n.Description,
		"date": n.Date, "ctime_change": n.Changed,
	}
	if items := n.Rewards.Tokens(); len(items) > 0 {
		out["items"] = items
		// Stock 0x33BA980 requires items AND a nonnegative idReward. Without
		// id_reward the constructor keeps -1, hiding the whole reward section.
		// Use the news ID as a stable reward ID, matching command 187 / slot 42.
		out["id_reward"] = n.ID
	}
	return out
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
		if wanted[n.ID] && n.VisibleAt(time.Now()) {
			news[strconv.Itoa(n.ID)] = mailboxWireNews(n)
		}
	}
	for _, l := range s.personalMailboxLetters(session) {
		if wanted[l.ID] {
			if wire := s.personalMailboxWire(session, l); wire != nil {
				news[strconv.Itoa(l.ID)] = wire
			}
		}
	}
	body := sioOK(reqID)
	body["news"] = news
	return body
}
func (s *Server) handleAdminMailboxNews(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		s.writeJSON(w, 200, map[string]any{"news": s.st.MailboxNewsForAdmin(), "limit": store.MailboxNewsLimit})
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

// refreshMailbox runs on the stock heartbeat (at most one ping interval late).
// Future content is filtered both here and in mailbox_get_messages/claim.
func (s *Server) refreshMailbox(session *sioSession) {
	session.mu.Lock()
	codec, ns, previous := session.mailboxCodec, session.mailboxNamespace, session.mailboxSignature
	personalCodec, personalNS, personalPrevious := session.personalMailboxCodec, session.personalMailboxNamespace, session.personalMailboxSignature
	session.mu.Unlock()
	if codec != nil && previous != "" {
		body := map[string]any{}
		for _, n := range s.st.MailboxNews() {
			if n.VisibleAt(time.Now()) {
				body[strconv.Itoa(n.ID)] = n.Changed
			}
		}
		signature, _ := json.Marshal(body)
		if string(signature) != previous {
			s.pushMailboxSubscription(session, ns, "keepalive", codec, sioHead{"type": mailboxUpdated})
		}
	}
	if personalCodec != nil && personalPrevious != "" {
		signature, _ := json.Marshal(s.personalMailboxMap(session))
		if string(signature) != personalPrevious {
			s.pushMailboxSubscription(session, personalNS, "keepalive", personalCodec, sioHead{"type": mailboxPersonalUpdated})
		}
	}
}
func (s *Server) handleAdminArchiveMailboxNews(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 {
		s.writeError(w, 400, "invalid_news", "")
		return
	}
	err = s.st.ArchiveMailboxNews(id)
	if errors.Is(err, store.ErrNotFound) {
		s.writeError(w, 404, "not_found", "")
		return
	}
	if err != nil {
		s.writeError(w, 503, "mailbox_unavailable", "")
		return
	}
	s.log.Event("admin.mailbox_news_archived", map[string]any{"id": id})
	s.writeJSON(w, 200, map[string]any{"archived": true})
}

func (s *Server) handleAdminUpdateMailboxNews(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 || id > 2147483647 {
		s.writeError(w, 400, "invalid_news", "")
		return
	}
	var update struct {
		store.MailboxNewsDraft
		ExpectedChanged int `json:"expected_ctime_change"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&update); err != nil || dec.Decode(&struct{}{}) != io.EOF {
		s.writeError(w, 400, "invalid_news", "")
		return
	}
	n, err := s.st.UpdateMailboxNews(id, update.ExpectedChanged, update.MailboxNewsDraft)
	switch {
	case errors.Is(err, store.ErrInvalidMailboxNews):
		s.writeError(w, 400, "invalid_news", "")
	case errors.Is(err, store.ErrNotFound):
		s.writeError(w, 404, "not_found", "")
	case errors.Is(err, store.ErrMailboxConflict):
		s.writeError(w, 409, "mailbox_conflict", "")
	case errors.Is(err, store.ErrMailboxArchived):
		s.writeError(w, 409, "mailbox_archived", "")
	case errors.Is(err, store.ErrMailboxDeliveryLocked):
		s.writeError(w, 409, "mailbox_delivery_locked", "")
	case err != nil:
		s.log.Event("admin.mailbox_update_failed", map[string]any{"id": id, "err": err.Error()})
		s.writeError(w, 503, "mailbox_unavailable", "")
	default:
		s.log.Event("admin.mailbox_news_updated", map[string]any{"id": id, "ctime_change": n.Changed})
		s.writeJSON(w, 200, map[string]any{"news": n})
	}
}
