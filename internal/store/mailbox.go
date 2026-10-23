// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"fmt"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
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
	Header           MailboxText    `json:"header"`
	ShortDescription MailboxText    `json:"short_description"`
	Description      MailboxText    `json:"description"`
	Type             string         `json:"type"`
	PublishAt        *time.Time     `json:"publish_at,omitempty"`
	Rewards          MailboxRewards `json:"rewards,omitempty"`
}
type MailboxNews struct {
	ID int `json:"id"`
	MailboxNewsDraft
	Date              int64 `json:"date"`
	Changed           int   `json:"ctime_change"`
	Archived          bool  `json:"archived,omitempty"`
	RewardWireVersion int   `json:"reward_wire_version,omitempty"`
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

type MailboxRewards struct {
	Coins  int64 `json:"coins,omitempty"`
	Gems   int64 `json:"gems,omitempty"`
	Weapon int   `json:"weapon,omitempty"`
}

// Stock 23.1.3 parses numeric currency catalog IDs (1015/2015), and even
// two-part named tokens, as OfferItemType.Currency (1200). The mailbox currency
// grid accepts only Coins (1010) / Gems (1000). Three-part enum tokens select
// those types; the empty ID is normalized by OfferItem's constructor.
func (r MailboxRewards) Tokens() []string {
	out := []string{}
	if r.Coins > 0 {
		out = append(out, fmt.Sprintf("Coins::%d", r.Coins))
	}
	if r.Gems > 0 {
		out = append(out, fmt.Sprintf("Gems::%d", r.Gems))
	}
	if r.Weapon > 0 {
		out = append(out, fmt.Sprintf("%d:1", r.Weapon))
	}
	return out
}
func (n MailboxNews) VisibleAt(now time.Time) bool {
	return !n.Archived && (n.PublishAt == nil || !now.Before(*n.PublishAt))
}

// Shared normalization prevents create/update validation from drifting.
func normalizeMailboxDraft(d MailboxNewsDraft) (MailboxNewsDraft, error) {
	var ok bool
	if d.Header, ok = normalizeMailboxText(d.Header, 512); !ok {
		return d, ErrInvalidMailboxNews
	}
	if d.ShortDescription, ok = normalizeMailboxText(d.ShortDescription, 2048); !ok {
		return d, ErrInvalidMailboxNews
	}
	if d.Description, ok = normalizeMailboxText(d.Description, 16384); !ok {
		return d, ErrInvalidMailboxNews
	}
	switch d.Type {
	case "DeveloperMessage", "Entertainment", "Update", "Upcoming", "Contest":
	default:
		return d, ErrInvalidMailboxNews
	}
	r := d.Rewards
	if r.Coins < 0 || r.Coins > 10000000 || r.Gems < 0 || r.Gems > 10000000 || r.Weapon < 0 {
		return d, ErrInvalidMailboxNews
	}
	if r.Weapon != 0 {
		if _, ok := catalog.Find(10, r.Weapon); !ok {
			return d, ErrInvalidMailboxNews
		}
	}
	if len(r.Tokens()) > 0 && d.Type != "DeveloperMessage" {
		return d, ErrInvalidMailboxNews
	}
	if d.PublishAt != nil {
		t := d.PublishAt.UTC().Truncate(time.Second)
		if t.Unix() < 1 || t.Unix() > 2147483647 {
			return d, ErrInvalidMailboxNews
		}
		d.PublishAt = &t
	}
	return d, nil
}

// CreateMailboxNews stores a global announcement. Gifts are granted only by
// an authenticated stock command 187, never on creation or on message fetch.
func (s *Store) CreateMailboxNews(d MailboxNewsDraft) (MailboxNews, error) {
	d, err := normalizeMailboxDraft(d)
	if err != nil {
		return MailboxNews{}, err
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
	if len(d.Rewards.Tokens()) > 0 {
		n.RewardWireVersion = mailboxRewardWireVersion
	}
	if d.PublishAt != nil {
		n.Date = d.PublishAt.Unix()
	}
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
	for i := range out {
		if out[i].PublishAt != nil {
			t := *out[i].PublishAt
			out[i].PublishAt = &t
		}
	}
	return out
}

// Archive never reuses IDs or deletes receipts. Unclaimed gifts become unavailable.
func (s *Store) ArchiveMailboxNews(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.MailboxNews {
		n := &s.state.MailboxNews[i]
		if n.ID != id {
			continue
		}
		if n.Archived {
			return nil
		}
		old := *n
		n.Archived = true
		n.Changed = int(time.Now().Unix())
		if err := s.persistLocked(); err != nil {
			*n = old
			return err
		}
		return nil
	}
	return ErrNotFound
}

// Version 2 retains id_reward and fixes stock Coins/Gems enum tokens. Bump cached gift bodies
// exactly once, retaining message/reward IDs, publication times and all claims.
const mailboxRewardWireVersion = 2

func migrateMailboxRewardWire(news []MailboxNews, now time.Time) bool {
	changed := false
	for i := range news {
		n := &news[i]
		if len(n.Rewards.Tokens()) == 0 || n.RewardWireVersion >= mailboxRewardWireVersion {
			continue
		}
		version := int(now.Unix())
		if version <= n.Changed {
			version = n.Changed + 1
		}
		n.Changed = version
		n.RewardWireVersion = mailboxRewardWireVersion
		changed = true
	}
	return changed
}

var ErrMailboxConflict = errors.New("store: mailbox news revision conflict")
var ErrMailboxArchived = errors.New("store: archived mailbox news cannot be edited")
var ErrMailboxDeliveryLocked = errors.New("store: published or claimed mailbox delivery cannot change")

// AdminMailboxNews includes server-computed edit permissions, not game wire fields.
type AdminMailboxNews struct {
	MailboxNews
	DeliveryEditable bool `json:"delivery_editable"`
}

func mailboxDeliveryEditable(n MailboxNews, now time.Time, claimed bool) bool {
	return !n.Archived && n.PublishAt != nil && now.Before(*n.PublishAt) && !claimed
}

func (s *Store) MailboxNewsForAdmin() []AdminMailboxNews {
	s.mu.RLock()
	defer s.mu.RUnlock()
	claimed := map[string]bool{}
	for _, p := range s.state.Profiles {
		for id, yes := range p.MailboxClaims {
			if yes {
				claimed[id] = true
			}
		}
	}
	now := time.Now()
	out := make([]AdminMailboxNews, 0, len(s.state.MailboxNews))
	for _, n := range s.state.MailboxNews {
		editable := mailboxDeliveryEditable(n, now, claimed[fmt.Sprint(n.ID)])
		if n.PublishAt != nil {
			at := *n.PublishAt
			n.PublishAt = &at
		}
		out = append(out, AdminMailboxNews{MailboxNews: n, DeliveryEditable: editable})
	}
	return out
}

func mailboxSameSchedule(a, b *time.Time) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && a.Equal(*b))
}

// UpdateMailboxNews atomically replaces editable fields, never identity/receipts.
// Scheduling and gift changes are allowed only BEFORE first publication, and
// only if nobody has claimed. Published letters cannot be moved back into the
// future to bypass this lock. Optimistic revisions reject stale admin forms.
func (s *Store) UpdateMailboxNews(id, expectedChanged int, d MailboxNewsDraft) (MailboxNews, error) {
	if id < 1 || expectedChanged < 1 {
		return MailboxNews{}, ErrInvalidMailboxNews
	}
	d, err := normalizeMailboxDraft(d)
	if err != nil {
		return MailboxNews{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.MailboxNews {
		old := s.state.MailboxNews[i]
		if old.ID != id {
			continue
		}
		if old.Archived {
			return MailboxNews{}, ErrMailboxArchived
		}
		if old.Changed != expectedChanged {
			return MailboxNews{}, ErrMailboxConflict
		}
		sameSchedule := mailboxSameSchedule(old.PublishAt, d.PublishAt)
		now := time.Now()
		if old.Rewards != d.Rewards || !sameSchedule {
			claimed := false
			for _, p := range s.state.Profiles {
				if p.MailboxClaims[fmt.Sprint(id)] {
					claimed = true
					break
				}
			}
			if !mailboxDeliveryEditable(old, now, claimed) {
				return MailboxNews{}, ErrMailboxDeliveryLocked
			}
		}
		if old.Header == d.Header && old.ShortDescription == d.ShortDescription &&
			old.Description == d.Description && old.Type == d.Type && old.Rewards == d.Rewards && sameSchedule {
			if old.PublishAt != nil {
				at := *old.PublishAt
				old.PublishAt = &at
			}
			return old, nil
		}
		next := old
		next.MailboxNewsDraft = d
		next.Changed = int(now.Unix())
		if next.Changed <= old.Changed {
			next.Changed = old.Changed + 1
		}
		if !sameSchedule {
			next.Date = now.Unix()
			if d.PublishAt != nil {
				next.Date = d.PublishAt.Unix()
			}
		}
		if len(d.Rewards.Tokens()) > 0 {
			next.RewardWireVersion = mailboxRewardWireVersion
		}
		s.state.MailboxNews[i] = next
		if err := s.persistLocked(); err != nil {
			s.state.MailboxNews[i] = old
			return MailboxNews{}, err
		}
		if next.PublishAt != nil {
			at := *next.PublishAt
			next.PublishAt = &at
		}
		return next, nil
	}
	return MailboxNews{}, ErrNotFound
}
