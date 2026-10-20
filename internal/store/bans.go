// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"strings"
	"time"
)

var ErrEmptyBanSubject = errors.New("store: empty ban subject")

func copyBan(b Ban) Ban {
	if b.Until != nil {
		deadline := *b.Until
		b.Until = &deadline
	}
	return b
}
func copyBans(bans []Ban) []Ban {
	out := make([]Ban, len(bans))
	for i, b := range bans {
		out[i] = copyBan(b)
	}
	return out
}
func (s *Store) normalizeBanLocked(b Ban) (Ban, error) {
	switch b.Kind {
	case BanPlayer, BanDevice, BanIP:
	default:
		return Ban{}, ErrBadBanKind
	}
	b.Subject = strings.TrimSpace(b.Subject)
	if b.Subject == "" {
		return Ban{}, ErrEmptyBanSubject
	}
	if b.Kind == BanPlayer {
		b.Subject = s.resolvePlayerIDLocked(b.Subject)
	}
	return copyBan(b), nil
}

// AddBans validates the complete batch before mutation. Replacing a subject
// removes historical duplicates, so shortening a ban cannot leave a hidden
// permanent record behind. A failed durable write rolls memory back too.
func (s *Store) AddBans(bans []Ban) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	normalized := make([]Ban, len(bans))
	for i, b := range bans {
		var err error
		normalized[i], err = s.normalizeBanLocked(b)
		if err != nil {
			return err
		}
		if normalized[i].CreatedAt.IsZero() {
			normalized[i].CreatedAt = time.Now().UTC()
		}
	}
	old := s.state.Bans
	next := copyBans(old)
	for _, b := range normalized {
		kept := make([]Ban, 0, len(next)+1)
		for _, existing := range next {
			subject := existing.Subject
			if existing.Kind == BanPlayer {
				subject = s.resolvePlayerIDLocked(subject)
			}
			if existing.Kind != b.Kind || subject != b.Subject {
				kept = append(kept, existing)
			}
		}
		next = append(kept, b)
	}
	if len(normalized) == 0 {
		return nil
	}
	s.state.Bans = next
	if err := s.persistLocked(); err != nil {
		s.state.Bans = old
		return err
	}
	return nil
}

// RemoveBanSubjects atomically lifts exactly the listed subjects. IP bans
// must be explicitly named; unbanning one player must not lift a shared IP ban.
func (s *Store) RemoveBanSubjects(subjects []Ban) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make(map[[2]string]bool)
	for _, b := range subjects {
		b, err := s.normalizeBanLocked(b)
		if err != nil {
			return 0, err
		}
		keys[[2]string{b.Kind, b.Subject}] = true
	}
	old := s.state.Bans
	kept := make([]Ban, 0, len(old))
	for _, b := range old {
		subject := b.Subject
		if b.Kind == BanPlayer {
			subject = s.resolvePlayerIDLocked(subject)
		}
		if !keys[[2]string{b.Kind, subject}] {
			kept = append(kept, b)
		}
	}
	removed := len(old) - len(kept)
	if removed == 0 {
		return 0, nil
	}
	s.state.Bans = kept
	if err := s.persistLocked(); err != nil {
		s.state.Bans = old
		return 0, err
	}
	return removed, nil
}

// AccountByDevice is a read-only lookup of the native HTTP identity. Ban
// responses must not rotate a session or create another account.
func (s *Store) AccountByDevice(device string) (Account, bool) {
	device = strings.TrimSpace(device)
	if device == "" {
		return Account{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.state.Accounts {
		if a.DeviceID == device {
			return *a, true
		}
	}
	return Account{}, false
}
