// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// ErrIdentityConflict means two independently populated accounts cannot be
// joined automatically without risking data loss.
var ErrIdentityConflict = errors.New("store: identity reconciliation conflict")

// IdentityReconciliation describes the canonical account selected when the
// native bootstrap identity and the game's socket identity are joined.
type IdentityReconciliation struct {
	Account    Account
	Changed    bool
	AliasAdded string
}

// ResolvePlayerID follows a retired id to the canonical account id.
func (s *Store) ResolvePlayerID(playerID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	canonical := s.resolvePlayerIDLocked(strings.TrimSpace(playerID))
	_, ok := s.state.Accounts[canonical]
	return canonical, ok
}

// Aliases returns every retired id that resolves to playerID.
func (s *Store) Aliases(playerID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	canonical := s.resolvePlayerIDLocked(playerID)
	aliases := make([]string, 0)
	for alias := range s.state.Aliases {
		if alias != canonical && s.resolvePlayerIDLocked(alias) == canonical {
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)
	return aliases
}

func (s *Store) resolvePlayerIDLocked(playerID string) string {
	current := strings.TrimSpace(playerID)
	seen := map[string]struct{}{}
	for current != "" {
		if _, loop := seen[current]; loop {
			return current
		}
		seen[current] = struct{}{}
		next := strings.TrimSpace(s.state.Aliases[current])
		if next == "" || next == current {
			return current
		}
		current = next
	}
	return current
}

// ReconcileAuthenticatedIdentity joins a legacy request's game id to the
// bootstrap account only after validating the reusable bootstrap session.
func (s *Store) ReconcileAuthenticatedIdentity(bootstrapID, session, gameID, socketDevice, ip string) (IdentityReconciliation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sourceID := s.resolvePlayerIDLocked(bootstrapID)
	source := s.state.Accounts[sourceID]
	if source == nil || session == "" || source.Session != session {
		return IdentityReconciliation{}, ErrNotFound
	}
	return s.reconcileIdentityLocked(sourceID, gameID, socketDevice, ip)
}

// AdoptSocketIdentityForAccount joins an already validated socket handshake to
// the id auth_v2 carries. No session token is retained on the socket object.
func (s *Store) AdoptSocketIdentityForAccount(bootstrapID, gameID, socketDevice, ip string) (IdentityReconciliation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sourceID := s.resolvePlayerIDLocked(bootstrapID)
	if s.state.Accounts[sourceID] == nil {
		return IdentityReconciliation{}, ErrNotFound
	}
	return s.reconcileIdentityLocked(sourceID, gameID, socketDevice, ip)
}

// ReconcileRecentHTTPIdentity joins the socket identity to a recently seen
// native/HTTP account only when the client address identifies exactly one
// account that has never been bound to a socket device. The lookup and merge
// happen under one lock so two simultaneous sockets cannot claim the same
// bootstrap account.
//
// candidates reports how many accounts matched. A value other than one means
// no change was made; callers should fall back to normal socket identity
// adoption and log an ambiguous match instead of guessing.
func (s *Store) ReconcileRecentHTTPIdentity(gameID, socketDevice, ip string, maxAge time.Duration) (result IdentityReconciliation, candidates int, err error) {
	ip = strings.TrimSpace(ip)
	if ip == "" || maxAge <= 0 {
		return IdentityReconciliation{}, 0, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().UTC().Add(-maxAge)
	var sourceID string
	for id, account := range s.state.Accounts {
		if account == nil || account.LastIP != ip || account.DeviceID == "" || account.SocketDevice != "" || account.LastSeenAt.Before(cutoff) {
			continue
		}
		candidates++
		sourceID = id
	}
	if candidates != 1 {
		return IdentityReconciliation{}, candidates, nil
	}
	result, err = s.reconcileIdentityLocked(sourceID, gameID, socketDevice, ip)
	return result, candidates, err
}

func (s *Store) reconcileIdentityLocked(sourceID, gameID, socketDevice, ip string) (IdentityReconciliation, error) {
	sourceID = s.resolvePlayerIDLocked(sourceID)
	source := s.state.Accounts[sourceID]
	if source == nil {
		return IdentityReconciliation{}, ErrNotFound
	}
	socketDevice = strings.TrimSpace(socketDevice)
	gameID = strings.TrimSpace(gameID)
	if gameID == "" {
		source.SocketDevice = firstNonEmptyString(socketDevice, source.SocketDevice)
		source.LastSeenAt = time.Now().UTC()
		if ip != "" {
			source.LastIP = ip
		}
		if s.state.Profiles == nil {
			s.state.Profiles = map[string]*Profile{}
		}
		if s.state.Profiles[sourceID] == nil {
			s.state.Profiles[sourceID] = &Profile{PlayerID: sourceID}
		}
		if err := s.persistLocked(); err != nil {
			return IdentityReconciliation{}, err
		}
		return IdentityReconciliation{Account: *source}, nil
	}
	if !looksLikePlayerID(gameID) {
		return IdentityReconciliation{}, errors.New("store: invalid game player id")
	}
	targetID := s.resolvePlayerIDLocked(gameID)
	if targetID == sourceID {
		source.SocketDevice = firstNonEmptyString(socketDevice, source.SocketDevice)
		source.LastSeenAt = time.Now().UTC()
		if ip != "" {
			source.LastIP = ip
		}
		if s.state.Profiles == nil {
			s.state.Profiles = map[string]*Profile{}
		}
		if s.state.Profiles[sourceID] == nil {
			s.state.Profiles[sourceID] = &Profile{PlayerID: sourceID}
		}
		if err := s.persistLocked(); err != nil {
			return IdentityReconciliation{}, err
		}
		return IdentityReconciliation{Account: *source}, nil
	}

	target := s.state.Accounts[targetID]
	if target != nil && socketDevice != "" && target.SocketDevice != "" && target.SocketDevice != socketDevice {
		return IdentityReconciliation{}, ErrIdentityConflict
	}
	if s.state.Profiles == nil {
		s.state.Profiles = map[string]*Profile{}
	}
	sourceProfile := s.state.Profiles[sourceID]
	targetProfile := s.state.Profiles[targetID]
	if profileHasPayload(sourceProfile) && profileHasPayload(targetProfile) {
		return IdentityReconciliation{}, ErrIdentityConflict
	}

	now := time.Now().UTC()
	if target == nil {
		target = source
		delete(s.state.Accounts, sourceID)
		target.PlayerID = targetID
		s.state.Accounts[targetID] = target
	} else {
		target.DeviceID = firstNonEmptyString(source.DeviceID, target.DeviceID)
		target.Session = firstNonEmptyString(source.Session, target.Session)
		target.Nick = preferredNick(target.Nick, source.Nick, targetID)
		if target.CreatedAt.IsZero() || (!source.CreatedAt.IsZero() && source.CreatedAt.Before(target.CreatedAt)) {
			target.CreatedAt = source.CreatedAt
		}
		delete(s.state.Accounts, sourceID)
	}
	target.SocketDevice = firstNonEmptyString(socketDevice, target.SocketDevice, source.SocketDevice)
	target.LastSeenAt = now
	if ip != "" {
		target.LastIP = ip
	} else if target.LastIP == "" {
		target.LastIP = source.LastIP
	}

	if targetProfile == nil && sourceProfile != nil {
		targetProfile = sourceProfile
		targetProfile.PlayerID = targetID
		s.state.Profiles[targetID] = targetProfile
	} else if targetProfile != nil && !profileHasPayload(targetProfile) && sourceProfile != nil {
		sourceProfile.PlayerID = targetID
		s.state.Profiles[targetID] = sourceProfile
	}
	delete(s.state.Profiles, sourceID)
	if s.state.Profiles[targetID] == nil {
		s.state.Profiles[targetID] = &Profile{PlayerID: targetID}
	}

	if s.state.Aliases == nil {
		s.state.Aliases = map[string]string{}
	}
	for alias, canonical := range s.state.Aliases {
		if s.resolvePlayerIDLocked(canonical) == sourceID || canonical == sourceID {
			s.state.Aliases[alias] = targetID
		}
	}
	s.state.Aliases[sourceID] = targetID
	for i := range s.state.Bans {
		if s.state.Bans[i].Kind == BanPlayer && s.state.Bans[i].Subject == sourceID {
			s.state.Bans[i].Subject = targetID
		}
	}
	if err := s.persistLocked(); err != nil {
		return IdentityReconciliation{}, err
	}
	return IdentityReconciliation{Account: *target, Changed: true, AliasAdded: sourceID}, nil
}

// ReconcileAccounts safely combines a known duplicate source account into a
// canonical target. It uses the same no-data-loss checks as automatic
// reconciliation and is intended for the authenticated support endpoint when
// address matching was ambiguous or the accounts predate that logic.
func (s *Store) ReconcileAccounts(sourceID, targetID string) (IdentityReconciliation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sourceID = s.resolvePlayerIDLocked(strings.TrimSpace(sourceID))
	targetID = s.resolvePlayerIDLocked(strings.TrimSpace(targetID))
	if sourceID == targetID {
		return IdentityReconciliation{}, ErrSameAccount
	}
	target := s.state.Accounts[targetID]
	if target == nil {
		return IdentityReconciliation{}, ErrNotFound
	}
	ip := firstNonEmptyString(target.LastIP)
	if source := s.state.Accounts[sourceID]; source != nil && ip == "" {
		ip = source.LastIP
	}
	return s.reconcileIdentityLocked(sourceID, targetID, target.SocketDevice, ip)
}

func profileHasPayload(profile *Profile) bool {
	if profile == nil {
		return false
	}
	// Identity metadata is not progress. Empty profiles still have a generated
	// nick and timestamps, so counting those fields as payload makes every
	// legitimate first-login reconciliation look like a data conflict.
	return profile.Level != 0 || profile.Experience != 0 || profile.Coins != 0 || profile.Gems != 0 ||
		len(profile.Prefs) != 0 || len(profile.Progress) != 0 ||
		len(profile.ProgressDocument) != 0 || len(profile.ProgressSlots) != 0 || len(profile.ClientState) != 0 ||
		profile.TransferredFrom != "" || profile.TransferredTo != "" || profile.TransferredAt != nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func looksLikePlayerID(value string) bool {
	if len(value) != 9 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func preferredNick(primary, secondary, id string) string {
	primary = strings.TrimSpace(primary)
	secondary = strings.TrimSpace(secondary)
	if primary == "" || primary == "Player"+id[len(id)-4:] {
		if secondary != "" {
			return secondary
		}
	}
	return primary
}
