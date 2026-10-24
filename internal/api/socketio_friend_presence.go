// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"strconv"
	"time"
)

// Native player_online callback 0x250E4F8 -> 0x250E57C iterates a bare
// id->dictionary map. Explicit null removes an offline peer from its cache.
// Nonempty dictionaries are stringified by 0x250E874; game_mode and protocol
// are read unconditionally by 0x1D9ACD8. Never include account/device secrets.
func (s *Server) friendPresence(id string, now time.Time) map[string]any {
	var selected *sioSession
	var newest time.Time
	var fields map[string]string
	for _, session := range s.stockMapSessions() {
		peer, ok := s.socketPlayerID(session)
		if !ok || peer != id {
			continue
		}
		session.mu.Lock()
		alive := !session.mapPresence.retired && !session.lastSeen.IsZero() && !now.Before(session.lastSeen) && now.Sub(session.lastSeen) <= stockMapPresenceLiveness
		if alive && (selected == nil || session.created.After(newest) || (session.created.Equal(newest) && session.sid > selected.sid)) {
			selected = session
			newest = session.created
			fields = make(map[string]string, len(session.mapPresence.fields))
			for k, v := range session.mapPresence.fields {
				fields[k] = v
			}
		}
		session.mu.Unlock()
	}
	if selected == nil {
		return nil
	}
	// Lobby is the observed stock default, not a fabricated Photon room.
	out := map[string]any{"game_mode": "100006", "protocol": "", "map": "-1", "room_name": "", "rank": "0"}
	if mode, err := strconv.Atoi(fields["game_mode"]); err == nil && mode >= 100000 && mode <= 999999 && strconv.Itoa(mode) == fields["game_mode"] {
		out["game_mode"] = fields["game_mode"]
		for _, k := range []string{"protocol", "map", "room_name"} {
			if v, ok := fields[k]; ok {
				out[k] = v
			}
		}
		if rank, err := strconv.ParseInt(fields["rank"], 10, 32); err == nil && rank >= 0 {
			out["rank"] = strconv.FormatInt(rank, 10)
		}
	}
	return out
}

func (s *Server) friendsOnlineBody(actor string, peers []string) map[string]any {
	out := map[string]any{}
	snap, err := s.st.SocialSnapshot(actor)
	if err != nil {
		return out
	}
	now := time.Now().UTC()
	for _, id := range peers {
		// A former/non-friend gets a null tombstone, not a location disclosure.
		canonical, ok := s.st.ResolvePlayerID(id)
		if !ok || !hasSocialID(snap.Friends, canonical) {
			out[id] = nil
			continue
		}
		info := s.friendPresence(canonical, now)
		if info == nil {
			out[id] = nil
		} else {
			out[id] = info
		}
	}
	return out
}

func (s *Server) recordSocketGameSeen(session *sioSession, flush bool) {
	id, ok := s.socketPlayerID(session)
	if !ok {
		return
	}
	session.mu.Lock()
	observed := session.lastSeen
	retired := session.mapPresence.retired
	session.mu.Unlock()
	if retired || observed.IsZero() {
		return
	}
	if err := s.st.RecordGameSeen(id, observed, flush); err != nil {
		s.log.Event("socket.presence_store_failed", map[string]any{"reason": "game_last_seen"})
	}
}

func (s *Server) endSocketGamePresence(session *sioSession) {
	s.recordSocketGameSeen(session, true)
	session.mu.Lock()
	session.mapPresence.retired = true
	session.mu.Unlock()
	s.refreshSocialAll()
}
