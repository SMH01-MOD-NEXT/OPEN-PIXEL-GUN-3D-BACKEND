// SPDX-License-Identifier: GPL-3.0-or-later

// Package store owns every piece of persistent server state: accounts,
// sessions and bans.
//
// The backing file is written through a temporary file plus rename(), so a
// crash or a pulled power cord cannot leave half a state file behind. The
// public API returns copies, never interior pointers, so a caller cannot race
// with the lock or mutate state behind it.
//
// The storage engine is deliberately behind this package's API. Phase 1 uses a
// single JSON document, which is correct and dependency-free for a private
// server with hundreds of accounts; moving to SQLite or Postgres later is a
// change to this file only.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Ban kinds.
const (
	BanPlayer = "player"
	BanDevice = "device"
	BanIP     = "ip"
)

var (
	// ErrNotFound is returned when no account matches.
	ErrNotFound = errors.New("store: account not found")
	// ErrBadBanKind is returned for an unknown ban kind.
	ErrBadBanKind = errors.New("store: unknown ban kind")
	// ErrIDExhausted is returned when a free account id could not be minted.
	ErrIDExhausted = errors.New("store: could not mint a free account id")
)

// Account is one registered player.
type Account struct {
	PlayerID string `json:"player_id"`
	DeviceID string `json:"device_id"`

	// SocketDevice is the device fingerprint the client presents on the
	// encrypted /sio channel. It is recorded separately because it is not the
	// same value as DeviceID: a captured session showed 918ad65... on the
	// socket while the HTTP registration for the same handset used 45fdd05...
	// Both are needed to ban a device and have the ban actually hold.
	SocketDevice string `json:"socket_device,omitempty"`

	Nick           string    `json:"nick"`
	Session        string    `json:"session"`
	CreatedAt      time.Time `json:"created_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	GameLastSeenAt time.Time `json:"game_last_seen_at,omitempty"`
	LastIP         string    `json:"last_ip"`
}

// Ban blocks a player id, a device id or an IP address.
type Ban struct {
	Kind      string     `json:"kind"`
	Subject   string     `json:"subject"`
	Reason    string     `json:"reason"`
	Until     *time.Time `json:"until,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// Active reports whether the ban still applies at the given instant. A nil
// Until means permanent.
func (b Ban) Active(now time.Time) bool {
	return b.Until == nil || b.Until.After(now)
}

type state struct {
	Social      socialState               `json:"social,omitempty"`
	Evidence    map[string]PlayerEvidence `json:"player_evidence,omitempty"`
	Accounts    map[string]*Account       `json:"accounts"`
	Profiles    map[string]*Profile       `json:"profiles,omitempty"`
	Aliases     map[string]string         `json:"aliases,omitempty"`
	Bans        []Ban                     `json:"bans"`
	MailboxNews []MailboxNews             `json:"mailbox_news,omitempty"`
}

// Options tunes how a store behaves.
type Options struct {
	// StableSessions derives the session token from the device id instead of
	// minting random bytes, so the same device is handed the same token on
	// every login.
	//
	// SECURITY-SENSITIVE DIAGNOSTIC, off by default. A deterministic session
	// token is a guessable credential and must never be enabled on a server
	// anyone else can reach. It exists to isolate one variable in the /sio
	// payload codec investigation; see docs/RUN.md.
	StableSessions bool
}

// Store is the concurrency-safe state holder.
type Store struct {
	mu              sync.RWMutex
	path            string
	state           state
	opts            Options
	economyObserver func(EconomyObservation)
	socialRates     map[string]socialRate
}

// Open loads (or creates) the state file in dir with default options.
func Open(dir string) (*Store, error) {
	return OpenWithOptions(dir, Options{})
}

// OpenWithOptions loads (or creates) the state file in dir.
func OpenWithOptions(dir string, opts Options) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: mkdir %s: %w", dir, err)
	}
	s := &Store{
		path: filepath.Join(dir, "state.json"),
		state: state{
			Accounts: map[string]*Account{},
			Profiles: map[string]*Profile{},
			Aliases:  map[string]string{},
		},
		opts: opts,
	}
	raw, err := os.ReadFile(s.path)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &s.state); err != nil {
			return nil, fmt.Errorf("store: parse %s: %w", s.path, err)
		}
		if s.state.Accounts == nil {
			s.state.Accounts = map[string]*Account{}
		}
		if s.state.Profiles == nil {
			s.state.Profiles = map[string]*Profile{}
		}
		if s.state.Aliases == nil {
			s.state.Aliases = map[string]string{}
		}
	case errors.Is(err, os.ErrNotExist):
		// First run.
	default:
		return nil, fmt.Errorf("store: read %s: %w", s.path, err)
	}
	// Legacy devices keep their local task cache on the ordinary synchronize
	// route. Deliver this repair once via the existing correlated full-download
	// revision flow. Preserve command receipts: this is not an account reset.
	repairedTutorial := false
	for _, p := range s.state.Profiles {
		if migratePassTutorial(p) {
			repairedTutorial = true
		}
		projectMailbox(p)
		projectPixelPass(p)
	}
	if repairedTutorial {
		if err := s.persistLocked(); err != nil {
			return nil, fmt.Errorf("store: persist Pixel Pass tutorial repair: %w", err)
		}
	}
	if migrateMailboxRewardWire(s.state.MailboxNews, time.Now().UTC()) {
		if err := s.persistLocked(); err != nil {
			return nil, fmt.Errorf("store: persist mailbox reward wire repair: %w", err)
		}
	}
	return s, nil
}

// RegisterOrLogin returns the account bound to deviceID, creating it on first
// contact. The session token is rotated on every call, so a leaked token stops
// working as soon as the real client logs in again. The bool reports creation.
//
// Options.StableSessions suspends that rotation for diagnostics; see
// newSession.
func (s *Store) RegisterOrLogin(deviceID, nick, ip string) (Account, bool, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return Account{}, false, errors.New("store: empty device id")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	session, err := s.newSession(deviceID)
	if err != nil {
		return Account{}, false, err
	}

	for _, account := range s.state.Accounts {
		if account.DeviceID != deviceID {
			continue
		}
		account.Session = session
		account.LastSeenAt = now
		account.LastIP = ip
		if nick = strings.TrimSpace(nick); nick != "" {
			account.Nick = nick
		}
		if s.state.Profiles == nil {
			s.state.Profiles = map[string]*Profile{}
		}
		if s.state.Profiles[account.PlayerID] == nil {
			s.state.Profiles[account.PlayerID] = &Profile{
				PlayerID: account.PlayerID, Nick: account.Nick, CreatedAt: now, UpdatedAt: now,
			}
		}
		if err := s.persistLocked(); err != nil {
			return Account{}, false, err
		}
		return *account, false, nil
	}

	playerID, err := s.mintFreePlayerIDLocked()
	if err != nil {
		return Account{}, false, err
	}
	if nick = strings.TrimSpace(nick); nick == "" {
		nick = "Player" + playerID[len(playerID)-4:]
	}
	account := &Account{
		PlayerID:   playerID,
		DeviceID:   deviceID,
		Nick:       nick,
		Session:    session,
		CreatedAt:  now,
		LastSeenAt: now,
		LastIP:     ip,
	}
	s.state.Accounts[playerID] = account
	if s.state.Profiles == nil {
		s.state.Profiles = map[string]*Profile{}
	}
	s.state.Profiles[playerID] = &Profile{
		PlayerID: playerID, Nick: nick, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.persistLocked(); err != nil {
		delete(s.state.Accounts, playerID)
		delete(s.state.Profiles, playerID)
		return Account{}, false, err
	}
	return *account, true, nil
}

// Authenticate validates a player id / session pair and refreshes last-seen.
func (s *Store) Authenticate(playerID, session, ip string) (Account, bool) {
	if playerID == "" || session == "" {
		return Account{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	playerID = s.resolvePlayerIDLocked(playerID)
	account, ok := s.state.Accounts[playerID]
	if !ok || account.Session != session {
		return Account{}, false
	}
	account.LastSeenAt = time.Now().UTC()
	if ip != "" {
		account.LastIP = ip
	}
	// A failed persist must not invalidate a valid session; the in-memory
	// answer stays authoritative and the error surfaces on the next write.
	_ = s.persistLocked()
	return *account, true
}

// Account returns one account by id.
func (s *Store) Account(playerID string) (Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	playerID = s.resolvePlayerIDLocked(playerID)
	account, ok := s.state.Accounts[playerID]
	if !ok {
		return Account{}, ErrNotFound
	}
	return *account, nil
}

// Accounts returns every account, ordered by creation time.
func (s *Store) Accounts() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Account, 0, len(s.state.Accounts))
	for _, account := range s.state.Accounts {
		out = append(out, *account)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// SetNick renames an account.
func (s *Store) SetNick(playerID, nick string) error {
	nick = strings.TrimSpace(nick)
	if nick == "" {
		return errors.New("store: empty nick")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(playerID)
	account, ok := s.state.Accounts[playerID]
	if !ok {
		return ErrNotFound
	}
	account.Nick = nick
	return s.persistLocked()
}

// AddBan upserts a single ban; use AddBans for an atomic player/device change.
func (s *Store) AddBan(ban Ban) error { return s.AddBans([]Ban{ban}) }

// RemoveBans removes matching records, including expired duplicates.
func (s *Store) RemoveBans(kind, subject string) (int, error) {
	return s.RemoveBanSubjects([]Ban{{Kind: kind, Subject: subject}})
}

// Bans returns detached records, including detached deadline pointers.
func (s *Store) Bans() []Ban {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyBans(s.state.Bans)
}

// MatchBan returns the first active ban hitting any of the supplied
// identifiers. Empty identifiers never match.
func (s *Store) MatchBan(playerID, deviceID, ip string) (Ban, bool) {
	now := time.Now().UTC()
	s.mu.RLock()
	defer s.mu.RUnlock()
	playerID = s.resolvePlayerIDLocked(playerID)
	for _, ban := range s.state.Bans {
		if !ban.Active(now) {
			continue
		}
		var candidate string
		subject := ban.Subject
		switch ban.Kind {
		case BanPlayer:
			candidate = playerID
			subject = s.resolvePlayerIDLocked(subject)
		case BanDevice:
			candidate = deviceID
		case BanIP:
			candidate = ip
		}
		if candidate != "" && candidate == subject {
			matched := copyBan(ban)
			matched.Subject = subject
			return matched, true
		}
	}
	return Ban{}, false
}

func (s *Store) persistLocked() error {
	raw, err := json.MarshalIndent(&s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode: %w", err)
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "state-*.json.tmp")
	if err != nil {
		return fmt.Errorf("store: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("store: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("store: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("store: rename: %w", err)
	}
	return nil
}

func (s *Store) mintFreePlayerIDLocked() (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		id, err := mintPlayerID()
		if err != nil {
			return "", err
		}
		_, accountTaken := s.state.Accounts[id]
		_, aliasTaken := s.state.Aliases[id]
		if !accountTaken && !aliasTaken {
			return id, nil
		}
	}
	return "", ErrIDExhausted
}

// mintPlayerID returns a nine-digit id, matching the shape the 23.1.3 client
// parsers expect from the retired account service.
func mintPlayerID() (string, error) {
	span := big.NewInt(900000000)
	n, err := rand.Int(rand.Reader, span)
	if err != nil {
		return "", fmt.Errorf("store: mint id: %w", err)
	}
	return fmt.Sprintf("%09d", n.Int64()+100000000), nil
}

// newSession returns the token to hand back for this login: 32 random bytes
// normally, or a deterministic function of the device id when the stable
// session diagnostic is on.
//
// SECURITY-SENSITIVE. The stable branch produces a guessable credential and is
// only reachable through Options.StableSessions, which defaults to off and is
// documented as lab-only.
func (s *Store) newSession(deviceID string) (string, error) {
	if s.opts.StableSessions {
		return stableSession(deviceID), nil
	}
	return mintSession()
}

// stableSessionDomain keeps the diagnostic token from colliding with any other
// hash of a device id.
const stableSessionDomain = "opg3d-stable-session\x00"

// stableSession derives a session token from the device id. The result has the
// same shape as a minted one, 32 bytes hex encoded, so nothing downstream can
// tell the difference and the diagnostic changes exactly one variable.
func stableSession(deviceID string) string {
	sum := sha256.Sum256([]byte(stableSessionDomain + deviceID))
	return hex.EncodeToString(sum[:])
}

func mintSession() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("store: mint session: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
