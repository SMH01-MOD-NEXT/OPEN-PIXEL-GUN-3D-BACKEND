// SPDX-License-Identifier: GPL-3.0-or-later
// Package clienterrors keeps a bounded, independent diagnostic journal, never progress.
package clienterrors

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const MaxEvents = 2000
const Retention = 7 * 24 * time.Hour

var ErrRate = errors.New("diagnostic rate limit")
var ErrUnavailable = errors.New("diagnostic journal unavailable")

type Event struct {
	ID          string    `json:"id"`
	At          time.Time `json:"at"`
	Player      string    `json:"player_id"`
	Source      string    `json:"source"`
	Level       string    `json:"level"`
	Code        string    `json:"code"`
	Message     string    `json:"message"`
	Stack       string    `json:"stack,omitempty"`
	Version     string    `json:"version,omitempty"`
	Module      string    `json:"module,omitempty"`
	Revision    uint64    `json:"revision,omitempty"`
	Fingerprint string    `json:"fingerprint"`
}
type Summary struct {
	Events      int    `json:"events"`
	Errors      int    `json:"errors"`
	Groups      int    `json:"groups"`
	Players     int    `json:"players"`
	Resyncs     int    `json:"resyncs"`
	Completed   int    `json:"completed"`
	Retained    int    `json:"retained"`
	Limited     uint64 `json:"rate_limited"`
	Unavailable bool   `json:"unavailable"`
}
type Group struct {
	Event
	Count   int       `json:"count"`
	Players int       `json:"players"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
}
type Query struct {
	Text, Player, Level, Source string
	Offset, Limit               int
}
type Result struct {
	Groups  []Group `json:"groups"`
	Total   int     `json:"total"`
	Summary Summary `json:"summary"`
}
type window struct {
	At    time.Time
	Count int
}
type Store struct {
	mu          sync.Mutex
	path        string
	events      []Event
	broken      bool
	writeFailed bool
	limits      map[string]window
	global      window
	limited     uint64
}

// Redact before persistence, fingerprints, standard logs or admin responses.
// Drop whole sensitive lines rather than trying to recognize every token format.
var sensitive = regexp.MustCompile(`(?i)authorization|bearer|password|passwd|secret|token|session|appid|receipt|cookie|device[_ -]?id`)
var url = regexp.MustCompile(`(?i)(https?|wss?)://[^\s<>"']+`)
var email = regexp.MustCompile(`[\w.+-]+@[\w.-]+\.[a-zA-Z]{2,}`)
var opaque = regexp.MustCompile(`[a-zA-Z0-9_+/.=-]{48,}`)
var path = regexp.MustCompile(`(?:/[\w.:-]+){2,}`)

func Clean(text string, max int) string {
	if len(text) > 16384 {
		text = text[:16384]
	}
	lines := strings.Split(strings.ToValidUTF8(text, ""), "\n")
	for i, line := range lines {
		if sensitive.MatchString(line) {
			lines[i] = "[sensitive line redacted]"
			continue
		}
		line = url.ReplaceAllString(line, "[url]")
		line = email.ReplaceAllString(line, "[email]")
		line = path.ReplaceAllString(line, "[path]")
		line = opaque.ReplaceAllString(line, "[opaque]")
		lines[i] = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) && r != '\t' {
				return -1
			}
			return r
		}, line)
	}
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	r := []rune(out)
	if len(r) > max {
		out = string(r[:max])
	}
	return out
}
func Open(dir string) (*Store, error) {
	s := &Store{limits: map[string]window{}}
	if dir == "" {
		return s, nil
	}
	s.path = filepath.Join(dir, "client-errors.json")
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &s.events)
	}
	if err != nil {
		s.broken = true
		return s, err
	}
	s.trim(time.Now().UTC())
	return s, nil
}
func (s *Store) trim(now time.Time) {
	kept := s.events[:0]
	for _, e := range s.events {
		if !e.At.Before(now.Add(-Retention)) {
			kept = append(kept, e)
		}
	}
	if len(kept) > MaxEvents {
		kept = kept[len(kept)-MaxEvents:]
	}
	s.events = kept
}
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(s.events)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".client-errors-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
func consume(w *window, now time.Time, n, max int) bool {
	if now.Sub(w.At) >= time.Minute {
		*w = window{At: now}
	}
	if w.Count+n > max {
		return false
	}
	w.Count += n
	return true
}

// Add accepts a batch atomically; authenticated canonical identity is set by the caller.
// Retry IDs are hashed with the player so clients cannot deduplicate another account.
func (s *Store) Add(player, source string, input []Event, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken {
		return 0, ErrUnavailable
	}
	if source == "client" || source == "backend" {
		rateKey := source + ":" + player
		w := s.limits[rateKey]
		if !consume(&w, now, len(input), 60) || !consume(&s.global, now, len(input), 600) {
			s.limited++
			return 0, ErrRate
		}
		_, existed := s.limits[rateKey]
		if !existed && len(s.limits) >= 4096 {
			for k, v := range s.limits {
				if now.Sub(v.At) >= time.Minute {
					delete(s.limits, k)
				}
			}
			if len(s.limits) >= 4096 {
				s.limited++
				return 0, ErrRate
			}
		}
		s.limits[rateKey] = w
	}
	old := append([]Event(nil), s.events...)
	s.trim(now)
	added := 0
	for _, e := range input {
		e.Player = player
		e.Source = source
		e.At = now.UTC()
		e.Message = Clean(e.Message, 1024)
		e.Stack = Clean(e.Stack, 2048)
		e.Version = Clean(e.Version, 40)
		e.Module = Clean(e.Module, 80)
		h := sha256.Sum256([]byte(source + "\x00" + e.Code + "\x00" + e.Message + "\x00" + e.Stack + "\x00" + e.Version + "\x00" + e.Module))
		e.Fingerprint = hex.EncodeToString(h[:12])
		if source == "backend" && e.ID == "" {
			repeated := false
			for i := len(s.events) - 1; i >= 0; i-- {
				p := s.events[i]
				if now.Sub(p.At) > 30*time.Second {
					break
				}
				if p.Player == player && p.Fingerprint == e.Fingerprint && p.Revision == e.Revision {
					repeated = true
					break
				}
			}
			if repeated {
				continue
			}
		}
		if e.ID != "" {
			h = sha256.Sum256([]byte(source + "\x00" + player + "\x00" + e.ID))
			e.ID = hex.EncodeToString(h[:16])
		}
		duplicate := false
		for _, p := range s.events {
			if e.ID != "" && e.ID == p.ID {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		s.events = append(s.events, e)
		added++
	}
	s.trim(now)
	if added == 0 {
		return 0, nil
	}
	if err := s.save(); err != nil {
		s.writeFailed = true
		s.events = old
		return 0, err
	}
	s.writeFailed = false
	return added, nil
}
func (s *Store) Query(q Query, now time.Time) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := Result{Groups: []Group{}, Summary: Summary{Limited: s.limited, Unavailable: s.broken || s.writeFailed}}
	players := map[string]bool{}
	allGroups := map[string]bool{}
	groups := map[string]*Group{}
	groupPlayers := map[string]map[string]bool{}
	text := strings.ToLower(q.Text)
	for _, e := range s.events {
		if e.At.Before(now.Add(-Retention)) {
			continue
		}
		result.Summary.Retained++
		if e.At.Before(now.Add(-24 * time.Hour)) {
			continue
		}
		sum := &result.Summary
		sum.Events++
		players[e.Player] = true
		allGroups[e.Fingerprint] = true
		if e.Level == "error" {
			sum.Errors++
		}
		if e.Source == "backend" && e.Code == "resync_required" {
			sum.Resyncs++
		}
		if e.Source == "backend" && e.Code == "resync_completed" {
			sum.Completed++
		}
		if q.Player != "" && q.Player != e.Player || q.Level != "" && q.Level != e.Level || q.Source != "" && q.Source != e.Source {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(e.Code+" "+e.Message+" "+e.Stack+" "+e.Version+" "+e.Module+" "+e.Player), text) {
			continue
		}
		g := groups[e.Fingerprint]
		if g == nil {
			g = &Group{Event: e, First: e.At}
			groups[e.Fingerprint] = g
			groupPlayers[e.Fingerprint] = map[string]bool{}
		}
		g.Event = e
		g.Count++
		g.Last = e.At
		groupPlayers[e.Fingerprint][e.Player] = true
		g.Players = len(groupPlayers[e.Fingerprint])
	}
	result.Summary.Groups = len(allGroups)
	result.Summary.Players = len(players)
	for _, g := range groups {
		result.Groups = append(result.Groups, *g)
	}
	sort.Slice(result.Groups, func(i, j int) bool {
		if result.Groups[i].Last.Equal(result.Groups[j].Last) {
			return result.Groups[i].Fingerprint < result.Groups[j].Fingerprint
		}
		return result.Groups[i].Last.After(result.Groups[j].Last)
	})
	result.Total = len(result.Groups)
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 50
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	if q.Offset > result.Total {
		q.Offset = result.Total
	}
	last := q.Offset + q.Limit
	if last > result.Total {
		last = result.Total
	}
	result.Groups = result.Groups[q.Offset:last]
	return result
}
