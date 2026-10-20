// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

var ErrStaleProgressRevision = errors.New("store: session must reload the current progress revision")

// ProgressWriteContext is supplied by the authenticated session, never by a
// payload player_id or client-provided revision. The comparison runs under the
// same lock as the write, closing the admin-edit/read/upload race.
type ProgressWriteContext struct {
	Device   string
	Revision uint64
}

func allowProfileClientWrite(p *Profile, contexts []ProgressWriteContext) (bool, error) {
	if len(contexts) == 0 {
		return true, nil
	} // legacy/internal callers
	ctx := contexts[0]
	if ctx.Revision != p.ProgressRevision {
		return false, ErrStaleProgressRevision
	}
	if p.ProgressRevision == 0 {
		return true, nil
	}
	_, pending := p.PendingProgressRevision(ctx.Device)
	if _, err := progressDeviceKey(ctx.Device); err != nil {
		return false, ErrStaleProgressRevision
	}
	// Boot uploads predate the server download. Preserve the ENTIRE admin
	// snapshot, not just money/XP. Login after sync_full unlocks gameplay.
	return !pending, nil
}

// reconcileProfileCounters removes the old flat-document override of newer
// gameplay projections. All channels now expose the same money, level and XP.
func reconcileProfileCounters(p *Profile) {
	if p.Progress == nil {
		p.Progress = map[string]any{}
	}
	p.Progress["level"], p.Progress["experience"] = p.Level, p.Experience
	p.Progress["coins"], p.Progress["gems"] = p.Coins, p.Gems
	if _, ok := p.Progress["exp"]; ok {
		p.Progress["exp"] = p.Experience
	}
	for key, value := range map[string]any{"level": p.Level, "experience": p.Experience, "exp": p.Experience, "coins": p.Coins, "gems": p.Gems} {
		if _, ok := counterInt64(p.ClientState[key]); ok {
			p.ClientState[key] = value
		}
	}
	// Do not invent slots for a new/empty profile or change raw JSON number
	// representations. Only reconcile core slots already present in a channel.
	for _, document := range []map[string]any{p.ProgressDocument, p.ProgressSlots} {
		if slot, ok := document["3"].(map[string]any); ok {
			setMatchingCounter(slot, "Coins", p.Coins)
			setMatchingCounter(slot, "GemsCurrency", p.Gems)
		}
		if slot, ok := document["4"].(map[string]any); ok {
			setMatchingCounter(slot, "l", int64(p.Level))
			setMatchingCounter(slot, "e", p.Experience)
			for key, value := range map[string]int64{"level": int64(p.Level), "experience": p.Experience, "exp": p.Experience} {
				if _, exists := slot[key]; exists {
					setMatchingCounter(slot, key, value)
				}
			}
		}
	}
}

func setMatchingCounter(slot map[string]any, key string, want int64) {
	if value, ok := counterInt64(slot[key]); ok && value == want {
		return
	}
	slot[key] = want
}

// ApplyProfileCounterCommands atomically persists measured counters AND inventory
// commands before acknowledgement. Its name is retained for API compatibility.
// A purchase charge and its ownership change share one candidate and one fsync.
// This compatibility reducer is not price validation or an anticheat system.
func (s *Store) ApplyProfileCounterCommands(playerID string, ctx ProgressWriteContext, commands []any) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(strings.TrimSpace(playerID))
	p, ok := s.state.Profiles[playerID]
	if !ok {
		return 0, ErrNotFound
	}
	allowed, err := allowProfileClientWrite(p, []ProgressWriteContext{ctx})
	if err != nil || !allowed {
		return 0, err
	}
	device, err := progressDeviceKey(ctx.Device)
	if err != nil {
		return 0, err
	}
	// Work on a private candidate: malformed commands, underflow and persistence
	// failures must not partially apply a batch or record successful receipts.
	candidate := p.clone()
	if candidate.ProgressCommandReceipts == nil {
		candidate.ProgressCommandReceipts = map[string]bool{}
	}
	if candidate.ProgressSlots == nil {
		candidate.ProgressSlots = map[string]any{}
	}
	currencies, _ := candidate.ProgressSlots["3"].(map[string]any)
	if currencies == nil {
		currencies = map[string]any{"Coins": candidate.Coins, "GemsCurrency": candidate.Gems}
		candidate.ProgressSlots["3"] = currencies
	}
	applied := 0
	for _, raw := range commands {
		command, ok := raw.(map[string]any)
		if !ok {
			return 0, errors.New("store: invalid progress command")
		}
		id, ok := counterInt64(command["id"])
		if !ok {
			return 0, errors.New("store: invalid progress command id")
		}
		if !SupportsProgressCommand(id) {
			continue
		}
		ci, _ := command["ci"].(string)
		if ci == "" || len(ci) > 128 {
			return 0, errors.New("store: counter command requires a bounded ci")
		}
		data, ok := command["p"].(map[string]any)
		if !ok {
			return 0, errors.New("store: invalid counter command payload")
		}
		// Include the content digest to avoid accidental collisions in stock's short
		// child ids; retries retain both ci and content, even in a repacked batch.
		encoded, err := json.Marshal(struct {
			ID   int64
			Data map[string]any
		}{id, data})
		if err != nil {
			return 0, err
		}
		digest := sha256.Sum256(append([]byte(device+":"+ci+":"), encoded...))
		key := fmt.Sprintf("%x", digest[:])
		if candidate.ProgressCommandReceipts[key] {
			continue
		}
		// Never silently evict receipts and make a replay charge money twice.
		if len(candidate.ProgressCommandReceipts) >= 100000 {
			return 0, errors.New("store: counter receipt limit reached; operator review required")
		}
		switch id {
		case 10, 66:
			name, _ := data["c"].(string)
			if name == "" || len(name) > 128 || strings.TrimSpace(name) != name {
				return 0, errors.New("store: invalid currency name")
			}
			amount, valid := counterInt64(data["v"])
			if !valid {
				return 0, errors.New("store: invalid currency amount")
			}
			current, exists := currencies[name]
			value := int64(0)
			if exists {
				var valid bool
				value, valid = counterInt64(current)
				if !valid {
					return 0, errors.New("store: invalid stored currency")
				}
			}
			if id == 10 {
				if amount > math.MaxInt64-value {
					return 0, errors.New("store: currency overflow")
				}
				value += amount
			} else {
				if amount > value {
					return 0, errors.New("store: insufficient stored currency")
				}
				value -= amount
			}
			currencies[name] = value
			if name == "Coins" {
				candidate.Coins = value
			}
			if name == "GemsCurrency" {
				candidate.Gems = value
			}
		case 64:
			value, valid := counterInt64(data["l"])
			if !valid || int64(int(value)) != value {
				return 0, errors.New("store: invalid level")
			}
			candidate.Level = int(value)
		case 65:
			value, valid := counterInt64(data["e"])
			if !valid {
				return 0, errors.New("store: invalid experience")
			}
			candidate.Experience = value
		default:
			if err := applyInventoryCommand(&candidate, id, data); err != nil {
				return 0, err
			}
		}
		candidate.ProgressCommandReceipts[key] = true
		applied++
	}
	if applied == 0 {
		return 0, nil
	}
	if candidate.ProgressDocument == nil {
		candidate.ProgressDocument = map[string]any{}
	}
	if _, ok := candidate.ProgressSlots["4"].(map[string]any); !ok {
		candidate.ProgressSlots["4"] = map[string]any{"l": candidate.Level, "e": candidate.Experience}
	}
	if _, ok := candidate.ProgressDocument["4"].(map[string]any); !ok {
		candidate.ProgressDocument["4"] = map[string]any{"l": candidate.Level, "e": candidate.Experience}
	}
	reconcileProfileCounters(&candidate)
	// Carry non-base currency balances into both full-sync channels too.
	candidate.ProgressDocument["3"] = cloneAnyMap(currencies)
	mirrorRawSlots(&candidate)
	candidate.UpdatedAt = time.Now().UTC()
	before := *p
	*p = candidate
	if err := s.persistLocked(); err != nil {
		*p = before
		return 0, err
	}
	return applied, nil
}
