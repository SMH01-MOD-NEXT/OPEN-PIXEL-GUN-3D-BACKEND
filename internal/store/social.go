// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	SocialFriendLimit        = 100
	SocialPendingLimit       = 100
	SocialMessageLimit       = 512 // Unicode code points, additionally capped at 2048 bytes.
	socialHistoryLimit       = 100
	socialGlobalMessageLimit = 20000
)

var (
	ErrSocialInvalid   = errors.New("social: invalid request")
	ErrSocialForbidden = errors.New("social: not permitted")
	ErrSocialFull      = errors.New("social: limit reached")
	ErrSocialRate      = errors.New("social: rate limited")
)

type FriendRequest struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Created int64  `json:"created"`
}
type Friendship struct {
	A string `json:"a"`
	B string `json:"b"`
}
type PrivateMessage struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Text        string `json:"text"`
	Stamp       int64  `json:"stamp_us"`
	ClientStamp string `json:"client_stamp,omitempty"`
}
type socialState struct {
	Requests  []FriendRequest  `json:"requests,omitempty"`
	Friends   []Friendship     `json:"friends,omitempty"`
	Messages  []PrivateMessage `json:"messages,omitempty"`
	LastStamp int64            `json:"last_stamp_us,omitempty"`
}
type SocialSnapshot struct {
	Friends  []string
	Incoming []string
	Outgoing []string
}
type socialRate struct {
	Minute                   int64
	Actions, Messages, Reads int
}

func (v socialState) clone() socialState {
	v.Requests = append([]FriendRequest(nil), v.Requests...)
	v.Friends = append([]Friendship(nil), v.Friends...)
	v.Messages = append([]PrivateMessage(nil), v.Messages...)
	return v
}
func samePair(a, b, c, d string) bool { return a == c && b == d || a == d && b == c }
func (s *Store) socialPairLocked(actor, target string) (string, string, error) {
	actor = s.resolvePlayerIDLocked(actor)
	target = s.resolvePlayerIDLocked(target)
	if s.state.Accounts[actor] == nil || s.state.Accounts[target] == nil {
		return "", "", ErrNotFound
	}
	if actor == target {
		return "", "", ErrSocialInvalid
	}
	return actor, target, nil
}
func (s *Store) socialRateLocked(actor string, kind string, now time.Time) error {
	if s.socialRates == nil {
		s.socialRates = map[string]socialRate{}
	}
	// Deleted accounts cannot leave an unbounded limiter behind.
	for id := range s.socialRates {
		if s.state.Accounts[id] == nil {
			delete(s.socialRates, id)
		}
	}
	r := s.socialRates[actor]
	if r.Minute != now.Unix()/60 {
		r = socialRate{Minute: now.Unix() / 60}
	}
	switch kind {
	case "message":
		if r.Messages >= 30 {
			return ErrSocialRate
		}
		r.Messages++
	case "read":
		if r.Reads >= 120 {
			return ErrSocialRate
		}
		r.Reads++
	default:
		if r.Actions >= 30 {
			return ErrSocialRate
		}
		r.Actions++
	}
	s.socialRates[actor] = r
	return nil
}

// Normalize identities and expire bounded chat/invites without trusting any
// client profile map. Persist only together with a successful social mutation.
func (s *Store) normalizeSocialLocked(now time.Time) {
	v := &s.state.Social
	canon := func(id string) string {
		id = s.resolvePlayerIDLocked(id)
		if s.state.Accounts[id] == nil {
			return ""
		}
		return id
	}
	friends := []Friendship{}
	seen := map[string]bool{}
	for _, f := range v.Friends {
		f.A = canon(f.A)
		f.B = canon(f.B)
		if f.A == "" || f.B == "" || f.A == f.B {
			continue
		}
		if f.A > f.B {
			f.A, f.B = f.B, f.A
		}
		key := f.A + ":" + f.B
		if !seen[key] {
			friends = append(friends, f)
			seen[key] = true
		}
	}
	v.Friends = friends
	requests := []FriendRequest{}
	seen = map[string]bool{}
	for _, r := range v.Requests {
		r.From = canon(r.From)
		r.To = canon(r.To)
		key := r.From + ":" + r.To
		if r.From == "" || r.To == "" || r.From == r.To || r.Created < now.Add(-14*24*time.Hour).Unix() || seen[key] || s.friendsLocked(r.From, r.To) {
			continue
		}
		seen[key] = true
		requests = append(requests, r)
	}
	v.Requests = requests
	messages := []PrivateMessage{}
	counts := map[string]int{}
	for i := len(v.Messages) - 1; i >= 0; i-- {
		m := v.Messages[i]
		m.From = canon(m.From)
		m.To = canon(m.To)
		a, b := m.From, m.To
		if a > b {
			a, b = b, a
		}
		key := a + ":" + b
		if a == "" || b == "" || a == b || m.Stamp < now.Add(-30*24*time.Hour).UnixMicro() || counts[key] >= socialHistoryLimit || len(messages) >= socialGlobalMessageLimit {
			continue
		}
		counts[key]++
		messages = append(messages, m)
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	v.Messages = messages
}
func (s *Store) friendsLocked(a, b string) bool {
	for _, f := range s.state.Social.Friends {
		if samePair(s.resolvePlayerIDLocked(f.A), s.resolvePlayerIDLocked(f.B), a, b) {
			return true
		}
	}
	return false
}
func (s *Store) snapshotSocialLocked(actor string, now time.Time) SocialSnapshot {
	out := SocialSnapshot{Friends: []string{}, Incoming: []string{}, Outgoing: []string{}}
	seen := map[string]bool{}
	for _, f := range s.state.Social.Friends {
		a, b := s.resolvePlayerIDLocked(f.A), s.resolvePlayerIDLocked(f.B)
		peer := ""
		if a == actor {
			peer = b
		} else if b == actor {
			peer = a
		}
		if peer != "" && peer != actor && s.state.Accounts[peer] != nil && !seen[peer] {
			out.Friends = append(out.Friends, peer)
			seen[peer] = true
		}
	}
	incoming, outgoing := map[string]bool{}, map[string]bool{}
	for _, r := range s.state.Social.Requests {
		a, b := s.resolvePlayerIDLocked(r.From), s.resolvePlayerIDLocked(r.To)
		if r.Created < now.Add(-14*24*time.Hour).Unix() || a == b {
			continue
		}
		if b == actor && s.state.Accounts[a] != nil && !incoming[a] && !seen[a] {
			out.Incoming = append(out.Incoming, a)
			incoming[a] = true
		}
		if a == actor && s.state.Accounts[b] != nil && !outgoing[b] && !seen[b] {
			out.Outgoing = append(out.Outgoing, b)
			outgoing[b] = true
		}
	}
	sort.Strings(out.Friends)
	sort.Strings(out.Incoming)
	sort.Strings(out.Outgoing)
	return out
}
func (s *Store) SocialSnapshot(actor string) (SocialSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	actor = s.resolvePlayerIDLocked(actor)
	if s.state.Accounts[actor] == nil {
		return SocialSnapshot{}, ErrNotFound
	}
	return s.snapshotSocialLocked(actor, time.Now().UTC()), nil
}

// SocialAction commits the entire two-sided transition or restores memory on
// disk failure. Crossed invitations remain pending until an explicit acceptance.
func (s *Store) SocialAction(actor, target, action string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	actor = s.resolvePlayerIDLocked(actor)
	if s.state.Accounts[actor] == nil {
		return "", ErrNotFound
	}
	var err error
	if action != "clear" {
		actor, target, err = s.socialPairLocked(actor, target)
		if err != nil {
			return "", err
		}
	}
	now := time.Now().UTC()
	if err = s.socialRateLocked(actor, "action", now); err != nil {
		return "", err
	}
	old := s.state.Social.clone()
	s.normalizeSocialLocked(now)
	committed := false
	defer func() {
		if !committed {
			s.state.Social = old
		}
	}()
	isFriend := s.friendsLocked(actor, target)
	out := s.snapshotSocialLocked(actor, now)
	peer := s.snapshotSocialLocked(target, now)
	hasRequest := false
	for _, r := range s.state.Social.Requests {
		if r.From == target && r.To == actor {
			hasRequest = true
		}
	}
	switch action {
	case "request":
		if isFriend {
			return target, nil
		}
		for _, r := range s.state.Social.Requests {
			if r.From == actor && r.To == target {
				return target, nil
			}
		}
		if len(out.Outgoing) >= SocialPendingLimit || len(peer.Incoming) >= SocialPendingLimit || len(out.Friends) >= SocialFriendLimit || len(peer.Friends) >= SocialFriendLimit {
			return "", ErrSocialFull
		}
		s.state.Social.Requests = append(s.state.Social.Requests, FriendRequest{actor, target, now.Unix()})
	case "accept":
		if !isFriend && !hasRequest {
			return "", ErrSocialForbidden
		}
		if !isFriend {
			if len(out.Friends) >= SocialFriendLimit || len(peer.Friends) >= SocialFriendLimit {
				return "", ErrSocialFull
			}
			s.state.Social.Friends = append(s.state.Social.Friends, Friendship{actor, target})
		}
		s.filterSocialRequestsLocked(func(r FriendRequest) bool { return !samePair(r.From, r.To, actor, target) })
	case "reject":
		s.filterSocialRequestsLocked(func(r FriendRequest) bool { return r.From != target || r.To != actor })
	case "clear":
		s.filterSocialRequestsLocked(func(r FriendRequest) bool { return r.To != actor })
	case "remove":
		friends := []Friendship{}
		for _, f := range s.state.Social.Friends {
			if !samePair(f.A, f.B, actor, target) {
				friends = append(friends, f)
			}
		}
		s.state.Social.Friends = friends
		s.filterSocialRequestsLocked(func(r FriendRequest) bool { return !samePair(r.From, r.To, actor, target) })
		messages := []PrivateMessage{}
		for _, m := range s.state.Social.Messages {
			if !samePair(m.From, m.To, actor, target) {
				messages = append(messages, m)
			}
		}
		s.state.Social.Messages = messages
	default:
		return "", ErrSocialInvalid
	}
	if err = s.persistLocked(); err != nil {
		return "", err
	}
	committed = true
	return target, nil
}
func (s *Store) filterSocialRequestsLocked(keep func(FriendRequest) bool) {
	out := []FriendRequest{}
	for _, r := range s.state.Social.Requests {
		if keep(r) {
			out = append(out, r)
		}
	}
	s.state.Social.Requests = out
}
func validPrivateText(text string) bool {
	if !utf8.ValidString(text) || len(text) > 2048 || utf8.RuneCountInString(text) > SocialMessageLimit || strings.TrimSpace(text) == "" {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}
func (s *Store) SendPrivateMessage(actor, target, text, clientStamp string) (PrivateMessage, error) {
	if !validPrivateText(text) || len(clientStamp) > 48 {
		return PrivateMessage{}, ErrSocialInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	actor, target, err := s.socialPairLocked(actor, target)
	if err != nil {
		return PrivateMessage{}, err
	}
	if !s.friendsLocked(actor, target) {
		return PrivateMessage{}, ErrSocialForbidden
	}
	now := time.Now().UTC()
	// The stock client retries an optimistic message with the same timestamp.
	// Deduplicate only the exact same actor/peer/text/token, never req_id alone.
	for _, m := range s.state.Social.Messages {
		if clientStamp != "" && m.ClientStamp == clientStamp && s.resolvePlayerIDLocked(m.From) == actor && s.resolvePlayerIDLocked(m.To) == target && m.Text == text && m.Stamp >= now.Add(-5*time.Minute).UnixMicro() {
			return m, nil
		}
	}
	if err = s.socialRateLocked(actor, "message", now); err != nil {
		return PrivateMessage{}, err
	}
	old := s.state.Social.clone()
	stamp := now.UnixMicro()
	if stamp <= s.state.Social.LastStamp {
		stamp = s.state.Social.LastStamp + 1
	}
	m := PrivateMessage{actor, target, text, stamp, clientStamp}
	s.state.Social.LastStamp = stamp
	s.state.Social.Messages = append(s.state.Social.Messages, m)
	s.normalizeSocialLocked(now)
	if err = s.persistLocked(); err != nil {
		s.state.Social = old
		return PrivateMessage{}, err
	}
	return m, nil
}

// Native 23.1.3 restores incoming history; its optimistic outbox is local.
// Unfriend immediately revokes read access, including existing subscriptions.
func (s *Store) PrivateInbox(actor string, peers []string, after float64) ([]PrivateMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	actor = s.resolvePlayerIDLocked(actor)
	if s.state.Accounts[actor] == nil {
		return nil, ErrNotFound
	}
	if len(peers) > SocialFriendLimit {
		return nil, ErrSocialInvalid
	}
	allowed := map[string]bool{}
	for _, peer := range peers {
		peer = s.resolvePlayerIDLocked(peer)
		if s.friendsLocked(actor, peer) {
			allowed[peer] = true
		}
	}
	out := []PrivateMessage{}
	cutoff := time.Now().Add(-30 * 24 * time.Hour).UnixMicro()
	for _, m := range s.state.Social.Messages {
		m.From = s.resolvePlayerIDLocked(m.From)
		m.To = s.resolvePlayerIDLocked(m.To)
		if m.To == actor && allowed[m.From] && m.Stamp >= cutoff && float64(m.Stamp)/1e6 > after {
			out = append(out, m)
		}
	}
	return out, nil
}
func (s *Store) SearchPlayers(actor, param string) ([]string, error) {
	param = strings.TrimSpace(param)
	if param == "" || len(param) > 128 || !utf8.ValidString(param) {
		return nil, ErrSocialInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	actor = s.resolvePlayerIDLocked(actor)
	if s.state.Accounts[actor] == nil {
		return nil, ErrNotFound
	}
	if err := s.socialRateLocked(actor, "read", time.Now()); err != nil {
		return nil, err
	}
	ids := []string{}
	q := strings.ToLower(param)
	exact := s.resolvePlayerIDLocked(param)
	for id, a := range s.state.Accounts {
		if id == actor {
			continue
		}
		nick := a.Nick
		if p := s.state.Profiles[id]; p != nil && p.Nick != "" {
			nick = p.Nick
		}
		if id == exact || strings.Contains(strings.ToLower(nick), q) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i] == exact && ids[j] != exact {
			return true
		}
		if ids[j] == exact && ids[i] != exact {
			return false
		}
		return ids[i] < ids[j]
	})
	if len(ids) > 20 {
		ids = ids[:20]
	}
	return ids, nil
}

// Identity renames move social ownership, not another account's credentials.
func (s *Store) renameSocialLocked(oldID, newID string) {
	v := &s.state.Social
	for i := range v.Friends {
		if v.Friends[i].A == oldID {
			v.Friends[i].A = newID
		}
		if v.Friends[i].B == oldID {
			v.Friends[i].B = newID
		}
	}
	for i := range v.Requests {
		if v.Requests[i].From == oldID {
			v.Requests[i].From = newID
		}
		if v.Requests[i].To == oldID {
			v.Requests[i].To = newID
		}
	}
	for i := range v.Messages {
		if v.Messages[i].From == oldID {
			v.Messages[i].From = newID
		}
		if v.Messages[i].To == oldID {
			v.Messages[i].To = newID
		}
	}
}
