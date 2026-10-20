// SPDX-License-Identifier: GPL-3.0-or-later

package store

// Player profiles, and the admin operations that move them between accounts.
//
// An account answers "who is this device" (store.go); a profile answers "what
// has this player got". They are separate records under the same player id
// because the support flows an operator actually needs treat them separately:
//
//   - Changing an id has to carry the profile with it, and must not leave the
//     old id's ban behind, or a rename would be a way out of a ban.
//   - Restoring somebody's account means moving a profile onto a different
//     account while both logins keep working. That is the flow Pixel Gun 3D
//     support uses, and it is why progress is not just a field on Account.
//
// Every accessor returns a deep copy. A profile holds nested maps that came
// from the client, so handing back an interior pointer would let a caller
// mutate stored state outside the lock.

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrIDTaken is returned when a requested player id already exists.
	ErrIDTaken = errors.New("store: player id is already taken")
	// ErrBadPlayerID is returned for an id the client's parsers would reject.
	ErrBadPlayerID = errors.New("store: player id must be nine digits")
	// ErrSameAccount is returned when a transfer's source and target match.
	ErrSameAccount = errors.New("store: source and target are the same account")
)

// Profile is the server-side player profile.
type Profile struct {
	PlayerID   string `json:"player_id"`
	Nick       string `json:"nick"`
	Level      int    `json:"level"`
	Experience int64  `json:"experience"`
	Coins      int64  `json:"coins"`
	Gems       int64  `json:"gems"`

	// Prefs mirrors the client's own key/value state verbatim. The 23.1.3
	// client owns these keys, so they are kept as they arrive instead of being
	// mapped onto a server-side schema that would silently drop whatever this
	// server does not recognise yet.
	Prefs map[string]string `json:"prefs,omitempty"`

	// Progress is the progress_status payload, likewise kept verbatim.
	Progress map[string]any `json:"progress,omitempty"`

	// ProgressDocument is the slot indexed document init_progress
	// synchronises. It is kept apart from Progress because that map also
	// carries the flat counters register_push sends, and the client's applier
	// reads every member of this document as a progress slot.
	ProgressDocument map[string]any `json:"progress_document,omitempty"`

	// ProgressSlots is the slot document the client uploads with create_slots
	// and reads back from get_progress_slots. It is kept apart from
	// ProgressDocument because the two travel under different members and the
	// client synchronises them at different points of its boot.
	ProgressSlots map[string]any `json:"progress_slots,omitempty"`

	// ProgressRevision is advanced by an operator edit that changes the core
	// profile. A revision fences stale sessions during an admin replacement;
	// once a device has applied it, gameplay owns subsequent counter changes.
	ProgressRevision uint64 `json:"progress_revision,omitempty"`

	// AppliedProgressRevisions records which authenticated devices completed
	// the stock server-profile application flow for the current revision. Keys
	// are fixed-size hashes of device fingerprints rather than raw identifiers.
	AppliedProgressRevisions map[string]uint64 `json:"applied_progress_revisions,omitempty"`

	// ProgressCommandReceipts prevents acknowledged counter commands from being
	// charged twice after a reconnect or server restart. Reset on admin edit.
	ProgressCommandReceipts map[string]bool `json:"progress_command_receipts,omitempty"`

	// ClientState contains the remaining structured state reported through
	// update_player, update_loadout, update_accessories and similar events. It
	// is intentionally schema-free: support must be able to inspect and edit
	// fields introduced by the frozen client without the server dropping them.
	ClientState map[string]any `json:"client_state,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Transfer audit, set when a profile moved between accounts, so an
	// operator can see later where a player's progress came from or went.
	TransferredFrom string     `json:"transferred_from,omitempty"`
	TransferredTo   string     `json:"transferred_to,omitempty"`
	TransferredAt   *time.Time `json:"transferred_at,omitempty"`
}

// clone deep copies a profile, including the client-owned nested maps.
func (p *Profile) clone() Profile {
	out := *p
	if p.Prefs != nil {
		out.Prefs = make(map[string]string, len(p.Prefs))
		for key, value := range p.Prefs {
			out.Prefs[key] = value
		}
	}
	out.Progress = cloneAnyMap(p.Progress)
	out.ProgressDocument = cloneAnyMap(p.ProgressDocument)
	out.ProgressSlots = cloneAnyMap(p.ProgressSlots)
	if p.AppliedProgressRevisions != nil {
		out.AppliedProgressRevisions = make(map[string]uint64, len(p.AppliedProgressRevisions))
		for device, revision := range p.AppliedProgressRevisions {
			out.AppliedProgressRevisions[device] = revision
		}
	}
	if p.ProgressCommandReceipts != nil {
		out.ProgressCommandReceipts = make(map[string]bool, len(p.ProgressCommandReceipts))
		for key, value := range p.ProgressCommandReceipts {
			out.ProgressCommandReceipts[key] = value
		}
	}
	out.ClientState = cloneAnyMap(p.ClientState)
	if p.TransferredAt != nil {
		at := *p.TransferredAt
		out.TransferredAt = &at
	}
	return out
}

// cloneAnyMap deep copies JSON-shaped data. The values arrived from a JSON
// decode, so the only composites that can occur are map[string]any and []any;
// anything else is a scalar and copies by value.
func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneAny(value)
	}
	return out
}

func cloneAny(in any) any {
	switch value := in.(type) {
	case map[string]any:
		return cloneAnyMap(value)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = cloneAny(item)
		}
		return out
	default:
		return value
	}
}

const maxAppliedProgressDevices = 64

func progressDeviceKey(device string) (string, error) {
	device = strings.TrimSpace(device)
	if device == "" {
		return "", errors.New("store: empty progress device")
	}
	if len(device) > 512 {
		return "", errors.New("store: progress device is too long")
	}
	sum := sha256.Sum256([]byte(device))
	return fmt.Sprintf("%x", sum[:16]), nil
}

func nextProgressRevision(current uint64) uint64 {
	if current == ^uint64(0) {
		return 1
	}
	return current + 1
}

func advanceProfileProgressRevision(profile *Profile) {
	profile.ProgressRevision = nextProgressRevision(profile.ProgressRevision)
	profile.AppliedProgressRevisions = nil
	profile.ProgressCommandReceipts = nil
}

// PendingProgressRevision reports the operator revision this device still has
// to apply. Client-owned profiles use revision zero and never enter this flow.
func (p Profile) PendingProgressRevision(device string) (uint64, bool) {
	if p.ProgressRevision == 0 {
		return 0, false
	}
	key, err := progressDeviceKey(device)
	if err != nil {
		return 0, false
	}
	return p.ProgressRevision, p.AppliedProgressRevisions[key] != p.ProgressRevision
}

// AcknowledgeProfileProgressRevision records an exact revision only while it
// is still current. This prevents a session that started before a newer admin
// edit from acknowledging data it was never offered.
func (s *Store) AcknowledgeProfileProgressRevision(playerID, device string, revision uint64) (bool, error) {
	key, err := progressDeviceKey(device)
	if err != nil {
		return false, err
	}
	if revision == 0 {
		return false, errors.New("store: zero progress revision cannot be acknowledged")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(strings.TrimSpace(playerID))
	profile, ok := s.state.Profiles[playerID]
	if !ok {
		return false, ErrNotFound
	}
	if profile.ProgressRevision != revision {
		return false, nil
	}
	if profile.AppliedProgressRevisions[key] == revision {
		return true, nil
	}
	if _, exists := profile.AppliedProgressRevisions[key]; !exists && len(profile.AppliedProgressRevisions) >= maxAppliedProgressDevices {
		return false, errors.New("store: too many devices acknowledged this progress revision")
	}
	if profile.AppliedProgressRevisions == nil {
		profile.AppliedProgressRevisions = make(map[string]uint64)
	}
	profile.AppliedProgressRevisions[key] = revision
	if err := s.persistLocked(); err != nil {
		delete(profile.AppliedProgressRevisions, key)
		return false, err
	}
	return true, nil
}

// counterInt64 accepts the integer representations produced by both Go code
// and encoding/json. Fractional, negative and overflowing values are kept in
// the raw map but are not projected into the trusted scalar counters.
func counterInt64(value any) (int64, bool) {
	var out int64
	switch number := value.(type) {
	case int:
		out = int64(number)
	case int8:
		out = int64(number)
	case int16:
		out = int64(number)
	case int32:
		out = int64(number)
	case int64:
		out = number
	case uint:
		if uint64(number) > math.MaxInt64 {
			return 0, false
		}
		out = int64(number)
	case uint8:
		out = int64(number)
	case uint16:
		out = int64(number)
	case uint32:
		out = int64(number)
	case uint64:
		if number > math.MaxInt64 {
			return 0, false
		}
		out = int64(number)
	case float32:
		value := float64(number)
		if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) || value > math.MaxInt64 {
			return 0, false
		}
		out = int64(value)
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) || number > math.MaxInt64 {
			return 0, false
		}
		out = int64(number)
	case json.Number:
		parsed, err := strconv.ParseInt(number.String(), 10, 64)
		if err != nil {
			return 0, false
		}
		out = parsed
	default:
		return 0, false
	}
	return out, out >= 0
}

func projectProfileCounters(profile *Profile, values map[string]any, compact bool) {
	if values == nil {
		return
	}
	project := func(keys []string, assign func(int64)) {
		for _, key := range keys {
			value, present := values[key]
			if !present {
				continue
			}
			if number, ok := counterInt64(value); ok {
				assign(number)
			}
			return
		}
	}
	levelKeys := []string{"level"}
	experienceKeys := []string{"experience", "exp"}
	if compact {
		levelKeys = append(levelKeys, "l")
		experienceKeys = append(experienceKeys, "e")
	}
	project(levelKeys, func(value int64) {
		if int64(int(value)) == value {
			profile.Level = int(value)
		}
	})
	project(experienceKeys, func(value int64) { profile.Experience = value })
	project([]string{"coins"}, func(value int64) { profile.Coins = value })
	project([]string{"gems"}, func(value int64) { profile.Gems = value })
}

func projectProgressDocumentCounters(profile *Profile, document map[string]any) {
	projectProfileCounters(profile, document, false)
	if section, ok := document["4"].(map[string]any); ok {
		projectProfileCounters(profile, section, true)
	}
}

// ValidPlayerID reports whether id has the shape the 23.1.3 client parsers
// expect: exactly nine decimal digits, the same shape mintPlayerID produces.
// An operator typing an id by hand is the main way a malformed one could enter
// the store, so it is checked at the door rather than at first use.
func ValidPlayerID(id string) bool {
	if len(id) != 9 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

// Profile returns one profile by player id.
func (s *Store) Profile(playerID string) (Profile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	playerID = s.resolvePlayerIDLocked(strings.TrimSpace(playerID))
	profile, ok := s.state.Profiles[playerID]
	if !ok {
		return Profile{}, ErrNotFound
	}
	return profile.clone(), nil
}

// Profiles returns every profile, ordered by player id so the listing is
// stable between calls.
func (s *Store) Profiles() []Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Profile, 0, len(s.state.Profiles))
	for _, profile := range s.state.Profiles {
		out = append(out, profile.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PlayerID < out[j].PlayerID })
	return out
}

// EnsureProfile returns the profile for playerID, creating an empty one on
// first contact. The nick is only used when creating, so a client that changed
// its display name cannot overwrite an admin rename by reconnecting.
func (s *Store) EnsureProfile(playerID, nick string) (Profile, error) {
	playerID = strings.TrimSpace(playerID)
	if playerID == "" {
		return Profile{}, errors.New("store: empty player id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(playerID)
	profile, err := s.ensureProfileLocked(playerID, nick)
	if err != nil {
		return Profile{}, err
	}
	return profile.clone(), nil
}

func (s *Store) ensureProfileLocked(playerID, nick string) (*Profile, error) {
	if profile, ok := s.state.Profiles[playerID]; ok {
		return profile, nil
	}
	// A state file written before profiles existed has no profiles member, and
	// a nil map is fine to read but panics on write.
	if s.state.Profiles == nil {
		s.state.Profiles = make(map[string]*Profile)
	}
	now := time.Now().UTC()
	profile := &Profile{
		PlayerID:  playerID,
		Nick:      strings.TrimSpace(nick),
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.state.Profiles[playerID] = profile
	if err := s.persistLocked(); err != nil {
		delete(s.state.Profiles, playerID)
		return nil, err
	}
	return profile, nil
}

// MergeProfilePrefs folds the client's key/value state into the stored profile
// and returns the merged result.
//
// Merging rather than replacing is deliberate: the client sends the keys it
// currently cares about, not the full set, so a replace would delete state on
// every partial sync. Deleting a key is an explicit admin action.
func (s *Store) MergeProfilePrefs(playerID string, prefs map[string]string, context ...ProgressWriteContext) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(strings.TrimSpace(playerID))
	profile, ok := s.state.Profiles[playerID]
	if !ok {
		return Profile{}, ErrNotFound
	}
	if allowed, err := allowProfileClientWrite(profile, context); err != nil {
		return Profile{}, err
	} else if !allowed {
		return profile.clone(), nil
	}
	before := profile.clone()
	if profile.Prefs == nil {
		profile.Prefs = make(map[string]string, len(prefs))
	}
	for key, value := range prefs {
		profile.Prefs[key] = value
	}
	profile.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		*profile = before
		return Profile{}, err
	}
	return profile.clone(), nil
}

func isCoreProgressKey(key string) bool {
	switch key {
	case "level", "experience", "exp", "coins", "gems":
		return true
	default:
		return false
	}
}

// MergeProfileProgress folds a progress payload into the stored profile, for
// the same reason MergeProfilePrefs merges.
func (s *Store) MergeProfileProgress(playerID string, progress map[string]any, context ...ProgressWriteContext) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(strings.TrimSpace(playerID))
	profile, ok := s.state.Profiles[playerID]
	if !ok {
		return Profile{}, ErrNotFound
	}
	if allowed, err := allowProfileClientWrite(profile, context); err != nil {
		return Profile{}, err
	} else if !allowed {
		return profile.clone(), nil
	}
	before := profile.clone()

	if profile.Progress == nil {
		profile.Progress = make(map[string]any, len(progress))
	}
	for key, value := range progress {
		if profile.ProgressRevision != 0 && len(context) == 0 && isCoreProgressKey(key) {
			continue
		}
		profile.Progress[key] = cloneAny(value)
	}
	if profile.ProgressRevision == 0 || len(context) != 0 {
		projectProfileCounters(profile, progress, false)
	}
	reconcileProfileCounters(profile)
	profile.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		*profile = before
		return Profile{}, err
	}
	return profile.clone(), nil
}

// MergeProfileProgressDocument folds the client's boot-time progress document
// into the stored profile.
//
// The document is merged a slot at a time for the same reason prefs are: the
// client owns the slot set, so a replace would drop a slot this server has not
// been taught about yet. A slot that does arrive replaces its stored copy
// whole, because a slot's members are the client's own encoding of that one
// slot, and half merging two versions of it would produce a state neither side
// ever held.
func (s *Store) MergeProfileProgressDocument(playerID string, document map[string]any, context ...ProgressWriteContext) (Profile, error) {
	playerID = strings.TrimSpace(playerID)
	if playerID == "" {
		return Profile{}, errors.New("store: empty player id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(playerID)
	profile, err := s.ensureProfileLocked(playerID, "")
	if err != nil {
		return Profile{}, err
	}
	if allowed, err := allowProfileClientWrite(profile, context); err != nil {
		return Profile{}, err
	} else if !allowed {
		return profile.clone(), nil
	}
	before := profile.clone()

	if profile.ProgressRevision != 0 && len(context) == 0 {
		document = cloneAnyMap(document)
		level, experience, coins, gems := profile.Counters()
		syncCoreSlotDocument(document, level, experience, coins, gems)
	}
	if profile.ProgressDocument == nil {
		profile.ProgressDocument = make(map[string]any, len(document))
	}
	for key, value := range document {
		profile.ProgressDocument[key] = cloneAny(value)
	}
	if profile.ProgressRevision == 0 || len(context) != 0 {
		projectProgressDocumentCounters(profile, document)
	}
	reconcileProfileCounters(profile)
	profile.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		*profile = before
		return Profile{}, err
	}
	return profile.clone(), nil
}

// MergeProfileProgressSlots folds the slot document the client uploads with
// create_slots into the stored profile.
//
// A slot arrives as the client's own encoding of that one slot, so a slot that
// is sent replaces its stored copy whole and a slot that is not sent is left
// as it was. That is the rule the boot document already follows, for the same
// reason: the client owns the slot set, and half merging two versions of one
// slot would produce a state neither side ever held.
func (s *Store) MergeProfileProgressSlots(playerID string, slots map[string]any, context ...ProgressWriteContext) (Profile, error) {
	playerID = strings.TrimSpace(playerID)
	if playerID == "" {
		return Profile{}, errors.New("store: empty player id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(playerID)
	profile, err := s.ensureProfileLocked(playerID, "")
	if err != nil {
		return Profile{}, err
	}
	if allowed, err := allowProfileClientWrite(profile, context); err != nil {
		return Profile{}, err
	} else if !allowed {
		return profile.clone(), nil
	}
	before := profile.clone()

	if profile.ProgressRevision != 0 && len(context) == 0 {
		slots = cloneAnyMap(slots)
		level, experience, coins, gems := profile.Counters()
		syncCoreSlotDocument(slots, level, experience, coins, gems)
	}
	if profile.ProgressSlots == nil {
		profile.ProgressSlots = make(map[string]any, len(slots))
	}
	for key, value := range slots {
		profile.ProgressSlots[key] = cloneAny(value)
	}
	mirrorRawSlots(profile)
	if len(context) != 0 || profile.ProgressRevision == 0 {
		projectReplacementSlotState(profile, slots)
	}
	reconcileProfileCounters(profile)
	profile.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		*profile = before
		return Profile{}, err
	}
	return profile.clone(), nil
}

// ReplaceProfileProgressSlots atomically installs the complete raw slot set
// sent by replace_progress. Unlike create_slots, that event is a snapshot: a
// slot omitted by the client is no longer part of the profile. Callers must
// validate and unwrap every wire {d,h} record before invoking this method.
func (s *Store) ReplaceProfileProgressSlots(playerID string, slots map[string]any, context ...ProgressWriteContext) (Profile, error) {
	playerID = strings.TrimSpace(playerID)
	if playerID == "" {
		return Profile{}, errors.New("store: empty player id")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(playerID)
	profile, existed := s.state.Profiles[playerID]
	profilesWereNil := s.state.Profiles == nil
	if !existed {
		if s.state.Profiles == nil {
			s.state.Profiles = make(map[string]*Profile)
		}
		now := time.Now().UTC()
		profile = &Profile{PlayerID: playerID, CreatedAt: now, UpdatedAt: now}
	}

	if allowed, err := allowProfileClientWrite(profile, context); err != nil {
		return Profile{}, err
	} else if !allowed {
		return profile.clone(), nil
	}

	if !existed {
		s.state.Profiles[playerID] = profile
	}

	before := profile.clone()
	account := s.state.Accounts[playerID]
	accountNick := ""
	if account != nil {
		accountNick = account.Nick
	}

	if profile.ProgressRevision != 0 && len(context) == 0 {
		slots = cloneAnyMap(slots)
		level, experience, coins, gems := profile.Counters()
		syncCoreSlotDocument(slots, level, experience, coins, gems)
	}
	profile.ProgressSlots = cloneAnyMap(slots)
	for key := range profile.ProgressDocument {
		if _, err := strconv.Atoi(key); err == nil {
			if _, present := slots[key]; !present {
				delete(profile.ProgressDocument, key)
			}
		}
	}
	mirrorRawSlots(profile)
	projectReplacementSlotState(profile, slots)
	reconcileProfileCounters(profile)
	if account != nil && profile.Nick != "" {
		account.Nick = profile.Nick
	}
	profile.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		if existed {
			*profile = before
		} else {
			delete(s.state.Profiles, playerID)
			if profilesWereNil {
				s.state.Profiles = nil
			}
		}
		if account != nil {
			account.Nick = accountNick
		}
		return Profile{}, err
	}
	return profile.clone(), nil
}

// projectReplacementSlotState maps only fields proven by 23.1.3 snapshots.
// Slot 1 owns the nickname, slot 3 owns the two base currencies, and slot 4
// owns level/experience. Empty currency slot 3 is the client's zero encoding.
func projectReplacementSlotState(profile *Profile, slots map[string]any) {
	if identity, ok := slots["1"].(map[string]any); ok {
		if nick, ok := identity["n"].(string); ok && strings.TrimSpace(nick) != "" {
			profile.Nick = strings.TrimSpace(nick)
		}
	}
	if currencies, ok := slots["3"].(map[string]any); ok {
		profile.Coins = 0
		profile.Gems = 0
		if value, ok := counterInt64(currencies["Coins"]); ok {
			profile.Coins = value
		}
		if value, ok := counterInt64(currencies["GemsCurrency"]); ok {
			profile.Gems = value
		}
	}
	if progress, ok := slots["4"].(map[string]any); ok {
		profile.Level = 0
		profile.Experience = 0
		projectProfileCounters(profile, progress, true)
	}
}

// MergeProfileClientState preserves structured fields that do not belong to
// one of the progress documents. As with progress, a partial client update is
// merged and unknown members survive unchanged.
func (s *Store) MergeProfileClientState(playerID string, values map[string]any, context ...ProgressWriteContext) (Profile, error) {
	playerID = strings.TrimSpace(playerID)
	if playerID == "" {
		return Profile{}, errors.New("store: empty player id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(playerID)
	profile, err := s.ensureProfileLocked(playerID, "")
	if err != nil {
		return Profile{}, err
	}
	if allowed, err := allowProfileClientWrite(profile, context); err != nil {
		return Profile{}, err
	} else if !allowed {
		return profile.clone(), nil
	}
	before := profile.clone()
	account := s.state.Accounts[playerID]
	accountNick := ""
	if account != nil {
		accountNick = account.Nick
	}

	if profile.ClientState == nil {
		profile.ClientState = make(map[string]any, len(values))
	}
	for key, value := range values {
		if profile.ProgressRevision != 0 && len(context) == 0 && isCoreProgressKey(key) {
			continue
		}
		profile.ClientState[key] = cloneAny(value)
	}
	if nick, ok := values["nick"].(string); ok && strings.TrimSpace(nick) != "" {
		profile.Nick = strings.TrimSpace(nick)
		if account != nil {
			account.Nick = profile.Nick
		}
	}
	if profile.ProgressRevision == 0 || len(context) != 0 {
		projectProfileCounters(profile, values, false)
	}
	reconcileProfileCounters(profile)
	profile.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		*profile = before
		if account != nil {
			account.Nick = accountNick
		}
		return Profile{}, err
	}
	return profile.clone(), nil
}

// ReplaceProfileRawState is the explicit support-editor operation. Unlike the
// client merge methods it replaces all five documents, because the operator
// submits a complete reviewed snapshot. Callers must validate that every
// document was present before invoking it; empty maps intentionally clear a
// document while unknown members are preserved verbatim.
func (s *Store) ReplaceProfileRawState(playerID string, prefs map[string]string, progress, document, slots, clientState map[string]any) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(strings.TrimSpace(playerID))
	profile, ok := s.state.Profiles[playerID]
	if !ok {
		return Profile{}, ErrNotFound
	}
	before := profile.clone()
	profile.Prefs = make(map[string]string, len(prefs))
	for key, value := range prefs {
		profile.Prefs[key] = value
	}
	profile.Progress = cloneAnyMap(progress)
	profile.ProgressDocument = cloneAnyMap(document)
	profile.ProgressSlots = cloneAnyMap(slots)
	profile.ClientState = cloneAnyMap(clientState)
	// This is a complete replacement, not a merge. Reset the projections first
	// so clearing raw documents also clears the support-visible counters rather
	// than leaving stale values from the previous snapshot.
	profile.Level = 0
	profile.Experience = 0
	profile.Coins = 0
	profile.Gems = 0
	projectProfileCounters(profile, profile.Progress, false)
	projectProgressDocumentCounters(profile, profile.ProgressDocument)
	projectProfileCounters(profile, profile.ClientState, false)
	projectReplacementSlotState(profile, profile.ProgressSlots)
	if _, core := profile.ProgressSlots["3"]; core {
		reconcileProfileCounters(profile)
	} else if _, core := profile.ProgressSlots["4"]; core {
		reconcileProfileCounters(profile)
	}
	advanceProfileProgressRevision(profile)
	profile.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		*profile = before
		return Profile{}, err
	}
	return profile.clone(), nil
}

func syncCoreSlotDocument(document map[string]any, level int, experience, coins, gems int64) {
	section, ok := document["4"].(map[string]any)
	if !ok {
		section = map[string]any{}
		document["4"] = section
	}
	section["l"] = level
	section["e"] = experience

	currencies, ok := document["3"].(map[string]any)
	if !ok {
		currencies = map[string]any{}
		document["3"] = currencies
	}
	currencies["Coins"] = coins
	currencies["GemsCurrency"] = gems
}

// syncClientFacingSlots writes an operator edit into the two slot documents the
// client actually applies, so a support change reaches the game instead of
// living only in the flat counters.
//
// Measured 2026-09-01 on player 541028684: an edit setting 123123123 coins and
// 123123123 gems reached Progress and the flat counters, and slot 4 reached
// level 32 with experience 20000, but slot 3 was never written and never
// existed in the stored document. The client applies the document slot by slot,
// so the level arrived and the two currencies did not. Slot 3 owns Coins and
// GemsCurrency and slot 4 owns l and e, the same members
// projectReplacementSlotState reads back.
//
// Both documents are written because they travel on different events -
// init_progress carries ProgressDocument and get_progress_slots carries
// ProgressSlots - and a value that differed between them would let whichever
// event the client trusts last undo the edit. Absent slots are created:
// declining to create slot 3 is exactly the defect this repairs. Unknown
// members of a slot that does exist are preserved.
func syncClientFacingSlots(profile *Profile, level int, experience, coins, gems int64) {
	if profile.ProgressDocument == nil {
		profile.ProgressDocument = map[string]any{}
	}
	syncCoreSlotDocument(profile.ProgressDocument, level, experience, coins, gems)
	if profile.ProgressSlots == nil {
		profile.ProgressSlots = map[string]any{}
	}
	syncCoreSlotDocument(profile.ProgressSlots, level, experience, coins, gems)
}

// Counters reports the four flat counters progress_status puts on the wire
// beside the raw progress document, reconciled so the two cannot contradict
// each other.
//
// The document wins wherever it carries a key, because both halves travel in
// one reply and the client has no way to choose between them. Measured
// 2026-09-01 on player 541028684: this server answered level 1 with 0 coins and
// 0 gems beside a document holding level 32 and 123123123 of each currency. The
// client adopted the flat zeros, and its next replace_progress uploaded them
// back as an empty slot 3 and a slot 4 holding l 1, so an operator edit was
// erased rather than applied.
//
// The projections remain the fallback: they are what a client that has already
// synchronised uploaded, and they hold keys the flat document need not carry.
func (p Profile) Counters() (level int, experience, coins, gems int64) {
	level, experience, coins, gems = p.Level, p.Experience, p.Coins, p.Gems
	if value, ok := counterInt64(p.Progress["level"]); ok && int64(int(value)) == value {
		level = int(value)
	}
	for _, key := range []string{"experience", "exp"} {
		if value, ok := counterInt64(p.Progress[key]); ok {
			experience = value
			break
		}
	}
	if value, ok := counterInt64(p.Progress["coins"]); ok {
		coins = value
	}
	if value, ok := counterInt64(p.Progress["gems"]); ok {
		gems = value
	}
	return level, experience, coins, gems
}

// SetProfile overwrites the operator-editable fields of a profile. The nested
// client-owned maps are left alone: they are synchronised by the game and an
// admin edit has no business silently dropping them.
func (s *Store) SetProfile(playerID string, nick string, level int, experience, coins, gems int64) (Profile, error) {
	if level < 0 || experience < 0 || coins < 0 || gems < 0 {
		return Profile{}, errors.New("store: profile values cannot be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(strings.TrimSpace(playerID))
	profile, ok := s.state.Profiles[playerID]
	if !ok {
		return Profile{}, ErrNotFound
	}
	before := profile.clone()
	account := s.state.Accounts[playerID]
	accountNick := ""
	if account != nil {
		accountNick = account.Nick
	}
	beforeLevel, beforeExperience, beforeCoins, beforeGems := profile.Counters()
	nick = strings.TrimSpace(nick)
	nickChanged := nick != "" && profile.Nick != nick
	advanceRevision := nickChanged || profile.ProgressRevision == 0 || beforeLevel != level || beforeExperience != experience || beforeCoins != coins || beforeGems != gems

	if nick = strings.TrimSpace(nick); nick != "" {
		profile.Nick = nick
		for _, document := range []map[string]any{profile.ProgressSlots, profile.ProgressDocument} {
			if slot, ok := document["1"].(map[string]any); ok {
				slot["n"] = nick
			}
		}
		if account := s.state.Accounts[playerID]; account != nil {
			account.Nick = nick
		}
	}
	profile.Level = level
	profile.Experience = experience
	profile.Coins = coins
	profile.Gems = gems
	if profile.Progress == nil {
		profile.Progress = map[string]any{}
	}
	profile.Progress["level"] = level
	profile.Progress["experience"] = experience
	profile.Progress["coins"] = coins
	profile.Progress["gems"] = gems
	syncClientFacingSlots(profile, level, experience, coins, gems)
	reconcileProfileCounters(profile)
	if advanceRevision {
		advanceProfileProgressRevision(profile)
	}
	profile.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		*profile = before
		if account != nil {
			account.Nick = accountNick
		}
		return Profile{}, err
	}
	return profile.clone(), nil
}

// AdoptSocketIdentity binds the identity the client presents on the encrypted
// /sio channel to an account.
//
// The socket carries its own uniq_id and device fingerprint, and they are not
// the ones the HTTP registration handed out: a captured session showed
// uniq_id 350713954 with device 918ad65... on the socket while the HTTP account
// was player 165330406 with device 45fdd05... The client keeps using its own
// uniq_id regardless of what this server would prefer, so the socket is
// treated as authoritative for identity and the account is created under that
// id when it is well formed and free. Anything else would make the account a
// player can be banned or restored under differ from the one the game shows.
//
// An empty uniq_id is not an error: it is how the client asks to be registered.
// A first run sends uniq_id and hash empty and identifies itself by device
// fingerprint alone, so this server mints the id in that case and the reply
// hands it back for the client to remember.
func (s *Store) AdoptSocketIdentity(uniqID, device, ip string) (Account, bool, error) {
	uniqID = strings.TrimSpace(uniqID)
	device = strings.TrimSpace(device)
	if uniqID == "" && device == "" {
		return Account{}, false, errors.New("store: the socket presented neither an id nor a device")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if uniqID != "" {
		uniqID = s.resolvePlayerIDLocked(uniqID)
	}

	if uniqID == "" {
		// The same handset has to come back to the same account, or every
		// launch of a freshly installed client would mint another player. The
		// oldest match wins so the result does not depend on map iteration
		// order if a device ever ends up on two accounts.
		var match *Account
		for _, account := range s.state.Accounts {
			if account.SocketDevice != device && account.DeviceID != device {
				continue
			}
			if match == nil || account.CreatedAt.Before(match.CreatedAt) {
				match = account
			}
		}
		if match != nil {
			uniqID = match.PlayerID
		} else {
			minted, err := s.mintFreePlayerIDLocked()
			if err != nil {
				return Account{}, false, err
			}
			uniqID = minted
		}
	}

	if account, ok := s.state.Accounts[uniqID]; ok {
		account.LastSeenAt = now
		if ip != "" {
			account.LastIP = ip
		}
		if device != "" {
			account.SocketDevice = device
		}
		if _, err := s.ensureProfileLocked(uniqID, account.Nick); err != nil {
			return Account{}, false, err
		}
		if err := s.persistLocked(); err != nil {
			return Account{}, false, err
		}
		return *account, false, nil
	}

	if !ValidPlayerID(uniqID) {
		return Account{}, false, ErrBadPlayerID
	}
	session, err := s.newSession(uniqID)
	if err != nil {
		return Account{}, false, err
	}
	account := &Account{
		PlayerID:     uniqID,
		DeviceID:     device,
		SocketDevice: device,
		Nick:         "Player" + uniqID[len(uniqID)-4:],
		Session:      session,
		CreatedAt:    now,
		LastSeenAt:   now,
		LastIP:       ip,
	}
	s.state.Accounts[uniqID] = account
	if _, err := s.ensureProfileLocked(uniqID, account.Nick); err != nil {
		delete(s.state.Accounts, uniqID)
		return Account{}, false, err
	}
	if err := s.persistLocked(); err != nil {
		delete(s.state.Accounts, uniqID)
		delete(s.state.Profiles, uniqID)
		return Account{}, false, err
	}
	return *account, true, nil
}

// ChangePlayerID moves an account and its profile to a new id.
//
// SECURITY: bans recorded against the old player id are rewritten to the new
// one. Without that, changing an id would be a one-click way out of a ban, and
// this operation exists precisely so an operator can hand out new ids freely.
func (s *Store) ChangePlayerID(oldID, newID string) (Account, error) {
	oldID = strings.TrimSpace(oldID)
	newID = strings.TrimSpace(newID)
	if !ValidPlayerID(newID) {
		return Account{}, ErrBadPlayerID
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	oldID = s.resolvePlayerIDLocked(oldID)
	if oldID == newID {
		return Account{}, ErrSameAccount
	}
	if _, retired := s.state.Aliases[newID]; retired {
		return Account{}, ErrIDTaken
	}

	account, ok := s.state.Accounts[oldID]
	if !ok {
		return Account{}, ErrNotFound
	}
	if _, taken := s.state.Accounts[newID]; taken {
		return Account{}, ErrIDTaken
	}

	delete(s.state.Accounts, oldID)
	account.PlayerID = newID
	s.state.Accounts[newID] = account

	if profile, ok := s.state.Profiles[oldID]; ok {
		delete(s.state.Profiles, oldID)
		profile.PlayerID = newID
		profile.UpdatedAt = time.Now().UTC()
		s.state.Profiles[newID] = profile
	}

	aliases := make([]string, 0)
	for alias := range s.state.Aliases {
		if s.resolvePlayerIDLocked(alias) == oldID {
			aliases = append(aliases, alias)
		}
	}
	for _, alias := range aliases {
		s.state.Aliases[alias] = newID
	}
	for i := range s.state.Bans {
		if s.state.Bans[i].Kind == BanPlayer && s.state.Bans[i].Subject == oldID {
			s.state.Bans[i].Subject = newID
		}
	}

	if err := s.persistLocked(); err != nil {
		return Account{}, err
	}
	return *account, nil
}

// TransferProgress copies a profile's progress onto another account, which is
// the account-restoration flow: a player who lost access to an old id keeps
// playing on a new one, and both logins stay valid.
//
// The source profile is emptied rather than deleted, so the same progress
// cannot be handed out twice by repeating the call, and both records keep an
// audit trail of where it went.
func (s *Store) TransferProgress(fromID, toID string) (Profile, error) {
	fromID = strings.TrimSpace(fromID)
	toID = strings.TrimSpace(toID)

	s.mu.Lock()
	defer s.mu.Unlock()
	fromID = s.resolvePlayerIDLocked(fromID)
	toID = s.resolvePlayerIDLocked(toID)
	if fromID == toID {
		return Profile{}, ErrSameAccount
	}

	if _, ok := s.state.Accounts[toID]; !ok {
		return Profile{}, fmt.Errorf("store: target %s: %w", toID, ErrNotFound)
	}
	source, ok := s.state.Profiles[fromID]
	if !ok {
		return Profile{}, fmt.Errorf("store: source %s: %w", fromID, ErrNotFound)
	}
	target, err := s.ensureProfileLocked(toID, "")
	if err != nil {
		return Profile{}, err
	}

	now := time.Now().UTC()
	targetNick := target.Nick
	targetCreated := target.CreatedAt

	moved := source.clone()
	moved.PlayerID = toID
	moved.CreatedAt = targetCreated
	moved.UpdatedAt = now
	moved.TransferredFrom = fromID
	moved.TransferredTo = ""
	moved.TransferredAt = &now
	if moved.Nick == "" {
		moved.Nick = targetNick
	}
	*target = moved
	advanceProfileProgressRevision(target)

	source.Level = 0
	source.Experience = 0
	source.Coins = 0
	source.Gems = 0
	source.Prefs = nil
	source.Progress = nil
	source.ProgressDocument = nil
	source.ProgressSlots = nil
	source.ClientState = nil
	advanceProfileProgressRevision(source)
	source.UpdatedAt = now
	source.TransferredTo = toID
	source.TransferredAt = &now

	if err := s.persistLocked(); err != nil {
		return Profile{}, err
	}
	return target.clone(), nil
}

// DeleteAccount removes an account and its profile. Bans are kept: deleting a
// cheater's account must not delete the record that stops them coming back.
func (s *Store) DeleteAccount(playerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(strings.TrimSpace(playerID))
	if _, ok := s.state.Accounts[playerID]; !ok {
		return ErrNotFound
	}
	aliases := make([]string, 0)
	for alias := range s.state.Aliases {
		if s.resolvePlayerIDLocked(alias) == playerID {
			aliases = append(aliases, alias)
		}
	}
	for _, alias := range aliases {
		delete(s.state.Aliases, alias)
	}
	delete(s.state.Accounts, playerID)
	delete(s.state.Profiles, playerID)
	return s.persistLocked()
}

// Stats is the server-wide summary an operator sees at a glance.
type Stats struct {
	Accounts     int       `json:"accounts"`
	Profiles     int       `json:"profiles"`
	Bans         int       `json:"bans"`
	ActiveBans   int       `json:"active_bans"`
	NewToday     int       `json:"new_today"`
	SeenLastHour int       `json:"seen_last_hour"`
	SeenToday    int       `json:"seen_today"`
	SeenThisWeek int       `json:"seen_this_week"`
	GeneratedAt  time.Time `json:"generated_at"`
}

// Stats computes the summary in one pass under a read lock.
func (s *Store) Stats() Stats {
	now := time.Now().UTC()
	hourAgo := now.Add(-time.Hour)
	dayAgo := now.Add(-24 * time.Hour)
	weekAgo := now.Add(-7 * 24 * time.Hour)

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := Stats{
		Accounts:    len(s.state.Accounts),
		Profiles:    len(s.state.Profiles),
		Bans:        len(s.state.Bans),
		GeneratedAt: now,
	}
	for _, account := range s.state.Accounts {
		if account.CreatedAt.After(dayAgo) {
			out.NewToday++
		}
		if account.LastSeenAt.After(hourAgo) {
			out.SeenLastHour++
		}
		if account.LastSeenAt.After(dayAgo) {
			out.SeenToday++
		}
		if account.LastSeenAt.After(weekAgo) {
			out.SeenThisWeek++
		}
	}
	for _, ban := range s.state.Bans {
		if ban.Active(now) {
			out.ActiveBans++
		}
	}
	return out
}
