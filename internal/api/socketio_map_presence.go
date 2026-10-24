// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"strconv"
	"strings"
	"time"
)

// This measures authenticated stock clients reporting an online room, NOT
// authoritative Photon actor counts. An alive API socket cannot prove that
// its separate Photon peer is alive. See docs/MAP_POPULARITY_23_1_3.md.
const stockMapPresenceLiveness = 60 * time.Second

type stockMapKey struct{ mode, protocol, mapID string }
type stockMapPresence struct {
	fields map[string]string
	// Catalog entries are private to this session. Only an unambiguous group 0
	// is supported: never substitute platform "group", raw "tier", or guess a
	// ranked partition from mmrank/tier_mm. Both phones validated this path.
	catalog map[stockMapKey]string
	retired bool
}

var stockMapPresenceFields = [...]string{"game_mode", "version", "room_name", "map", "protocol", "mmrank", "tier_mm", "rank"}

func (session *sioSession) mergeStockMapPresence(head sioHead) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.mapPresence.fields == nil {
		session.mapPresence.fields = make(map[string]string)
	}
	// The stock collector emits deltas. Missing fields are not clears; explicit
	// empty room/protocol and map=-1 ARE clears. Bad supplied values clear the
	// old value instead of keeping a previously countable room alive.
	for _, key := range stockMapPresenceFields {
		if _, supplied := head[key]; !supplied {
			continue
		}
		value := ""
		switch head[key].(type) {
		case string, float64, int:
			value = head.text(key)
		}
		if len(value) > mapPopularityMaxKeyBytes || strings.ContainsAny(value, "\x00\r\n\t") {
			value = ""
		}
		if key == "tier_mm" && value == "" {
			// An explicitly malformed optional rank is not an absent rank.
			value = "invalid"
		}
		session.mapPresence.fields[key] = value
	}
}

// Called only after the request has passed emptyRequestedMapPopularity's full
// validation/bounds. An absent/empty params poll does not erase a known catalog.
func (session *sioSession) rememberStockMapCatalog(raw any) {
	modes, _ := raw.(map[string]any)
	if len(modes) == 0 {
		return
	}
	catalog := make(map[stockMapKey]string)
	for mode, rv := range modes {
		for protocol, rg := range rv.(map[string]any) {
			for group, rm := range rg.(map[string]any) {
				for _, name := range rm.([]any) {
					key := stockMapKey{mode, protocol, name.(string)}
					if previous, exists := catalog[key]; exists && previous != group {
						catalog[key] = "ambiguous"
					} else {
						catalog[key] = group
					}
				}
			}
		}
	}
	session.mu.Lock()
	session.mapPresence.catalog = catalog
	session.mu.Unlock()
}

func (s *Server) stockMapSessions() []*sioSession {
	s.sio.mu.Lock()
	defer s.sio.mu.Unlock()
	sessions := make([]*sioSession, 0, len(s.sio.sessions))
	for _, session := range s.sio.sessions {
		sessions = append(sessions, session)
	}
	return sessions
}

// Permanently retire older sockets for this account. Otherwise dropping a new
// lobby socket could resurrect its old in-match socket. No per-account global
// tombstone is needed: the flag dies with the bounded existing session record.
func (s *Server) retireOlderMapPresence(current *sioSession) {
	playerID, ok := s.socketPlayerID(current)
	if !ok {
		return
	}
	for _, other := range s.stockMapSessions() {
		if other == current {
			continue
		}
		otherID, ok := s.socketPlayerID(other)
		if !ok || otherID != playerID {
			continue
		}
		older := other
		if current.created.Before(other.created) || (current.created.Equal(other.created) && current.sid < other.sid) {
			older = current
		}
		older.mu.Lock()
		older.mapPresence.retired = true
		older.mu.Unlock()
	}
}

func (session *sioSession) stockMapObservation(now time.Time) (stockMapKey, bool) {
	session.mu.Lock()
	defer session.mu.Unlock()
	p := session.mapPresence
	if p.retired || session.lastSeen.IsZero() || now.Before(session.lastSeen) || now.Sub(session.lastSeen) > stockMapPresenceLiveness {
		return stockMapKey{}, false
	}
	f := p.fields
	// Only the measured default platform=1, variant=0 partition is enabled.
	// Other platforms/variants need independent protocol/group verification.
	// These bits do NOT prove online: offline rooms use the same prefix.
	encoded, err := strconv.Atoi(f["game_mode"])
	if err != nil || strconv.Itoa(encoded) != f["game_mode"] || encoded < 110000 || encoded >= 110100 {
		return stockMapKey{}, false
	}
	room := f["room_name"]
	if room == "" || strings.TrimSpace(room) != room || strings.EqualFold(room, "offline") || f["version"] != "23.1.3" || !mapPopularityTextKey(f["protocol"]) || !mapPopularityIntegerKey(f["map"]) {
		return stockMapKey{}, false
	}
	// Missing rank is not rank zero. Nonzero/conditional ranked partitions need
	// the normalized frontend contract; returning zero is safer than miscounting.
	if f["mmrank"] != "0" || (f["tier_mm"] != "" && f["tier_mm"] != "0") {
		return stockMapKey{}, false
	}
	key := stockMapKey{strconv.Itoa(encoded % 100), f["protocol"], f["map"]}
	group, exists := p.catalog[key]
	return key, exists && group == "0"
}

// The response has no protocol dimension. If this request maps the same
// mode/group/map to several protocols, do not silently sum incompatible pools.
func requestedStockMap(raw any, key stockMapKey) bool {
	modes, _ := raw.(map[string]any)
	versions, _ := modes[key.mode].(map[string]any)
	matches := 0
	requested := false
	for protocol, rg := range versions {
		groups, _ := rg.(map[string]any)
		names, _ := groups["0"].([]any)
		for _, name := range names {
			if name == key.mapID {
				matches++
				requested = protocol == key.protocol
				break
			}
		}
	}
	return matches == 1 && requested
}

func (s *Server) fillStockMapPopularity(data mapPopularityData, raw any, now time.Time) {
	seen := make(map[string]bool)
	for _, session := range s.stockMapSessions() {
		playerID, ok := s.socketPlayerID(session)
		if !ok || seen[playerID] {
			continue
		}
		key, ok := session.stockMapObservation(now)
		if !ok || !requestedStockMap(raw, key) {
			continue
		}
		seen[playerID] = true
		data[key.mode]["0"][key.mapID]++
	}
}
