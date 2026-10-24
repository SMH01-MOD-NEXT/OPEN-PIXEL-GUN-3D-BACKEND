// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

// Measured stock client: request 0x456ACA4 / 0x456B2A8; subscription
// 0x456AC18; consumer 0x4569F68 requires squad_invites list entries with
// id/name/squad/protocol and integer invite_number / *_mode flags.
// Photon remains responsible for creating/joining the actual room. Invitations
// are short-lived signalling, never proof of Photon room ownership/membership.
const squadInviteTTL = 2 * time.Minute

type squadInvite struct {
	sender, receiver, room, protocol string
	number, siege, clanwar, test     int
	expires                          time.Time
}
type squadInviteRate struct {
	minute int64
	count  int
}
type squadInvitesState struct {
	sync.Mutex
	invites []squadInvite
	rates   map[string]squadInviteRate
}

func squadText(v any, max int) (string, bool) {
	text, ok := v.(string)
	if !ok || text == "" || len(text) > max || !utf8.ValidString(text) || strings.TrimSpace(text) != text {
		return "", false
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return text, true
}
func squadInteger(v any, max int) (int, bool) {
	n, ok := socialNumber(v)
	if !ok || n > float64(max) {
		return 0, false
	}
	return int(n), n == float64(int(n))
}
func (s *Server) answerSendSquadInvite(session *sioSession, head sioHead, req int) map[string]any {
	actor, ok := s.socketPlayerID(session)
	if !ok {
		return socketStateFailure(req, "not_authorized")
	}
	target, ok := s.st.ResolvePlayerID(head.text("receiver_id"))
	if !ok || target == actor {
		return socialFailure(req, store.ErrSocialInvalid)
	}
	room, roomOK := squadText(head["squad_name"], 256)
	protocol, protocolOK := squadText(head["protocol"], 64)
	number, numberOK := squadInteger(head["invite_number"], 2147483647)
	siege, siegeOK := squadInteger(head["siege_mode"], 1)
	clanwar, clanwarOK := squadInteger(head["clanwar_mode"], 1)
	test, testOK := squadInteger(head["test_mode"], 1)
	if !roomOK || !protocolOK || !numberOK || head["invite_number"] == nil || !siegeOK || !clanwarOK || !testOK {
		return socialFailure(req, store.ErrSocialInvalid)
	}
	// Clan/test/siege modes have additional membership requirements. They are
	// deliberately unsupported rather than trusting arbitrary mode flags.
	if siege != 0 || clanwar != 0 || test != 0 {
		return socketStateFailure(req, "unsupported_squad_mode")
	}
	snap, err := s.st.SocialSnapshot(actor)
	if err != nil {
		return socialFailure(req, err)
	}
	if !hasSocialID(snap.Friends, target) {
		return socialFailure(req, store.ErrSocialForbidden)
	}
	now := time.Now().UTC()
	state := &s.sio.squads
	state.Lock()
	defer state.Unlock()
	state.prune(now)
	for _, invite := range state.invites {
		if invite.sender == actor && invite.receiver == target && invite.room == room && invite.protocol == protocol && invite.number == number {
			return sioOK(req)
		}
	}
	if state.rates == nil {
		state.rates = map[string]squadInviteRate{}
	}
	rate := state.rates[actor]
	if rate.minute != now.Unix()/60 {
		rate = squadInviteRate{minute: now.Unix() / 60}
	}
	if rate.count >= 10 {
		return socialFailure(req, store.ErrSocialRate)
	}
	total := 0
	for _, invite := range state.invites {
		if invite.receiver == target && invite.sender != actor {
			total++
		}
	}
	replacing := false
	for _, invite := range state.invites {
		if invite.sender == actor && invite.receiver == target {
			replacing = true
			break
		}
	}
	if total >= 20 || (len(state.invites) >= 2000 && !replacing) {
		return socialFailure(req, store.ErrSocialFull)
	}
	rate.count++
	state.rates[actor] = rate
	// A newer invite from the same friend replaces their obsolete room/number.
	kept := state.invites[:0]
	for _, invite := range state.invites {
		if invite.sender != actor || invite.receiver != target {
			kept = append(kept, invite)
		}
	}
	state.invites = append(kept, squadInvite{actor, target, room, protocol, number, siege, clanwar, test, now.Add(squadInviteTTL)})
	return sioOK(req)
}
func (state *squadInvitesState) prune(now time.Time) {
	kept := state.invites[:0]
	for _, invite := range state.invites {
		if invite.expires.After(now) {
			kept = append(kept, invite)
		}
	}
	state.invites = kept
	for actor, rate := range state.rates {
		if rate.minute < now.Unix()/60-1 {
			delete(state.rates, actor)
		}
	}
}
func (s *Server) squadInvitesBody(actor string, now time.Time) map[string]any {
	state := &s.sio.squads
	state.Lock()
	state.prune(now)
	invites := append([]squadInvite(nil), state.invites...)
	state.Unlock()
	rows := []any{}
	snap, err := s.st.SocialSnapshot(actor)
	if err != nil {
		return map[string]any{"squad_invites": rows}
	}
	// Resolve aliases at read time so ID changes never retarget a pending invite.
	sort.Slice(invites, func(i, j int) bool { return invites[i].sender < invites[j].sender })
	for _, invite := range invites {
		receiver, ok := s.st.ResolvePlayerID(invite.receiver)
		if !ok || receiver != actor {
			continue
		}
		sender, ok := s.st.ResolvePlayerID(invite.sender)
		if !ok || !hasSocialID(snap.Friends, sender) {
			continue
		}
		p, err := s.st.Profile(sender)
		if err != nil {
			continue
		}
		rows = append(rows, map[string]any{"id": sender, "name": p.Nick, "squad": invite.room, "protocol": invite.protocol, "invite_number": invite.number, "siege_mode": invite.siege, "clanwar_mode": invite.clanwar, "test_mode": invite.test})
	}
	return map[string]any{"squad_invites": rows}
}
func (s *Server) answerSquadInvites(session *sioSession, _ sioHead, req int) map[string]any {
	actor, ok := s.socketPlayerID(session)
	if !ok {
		return socketStateFailure(req, "not_authorized")
	}
	body := s.squadInvitesBody(actor, time.Now())
	body[sioRequestIDKey] = req
	return body
}
