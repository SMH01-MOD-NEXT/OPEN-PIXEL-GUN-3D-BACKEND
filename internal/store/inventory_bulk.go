// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
)

// Bulk actions never reset the entire profile and never invent ownership adapters.
// Grant adds missing weapons only. Revoke leaves equipped and unsupported items.
type InventoryBulkEdit struct {
	Action          string    `json:"action"`
	Level           int64     `json:"level"`
	Rarity          int64     `json:"rarity"`
	Revision        uint64    `json:"revision"`
	UpdatedAt       time.Time `json:"updated_at"`
	PlanToken       string    `json:"plan_token,omitempty"`
	ConfirmPlayerID string    `json:"confirm_player_id,omitempty"`
}
type InventoryBulkPlan struct {
	PlayerID           string         `json:"player_id"`
	Action             string         `json:"action"`
	Revision           uint64         `json:"revision"`
	UpdatedAt          time.Time      `json:"updated_at"`
	Changes            int            `json:"changes"`
	PreservedOwned     int            `json:"preserved_owned"`
	SkippedEquipped    int            `json:"skipped_equipped"`
	SkippedUnsupported int            `json:"skipped_unsupported"`
	Sample             []catalog.Item `json:"sample"`
	PlanToken          string         `json:"plan_token"`
}

func inventoryBulkPlan(p Profile, e InventoryBulkEdit) (InventoryBulkPlan, []catalog.Item, error) {
	plan := InventoryBulkPlan{PlayerID: p.PlayerID, Action: e.Action, Revision: p.ProgressRevision, UpdatedAt: p.UpdatedAt, Sample: []catalog.Item{}}
	if e.Action != "grant_weapons" && e.Action != "revoke_weapons" && e.Action != "revoke_all" {
		return plan, nil, errors.New("store: invalid bulk action")
	}
	if e.Level < 1 || e.Level > 65 || e.Rarity < 0 || e.Rarity > 5 {
		return plan, nil, errors.New("store: invalid bulk level or rarity")
	}
	if e.Revision != p.ProgressRevision || !e.UpdatedAt.Equal(p.UpdatedAt) {
		return plan, nil, ErrInventoryConflict
	}
	changes := []catalog.Item{}
	if e.Action == "grant_weapons" {
		for _, item := range catalog.All() {
			if item.Type != 10 {
				continue
			}
			// Require a stock weapon definition as well as a registry index.
			if !item.Editable || item.WeaponCategory == nil {
				plan.SkippedUnsupported++
				continue
			}
			q, _, _ := inventoryValue(p, item)
			if q > 0 {
				plan.PreservedOwned++
				continue
			}
			changes = append(changes, item)
		}
	} else {
		for _, item := range Inventory(p) {
			if e.Action == "revoke_weapons" && item.Type != 10 {
				continue
			}
			if !item.Editable {
				plan.SkippedUnsupported++
				continue
			}
			if item.Equipped {
				plan.SkippedEquipped++
				continue
			}
			changes = append(changes, item.Item)
		}
	}
	plan.Changes = len(changes)
	for _, v := range changes {
		if len(plan.Sample) < 8 {
			plan.Sample = append(plan.Sample, v)
		}
	}
	data, _ := json.Marshal(struct {
		Player string
		Edit   InventoryBulkEdit
		Items  []catalog.Item
	}{p.PlayerID, InventoryBulkEdit{Action: e.Action, Level: e.Level, Rarity: e.Rarity, Revision: e.Revision, UpdatedAt: e.UpdatedAt}, changes})
	sum := sha256.Sum256(data)
	plan.PlanToken = hex.EncodeToString(sum[:])
	return plan, changes, nil
}
func (s *Store) PreviewInventoryBulk(playerID string, e InventoryBulkEdit) (InventoryBulkPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(strings.TrimSpace(playerID))
	p, ok := s.state.Profiles[playerID]
	if !ok {
		return InventoryBulkPlan{}, ErrNotFound
	}
	plan, _, err := inventoryBulkPlan(*p, e)
	return plan, err
}
func (s *Store) EditInventoryBulk(playerID string, e InventoryBulkEdit) (Profile, InventoryBulkPlan, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(strings.TrimSpace(playerID))
	p, ok := s.state.Profiles[playerID]
	if !ok {
		return Profile{}, InventoryBulkPlan{}, false, ErrNotFound
	}
	plan, items, err := inventoryBulkPlan(*p, e)
	if err != nil {
		return Profile{}, plan, false, err
	}
	if e.ConfirmPlayerID != p.PlayerID || len(e.PlanToken) != 64 || subtle.ConstantTimeCompare([]byte(e.PlanToken), []byte(plan.PlanToken)) != 1 {
		return Profile{}, plan, false, errors.New("store: bulk preview and exact player confirmation required")
	}
	if len(items) == 0 {
		return p.clone(), plan, false, nil
	}
	candidate := p.clone()
	if candidate.ProgressSlots == nil {
		candidate.ProgressSlots = map[string]any{}
	}
	action := "revoke"
	if e.Action == "grant_weapons" {
		action = "grant"
	}
	for _, item := range items {
		if err := editInventorySlots(&candidate, item, InventoryEdit{Action: action, Quantity: 1, Level: e.Level, Rarity: e.Rarity}); err != nil {
			return Profile{}, plan, false, err
		}
	}
	if reflect.DeepEqual(candidate.ProgressSlots, p.ProgressSlots) {
		return p.clone(), plan, false, nil
	}
	mirrorRawSlots(&candidate)
	advanceProfileProgressRevision(&candidate)
	candidate.UpdatedAt = time.Now().UTC()
	before := *p
	*p = candidate
	if err := s.persistLocked(); err != nil {
		*p = before
		return Profile{}, plan, false, fmt.Errorf("%w: %v", ErrInventoryPersistence, err)
	}
	return p.clone(), plan, true, nil
}
