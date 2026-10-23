// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"bytes"
	"encoding/json"
	"errors"
)

// EconomyObservation describes evidence coverage, NOT a cheating verdict. It
// deliberately contains no payloads, item names, device IDs, IDs supplied by a
// command, credentials or free-form errors. Unverified operations remain enabled.
type EconomyObservation struct {
	Player                                            string
	Revision                                          uint64
	Channel                                           string
	Outcome                                           string
	Rule                                              string
	Verified, Unverified, Replayed, ChangedSlots      int
	CountersChanged, OpaqueChanged                    bool
	Currency, Inventory, Progression, Combat, Loadout int
}

// SetEconomyObserver installs a best-effort diagnostic sink. It runs after the
// state lock is released; a failed journal cannot change a gameplay decision.
// Production installs this in api.New. Store-only offline tools have no sink.
func (s *Store) SetEconomyObserver(sink func(EconomyObservation)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.economyObserver = sink
}

func economyRule(err error) string {
	switch {
	case errors.Is(err, ErrMailboxClaim):
		return "mailbox"
	case errors.Is(err, errExchange):
		return "exchange"
	case errors.Is(err, errPass), errors.Is(err, ErrPixelPassStateConflict):
		return "pixelpass"
	case errors.Is(err, ErrStaleProgressRevision):
		return "revision"
	default:
		return "validation_or_storage"
	}
}

// RequiresEconomyResync covers only proven server-backed transaction conflicts.
// Other pass errors retain their existing behavior: malformed catalog requests
// are not grounds for scheduling a download on every request.
func RequiresEconomyResync(err error) bool {
	return errors.Is(err, ErrMailboxClaim) || errors.Is(err, errExchange) || errors.Is(err, ErrPixelPassStateConflict)
}

// finishEconomyObservation must replace (not accompany) defer mu.Unlock(). The
// pointer is populated only for authenticated client calls, never admin writes.
func (s *Store) finishEconomyObservation(event **EconomyObservation, result *error) {
	sink := s.economyObserver
	e := *event
	if e != nil && *result != nil {
		e.Outcome, e.Rule = "rejected", economyRule(*result)
	}
	s.mu.Unlock()
	if sink != nil && e != nil {
		sink(*e)
	}
}

func sameEconomyJSON(a, b any) bool {
	x, xe := json.Marshal(a)
	y, ye := json.Marshal(b)
	return xe == nil && ye == nil && bytes.Equal(x, y)
}

// All snapshot channels are observed, including opaque prefs/client fields.
// Uploads are not proof of purchases. Do not invent ownership/prices or reject
// legitimate stock boot snapshots during this explicitly gradual rollout.
func snapshotObservation(p *Profile, channel string, before, incoming map[string]any, replace bool) *EconomyObservation {
	e := &EconomyObservation{Player: p.PlayerID, Revision: p.ProgressRevision, Channel: channel, Outcome: "observed", Rule: "unverified_snapshot"}
	keys := map[string]bool{}
	for k := range incoming {
		keys[k] = true
	}
	if replace {
		for k := range before {
			keys[k] = true
		}
	}
	for k := range keys {
		old, hasOld := before[k]
		next, hasNext := incoming[k]
		if hasOld == hasNext && sameEconomyJSON(old, next) {
			continue
		}
		switch k {
		case "3", "4", "coins", "gems", "level", "experience", "exp":
			e.CountersChanged = true
		// Measured inventory, loadout and server-owned claim projections. We only
		// count differences, never inspect or log arbitrary uploaded strings.
		case "9", "10", "11", "13", "16", "24", "26", "27", "28", "29", "33", "37", "42", "43", "44", "53", "67", "74":
			e.ChangedSlots++
		default:
			e.OpaqueChanged = true
		}
	}
	if !e.CountersChanged && e.ChangedSlots == 0 && !e.OpaqueChanged {
		return nil
	}
	return e
}
