// SPDX-License-Identifier: GPL-3.0-or-later
package store

import "time"

// RecordGameSeen records server-observed game activity, separate from HTTP/web
// authentication. Routine heartbeats are coalesced to at most one write/minute;
// clean disconnects flush the exact observation. It never changes progress.
func (s *Store) RecordGameSeen(id string, observed time.Time, flush bool) error {
	if observed.IsZero() || observed.After(time.Now().UTC()) {
		return ErrSocialInvalid
	}
	observed = observed.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.state.Accounts[s.resolvePlayerIDLocked(id)]
	if a == nil {
		return ErrNotFound
	}
	previous := a.GameLastSeenAt
	if !observed.After(previous) || (!flush && observed.Sub(previous) < time.Minute) {
		return nil
	}
	a.GameLastSeenAt = observed
	if err := s.persistLocked(); err != nil {
		a.GameLastSeenAt = previous
		return err
	}
	return nil
}
