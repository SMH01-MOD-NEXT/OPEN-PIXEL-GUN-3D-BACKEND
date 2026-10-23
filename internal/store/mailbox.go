// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const MailboxNewsLimit = 500

var ErrInvalidMailboxNews = errors.New("store: invalid mailbox news")
var ErrMailboxFull = errors.New("store: mailbox news limit reached")

// MailboxText uses the language names read by the unmodified 23.1.3 client.
// English is always populated because it is the stock fallback language.
type MailboxText struct {
	Russian string `json:"Russian"`
	English string `json:"English"`
}
type MailboxNewsDraft struct {
	Header           MailboxText `json:"header"`
	ShortDescription MailboxText `json:"short_description"`
	Description      MailboxText `json:"description"`
	Type             string      `json:"type"`
}
type MailboxNews struct {
	ID int `json:"id"`
	MailboxNewsDraft
	Date    int64 `json:"date"`
	Changed int   `json:"ctime_change"`
}

func normalizeMailboxText(t MailboxText, limit int) (MailboxText, bool) {
	t.Russian = strings.TrimSpace(t.Russian)
	t.English = strings.TrimSpace(t.English)
	for _, s := range []string{t.Russian, t.English} {
		if !utf8.ValidString(s) || len(s) > limit || strings.ContainsRune(s, 0) {
			return t, false
		}
	}
	if t.English == "" {
		t.English = t.Russian
	}
	if t.Russian == "" {
		t.Russian = t.English
	}
	return t, t.English != ""
}

// CreateMailboxNews atomically publishes one global, text-only news item.
// Dates and IDs are server-owned; no account state, rewards or balances change.
func (s *Store) CreateMailboxNews(d MailboxNewsDraft) (MailboxNews, error) {
	var ok bool
	if d.Header, ok = normalizeMailboxText(d.Header, 512); !ok {
		return MailboxNews{}, ErrInvalidMailboxNews
	}
	if d.ShortDescription, ok = normalizeMailboxText(d.ShortDescription, 2048); !ok {
		return MailboxNews{}, ErrInvalidMailboxNews
	}
	if d.Description, ok = normalizeMailboxText(d.Description, 16384); !ok {
		return MailboxNews{}, ErrInvalidMailboxNews
	}
	switch d.Type {
	case "DeveloperMessage", "Entertainment", "Update", "Upcoming", "Contest":
	default:
		return MailboxNews{}, ErrInvalidMailboxNews
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.state.MailboxNews) >= MailboxNewsLimit {
		return MailboxNews{}, ErrMailboxFull
	}
	id := 1
	for _, n := range s.state.MailboxNews {
		if n.ID >= id {
			id = n.ID + 1
		}
	}
	now := time.Now().Unix()
	n := MailboxNews{ID: id, MailboxNewsDraft: d, Date: now, Changed: int(now)}
	old := s.state.MailboxNews
	s.state.MailboxNews = append(s.state.MailboxNews, n)
	if err := s.persistLocked(); err != nil {
		s.state.MailboxNews = old
		return MailboxNews{}, err
	}
	return n, nil
}

// MailboxNews returns independent values in publication order.
func (s *Store) MailboxNews() []MailboxNews {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MailboxNews, len(s.state.MailboxNews))
	copy(out, s.state.MailboxNews)
	return out
}
