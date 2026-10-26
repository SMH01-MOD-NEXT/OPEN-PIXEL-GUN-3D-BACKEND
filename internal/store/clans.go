// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Clans are server-owned shared state. The 23.1.3 client keeps a local model
// and merges compact sections pushed by the server (ParseSections 0x3DB785C),
// so every membership decision is made here and never trusted from a client
// snapshot. See docs/CLANS_23_1_3.md for the measured contracts.
const (
	ClanMemberLimit        = 20
	ClanMemberLimitMax     = 100
	ClanPendingLimit       = 50 // incoming requests and outgoing invites, each, per clan
	ClanPlayerPendingLimit = 20 // outgoing requests and incoming invites, each, per player
	ClanNameLimit          = 40
	ClanSloganLimit        = 200
	ClanLeaderMessageLimit = 500
	ClanRequestTextLimit   = 200
	ClanLogoLimit          = 16 << 10 // base64 characters
	ClanSearchLimit        = 20
	ClanLeaveCooldown      = 20 * time.Minute
	clanHistoryLimit       = 200
	clanHistoryTTL         = 30 * 24 * time.Hour
	clanPendingTTL         = 14 * 24 * time.Hour
)

// Native PGCompany.Clans role and type values.
const (
	ClanRoleNone    = -1
	ClanRoleSoldier = 0
	ClanRoleLeader  = 1
	ClanRoleOfficer = 2

	ClanTypePublic  = 1
	ClanTypePrivate = 2
)

// Native service message actions rendered by the clan chat.
const (
	ClanServiceJoined           = 1
	ClanServiceLeft             = 2
	ClanServiceBanished         = 3
	ClanServiceBoughtMedals     = 4
	ClanServiceItemCrafted      = 5
	ClanServiceBoughtFortItems  = 6
	ClanServiceBecomeOfficer    = 7
	ClanServiceBecomeSoldier    = 8
	ClanServiceBecomeLeader     = 9
	ClanServiceDetailsFromChest = 10
	ClanServiceItemUpgraded     = 11
	ClanServiceBoughtDetails    = 12
	ClanServiceStartUpgrade     = 13
	ClanServiceFinishUpgrade    = 14
	ClanServiceFortRebuild      = 15
	ClanServiceTankRebuild      = 16
	ClanServiceUpgradeAvailable = 17
)

var (
	ErrClanInvalid   = errors.New("clan: invalid request")
	ErrClanNotFound  = errors.New("clan: not found")
	ErrClanForbidden = errors.New("clan: not permitted")
	ErrClanNameTaken = errors.New("clan: name taken")
	ErrClanFull      = errors.New("clan: member limit reached")
	ErrClanMember    = errors.New("clan: player already belongs to a clan")
	ErrClanNotMember = errors.New("clan: player is not a member")
	ErrClanRank      = errors.New("clan: player rank below the clan minimum")
	ErrClanClosed    = errors.New("clan: clan accepts members by request only")
	ErrClanRate      = errors.New("clan: rate limited")
	ErrClanPending   = errors.New("clan: too many pending requests")
	ErrClanUpgrade   = errors.New("clan: upgrade state mismatch")
)

// ClanCooldownError reports the time left before the player may create, join
// or ask to join a clan again (native error 1710 carries it as `time`).
type ClanCooldownError struct{ Remaining time.Duration }

func (e *ClanCooldownError) Error() string {
	return fmt.Sprintf("clan: cooldown, %d s left", e.Seconds())
}

// Seconds is the remaining cooldown rounded up, never below one second.
func (e *ClanCooldownError) Seconds() int64 {
	sec := int64((e.Remaining + time.Second - 1) / time.Second)
	if sec < 1 {
		sec = 1
	}
	return sec
}

type ClanMember struct {
	ID       string `json:"id"`
	Role     int    `json:"role"`
	JoinedAt int64  `json:"joined_at"`
	WarScore int    `json:"war_score,omitempty"`
	GemsUp   int    `json:"gems_up,omitempty"`
	Details  int    `json:"details,omitempty"`
	RankExp  int    `json:"rank_exp,omitempty"`
}
type ClanRequest struct {
	Player  string `json:"player"`
	Message string `json:"message,omitempty"`
	Created int64  `json:"created"`
}
type ClanInvite struct {
	Player  string `json:"player"`
	From    string `json:"from,omitempty"`
	Created int64  `json:"created"`
}
type ClanMessage struct {
	Stamp       int64  `json:"stamp_us"`
	From        string `json:"from"`
	Text        string `json:"text"`
	ClientStamp string `json:"client_stamp,omitempty"`
}
type ClanServiceMessage struct {
	Stamp      int64  `json:"stamp_us"`
	Action     int    `json:"action"`
	From       string `json:"from,omitempty"`
	Target     string `json:"target,omitempty"`
	ChestIndex int    `json:"chest_index,omitempty"`
	ItemIndex  int    `json:"item_index,omitempty"`
	ItemLevel  int    `json:"item_level,omitempty"`
	Details    int    `json:"details,omitempty"`
	Power      int    `json:"power,omitempty"`
	Price      string `json:"price,omitempty"`
	Currency   int    `json:"currency,omitempty"`
	Info       string `json:"info,omitempty"`
}

// ClanItem is one stack in the shared clan storage (`clan_items` {i,c,l}).
type ClanItem struct {
	Index int `json:"i"`
	Count int `json:"c"`
	Level int `json:"l"`
}

// ClanFieldCell is one placed fort cell (`fl` {p:{x,y,z},i,r,s}).
type ClanFieldCell struct {
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Z        float64 `json:"z"`
	Item     int     `json:"i"`
	Rotation int     `json:"r"`
	Size     int     `json:"s"`
}

// ClanTankSlot is one placed tank module (`tn` {p,in}).
type ClanTankSlot struct {
	Place int `json:"p"`
	Item  int `json:"in"`
}

// ClanUpgrade is an item upgrade timer (`up` {i,f,fl}).
type ClanUpgrade struct {
	Item     int    `json:"i"`
	From     int    `json:"f"`
	To       int    `json:"t"`
	Type     int    `json:"type,omitempty"`
	FinishAt int64  `json:"fl"`
	Player   string `json:"player,omitempty"`
}

// ClanBuilding is one constructed clan building (`bn` {i, if:{c,l,bc,pp}}).
type ClanBuilding struct {
	Index  int            `json:"i"`
	Count  int            `json:"c"`
	Level  int            `json:"l"`
	Built  int            `json:"bc"`
	Points map[string]int `json:"pp,omitempty"`
}

type Clan struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Creator       string               `json:"creator_id"`
	Type          int                  `json:"type"`
	Slogan        string               `json:"slogan,omitempty"`
	Logo          string               `json:"logo,omitempty"`
	LogoHash      string               `json:"logo_hash,omitempty"`
	LeaderMessage string               `json:"leader_message,omitempty"`
	Continent     string               `json:"continent,omitempty"`
	MinRank       int                  `json:"min_rank,omitempty"`
	MemberLimit   int                  `json:"member_limit,omitempty"`
	SiegesWon     int                  `json:"sieges_won,omitempty"`
	Shields       int                  `json:"shields,omitempty"`
	Division      int                  `json:"division,omitempty"`
	WarWins       int                  `json:"war_wins,omitempty"`
	PromoUntil    int64                `json:"promo_until,omitempty"`
	CreatedAt     int64                `json:"created_at"`
	Members       []ClanMember         `json:"members"`
	Requests      []ClanRequest        `json:"requests,omitempty"`
	Invites       []ClanInvite         `json:"invites,omitempty"`
	Messages      []ClanMessage        `json:"messages,omitempty"`
	Service       []ClanServiceMessage `json:"service,omitempty"`
	Items         []ClanItem           `json:"items,omitempty"`
	Field         []ClanFieldCell      `json:"field,omitempty"`
	Decor         []int                `json:"decor,omitempty"`
	Tank          []ClanTankSlot       `json:"tank,omitempty"`
	FieldLevel    int                  `json:"field_level,omitempty"`
	FieldPoints   int                  `json:"field_points,omitempty"`
	FieldEnergy   int                  `json:"field_energy,omitempty"`
	FieldPower    int                  `json:"field_power,omitempty"`
	TankLevel     int                  `json:"tank_level,omitempty"`
	TankPoints    int                  `json:"tank_points,omitempty"`
	TankEnergy    int                  `json:"tank_energy,omitempty"`
	TankPower     int                  `json:"tank_power,omitempty"`
	Upgrades      []ClanUpgrade        `json:"upgrades,omitempty"`
	Buildings     []ClanBuilding       `json:"buildings,omitempty"`
	WarMarks      []int                `json:"war_marks,omitempty"`
	// Receipts deduplicates retried clan progress commands (client unique
	// ids), unix second of first application.
	Receipts map[string]int64 `json:"receipts,omitempty"`
}

type clanState struct {
	Clans     map[string]*Clan `json:"clans,omitempty"`
	Cooldowns map[string]int64 `json:"cooldowns,omitempty"` // player -> unix second the cooldown ends
	LastStamp int64            `json:"last_stamp_us,omitempty"`
}

type clanRate struct {
	Minute                         int64
	Actions, Messages, Reads, Edit int
}

// ClanMemberView is a member enriched with the public profile fields the
// native member list renders.
type ClanMemberView struct {
	ClanMember
	Nick       string
	Rank       int
	ClanRank   int
	LastOnline int64
}

// ClanView is a detached copy of one clan for rendering.
type ClanView struct {
	Clan
	MemberViews []ClanMemberView
	HasRequest  bool
}

// Limit is the effective member capacity.
func (c *Clan) Limit() int {
	if c.MemberLimit < ClanMemberLimit {
		return ClanMemberLimit
	}
	if c.MemberLimit > ClanMemberLimitMax {
		return ClanMemberLimitMax
	}
	return c.MemberLimit
}

func (c *Clan) member(id string) *ClanMember {
	for i := range c.Members {
		if c.Members[i].ID == id {
			return &c.Members[i]
		}
	}
	return nil
}

// Role returns the member's native role, or ClanRoleNone.
func (c *Clan) Role(id string) int {
	if m := c.member(id); m != nil {
		return m.Role
	}
	return ClanRoleNone
}

func (c *Clan) clone() *Clan {
	if c == nil {
		return nil
	}
	out := *c
	out.Members = append([]ClanMember(nil), c.Members...)
	out.Requests = append([]ClanRequest(nil), c.Requests...)
	out.Invites = append([]ClanInvite(nil), c.Invites...)
	out.Messages = append([]ClanMessage(nil), c.Messages...)
	out.Service = append([]ClanServiceMessage(nil), c.Service...)
	out.Items = append([]ClanItem(nil), c.Items...)
	out.Field = append([]ClanFieldCell(nil), c.Field...)
	out.Decor = append([]int(nil), c.Decor...)
	out.Tank = append([]ClanTankSlot(nil), c.Tank...)
	out.Upgrades = append([]ClanUpgrade(nil), c.Upgrades...)
	out.WarMarks = append([]int(nil), c.WarMarks...)
	if c.Receipts != nil {
		out.Receipts = make(map[string]int64, len(c.Receipts))
		for k, v := range c.Receipts {
			out.Receipts[k] = v
		}
	}
	out.Buildings = make([]ClanBuilding, len(c.Buildings))
	for i, b := range c.Buildings {
		if b.Points != nil {
			points := make(map[string]int, len(b.Points))
			for k, v := range b.Points {
				points[k] = v
			}
			b.Points = points
		}
		out.Buildings[i] = b
	}
	return &out
}

func (v clanState) clone() clanState {
	out := clanState{LastStamp: v.LastStamp}
	if v.Clans != nil {
		out.Clans = make(map[string]*Clan, len(v.Clans))
		for id, c := range v.Clans {
			out.Clans[id] = c.clone()
		}
	}
	if v.Cooldowns != nil {
		out.Cooldowns = make(map[string]int64, len(v.Cooldowns))
		for id, until := range v.Cooldowns {
			out.Cooldowns[id] = until
		}
	}
	return out
}

// ValidClanID accepts the 8 digit ids this server mints. The native PlayerData
// carries the clan id as an Int32 (`ic`), so ids stay numeric.
func ValidClanID(id string) bool {
	if len(id) < 1 || len(id) > 9 || id[0] == '0' {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func mintClanID() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(90000000))
	if err != nil {
		return "", fmt.Errorf("store: mint clan id: %w", err)
	}
	return fmt.Sprintf("%08d", n.Int64()+10000000), nil
}

func (s *Store) mintClanIDLocked() (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		id, err := mintClanID()
		if err != nil {
			return "", err
		}
		if s.state.Clans.Clans[id] == nil {
			return id, nil
		}
	}
	return "", errors.New("store: could not mint a free clan id")
}

func (s *Store) clanRateLocked(actor, kind string, now time.Time) error {
	if s.clanRates == nil {
		s.clanRates = map[string]clanRate{}
	}
	for id := range s.clanRates {
		if s.state.Accounts[id] == nil {
			delete(s.clanRates, id)
		}
	}
	r := s.clanRates[actor]
	if r.Minute != now.Unix()/60 {
		r = clanRate{Minute: now.Unix() / 60}
	}
	switch kind {
	case "message":
		if r.Messages >= 30 {
			return ErrClanRate
		}
		r.Messages++
	case "read":
		if r.Reads >= 240 {
			return ErrClanRate
		}
		r.Reads++
	case "edit":
		if r.Edit >= 240 {
			return ErrClanRate
		}
		r.Edit++
	default:
		if r.Actions >= 60 {
			return ErrClanRate
		}
		r.Actions++
	}
	s.clanRates[actor] = r
	return nil
}

func (s *Store) canonClanPlayerLocked(id string) string {
	id = s.resolvePlayerIDLocked(strings.TrimSpace(id))
	if id == "" || s.state.Accounts[id] == nil {
		return ""
	}
	return id
}

// sortedClansLocked orders clans deterministically: oldest first, so the
// oldest membership wins if a corrupt state file lists a player twice.
func (s *Store) sortedClansLocked() []*Clan {
	out := make([]*Clan, 0, len(s.state.Clans.Clans))
	for _, c := range s.state.Clans.Clans {
		if c != nil {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// normalizeClansLocked canonicalises identities, enforces one clan per
// player, keeps exactly one leader (the creator), deletes empty clans and
// bounds every list. It is idempotent and runs around every clan mutation.
func (s *Store) normalizeClansLocked(now time.Time) {
	v := &s.state.Clans
	if v.Clans == nil {
		v.Clans = map[string]*Clan{}
	}
	for id, c := range v.Clans {
		if c == nil || !ValidClanID(id) {
			delete(v.Clans, id)
			continue
		}
		c.ID = id
	}
	owner := map[string]string{}
	for _, c := range s.sortedClansLocked() {
		members := make([]ClanMember, 0, len(c.Members))
		for _, m := range c.Members {
			m.ID = s.canonClanPlayerLocked(m.ID)
			if m.ID == "" || owner[m.ID] != "" {
				continue
			}
			owner[m.ID] = c.ID
			if m.Role != ClanRoleOfficer {
				m.Role = ClanRoleSoldier
			}
			if m.JoinedAt <= 0 {
				m.JoinedAt = now.Unix()
			}
			members = append(members, m)
		}
		if len(members) == 0 {
			delete(v.Clans, c.ID)
			continue
		}
		c.Members = members
		c.Creator = s.canonClanPlayerLocked(c.Creator)
		if c.member(c.Creator) == nil {
			c.Creator = successor(c.Members, "")
		}
		c.member(c.Creator).Role = ClanRoleLeader
		if c.Type != ClanTypePrivate {
			c.Type = ClanTypePublic
		}
		if c.MemberLimit < ClanMemberLimit {
			c.MemberLimit = ClanMemberLimit
		}
		if c.MemberLimit > ClanMemberLimitMax {
			c.MemberLimit = ClanMemberLimitMax
		}
		if c.CreatedAt <= 0 {
			c.CreatedAt = now.Unix()
		}
	}
	cutoff := now.Add(-clanPendingTTL).Unix()
	history := now.Add(-clanHistoryTTL).UnixMicro()
	for _, c := range v.Clans {
		requests := []ClanRequest{}
		seen := map[string]bool{}
		for i := len(c.Requests) - 1; i >= 0; i-- {
			r := c.Requests[i]
			r.Player = s.canonClanPlayerLocked(r.Player)
			if r.Player == "" || owner[r.Player] != "" || seen[r.Player] || r.Created < cutoff || len(requests) >= ClanPendingLimit {
				continue
			}
			seen[r.Player] = true
			requests = append(requests, r)
		}
		reverseRequests(requests)
		c.Requests = requests
		invites := []ClanInvite{}
		seen = map[string]bool{}
		for i := len(c.Invites) - 1; i >= 0; i-- {
			r := c.Invites[i]
			r.Player = s.canonClanPlayerLocked(r.Player)
			r.From = s.canonClanPlayerLocked(r.From)
			if r.Player == "" || owner[r.Player] != "" || seen[r.Player] || r.Created < cutoff || len(invites) >= ClanPendingLimit {
				continue
			}
			seen[r.Player] = true
			invites = append(invites, r)
		}
		for i, j := 0, len(invites)-1; i < j; i, j = i+1, j-1 {
			invites[i], invites[j] = invites[j], invites[i]
		}
		c.Invites = invites
		messages := []ClanMessage{}
		for _, m := range c.Messages {
			if m.Stamp < history {
				continue
			}
			if id := s.canonClanPlayerLocked(m.From); id != "" {
				m.From = id
			}
			messages = append(messages, m)
		}
		if len(messages) > clanHistoryLimit {
			messages = messages[len(messages)-clanHistoryLimit:]
		}
		c.Messages = messages
		service := []ClanServiceMessage{}
		for _, m := range c.Service {
			if m.Stamp >= history {
				service = append(service, m)
			}
		}
		if len(service) > clanHistoryLimit {
			service = service[len(service)-clanHistoryLimit:]
		}
		c.Service = service
		trimClanReceipts(c, cutoff)
		for i := range c.Buildings {
			for id := range c.Buildings[i].Points {
				if canon := s.canonClanPlayerLocked(id); canon != id {
					points := c.Buildings[i].Points[id]
					delete(c.Buildings[i].Points, id)
					if canon != "" {
						c.Buildings[i].Points[canon] += points
					}
				}
			}
		}
	}
	// A player keeps at most ClanPlayerPendingLimit outgoing requests and
	// incoming invites; the newest win.
	trimPlayerPending(v.Clans)
	for id, until := range v.Cooldowns {
		if until <= now.Unix() || s.canonClanPlayerLocked(id) != id {
			delete(v.Cooldowns, id)
		}
	}
}

const clanReceiptLimit = 2048

func trimClanReceipts(c *Clan, cutoff int64) {
	for k, at := range c.Receipts {
		if at < cutoff {
			delete(c.Receipts, k)
		}
	}
	if len(c.Receipts) <= clanReceiptLimit {
		return
	}
	type receipt struct {
		key string
		at  int64
	}
	list := make([]receipt, 0, len(c.Receipts))
	for k, at := range c.Receipts {
		list = append(list, receipt{k, at})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].at != list[j].at {
			return list[i].at < list[j].at
		}
		return list[i].key < list[j].key
	})
	for _, r := range list[:len(list)-clanReceiptLimit] {
		delete(c.Receipts, r.key)
	}
}

func reverseRequests(v []ClanRequest) {
	for i, j := 0, len(v)-1; i < j; i, j = i+1, j-1 {
		v[i], v[j] = v[j], v[i]
	}
}

func trimPlayerPending(clans map[string]*Clan) {
	type ref struct {
		clan    string
		created int64
	}
	requests, invites := map[string][]ref{}, map[string][]ref{}
	for _, c := range clans {
		for _, r := range c.Requests {
			requests[r.Player] = append(requests[r.Player], ref{c.ID, r.Created})
		}
		for _, r := range c.Invites {
			invites[r.Player] = append(invites[r.Player], ref{c.ID, r.Created})
		}
	}
	drop := func(refs map[string][]ref) map[string]bool {
		out := map[string]bool{}
		for player, list := range refs {
			if len(list) <= ClanPlayerPendingLimit {
				continue
			}
			sort.Slice(list, func(i, j int) bool {
				if list[i].created != list[j].created {
					return list[i].created > list[j].created
				}
				return list[i].clan < list[j].clan
			})
			for _, r := range list[ClanPlayerPendingLimit:] {
				out[r.clan+":"+player] = true
			}
		}
		return out
	}
	dropRequests, dropInvites := drop(requests), drop(invites)
	if len(dropRequests) == 0 && len(dropInvites) == 0 {
		return
	}
	for _, c := range clans {
		keptRequests := c.Requests[:0]
		for _, r := range c.Requests {
			if !dropRequests[c.ID+":"+r.Player] {
				keptRequests = append(keptRequests, r)
			}
		}
		c.Requests = keptRequests
		keptInvites := c.Invites[:0]
		for _, r := range c.Invites {
			if !dropInvites[c.ID+":"+r.Player] {
				keptInvites = append(keptInvites, r)
			}
		}
		c.Invites = keptInvites
	}
}

// successor picks the next leader: the longest-serving officer, otherwise the
// longest-serving member. except is skipped (the departing leader).
func successor(members []ClanMember, except string) string {
	best := -1
	for i, m := range members {
		if m.ID == except {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		b := members[best]
		mOfficer, bOfficer := m.Role == ClanRoleOfficer, b.Role == ClanRoleOfficer
		if mOfficer != bOfficer {
			if mOfficer {
				best = i
			}
			continue
		}
		if m.JoinedAt < b.JoinedAt || m.JoinedAt == b.JoinedAt && m.ID < b.ID {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return members[best].ID
}

// mutateClansLocked runs fn against normalized state, normalizes again and
// persists; any failure restores the previous clan state.
func (s *Store) mutateClansLocked(now time.Time, fn func() error) error {
	old := s.state.Clans.clone()
	s.normalizeClansLocked(now)
	if err := fn(); err != nil {
		s.state.Clans = old
		return err
	}
	s.normalizeClansLocked(now)
	if err := s.persistLocked(); err != nil {
		s.state.Clans = old
		return err
	}
	return nil
}

func (s *Store) clanOfLocked(player string) *Clan {
	for _, c := range s.state.Clans.Clans {
		if c != nil && c.member(player) != nil {
			return c
		}
	}
	return nil
}

func (s *Store) clanStampLocked(now time.Time) int64 {
	stamp := now.UnixMicro()
	if stamp <= s.state.Clans.LastStamp {
		stamp = s.state.Clans.LastStamp + 1
	}
	s.state.Clans.LastStamp = stamp
	return stamp
}

func (s *Store) clanServiceLocked(c *Clan, now time.Time, m ClanServiceMessage) {
	m.Stamp = s.clanStampLocked(now)
	c.Service = append(c.Service, m)
}

func (s *Store) cooldownLocked(player string, now time.Time) error {
	if until := s.state.Clans.Cooldowns[player]; until > now.Unix() {
		return &ClanCooldownError{Remaining: time.Unix(until, 0).Sub(now)}
	}
	return nil
}

func (s *Store) startCooldownLocked(player string, now time.Time) {
	if s.state.Clans.Cooldowns == nil {
		s.state.Clans.Cooldowns = map[string]int64{}
	}
	s.state.Clans.Cooldowns[player] = now.Add(ClanLeaveCooldown).Unix()
}

func (s *Store) playerLevelLocked(player string) int {
	if p := s.state.Profiles[player]; p != nil {
		if p.Level > 0 {
			return p.Level
		}
		if n, ok := counterInt64(p.ClientState["rank"]); ok && n > 0 {
			return int(n)
		}
	}
	return 1
}

func (s *Store) playerNickLocked(player string) string {
	if p := s.state.Profiles[player]; p != nil && strings.TrimSpace(p.Nick) != "" {
		return p.Nick
	}
	if a := s.state.Accounts[player]; a != nil {
		return a.Nick
	}
	return ""
}

// Text validation shared by clan names, slogans and messages.
func validClanText(text string, limit int, allowEmpty, allowNewline bool) bool {
	if !utf8.ValidString(text) || len(text) > limit*4 || utf8.RuneCountInString(text) > limit {
		return false
	}
	if !allowEmpty && strings.TrimSpace(text) == "" {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) && !(allowNewline && (r == '\n' || r == '\t')) {
			return false
		}
	}
	return true
}

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// validClanLogo accepts the base64 PNG emblem the native editor uploads.
func validClanLogo(logo string) bool {
	if logo == "" {
		return true
	}
	if len(logo) > ClanLogoLimit {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(logo)
	if err != nil {
		return false
	}
	return len(raw) >= len(pngSignature) && string(raw[:len(pngSignature)]) == string(pngSignature)
}

func clanLogoHash(logo string) string {
	if logo == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(logo))
	return hex.EncodeToString(sum[:16])
}

func normalizeContinent(v string) (string, bool) {
	v = strings.ToUpper(strings.TrimSpace(v))
	if len(v) > 16 {
		return "", false
	}
	for _, r := range v {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return "", false
		}
	}
	return v, true
}

func (s *Store) clanNameTakenLocked(name, except string) bool {
	key := strings.ToLower(strings.TrimSpace(name))
	for id, c := range s.state.Clans.Clans {
		if id != except && c != nil && strings.ToLower(strings.TrimSpace(c.Name)) == key {
			return true
		}
	}
	return false
}

// ClanCreate is the create_clan_v2 payload.
type ClanCreate struct {
	Name, Logo, Slogan, Continent string
	Type, MinRank                 int
}

// CreateClan founds a clan led by actor. The stock client charges the price
// locally before the request, so the server only owns uniqueness and limits.
func (s *Store) CreateClan(actor string, in ClanCreate) (string, error) {
	in.Name = strings.TrimSpace(in.Name)
	continent, ok := normalizeContinent(in.Continent)
	if !ok || !validClanText(in.Name, ClanNameLimit, false, false) || !validClanText(in.Slogan, ClanSloganLimit, true, true) || !validClanLogo(in.Logo) || in.MinRank < 0 || in.MinRank > 100 {
		return "", ErrClanInvalid
	}
	if in.Type != ClanTypePrivate {
		in.Type = ClanTypePublic
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	actor = s.canonClanPlayerLocked(actor)
	if actor == "" {
		return "", ErrNotFound
	}
	now := time.Now().UTC()
	if err := s.clanRateLocked(actor, "action", now); err != nil {
		return "", err
	}
	var id string
	err := s.mutateClansLocked(now, func() error {
		if s.clanOfLocked(actor) != nil {
			return ErrClanMember
		}
		if err := s.cooldownLocked(actor, now); err != nil {
			return err
		}
		if s.clanNameTakenLocked(in.Name, "") {
			return ErrClanNameTaken
		}
		var err error
		if id, err = s.mintClanIDLocked(); err != nil {
			return err
		}
		c := &Clan{
			ID: id, Name: in.Name, Creator: actor, Type: in.Type, Slogan: in.Slogan,
			Logo: in.Logo, LogoHash: clanLogoHash(in.Logo), Continent: continent,
			MinRank: in.MinRank, MemberLimit: ClanMemberLimit, CreatedAt: now.Unix(),
			Members: []ClanMember{{ID: actor, Role: ClanRoleLeader, JoinedAt: now.Unix()}},
		}
		s.state.Clans.Clans[id] = c
		s.clanServiceLocked(c, now, ClanServiceMessage{Action: ClanServiceJoined, From: actor, Target: actor})
		return nil
	})
	return id, err
}

// ClanParams carries the optional change_clan_params members.
type ClanParams struct {
	Name, Slogan, Logo, LeaderMessage, Continent *string
	MinRank, Limit, Type                         *int
}

// ChangeClanParams edits the clan profile. The leader owns every field;
// officers may update the leader message only.
func (s *Store) ChangeClanParams(actor, clanID string, in ClanParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	actor = s.canonClanPlayerLocked(actor)
	if actor == "" {
		return ErrNotFound
	}
	now := time.Now().UTC()
	if err := s.clanRateLocked(actor, "action", now); err != nil {
		return err
	}
	return s.mutateClansLocked(now, func() error {
		c := s.state.Clans.Clans[clanID]
		if c == nil {
			return ErrClanNotFound
		}
		role := c.Role(actor)
		if role == ClanRoleNone {
			return ErrClanNotMember
		}
		leaderOnly := in.Name != nil || in.Slogan != nil || in.Logo != nil || in.Continent != nil || in.MinRank != nil || in.Limit != nil || in.Type != nil
		if role != ClanRoleLeader && (leaderOnly || role != ClanRoleOfficer) {
			return ErrClanForbidden
		}
		if in.Name != nil {
			name := strings.TrimSpace(*in.Name)
			if !validClanText(name, ClanNameLimit, false, false) {
				return ErrClanInvalid
			}
			if s.clanNameTakenLocked(name, c.ID) {
				return ErrClanNameTaken
			}
			c.Name = name
		}
		if in.Slogan != nil {
			if !validClanText(*in.Slogan, ClanSloganLimit, true, true) {
				return ErrClanInvalid
			}
			c.Slogan = *in.Slogan
		}
		if in.LeaderMessage != nil {
			if !validClanText(*in.LeaderMessage, ClanLeaderMessageLimit, true, true) {
				return ErrClanInvalid
			}
			c.LeaderMessage = *in.LeaderMessage
		}
		if in.Logo != nil {
			if !validClanLogo(*in.Logo) {
				return ErrClanInvalid
			}
			c.Logo = *in.Logo
			c.LogoHash = clanLogoHash(c.Logo)
		}
		if in.Continent != nil {
			continent, ok := normalizeContinent(*in.Continent)
			if !ok {
				return ErrClanInvalid
			}
			c.Continent = continent
		}
		if in.MinRank != nil {
			if *in.MinRank < 0 || *in.MinRank > 100 {
				return ErrClanInvalid
			}
			c.MinRank = *in.MinRank
		}
		if in.Type != nil {
			switch *in.Type {
			case ClanTypePublic, ClanTypePrivate:
				c.Type = *in.Type
			default:
				return ErrClanInvalid
			}
		}
		if in.Limit != nil {
			// The native member-limit upgrade is paid on the client; the
			// server only accepts a bounded, never shrinking capacity.
			if *in.Limit < len(c.Members) || *in.Limit > ClanMemberLimitMax {
				return ErrClanInvalid
			}
			if *in.Limit > c.MemberLimit {
				c.MemberLimit = *in.Limit
			}
		}
		return nil
	})
}

// PlayerClanID returns the clan id of the player's clan, or "".
func (s *Store) PlayerClanID(player string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	player = s.canonClanPlayerLocked(player)
	if player == "" {
		return ""
	}
	if c := s.clanOfLocked(player); c != nil {
		return c.ID
	}
	return ""
}

func (s *Store) clanViewLocked(c *Clan, viewer string) ClanView {
	out := ClanView{Clan: *c.clone()}
	for _, m := range c.Members {
		view := ClanMemberView{ClanMember: m, Nick: s.playerNickLocked(m.ID), Rank: s.playerLevelLocked(m.ID)}
		if p := s.state.Profiles[m.ID]; p != nil {
			if n, ok := counterInt64(p.ClientState["clan_rank"]); ok && n >= 0 && n < 1000 {
				view.ClanRank = int(n)
			}
		}
		if a := s.state.Accounts[m.ID]; a != nil {
			last := a.LastSeenAt
			if a.GameLastSeenAt.After(last) {
				last = a.GameLastSeenAt
			}
			if !last.IsZero() {
				view.LastOnline = last.Unix()
			}
		}
		out.MemberViews = append(out.MemberViews, view)
	}
	if viewer != "" {
		for _, r := range c.Requests {
			if r.Player == viewer {
				out.HasRequest = true
			}
		}
	}
	return out
}

// ClanByID returns a detached view of one clan.
func (s *Store) ClanByID(clanID, viewer string) (ClanView, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.state.Clans.Clans[strings.TrimSpace(clanID)]
	if c == nil || len(c.Members) == 0 {
		return ClanView{}, false
	}
	return s.clanViewLocked(c, s.canonClanPlayerLocked(viewer)), true
}

// PlayerClan returns the player's clan view.
func (s *Store) PlayerClan(player string) (ClanView, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	player = s.canonClanPlayerLocked(player)
	if player == "" {
		return ClanView{}, false
	}
	c := s.clanOfLocked(player)
	if c == nil {
		return ClanView{}, false
	}
	return s.clanViewLocked(c, player), true
}

// LeadingClan is the clan on the lobby leader banner (get_leader_clan): most
// clan war wins, then sieges won, then members, then the oldest. ok is false
// until some clan has won a war.
func (s *Store) LeadingClan() (ClanSummary, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *Clan
	for _, c := range s.state.Clans.Clans {
		if c == nil || len(c.Members) == 0 || c.WarWins <= 0 {
			continue
		}
		if best == nil || clanLeads(c, best) {
			best = c
		}
	}
	if best == nil {
		return ClanSummary{}, false
	}
	return ClanSummary{ID: best.ID, Name: best.Name, Logo: best.Logo, LogoHash: best.LogoHash, Creator: best.Creator, Role: ClanRoleNone}, true
}

func clanLeads(a, b *Clan) bool {
	if a.WarWins != b.WarWins {
		return a.WarWins > b.WarWins
	}
	if a.SiegesWon != b.SiegesWon {
		return a.SiegesWon > b.SiegesWon
	}
	if len(a.Members) != len(b.Members) {
		return len(a.Members) > len(b.Members)
	}
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	return a.ID < b.ID
}

// ClanSummary is the light-weight membership projection used by PlayerData.
type ClanSummary struct {
	ID, Name, Logo, LogoHash, Creator string
	Role                              int
}

// PlayerClanSummary returns the player's clan badge without copying history.
func (s *Store) PlayerClanSummary(player string) (ClanSummary, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	player = s.canonClanPlayerLocked(player)
	if player == "" {
		return ClanSummary{}, false
	}
	c := s.clanOfLocked(player)
	if c == nil {
		return ClanSummary{}, false
	}
	return ClanSummary{ID: c.ID, Name: c.Name, Logo: c.Logo, LogoHash: c.LogoHash, Creator: c.Creator, Role: c.Role(player)}, true
}

// ClanInfos returns the requested clans in request order, skipping unknown ids.
func (s *Store) ClanInfos(viewer string, ids []string) ([]ClanView, error) {
	if len(ids) > 100 {
		return nil, ErrClanInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	viewer = s.canonClanPlayerLocked(viewer)
	if viewer == "" {
		return nil, ErrNotFound
	}
	if err := s.clanRateLocked(viewer, "read", time.Now()); err != nil {
		return nil, err
	}
	out := []ClanView{}
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if c := s.state.Clans.Clans[id]; c != nil && len(c.Members) > 0 && !seen[id] {
			seen[id] = true
			out = append(out, s.clanViewLocked(c, viewer))
		}
	}
	return out, nil
}

// SearchClans matches an exact clan id or a case-insensitive name substring.
func (s *Store) SearchClans(viewer, param string) ([]ClanView, error) {
	param = strings.TrimSpace(param)
	if param == "" || !validClanText(param, 64, false, false) {
		return nil, ErrClanInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	viewer = s.canonClanPlayerLocked(viewer)
	if viewer == "" {
		return nil, ErrNotFound
	}
	if err := s.clanRateLocked(viewer, "read", time.Now()); err != nil {
		return nil, err
	}
	q := strings.ToLower(param)
	matches := []*Clan{}
	for _, c := range s.sortedClansLocked() {
		if len(c.Members) > 0 && (c.ID == param || strings.Contains(strings.ToLower(c.Name), q)) {
			matches = append(matches, c)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		ai, aj := matches[i].ID == param, matches[j].ID == param
		if ai != aj {
			return ai
		}
		ei, ej := strings.ToLower(matches[i].Name) == q, strings.ToLower(matches[j].Name) == q
		if ei != ej {
			return ei
		}
		return len(matches[i].Members) > len(matches[j].Members)
	})
	if len(matches) > ClanSearchLimit {
		matches = matches[:ClanSearchLimit]
	}
	out := make([]ClanView, 0, len(matches))
	for _, c := range matches {
		out = append(out, s.clanViewLocked(c, viewer))
	}
	return out, nil
}

// PossibleClans recommends joinable clans: open seats first, then promoted
// clans, the viewer's continent and public clans the viewer may join directly.
func (s *Store) PossibleClans(viewer, continent string) ([]ClanView, error) {
	continent, ok := normalizeContinent(continent)
	if !ok {
		return nil, ErrClanInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	viewer = s.canonClanPlayerLocked(viewer)
	if viewer == "" {
		return nil, ErrNotFound
	}
	now := time.Now()
	if err := s.clanRateLocked(viewer, "read", now); err != nil {
		return nil, err
	}
	level := s.playerLevelLocked(viewer)
	type scored struct {
		c     *Clan
		score int
	}
	list := []scored{}
	for _, c := range s.sortedClansLocked() {
		if len(c.Members) == 0 || len(c.Members) >= c.Limit() || c.member(viewer) != nil {
			continue
		}
		score := 0
		if c.PromoUntil > now.Unix() {
			score += 8
		}
		if continent != "" && c.Continent == continent {
			score += 4
		}
		if c.Type == ClanTypePublic {
			score += 2
		}
		if c.MinRank <= level {
			score++
		}
		list = append(list, scored{c, score})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return len(list[i].c.Members) > len(list[j].c.Members)
	})
	if len(list) > ClanSearchLimit {
		list = list[:ClanSearchLimit]
	}
	out := make([]ClanView, 0, len(list))
	for _, v := range list {
		out = append(out, s.clanViewLocked(v.c, viewer))
	}
	return out, nil
}

func (s *Store) addMemberLocked(c *Clan, player string, now time.Time) error {
	if len(c.Members) >= c.Limit() {
		return ErrClanFull
	}
	c.Members = append(c.Members, ClanMember{ID: player, Role: ClanRoleSoldier, JoinedAt: now.Unix()})
	s.clanServiceLocked(c, now, ClanServiceMessage{Action: ClanServiceJoined, From: player, Target: player})
	return nil
}

func (s *Store) clanActorLocked(actor string, now time.Time, kind string) (string, error) {
	actor = s.canonClanPlayerLocked(actor)
	if actor == "" {
		return "", ErrNotFound
	}
	return actor, s.clanRateLocked(actor, kind, now)
}

// JoinClan enters a public clan directly. A pending invitation from the clan
// waives the type and rank restrictions, matching the native invite flow.
func (s *Store) JoinClan(actor, clanID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	actor, err := s.clanActorLocked(actor, now, "action")
	if err != nil {
		return err
	}
	return s.mutateClansLocked(now, func() error {
		c := s.state.Clans.Clans[clanID]
		if c == nil {
			return ErrClanNotFound
		}
		if s.clanOfLocked(actor) != nil {
			return ErrClanMember
		}
		if err := s.cooldownLocked(actor, now); err != nil {
			return err
		}
		invited := false
		for _, r := range c.Invites {
			if r.Player == actor {
				invited = true
			}
		}
		if !invited {
			if c.Type == ClanTypePrivate {
				return ErrClanClosed
			}
			if c.MinRank > s.playerLevelLocked(actor) {
				return ErrClanRank
			}
		}
		return s.addMemberLocked(c, actor, now)
	})
}

// AskToJoinClan files a join request with an optional note.
func (s *Store) AskToJoinClan(actor, clanID, text string) error {
	if !validClanText(text, ClanRequestTextLimit, true, true) {
		return ErrClanInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	actor, err := s.clanActorLocked(actor, now, "action")
	if err != nil {
		return err
	}
	return s.mutateClansLocked(now, func() error {
		c := s.state.Clans.Clans[clanID]
		if c == nil {
			return ErrClanNotFound
		}
		if s.clanOfLocked(actor) != nil {
			return ErrClanMember
		}
		if err := s.cooldownLocked(actor, now); err != nil {
			return err
		}
		if c.MinRank > s.playerLevelLocked(actor) {
			return ErrClanRank
		}
		if len(c.Members) >= c.Limit() {
			return ErrClanFull
		}
		for i := range c.Requests {
			if c.Requests[i].Player == actor {
				c.Requests[i].Message = text
				c.Requests[i].Created = now.Unix()
				return nil
			}
		}
		if len(c.Requests) >= ClanPendingLimit {
			return ErrClanPending
		}
		outgoing := 0
		for _, other := range s.state.Clans.Clans {
			for _, r := range other.Requests {
				if r.Player == actor {
					outgoing++
				}
			}
		}
		if outgoing >= ClanPlayerPendingLimit {
			return ErrClanPending
		}
		c.Requests = append(c.Requests, ClanRequest{Player: actor, Message: text, Created: now.Unix()})
		return nil
	})
}

func (s *Store) officerClanLocked(actor, clanID string) (*Clan, error) {
	c := s.state.Clans.Clans[clanID]
	if c == nil {
		return nil, ErrClanNotFound
	}
	switch c.Role(actor) {
	case ClanRoleLeader, ClanRoleOfficer:
		return c, nil
	case ClanRoleNone:
		return nil, ErrClanNotMember
	}
	return nil, ErrClanForbidden
}

func (s *Store) memberClanLocked(actor, clanID string) (*Clan, error) {
	c := s.state.Clans.Clans[clanID]
	if c == nil {
		return nil, ErrClanNotFound
	}
	if c.member(actor) == nil {
		return nil, ErrClanNotMember
	}
	return c, nil
}

// AnswerClanRequest lets a leader or officer accept or reject a join request.
// A request that can no longer succeed is withdrawn before reporting, so the
// native list drops it (1705 / 1400) instead of offering it again.
func (s *Store) AnswerClanRequest(actor, clanID, player string, accept bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	actor, err := s.clanActorLocked(actor, now, "action")
	if err != nil {
		return err
	}
	player = s.canonClanPlayerLocked(player)
	var stale error
	err = s.mutateClansLocked(now, func() error {
		c, err := s.officerClanLocked(actor, clanID)
		if err != nil {
			return err
		}
		found := false
		kept := []ClanRequest{}
		for _, r := range c.Requests {
			if r.Player == player && player != "" {
				found = true
				continue
			}
			kept = append(kept, r)
		}
		if !found {
			return ErrClanInvalid
		}
		c.Requests = kept
		if !accept {
			return nil
		}
		if s.clanOfLocked(player) != nil {
			stale = ErrClanMember
			return nil
		}
		if len(c.Members) >= c.Limit() {
			return ErrClanFull
		}
		return s.addMemberLocked(c, player, now)
	})
	if err == nil && stale != nil {
		return stale
	}
	return err
}

// RejectAllClanRequests clears the clan's request queue.
func (s *Store) RejectAllClanRequests(actor, clanID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	actor, err := s.clanActorLocked(actor, now, "action")
	if err != nil {
		return err
	}
	return s.mutateClansLocked(now, func() error {
		c, err := s.officerClanLocked(actor, clanID)
		if err != nil {
			return err
		}
		c.Requests = nil
		return nil
	})
}

// InviteToClan sends a clan invitation to a clanless player.
func (s *Store) InviteToClan(actor, clanID, player string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	actor, err := s.clanActorLocked(actor, now, "action")
	if err != nil {
		return err
	}
	player = s.canonClanPlayerLocked(player)
	if player == "" {
		return ErrNotFound
	}
	return s.mutateClansLocked(now, func() error {
		c, err := s.officerClanLocked(actor, clanID)
		if err != nil {
			return err
		}
		if s.clanOfLocked(player) != nil {
			return ErrClanMember
		}
		for i := range c.Invites {
			if c.Invites[i].Player == player {
				c.Invites[i].Created = now.Unix()
				c.Invites[i].From = actor
				return nil
			}
		}
		if len(c.Invites) >= ClanPendingLimit {
			return ErrClanPending
		}
		incoming := 0
		for _, other := range s.state.Clans.Clans {
			for _, r := range other.Invites {
				if r.Player == player {
					incoming++
				}
			}
		}
		if incoming >= ClanPlayerPendingLimit {
			return ErrClanPending
		}
		c.Invites = append(c.Invites, ClanInvite{Player: player, From: actor, Created: now.Unix()})
		return nil
	})
}

// AnswerClanInvite accepts or rejects an invitation addressed to actor.
func (s *Store) AnswerClanInvite(actor, clanID string, accept bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	actor, err := s.clanActorLocked(actor, now, "action")
	if err != nil {
		return err
	}
	return s.mutateClansLocked(now, func() error {
		c := s.state.Clans.Clans[clanID]
		if c == nil {
			return ErrClanNotFound
		}
		found := false
		kept := []ClanInvite{}
		for _, r := range c.Invites {
			if r.Player == actor {
				found = true
				continue
			}
			kept = append(kept, r)
		}
		if !found {
			return ErrClanInvalid
		}
		c.Invites = kept
		if !accept {
			return nil
		}
		if s.clanOfLocked(actor) != nil {
			return ErrClanMember
		}
		if err := s.cooldownLocked(actor, now); err != nil {
			return err
		}
		return s.addMemberLocked(c, actor, now)
	})
}

// ClanInvitesFor lists the clans currently inviting player, oldest first.
func (s *Store) ClanInvitesFor(player string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	player = s.canonClanPlayerLocked(player)
	out := []string{}
	if player == "" || s.clanOfLocked(player) != nil {
		return out
	}
	cutoff := time.Now().Add(-clanPendingTTL).Unix()
	type ref struct {
		id      string
		created int64
	}
	refs := []ref{}
	for _, c := range s.state.Clans.Clans {
		for _, r := range c.Invites {
			if s.canonClanPlayerLocked(r.Player) == player && r.Created >= cutoff {
				refs = append(refs, ref{c.ID, r.Created})
				break
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].created != refs[j].created {
			return refs[i].created < refs[j].created
		}
		return refs[i].id < refs[j].id
	})
	for _, r := range refs {
		out = append(out, r.id)
	}
	return out
}

// ChangeClanRole promotes, demotes or (role 1) hands leadership over. Only the
// leader may change roles; the old leader becomes an officer on transfer.
func (s *Store) ChangeClanRole(actor, clanID, player string, role int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	actor, err := s.clanActorLocked(actor, now, "action")
	if err != nil {
		return err
	}
	player = s.canonClanPlayerLocked(player)
	return s.mutateClansLocked(now, func() error {
		c, err := s.memberClanLocked(actor, clanID)
		if err != nil {
			return err
		}
		if c.Role(actor) != ClanRoleLeader {
			return ErrClanForbidden
		}
		target := c.member(player)
		if target == nil || player == actor {
			return ErrClanInvalid
		}
		action := 0
		switch role {
		case ClanRoleSoldier:
			action = ClanServiceBecomeSoldier
		case ClanRoleOfficer:
			action = ClanServiceBecomeOfficer
		case ClanRoleLeader:
			action = ClanServiceBecomeLeader
		default:
			return ErrClanInvalid
		}
		if target.Role == role {
			return nil
		}
		target.Role = role
		if role == ClanRoleLeader {
			c.member(actor).Role = ClanRoleOfficer
			c.Creator = player
		}
		s.clanServiceLocked(c, now, ClanServiceMessage{Action: action, From: actor, Target: player})
		return nil
	})
}

// KickFromClan removes a member. The leader may remove anyone else; officers
// may remove soldiers only.
func (s *Store) KickFromClan(actor, clanID, player string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	actor, err := s.clanActorLocked(actor, now, "action")
	if err != nil {
		return err
	}
	player = s.canonClanPlayerLocked(player)
	return s.mutateClansLocked(now, func() error {
		c, err := s.officerClanLocked(actor, clanID)
		if err != nil {
			return err
		}
		target := c.member(player)
		if target == nil || player == actor {
			return ErrClanInvalid
		}
		if c.Role(actor) != ClanRoleLeader && target.Role != ClanRoleSoldier {
			return ErrClanForbidden
		}
		removeClanMember(c, player)
		s.clanServiceLocked(c, now, ClanServiceMessage{Action: ClanServiceBanished, From: actor, Target: player})
		return nil
	})
}

func removeClanMember(c *Clan, player string) {
	kept := c.Members[:0]
	for _, m := range c.Members {
		if m.ID != player {
			kept = append(kept, m)
		}
	}
	c.Members = kept
}

// LeaveClan removes actor. A departing leader hands the clan to the next
// officer (or oldest member); the last member leaving dissolves the clan.
func (s *Store) LeaveClan(actor, clanID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	actor, err := s.clanActorLocked(actor, now, "action")
	if err != nil {
		return err
	}
	return s.mutateClansLocked(now, func() error {
		c, err := s.memberClanLocked(actor, clanID)
		if err != nil {
			return err
		}
		if c.Creator == actor {
			if next := successor(c.Members, actor); next != "" {
				c.Creator = next
				c.member(next).Role = ClanRoleLeader
				s.clanServiceLocked(c, now, ClanServiceMessage{Action: ClanServiceBecomeLeader, From: actor, Target: next})
			}
		}
		removeClanMember(c, actor)
		if len(c.Members) == 0 {
			delete(s.state.Clans.Clans, c.ID)
		} else {
			s.clanServiceLocked(c, now, ClanServiceMessage{Action: ClanServiceLeft, From: actor, Target: actor})
		}
		s.startCooldownLocked(actor, now)
		return nil
	})
}

// DeleteClan dissolves the clan; only its leader may do so.
func (s *Store) DeleteClan(actor, clanID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	actor, err := s.clanActorLocked(actor, now, "action")
	if err != nil {
		return err
	}
	return s.mutateClansLocked(now, func() error {
		c, err := s.memberClanLocked(actor, clanID)
		if err != nil {
			return err
		}
		if c.Role(actor) != ClanRoleLeader {
			return ErrClanForbidden
		}
		delete(s.state.Clans.Clans, c.ID)
		s.startCooldownLocked(actor, now)
		return nil
	})
}

// ClanCooldown reports the time left before actor may join a clan again.
func (s *Store) ClanCooldown(actor string) time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	actor = s.canonClanPlayerLocked(actor)
	now := time.Now()
	if until := s.state.Clans.Cooldowns[actor]; actor != "" && until > now.Unix() {
		return time.Unix(until, 0).Sub(now)
	}
	return 0
}

// SendClanMessage appends a chat line to actor's clan. A retried optimistic
// message carrying the same client stamp is answered with the stored copy.
func (s *Store) SendClanMessage(actor, text, clientStamp string) (ClanMessage, string, error) {
	if !validPrivateText(text) || len(clientStamp) > 48 {
		return ClanMessage{}, "", ErrClanInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	actor = s.canonClanPlayerLocked(actor)
	if actor == "" {
		return ClanMessage{}, "", ErrNotFound
	}
	c := s.clanOfLocked(actor)
	if c == nil {
		return ClanMessage{}, "", ErrClanNotMember
	}
	for _, m := range c.Messages {
		if clientStamp != "" && m.ClientStamp == clientStamp && m.From == actor && m.Text == text && m.Stamp >= now.Add(-5*time.Minute).UnixMicro() {
			return m, c.ID, nil
		}
	}
	if err := s.clanRateLocked(actor, "message", now); err != nil {
		return ClanMessage{}, "", err
	}
	var out ClanMessage
	clanID := c.ID
	err := s.mutateClansLocked(now, func() error {
		c := s.state.Clans.Clans[clanID]
		if c == nil || c.member(actor) == nil {
			return ErrClanNotMember
		}
		out = ClanMessage{Stamp: s.clanStampLocked(now), From: actor, Text: text, ClientStamp: clientStamp}
		c.Messages = append(c.Messages, out)
		return nil
	})
	return out, clanID, err
}

// ClanStampString renders a server stamp the way the native chat parses it
// (a double of Unix seconds).
func ClanStampString(stamp int64) string {
	return strconv.FormatFloat(float64(stamp)/1e6, 'f', 6, 64)
}

// renameClansLocked moves clan ownership with a player id change. newID ""
// erases the player; normalizeClansLocked then drops the empty references.
func (s *Store) renameClansLocked(oldID, newID string) {
	v := &s.state.Clans
	swap := func(id *string) {
		if *id == oldID {
			*id = newID
		}
	}
	for _, c := range v.Clans {
		if c == nil {
			continue
		}
		swap(&c.Creator)
		for i := range c.Members {
			swap(&c.Members[i].ID)
		}
		for i := range c.Requests {
			swap(&c.Requests[i].Player)
		}
		for i := range c.Invites {
			swap(&c.Invites[i].Player)
			swap(&c.Invites[i].From)
		}
		for i := range c.Messages {
			swap(&c.Messages[i].From)
		}
		for i := range c.Service {
			swap(&c.Service[i].From)
			swap(&c.Service[i].Target)
		}
		for i := range c.Upgrades {
			swap(&c.Upgrades[i].Player)
		}
		for i := range c.Buildings {
			if points, ok := c.Buildings[i].Points[oldID]; ok {
				delete(c.Buildings[i].Points, oldID)
				if newID != "" {
					c.Buildings[i].Points[newID] += points
				}
			}
		}
	}
	if until, ok := v.Cooldowns[oldID]; ok {
		delete(v.Cooldowns, oldID)
		if newID != "" {
			v.Cooldowns[newID] = until
		}
	}
}

// clanDebugJSON is used by tests to compare whole clan states.
func (s *Store) clanDebugJSON() string {
	raw, _ := json.Marshal(s.state.Clans)
	return string(raw)
}
