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
	"errors"
	"fmt"
	"sort"
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
func (s *Store) MergeProfilePrefs(playerID string, prefs map[string]string) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile, ok := s.state.Profiles[playerID]
	if !ok {
		return Profile{}, ErrNotFound
	}
	if profile.Prefs == nil {
		profile.Prefs = make(map[string]string, len(prefs))
	}
	for key, value := range prefs {
		profile.Prefs[key] = value
	}
	profile.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		return Profile{}, err
	}
	return profile.clone(), nil
}

// MergeProfileProgress folds a progress payload into the stored profile, for
// the same reason MergeProfilePrefs merges.
func (s *Store) MergeProfileProgress(playerID string, progress map[string]any) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile, ok := s.state.Profiles[playerID]
	if !ok {
		return Profile{}, ErrNotFound
	}
	if profile.Progress == nil {
		profile.Progress = make(map[string]any, len(progress))
	}
	for key, value := range progress {
		profile.Progress[key] = cloneAny(value)
	}
	profile.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		return Profile{}, err
	}
	return profile.clone(), nil
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
	profile, ok := s.state.Profiles[playerID]
	if !ok {
		return Profile{}, ErrNotFound
	}
	if nick = strings.TrimSpace(nick); nick != "" {
		profile.Nick = nick
	}
	profile.Level = level
	profile.Experience = experience
	profile.Coins = coins
	profile.Gems = gems
	profile.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
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
func (s *Store) AdoptSocketIdentity(uniqID, device, ip string) (Account, bool, error) {
	uniqID = strings.TrimSpace(uniqID)
	if uniqID == "" {
		return Account{}, false, errors.New("store: empty socket uniq id")
	}
	device = strings.TrimSpace(device)

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
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
	if oldID == newID {
		return Account{}, ErrSameAccount
	}
	if !ValidPlayerID(newID) {
		return Account{}, ErrBadPlayerID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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
	if fromID == toID {
		return Profile{}, ErrSameAccount
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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

	source.Level = 0
	source.Experience = 0
	source.Coins = 0
	source.Gems = 0
	source.Prefs = nil
	source.Progress = nil
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
	if _, ok := s.state.Accounts[playerID]; !ok {
		return ErrNotFound
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
