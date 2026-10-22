// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"strings"
	"time"
)

// RequestProfileProgressResync schedules the existing correlated full-download
// handshake without editing gameplay data. Unlike an operator replacement it
// MUST preserve command receipts: a reload must never make paid retries charge
// again. Repeated requests from a pending/stale session reuse the same revision.
func (s *Store) RequestProfileProgressResync(playerID string, ctx ProgressWriteContext) (Profile, error) {
	if _, err := progressDeviceKey(ctx.Device); err != nil {
		return Profile{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(strings.TrimSpace(playerID))
	p, ok := s.state.Profiles[playerID]
	if !ok {
		return Profile{}, ErrNotFound
	}
	if _, pending := p.PendingProgressRevision(ctx.Device); pending {
		return p.clone(), nil
	}
	if ctx.Revision != p.ProgressRevision {
		return Profile{}, ErrStaleProgressRevision
	}
	before := p.clone()
	p.ProgressRevision = nextProgressRevision(p.ProgressRevision)
	p.AppliedProgressRevisions = nil
	p.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		*p = before
		return Profile{}, err
	}
	return p.clone(), nil
}
