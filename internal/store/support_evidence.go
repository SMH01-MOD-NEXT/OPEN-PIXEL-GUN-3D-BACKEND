// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"strings"
	"time"
	"unicode"
)

type DeviceObservation struct {
	At        time.Time `json:"at"`
	IP        string    `json:"ip"`
	Model     string    `json:"model,omitempty"`
	OS        string    `json:"os,omitempty"`
	Platform  string    `json:"platform,omitempty"`
	Version   string    `json:"version,omitempty"`
	UserAgent string    `json:"user_agent,omitempty"`
	Source    string    `json:"source"`
}
type PlayerEvidence struct {
	CreatedAt    time.Time           `json:"account_created_at"`
	Observations []DeviceObservation `json:"observations"`
}
type WalletEvent struct {
	At      time.Time        `json:"at"`
	Source  string           `json:"source"`
	OfferID int              `json:"offer_id,omitempty"`
	Delta   map[string]int64 `json:"delta"`
}

func boundedEvidence(v string, n int) string {
	v = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, v))
	r := []rune(v)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}
func (s *Store) ObserveDevice(owner Account, o DeviceObservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.state.Accounts[owner.PlayerID]
	if a == nil || a.DeviceID != owner.DeviceID || a.SocketDevice != owner.SocketDevice || !a.CreatedAt.Equal(owner.CreatedAt) {
		return ErrNotFound
	}
	o.Model = boundedEvidence(o.Model, 128)
	o.OS = boundedEvidence(o.OS, 128)
	o.Platform = boundedEvidence(o.Platform, 32)
	o.Version = boundedEvidence(o.Version, 64)
	o.UserAgent = boundedEvidence(o.UserAgent, 256)
	o.Source = boundedEvidence(o.Source, 32)
	o.IP = boundedEvidence(o.IP, 64)
	o.At = time.Now().UTC()
	if s.state.Evidence == nil {
		s.state.Evidence = map[string]PlayerEvidence{}
	}
	old, exists := s.state.Evidence[owner.PlayerID]
	next := old
	if !old.CreatedAt.Equal(owner.CreatedAt) {
		next = PlayerEvidence{CreatedAt: owner.CreatedAt}
	}
	next.Observations = append([]DeviceObservation{}, next.Observations...)
	if n := len(next.Observations); n > 0 {
		last := next.Observations[n-1]
		if last.IP == o.IP && last.Model == o.Model && last.OS == o.OS && last.Platform == o.Platform && last.Version == o.Version && last.UserAgent == o.UserAgent && last.Source == o.Source && o.At.Sub(last.At) < 24*time.Hour {
			return nil
		}
	}
	next.Observations = append(next.Observations, o)
	if len(next.Observations) > 24 {
		next.Observations = next.Observations[len(next.Observations)-24:]
	}
	s.state.Evidence[owner.PlayerID] = next
	if e := s.persistLocked(); e != nil {
		if exists {
			s.state.Evidence[owner.PlayerID] = old
		} else {
			delete(s.state.Evidence, owner.PlayerID)
		}
		return e
	}
	return nil
}
func (s *Store) Evidence(id string) PlayerEvidence {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id = s.resolvePlayerIDLocked(id)
	e := s.state.Evidence[id]
	if a := s.state.Accounts[id]; a == nil || !a.CreatedAt.Equal(e.CreatedAt) {
		return PlayerEvidence{Observations: []DeviceObservation{}}
	}
	e.Observations = append([]DeviceObservation{}, e.Observations...)
	return e
}
func recordWalletEvent(before Profile, after *Profile, source string, offerID int, now time.Time) {
	d := map[string]int64{}
	for _, k := range []string{"Coins", "GemsCurrency", "PixelPassCurrency", "BattlePassCurrency"} {
		read := func(p Profile) int64 {
			m, _ := p.ProgressSlots["3"].(map[string]any)
			v, _ := counterInt64(m[k])
			if m == nil {
				if k == "Coins" {
					v = p.Coins
				}
				if k == "GemsCurrency" {
					v = p.Gems
				}
			}
			return v
		}
		delta := read(*after) - read(before)
		if delta != 0 {
			d[k] = delta
		}
	}
	if len(d) == 0 {
		return
	}
	after.WalletHistory = append(after.WalletHistory, WalletEvent{now.UTC(), source, offerID, d})
	if len(after.WalletHistory) > 100 {
		after.WalletHistory = after.WalletHistory[len(after.WalletHistory)-100:]
	}
}
