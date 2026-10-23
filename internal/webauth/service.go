// SPDX-License-Identifier: GPL-3.0-or-later
// Package webauth owns ephemeral login challenges and encrypted durable sessions.
// Raw session tokens and OTPs are never persisted, nor written to gameplay state.
package webauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

const ChallengeTTL = 5 * time.Minute
const SessionTTL = 90 * 24 * time.Hour
const IdleTTL = SessionTTL
const maxRecords = 2048
const maxBuckets = 8192

var ErrLimited = errors.New("login limited")
var ErrInvalid = errors.New("invalid login")

type Identity struct {
	PlayerID, Fingerprint string
	Epoch                 uint64 `json:"epoch,omitempty"`
}
type Challenge struct {
	Token, Reference string
	ExpiresAt        time.Time
}
type Letter struct {
	ID, Changed     int
	CreatedAt       time.Time
	Reference, Code string
	Active          bool
}
type Session struct {
	Token, CSRF     string
	Identity        Identity
	ExpiresAt       time.Time
	AuthenticatedAt time.Time
}
type challenge struct {
	identity         Identity
	id               int
	created, expires time.Time
	attempts         int
	closed           bool
	reference        string
}
type session struct {
	identity      Identity
	created, seen time.Time
}
type bucket struct {
	since time.Time
	count int
}
type Service struct {
	mu         sync.Mutex
	path       string
	storageKey [32]byte
	failed     bool
	key        [32]byte
	challenges map[[32]byte]*challenge
	sessions   map[[32]byte]*session
	buckets    map[string]bucket
	nextID     int
	now        func() time.Time
}

func New() (*Service, error) {
	s := &Service{challenges: map[[32]byte]*challenge{}, sessions: map[[32]byte]*session{}, buckets: map[string]bucket{}, now: time.Now}
	if _, err := rand.Read(s.key[:]); err != nil {
		return nil, err
	}
	// Reserved positive Int32 range, disjoint from bounded global news IDs.
	s.nextID = 1000000000 + int(binary.BigEndian.Uint32(s.key[:4])%900000000)
	return s, nil
}
func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func tokenKey(token string) ([32]byte, bool) {
	b, err := hex.DecodeString(token)
	if err != nil || len(b) != 32 {
		return [32]byte{}, false
	}
	return sha256.Sum256(b), true
}
func (s *Service) derived(label string, k [32]byte) []byte {
	h := hmac.New(sha256.New, s.key[:])
	h.Write([]byte(label))
	h.Write(k[:])
	return h.Sum(nil)
}
func (s *Service) code(k [32]byte) string {
	return fmt.Sprintf("%08d", binary.BigEndian.Uint64(s.derived("otp:", k)[:8])%100000000)
}
func (s *Service) csrf(k [32]byte) string { return hex.EncodeToString(s.derived("csrf:", k)) }
func (s *Service) prune(now time.Time) {
	for k, c := range s.challenges {
		if !now.Before(c.expires.Add(10 * time.Minute)) {
			delete(s.challenges, k)
		}
	}
	for k, c := range s.sessions {
		if !now.Before(c.created.Add(SessionTTL)) || !now.Before(c.seen.Add(IdleTTL)) {
			delete(s.sessions, k)
		}
	}
	for k, b := range s.buckets {
		if !now.Before(b.since.Add(10 * time.Minute)) {
			delete(s.buckets, k)
		}
	}
}
func (s *Service) allow(key string, limit int, now time.Time) bool {
	b, ok := s.buckets[key]
	if !ok || !now.Before(b.since.Add(10*time.Minute)) {
		b = bucket{since: now}
	}
	if !ok && len(s.buckets) >= maxBuckets {
		return false
	}
	if b.count >= limit {
		return false
	}
	b.count++
	s.buckets[key] = b
	return true
}

// Start intentionally accepts an empty identity (unknown/banned account). The
// browser response is identical, but no mailbox is addressed and verification fails.
func (s *Service) Start(ip, accountKey string, identity Identity) (Challenge, error) {
	token, err := randomToken()
	if err != nil {
		return Challenge{}, err
	}
	k, _ := tokenKey(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.prune(now)
	if !s.allow("start-ip:"+ip, 10, now) || !s.allow("start-account:"+accountKey, 3, now) || len(s.challenges) >= maxRecords {
		return Challenge{}, ErrLimited
	}
	if s.nextID >= 2147483647 {
		return Challenge{}, ErrLimited
	}
	s.nextID++
	reference := hex.EncodeToString(s.derived("reference:", k)[:3])
	c := &challenge{identity: identity, id: s.nextID, created: now, expires: now.Add(ChallengeTTL), reference: reference}
	s.challenges[k] = c
	return Challenge{Token: token, Reference: reference, ExpiresAt: c.expires}, nil
}

// Letters includes code-free tombstones only for IDs this socket has already
// seen. Fresh subscriptions omit finished letters so the stock initial cleanup
// removes its cached DeveloperMessage records. State is strictly owner-bound.
func (s *Service) Letters(identity Identity, seen map[int]bool) []Letter {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.prune(now)
	out := []Letter{}
	for k, c := range s.challenges {
		if c.identity != identity || identity.PlayerID == "" {
			continue
		}
		active := !c.closed && now.Before(c.expires)
		if !active && !seen[c.id] {
			continue
		}
		l := Letter{ID: c.id, Changed: 1, CreatedAt: c.created, Reference: c.reference, Active: active}
		if active {
			l.Code = s.code(k)
		} else {
			l.Changed = 2
		}
		out = append(out, l)
	}
	return out
}

// Verify checks owner state inside the single-use critical section. valid must
// only query the account store; it must not call back into this service.
func (s *Service) Verify(ip, token, code string, valid func(Identity) bool) (Session, error) {
	k, ok := tokenKey(token)
	if !ok {
		return Session{}, ErrInvalid
	}
	next, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	sk, _ := tokenKey(next)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.prune(now)
	if !s.allow("verify-ip:"+ip, 60, now) {
		return Session{}, ErrLimited
	}
	c := s.challenges[k]
	if s.failed || c == nil || c.closed || !now.Before(c.expires) {
		return Session{}, ErrInvalid
	}
	c.attempts++
	correct := len(code) == 8 && subtle.ConstantTimeCompare([]byte(code), []byte(s.code(k))) == 1
	if !correct || c.identity.PlayerID == "" || !valid(c.identity) {
		if c.attempts >= 5 {
			c.closed = true
		}
		return Session{}, ErrInvalid
	}
	if len(s.sessions) >= maxRecords {
		return Session{}, ErrLimited
	}
	c.closed = true
	s.sessions[sk] = &session{identity: c.identity, created: now, seen: now}
	if e := s.persist(); e != nil {
		delete(s.sessions, sk)
		return Session{}, e
	}
	return Session{Token: next, CSRF: s.csrf(sk), Identity: c.identity, ExpiresAt: now.Add(SessionTTL), AuthenticatedAt: now}, nil
}
func (s *Service) Current(token string, valid func(Identity) bool) (Session, error) {
	k, ok := tokenKey(token)
	if !ok {
		return Session{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.prune(now)
	c := s.sessions[k]
	if s.failed || c == nil {
		return Session{}, ErrInvalid
	}
	if !valid(c.identity) {
		delete(s.sessions, k)
		return Session{}, ErrInvalid
	}
	c.seen = now
	return Session{CSRF: s.csrf(k), Identity: c.identity, ExpiresAt: c.created.Add(SessionTTL), AuthenticatedAt: c.created}, nil
}
func (s *Service) Logout(token, csrf string) bool {
	k, ok := tokenKey(token)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[k] == nil || subtle.ConstantTimeCompare([]byte(csrf), []byte(s.csrf(k))) != 1 {
		return false
	}
	old := s.sessions[k]
	delete(s.sessions, k)
	if s.persist() != nil {
		s.sessions[k] = old
		s.failed = true
		return false
	}
	return true
}
func (s *Service) Cancel(token string) {
	k, ok := tokenKey(token)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.challenges[k]; c != nil {
		c.closed = true
	}
}

// Issue is called only after another fully verified authentication ceremony.
func (s *Service) Issue(identity Identity) (Session, error) { return s.IssueAt(identity, time.Time{}) }

// IssueAt rotates credentials without extending the fresh-authentication window.
func (s *Service) IssueAt(identity Identity, authenticatedAt time.Time) (Session, error) {
	token, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	k, _ := tokenKey(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.prune(now)
	if s.failed || identity.PlayerID == "" || len(s.sessions) >= maxRecords {
		return Session{}, ErrLimited
	}
	if authenticatedAt.IsZero() || authenticatedAt.After(now) {
		authenticatedAt = now
	}
	s.sessions[k] = &session{identity: identity, created: authenticatedAt, seen: now}
	if e := s.persist(); e != nil {
		delete(s.sessions, k)
		return Session{}, e
	}
	return Session{Token: token, CSRF: s.csrf(k), Identity: identity, ExpiresAt: authenticatedAt.Add(SessionTTL), AuthenticatedAt: authenticatedAt}, nil
}
func (s *Service) Revoke(token string) {
	k, ok := tokenKey(token)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, k)
	if s.persist() != nil {
		s.failed = true
	}
}
func (s *Service) RevokeIdentity(identity Identity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.sessions {
		if v.identity.PlayerID == identity.PlayerID && v.identity.Fingerprint == identity.Fingerprint {
			delete(s.sessions, k)
		}
	}
	if s.persist() != nil {
		s.failed = true
	}
}
func (s *Service) Limit(key string, n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.prune(now)
	return s.allow(key, n, now)
}
