// SPDX-License-Identifier: GPL-3.0-or-later
// Package support owns durable, encrypted tickets and separate limited guest grants.
package support

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/securefile"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrInvalid = errors.New("invalid support request")
var ErrDenied = errors.New("support access denied")
var ErrConflict = errors.New("ticket changed")
var ErrLimit = errors.New("support limit")

const TTL = 90 * 24 * time.Hour
const maxTickets = 10000

// Bound support memory and rewrite costs independently of gameplay state.
const maxStateBytes = 8 << 20

// Actor's PlayerID is the authenticated sender, never the claimed recovery target.
type Actor struct {
	PlayerID    string `json:"player_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Browser     string `json:"-"`
	Operator    bool   `json:"-"`
}
type Message struct {
	ID     int       `json:"id"`
	Author string    `json:"author"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}
type Event struct {
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	At     time.Time `json:"at"`
}
type Ticket struct {
	ID               string            `json:"id"`
	Kind             string            `json:"kind"`
	TargetID         string            `json:"target_id"`
	Subject          string            `json:"subject"`
	Answers          map[string]string `json:"answers"`
	Messages         []Message         `json:"messages"`
	Status           string            `json:"status"`
	Revision         int               `json:"revision"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	OwnerID          string            `json:"owner_id,omitempty"`
	OwnerFingerprint string            `json:"-"`
	CreatorBrowser   string            `json:"-"`
	SecretHash       string            `json:"-"`
	RequestHash      string            `json:"-"`
	Operation        string            `json:"-"`
	IP               string            `json:"-"`
	Audit            []Event           `json:"-"`
	Operations       map[string]string `json:"-"`
}

// Separate wire shape excludes all internal proofs and IPs from player responses.
type diskTicket struct {
	Ticket
	OwnerFP, Creator, Secret, Request, Op, IPValue string
	Events                                         []Event
	Ops                                            map[string]string
}
type Grant struct {
	Expires time.Time `json:"expires"`
	Tickets []string  `json:"tickets"`
}
type state struct {
	Tickets   map[string]diskTicket `json:"tickets"`
	Grants    map[string]Grant      `json:"grants"`
	Admins    map[string]string     `json:"admins"`
	RoleAudit []Event               `json:"role_audit"`
}
type Store struct {
	mu    sync.Mutex
	path  string
	key   []byte
	state state
	now   func() time.Time
}

func Open(path string, key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, ErrInvalid
	}
	s := &Store{path: path, key: append([]byte{}, key...), now: time.Now, state: state{Tickets: map[string]diskTicket{}, Grants: map[string]Grant{}, Admins: map[string]string{}}}
	if e := securefile.Read(path, key, "OPG3D-SUPPORT-v1", &s.state); e != nil {
		return nil, e
	}
	if s.state.Tickets == nil || s.state.Grants == nil || s.state.Admins == nil || len(s.state.Tickets) > maxTickets {
		return nil, ErrInvalid
	}
	return s, nil
}
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (s *Store) proof(label string) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(label))
	return hex.EncodeToString(h.Sum(nil))
}
func token() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
func (s *Store) tx(fn func(*state) error) error {
	b, _ := json.Marshal(s.state)
	var next state
	if json.Unmarshal(b, &next) != nil {
		return ErrInvalid
	}
	if e := fn(&next); e != nil {
		return e
	}
	encoded, e := json.Marshal(next)
	if e != nil {
		return e
	}
	if len(encoded) > maxStateBytes {
		return ErrLimit
	}
	if e := securefile.Write(s.path, s.key, "OPG3D-SUPPORT-v1", next); e != nil {
		return e
	}
	s.state = next
	return nil
}
func fromDisk(d diskTicket) Ticket {
	t := d.Ticket
	t.Answers = maps.Clone(t.Answers)
	t.Messages = slices.Clone(t.Messages)
	t.OwnerFingerprint = d.OwnerFP
	t.CreatorBrowser = d.Creator
	t.SecretHash = d.Secret
	t.RequestHash = d.Request
	t.Operation = d.Op
	t.IP = d.IPValue
	t.Audit = append([]Event{}, d.Events...)
	t.Operations = maps.Clone(d.Ops)
	return t
}
func toDisk(t Ticket) diskTicket {
	t.Answers = maps.Clone(t.Answers)
	t.Messages = slices.Clone(t.Messages)
	return diskTicket{t, t.OwnerFingerprint, t.CreatorBrowser, t.SecretHash, t.RequestHash, t.Operation, t.IP, slices.Clone(t.Audit), maps.Clone(t.Operations)}
}
func (s *Store) Browser(raw string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := digest(raw)
	if raw != "" && s.now().Before(s.state.Grants[key].Expires) {
		return raw, s.proof("csrf:" + raw), nil
	}
	raw = token() + token()
	if len(raw) != 64 {
		return "", "", ErrInvalid
	}
	key = digest(raw)
	e := s.tx(func(st *state) error {
		for k, g := range st.Grants {
			if !s.now().Before(g.Expires) {
				delete(st.Grants, k)
			}
		}
		if len(st.Grants) >= 8192 {
			return ErrLimit
		}
		st.Grants[key] = Grant{Expires: s.now().Add(TTL), Tickets: []string{}}
		return nil
	})
	return raw, s.proof("csrf:" + raw), e
}
func (s *Store) CSRF(raw, csrf string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return raw != "" && s.now().Before(s.state.Grants[digest(raw)].Expires) && subtle.ConstantTimeCompare([]byte(s.proof("csrf:"+raw)), []byte(csrf)) == 1
}
func (s *Store) admin(st state, a Actor) bool {
	return a.Operator || a.PlayerID != "" && st.Admins[a.PlayerID] == a.Fingerprint && a.Fingerprint != ""
}
func (s *Store) Admin(a Actor) bool { s.mu.Lock(); defer s.mu.Unlock(); return s.admin(s.state, a) }
func (s *Store) SetAdmin(id, fp string, on bool, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" || fp == "" {
		return ErrInvalid
	}
	return s.tx(func(st *state) error {
		if on {
			st.Admins[id] = fp
		} else {
			delete(st.Admins, id)
		}
		st.RoleAudit = append(st.RoleAudit, Event{actor, "role:" + id + ":" + map[bool]string{true: "grant", false: "revoke"}[on], s.now().UTC()})
		if len(st.RoleAudit) > 1000 {
			st.RoleAudit = st.RoleAudit[len(st.RoleAudit)-1000:]
		}
		return nil
	})
}
func (s *Store) owns(st state, t Ticket, a Actor) bool {
	if t.OwnerID != "" && a.PlayerID == t.OwnerID && a.Fingerprint == t.OwnerFingerprint {
		return true
	}
	g := st.Grants[digest(a.Browser)]
	if a.Browser != "" && s.now().Before(g.Expires) {
		for _, id := range g.Tickets {
			if id == t.ID {
				return true
			}
		}
	}
	return false
}
func (s *Store) List(a Actor, admin bool, offset int, status string) ([]Ticket, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if admin && !s.admin(s.state, a) {
		return nil, 0, ErrDenied
	}
	out := []Ticket{}
	for _, d := range s.state.Tickets {
		t := fromDisk(d)
		if (!admin && !s.owns(s.state, t, a)) || (status != "" && t.Status != status) {
			continue
		}
		t.Messages = nil
		t.Audit = nil
		t.Answers = nil
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	total := len(out)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + 30
	if end > total {
		end = total
	}
	return out[offset:end], total, nil
}
func (s *Store) Get(id string, a Actor, admin bool) (Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.state.Tickets[id]
	t := fromDisk(d)
	if !ok || (admin && !s.admin(s.state, a)) || (!admin && !s.owns(s.state, t, a)) {
		return Ticket{}, ErrDenied
	}
	b, _ := json.Marshal(toDisk(t))
	var copy diskTicket
	json.Unmarshal(b, &copy)
	return fromDisk(copy), nil
}

type Create struct {
	Kind      string            `json:"kind"`
	TargetID  string            `json:"target_id"`
	Subject   string            `json:"subject"`
	Details   string            `json:"details"`
	Answers   map[string]string `json:"answers"`
	Operation string            `json:"operation"`
}

func operation(op string) bool {
	b, e := hex.DecodeString(op)
	return e == nil && len(b) == 16 && strings.ToLower(op) == op
}
func Validate(c Create) bool {
	if !operation(c.Operation) || len(strings.TrimSpace(c.Subject)) < 3 || len(c.Subject) > 160 || (c.Kind != "recovery" && len(strings.TrimSpace(c.Details)) < 3) || len(c.Details) > 6000 || len(c.TargetID) > 32 {
		return false
	}
	if c.Kind != "recovery" && c.Kind != "bug" && c.Kind != "question" {
		return false
	}
	for _, r := range c.TargetID {
		if r < '0' || r > '9' {
			return false
		}
	}
	allowed := map[string]int{}
	if c.Kind == "recovery" {
		if c.TargetID == "" {
			return false
		}
		allowed = map[string]int{"old_device": 3, "changed_device": 3, "last_device": 500, "last_login": 300, "last_purchases": 1500, "last_spending": 1500}
	} else if c.Kind == "bug" {
		allowed = map[string]int{"steps": 2000, "expected": 1000, "actual": 1000, "version_device": 500}
	}
	for k, v := range c.Answers {
		limit, ok := allowed[k]
		if !ok || len(v) > limit && limit != 3 {
			return false
		}
		if limit == 3 && v != "yes" && v != "no" && v != "unknown" {
			return false
		}
	}
	if c.Kind == "recovery" && (c.Answers["old_device"] == "" || c.Answers["changed_device"] == "") {
		return false
	}
	return true
}
func (s *Store) Create(a Actor, c Create, ip string) (Ticket, string, error) {
	if !Validate(c) || a.PlayerID == "" && c.Kind != "recovery" {
		return Ticket{}, "", ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	browser := digest(a.Browser)
	if a.Browser == "" || !s.now().Before(s.state.Grants[browser].Expires) {
		return Ticket{}, "", ErrDenied
	}
	raw, _ := json.Marshal(c)
	request := digest(string(raw))
	for _, d := range s.state.Tickets {
		t := fromDisk(d)
		if t.CreatorBrowser == browser && t.Operation == c.Operation {
			if t.OwnerID != a.PlayerID || t.OwnerFingerprint != a.Fingerprint {
				return Ticket{}, "", ErrDenied
			}
			if t.RequestHash != request {
				return Ticket{}, "", ErrConflict
			}
			return t, s.proof("ticket:" + t.ID), nil
		}
	}
	if len(s.state.Tickets) >= maxTickets {
		return Ticket{}, "", ErrLimit
	}
	count := 0
	for _, d := range s.state.Tickets {
		t := fromDisk(d)
		if t.CreatorBrowser == browser || a.PlayerID != "" && t.OwnerID == a.PlayerID && t.OwnerFingerprint == a.Fingerprint {
			count++
		}
	}
	if count >= 100 {
		return Ticket{}, "", ErrLimit
	}
	id := token()
	if id == "" {
		return Ticket{}, "", ErrInvalid
	}
	secret := s.proof("ticket:" + id)
	now := s.now().UTC()
	t := Ticket{ID: id, Kind: c.Kind, TargetID: c.TargetID, Subject: strings.TrimSpace(c.Subject), Answers: c.Answers, Messages: []Message{{1, "player", strings.TrimSpace(c.Details), now}}, Status: "open", Revision: 1, CreatedAt: now, UpdatedAt: now, OwnerID: a.PlayerID, OwnerFingerprint: a.Fingerprint, CreatorBrowser: browser, SecretHash: digest(secret), RequestHash: request, Operation: c.Operation, IP: ip, Audit: []Event{{actorName(a), "created", now}}, Operations: map[string]string{}}
	e := s.tx(func(st *state) error {
		if _, ok := st.Tickets[id]; ok {
			return ErrConflict
		}
		st.Tickets[id] = toDisk(t)
		g := st.Grants[browser]
		if len(g.Tickets) >= 100 {
			return ErrLimit
		}
		if a.PlayerID == "" {
			g.Tickets = append(g.Tickets, id)
		}
		st.Grants[browser] = g
		return nil
	})
	return t, secret, e
}
func (s *Store) Unlock(a Actor, id, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.state.Tickets[id]
	if !ok || d.OwnerID != "" || len(code) != 64 || subtle.ConstantTimeCompare([]byte(d.Secret), []byte(digest(strings.TrimSpace(code)))) != 1 {
		return ErrDenied
	}
	key := digest(a.Browser)
	g := s.state.Grants[key]
	if !s.now().Before(g.Expires) {
		return ErrDenied
	}
	for _, v := range g.Tickets {
		if v == id {
			return nil
		}
	}
	if len(g.Tickets) >= 100 {
		return ErrLimit
	}
	return s.tx(func(st *state) error {
		g := st.Grants[key]
		g.Tickets = append(g.Tickets, id)
		st.Grants[key] = g
		return nil
	})
}
func actorName(a Actor) string {
	if a.Operator {
		return "operator"
	}
	if a.PlayerID != "" {
		return a.PlayerID
	}
	return "guest"
}

type Update struct {
	Text      string `json:"text"`
	Status    string `json:"status"`
	Revision  int    `json:"revision"`
	Operation string `json:"operation"`
}

func (s *Store) Update(id string, a Actor, admin bool, u Update) (Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !operation(u.Operation) || len(u.Text) > 6000 || len(u.Status) > 30 {
		return Ticket{}, ErrInvalid
	}
	d, ok := s.state.Tickets[id]
	t := fromDisk(d)
	if !ok || admin && !s.admin(s.state, a) || !admin && !s.owns(s.state, t, a) {
		return Ticket{}, ErrDenied
	}
	b, _ := json.Marshal(u)
	hash := digest(string(b))
	if old, ok := t.Operations[u.Operation]; ok {
		if old != hash {
			return Ticket{}, ErrConflict
		}
		return t, nil
	}
	if u.Revision != t.Revision {
		return Ticket{}, ErrConflict
	}
	if len(t.Audit) >= 1000 || len(t.Messages) >= 200 {
		return Ticket{}, ErrLimit
	}
	if !admin && (u.Status != "" || t.Status == "closed" || t.Status == "resolved") {
		return Ticket{}, ErrDenied
	}
	if u.Status != "" && u.Status != "open" && u.Status != "needs_info" && u.Status != "resolved" && u.Status != "closed" {
		return Ticket{}, ErrInvalid
	}
	if strings.TrimSpace(u.Text) == "" && u.Status == "" {
		return Ticket{}, ErrInvalid
	}
	now := s.now().UTC()
	if strings.TrimSpace(u.Text) != "" {
		author := "player"
		if admin {
			author = "support"
		}
		t.Messages = append(t.Messages, Message{len(t.Messages) + 1, author, strings.TrimSpace(u.Text), now})
		if !admin && t.Status == "needs_info" {
			t.Status = "open"
		}
	}
	if u.Status != "" {
		t.Status = u.Status
	}
	t.Revision++
	t.UpdatedAt = now
	t.Audit = append(t.Audit, Event{actorName(a), "update:" + t.Status, now})
	if t.Operations == nil {
		t.Operations = map[string]string{}
	}
	t.Operations[u.Operation] = hash
	e := s.tx(func(st *state) error { st.Tickets[id] = toDisk(t); return nil })
	return t, e
}

func (s *Store) Forget(raw string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tx(func(st *state) error { delete(st.Grants, digest(raw)); return nil })
}
