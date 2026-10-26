// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"math"
	"sort"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
)

// Fort, tank, storage and building state shared by a clan. The native client
// computes layouts, power and prices itself (its configs ship the balance), so
// the server validates identities, permissions and bounds and then becomes
// the single source of truth that every member's client merges.
const (
	ClanFieldCellLimit = 512
	ClanDecorLimit     = 256
	ClanTankSlotLimit  = 64
	ClanItemKindLimit  = 1024
	ClanItemCountMax   = 1_000_000
	ClanItemLevelMax   = 100
	ClanUpgradeLimit   = 64
	ClanBuildingLimit  = 64
	ClanWarMarkLimit   = 256
	clanValueMax       = 1_000_000_000
	clanCoordinateMax  = 10_000
)

// Native catalog types for clan content (internal/catalog/stock2313.json).
const (
	catalogClanPlaceable      = 1240
	catalogClanPlaceablePoint = 1250
	catalogFortItem           = 90
	catalogClanBuilding       = 1360
	catalogClanBuildingPoint  = 1370
)

func validClanPlaceable(index int) bool {
	_, ok := catalog.Find(catalogClanPlaceable, index)
	return ok
}

func validClanStorageItem(index int) bool {
	for _, t := range []int{catalogClanPlaceable, catalogClanPlaceablePoint, catalogFortItem} {
		if _, ok := catalog.Find(t, index); ok {
			return true
		}
	}
	return false
}

func validClanBuilding(index int) bool {
	for _, t := range []int{catalogClanBuilding, catalogClanBuildingPoint} {
		if _, ok := catalog.Find(t, index); ok {
			return true
		}
	}
	return false
}

// validFieldSize mirrors PGCompany SquareFieldCellSize.
func validFieldSize(size int) bool {
	switch size {
	case 0, 1, 2, 21, 3, 31, 32, 4, 41, 42, 43:
		return true
	}
	return false
}

func finiteCoordinate(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= clanCoordinateMax
}

func clanValue(v int) bool { return v >= 0 && v <= clanValueMax }

// clanEditLocked resolves actor's clan for a layout edit (leader/officer).
func (s *Store) clanEditLocked(actor, clanID string, officer bool) (*Clan, error) {
	if officer {
		return s.officerClanLocked(actor, clanID)
	}
	return s.memberClanLocked(actor, clanID)
}

func (s *Store) clanEdit(actor, clanID string, officer bool, fn func(c *Clan, actor string, now time.Time) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	actor, err := s.clanActorLocked(actor, now, "edit")
	if err != nil {
		return err
	}
	return s.mutateClansLocked(now, func() error {
		c, err := s.clanEditLocked(actor, clanID, officer)
		if err != nil {
			return err
		}
		return fn(c, actor, now)
	})
}

// rebuildNoticeLocked posts one fort/tank rebuild notice per editor burst.
func (s *Store) rebuildNoticeLocked(c *Clan, actor string, action, power int, now time.Time) {
	for i := len(c.Service) - 1; i >= 0; i-- {
		m := c.Service[i]
		if m.Action == action && m.From == actor && m.Stamp >= now.Add(-5*time.Minute).UnixMicro() {
			c.Service[i].Power = power
			return
		}
	}
	s.clanServiceLocked(c, now, ClanServiceMessage{Action: action, From: actor, Power: power})
}

// UpdateClanField replaces the fort layout (update_clan_field).
func (s *Store) UpdateClanField(actor, clanID string, cells []ClanFieldCell, power int) error {
	if len(cells) > ClanFieldCellLimit || !clanValue(power) {
		return ErrClanInvalid
	}
	for _, cell := range cells {
		if !finiteCoordinate(cell.X) || !finiteCoordinate(cell.Y) || !finiteCoordinate(cell.Z) || !validFieldSize(cell.Size) || cell.Rotation < -360 || cell.Rotation > 360 || cell.Item != 0 && !validClanPlaceable(cell.Item) {
			return ErrClanInvalid
		}
	}
	return s.clanEdit(actor, clanID, true, func(c *Clan, actor string, now time.Time) error {
		c.Field = append([]ClanFieldCell{}, cells...)
		c.FieldPower = power
		s.rebuildNoticeLocked(c, actor, ClanServiceFortRebuild, power, now)
		return nil
	})
}

// UpdateClanDecor replaces the fort decoration list.
func (s *Store) UpdateClanDecor(actor, clanID string, decor []int) error {
	if len(decor) > ClanDecorLimit {
		return ErrClanInvalid
	}
	for _, index := range decor {
		if !validClanStorageItem(index) {
			return ErrClanInvalid
		}
	}
	return s.clanEdit(actor, clanID, true, func(c *Clan, _ string, _ time.Time) error {
		c.Decor = append([]int{}, decor...)
		return nil
	})
}

// UpdateClanTank replaces the tank loadout (update_clan_tank).
func (s *Store) UpdateClanTank(actor, clanID string, slots []ClanTankSlot, power int) error {
	if len(slots) > ClanTankSlotLimit || !clanValue(power) {
		return ErrClanInvalid
	}
	seen := map[int]bool{}
	for _, slot := range slots {
		if slot.Place < 0 || slot.Place > 10_000 || seen[slot.Place] || !validClanPlaceable(slot.Item) {
			return ErrClanInvalid
		}
		seen[slot.Place] = true
	}
	return s.clanEdit(actor, clanID, true, func(c *Clan, actor string, now time.Time) error {
		c.Tank = append([]ClanTankSlot{}, slots...)
		c.TankPower = power
		s.rebuildNoticeLocked(c, actor, ClanServiceTankRebuild, power, now)
		return nil
	})
}

// ClanBuildingLevels carries the optional set_clan_building_levels members.
type ClanBuildingLevels struct {
	FieldLevel, FieldPoints, TankLevel, TankPoints *int
}

// SetClanBuildingLevels stores fort/tank level progress reported by members.
func (s *Store) SetClanBuildingLevels(actor, clanID string, in ClanBuildingLevels) error {
	for _, v := range []*int{in.FieldLevel, in.TankLevel} {
		if v != nil && (*v < 0 || *v > ClanItemLevelMax) {
			return ErrClanInvalid
		}
	}
	for _, v := range []*int{in.FieldPoints, in.TankPoints} {
		if v != nil && !clanValue(*v) {
			return ErrClanInvalid
		}
	}
	return s.clanEdit(actor, clanID, false, func(c *Clan, _ string, _ time.Time) error {
		if in.FieldLevel != nil {
			c.FieldLevel = *in.FieldLevel
		}
		if in.FieldPoints != nil {
			c.FieldPoints = *in.FieldPoints
		}
		if in.TankLevel != nil {
			c.TankLevel = *in.TankLevel
		}
		if in.TankPoints != nil {
			c.TankPoints = *in.TankPoints
		}
		return nil
	})
}

func (c *Clan) item(index int) *ClanItem {
	for i := range c.Items {
		if c.Items[i].Index == index {
			return &c.Items[i]
		}
	}
	return nil
}

// ClanItemLevel returns the stored level of an item, 0 when absent.
func (c *Clan) ClanItemLevel(index int) int {
	if it := c.item(index); it != nil {
		return it.Level
	}
	return 0
}

// AddClanUpgrade starts an item upgrade timer (add_upgrading_item).
func (s *Store) AddClanUpgrade(actor, clanID string, up ClanUpgrade) error {
	if !validClanStorageItem(up.Item) || up.From < 0 || up.To <= up.From || up.To > ClanItemLevelMax || up.FinishAt < 0 || up.FinishAt > time.Now().Add(366*24*time.Hour).Unix() || up.Type < 0 || up.Type > 100 {
		return ErrClanInvalid
	}
	return s.clanEdit(actor, clanID, false, func(c *Clan, actor string, now time.Time) error {
		if c.ClanItemLevel(up.Item) != up.From {
			return ErrClanUpgrade
		}
		for _, existing := range c.Upgrades {
			if existing.Item == up.Item {
				return ErrClanUpgrade
			}
		}
		if len(c.Upgrades) >= ClanUpgradeLimit {
			return ErrClanFull
		}
		up.Player = actor
		if up.FinishAt < now.Unix() {
			up.FinishAt = now.Unix()
		}
		c.Upgrades = append(c.Upgrades, up)
		s.clanServiceLocked(c, now, ClanServiceMessage{Action: ClanServiceStartUpgrade, From: actor, ItemIndex: up.Item, ItemLevel: up.To})
		return nil
	})
}

// FinishClanUpgrade completes a running upgrade. An upgrade may finish early
// only when the client reports a paid speed-up (price > 0); it returns the
// item's resulting level.
func (s *Store) FinishClanUpgrade(actor, clanID string, item, from, to int, paid bool) (int, error) {
	level := 0
	err := s.clanEdit(actor, clanID, false, func(c *Clan, actor string, now time.Time) error {
		index := -1
		for i, up := range c.Upgrades {
			if up.Item == item {
				index = i
			}
		}
		if index < 0 {
			return ErrClanUpgrade
		}
		up := c.Upgrades[index]
		if up.From != from || up.To != to || !paid && up.FinishAt > now.Add(5*time.Second).Unix() {
			return ErrClanUpgrade
		}
		c.Upgrades = append(c.Upgrades[:index], c.Upgrades[index+1:]...)
		stack := c.item(item)
		if stack == nil {
			if len(c.Items) >= ClanItemKindLimit {
				return ErrClanFull
			}
			c.Items = append(c.Items, ClanItem{Index: item})
			stack = &c.Items[len(c.Items)-1]
		}
		stack.Level = to
		level = to
		s.clanServiceLocked(c, now, ClanServiceMessage{Action: ClanServiceFinishUpgrade, From: actor, ItemIndex: item, ItemLevel: to})
		return nil
	})
	return level, err
}

// CreateClanBuilding constructs (or re-levels) a clan building.
func (s *Store) CreateClanBuilding(actor, clanID string, building, level int) error {
	if !validClanBuilding(building) || level < 0 || level > ClanItemLevelMax {
		return ErrClanInvalid
	}
	return s.clanEdit(actor, clanID, true, func(c *Clan, _ string, _ time.Time) error {
		for i := range c.Buildings {
			if c.Buildings[i].Index == building {
				if level > c.Buildings[i].Level {
					c.Buildings[i].Level = level
				}
				c.Buildings[i].Built++
				return nil
			}
		}
		if len(c.Buildings) >= ClanBuildingLimit {
			return ErrClanFull
		}
		c.Buildings = append(c.Buildings, ClanBuilding{Index: building, Level: level, Built: 1})
		return nil
	})
}

// UnsetClanWarMarks removes war marks the client acknowledged.
func (s *Store) UnsetClanWarMarks(actor string, marks []int) error {
	if len(marks) > ClanWarMarkLimit {
		return ErrClanInvalid
	}
	clanID := s.PlayerClanID(actor)
	if clanID == "" {
		return ErrClanNotMember
	}
	return s.clanEdit(actor, clanID, false, func(c *Clan, _ string, _ time.Time) error {
		drop := map[int]bool{}
		for _, m := range marks {
			drop[m] = true
		}
		kept := []int{}
		for _, m := range c.WarMarks {
			if !drop[m] {
				kept = append(kept, m)
			}
		}
		c.WarMarks = kept
		return nil
	})
}

// ClanItemGrant is one CreateClanItems (progress command 182) entry, class
// 0x2494A58: {id:UniqueClientId, clan, item_index, count, level, buy,
// from_chest:chest index}.
type ClanItemGrant struct {
	UniqueID     string
	Clan         string
	Index, Count int
	Level        int
	Buy          bool
	FromChest    int
}

// ClanBuildingGrant is one ClanBuildingsAddItems (progress command 206)
// entry {i, c, sp, ci:clan, up, mu}.
type ClanBuildingGrant struct {
	Clan                string
	Index, Count, Spent int
	Upgrade, Multiplier int
}

// ClanProgressCommand is the clan side of one update_progress child command.
// Receipt is a stable digest of the command used to drop client retries.
type ClanProgressCommand struct {
	Receipt   string
	Items     []ClanItemGrant
	Buildings []ClanBuildingGrant
}

func validClanProgress(cmd ClanProgressCommand) bool {
	if len(cmd.Items) > 256 || len(cmd.Buildings) > 256 || len(cmd.Receipt) > 128 {
		return false
	}
	for _, g := range cmd.Items {
		if len(g.UniqueID) > 128 || !validClanStorageItem(g.Index) || g.Count < -ClanItemCountMax || g.Count > ClanItemCountMax || g.Level < 0 || g.Level > ClanItemLevelMax || g.FromChest < 0 || g.FromChest > clanValueMax {
			return false
		}
	}
	for _, g := range cmd.Buildings {
		if g.Index <= 0 || g.Index > math.MaxInt32 || g.Count < 0 || g.Count > ClanItemCountMax || !clanValue(g.Spent) || !clanValue(g.Upgrade) || !clanValue(g.Multiplier) {
			return false
		}
	}
	return true
}

// ApplyClanProgress mirrors the clan side of a player's update_progress batch:
// crafted, bought and chest items land in the shared storage and building
// contributions are credited. A command addressed to another clan is refused
// as a whole; retried commands are applied once. It returns how many commands
// changed the clan.
func (s *Store) ApplyClanProgress(actor string, commands []ClanProgressCommand) (int, error) {
	if len(commands) == 0 {
		return 0, nil
	}
	if len(commands) > 64 {
		return 0, ErrClanInvalid
	}
	for _, cmd := range commands {
		if !validClanProgress(cmd) {
			return 0, ErrClanInvalid
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	actor, err := s.clanActorLocked(actor, now, "edit")
	if err != nil {
		return 0, err
	}
	applied := 0
	err = s.mutateClansLocked(now, func() error {
		c := s.clanOfLocked(actor)
		if c == nil {
			return ErrClanNotMember
		}
		for _, cmd := range commands {
			for _, g := range cmd.Items {
				if g.Clan != "" && g.Clan != c.ID {
					return ErrClanForbidden
				}
			}
			for _, g := range cmd.Buildings {
				if g.Clan != "" && g.Clan != c.ID {
					return ErrClanForbidden
				}
			}
		}
		if c.Receipts == nil {
			c.Receipts = map[string]int64{}
		}
		member := c.member(actor)
		for _, cmd := range commands {
			if cmd.Receipt != "" {
				key := actor + ":" + cmd.Receipt
				if _, done := c.Receipts[key]; done {
					continue
				}
				c.Receipts[key] = now.Unix()
			}
			changed := false
			for _, g := range cmd.Items {
				if g.UniqueID != "" {
					key := actor + ":item:" + g.UniqueID
					if _, done := c.Receipts[key]; done {
						continue
					}
					c.Receipts[key] = now.Unix()
				}
				changed = true
				stack := c.item(g.Index)
				if stack == nil {
					if len(c.Items) >= ClanItemKindLimit {
						return ErrClanFull
					}
					c.Items = append(c.Items, ClanItem{Index: g.Index})
					stack = &c.Items[len(c.Items)-1]
				}
				stack.Count += g.Count
				if stack.Count < 0 {
					stack.Count = 0
				}
				if stack.Count > ClanItemCountMax {
					stack.Count = ClanItemCountMax
				}
				if g.Level > stack.Level {
					stack.Level = g.Level
				}
				if g.Count <= 0 {
					continue
				}
				_, detail := catalog.Find(catalogClanPlaceablePoint, g.Index)
				notice := ClanServiceMessage{From: actor, ItemIndex: g.Index, ItemLevel: stack.Level, Details: g.Count}
				switch {
				case g.FromChest > 0:
					notice.Action = ClanServiceDetailsFromChest
					notice.ChestIndex = g.FromChest
				case g.Buy && detail:
					notice.Action = ClanServiceBoughtDetails
				case g.Buy:
					notice.Action = ClanServiceBoughtFortItems
				default:
					notice.Action = ClanServiceItemCrafted
				}
				if detail && member != nil && member.Details <= clanValueMax-g.Count {
					member.Details += g.Count
				}
				s.clanServiceLocked(c, now, notice)
			}
			for _, g := range cmd.Buildings {
				changed = true
				index := -1
				for i := range c.Buildings {
					if c.Buildings[i].Index == g.Index {
						index = i
					}
				}
				if index < 0 {
					if len(c.Buildings) >= ClanBuildingLimit {
						return ErrClanFull
					}
					c.Buildings = append(c.Buildings, ClanBuilding{Index: g.Index})
					index = len(c.Buildings) - 1
				}
				b := &c.Buildings[index]
				if b.Count <= ClanItemCountMax-g.Count {
					b.Count += g.Count
				}
				if g.Upgrade > b.Level && g.Upgrade <= ClanItemLevelMax {
					b.Level = g.Upgrade
				}
				if b.Points == nil {
					b.Points = map[string]int{}
				}
				credit := g.Spent
				if credit == 0 {
					credit = g.Count
				}
				if b.Points[actor] <= clanValueMax-credit {
					b.Points[actor] += credit
				}
			}
			if changed {
				applied++
			}
		}
		sort.SliceStable(c.Items, func(i, j int) bool { return c.Items[i].Index < c.Items[j].Index })
		return nil
	})
	if err != nil {
		return 0, err
	}
	return applied, nil
}

// ActivateClanPromo records a paid clan promotion window. The stock client
// charges the promotion locally; the server extends the advertised window.
func (s *Store) ActivateClanPromo(actor, clanID string, duration time.Duration) (int64, error) {
	if duration <= 0 || duration > 30*24*time.Hour {
		return 0, ErrClanInvalid
	}
	var until int64
	err := s.clanEdit(actor, clanID, true, func(c *Clan, _ string, now time.Time) error {
		if c.PromoUntil > now.Unix() {
			return ErrClanPending
		}
		c.PromoUntil = now.Add(duration).Unix()
		until = c.PromoUntil
		return nil
	})
	return until, err
}
