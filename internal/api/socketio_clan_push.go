// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

// Clan push topics (subscribe {type}). Stock 23.1.3 consumers:
//
//	change_clan_state       0x29562FC  existed_clan 0|1, added_clan, removed_clan
//	get_lobby_clan          ClansController.ApplyClanData 0x3C1B930 -> ParseSections 0x3DB785C
//	get_start_clan          same merger; params lh, last_timeStamp, last_timeStamp_service
//	get_clan_builds         same merger
//	get_invites_from_clans  clears, then adds every id of `invites`
//	get_clans_core          Rilisoft.丐丅丑不专丁丘不丄 0x25DF498: {clanId: sections}
//
// After `added_clan` the client subscribes the three section topics itself,
// so a section subscription is bound to the clan it was opened for and goes
// quiet once its owner is no longer a member.
const (
	clanTopicState   = "change_clan_state"
	clanTopicLobby   = "get_lobby_clan"
	clanTopicStart   = "get_start_clan"
	clanTopicBuilds  = "get_clan_builds"
	clanTopicInvites = "get_invites_from_clans"
	clanTopicCores   = "get_clans_core"

	clanCoresLimit    = 50
	clanChatBacklog   = 50
	clanChatLiveLimit = 200
	clanPushTransport = "clan_update"
)

var clanTopicSections = map[string][]string{
	clanTopicLobby:  {"cr", "mb"},
	clanTopicStart:  {"cr", "mb", "ir", "or", "it", "wm", "ms"},
	clanTopicBuilds: {"fl", "fd", "tn", "bl", "up", "bn", "it"},
}

var clanCoreSections = []string{"cr", "mb", "fl", "fd", "tn", "bl"}

func isClanTopic(t string) bool {
	switch t {
	case clanTopicState, clanTopicLobby, clanTopicStart, clanTopicBuilds, clanTopicInvites, clanTopicCores:
		return true
	}
	return false
}

type clanSubscription struct {
	owner, namespace string
	codec            *sio.Codec
	clanID           string
	list             []string
	sigs             map[string]string
	logoHash         string
	afterMessage     int64
	afterService     int64
	requests         map[string]bool
	invites          map[string]bool
	upgrades         map[int]string
	initialized      bool
}

// clanSubscriptions follows the socialSubscriptions locking rules: never take
// it while holding store.mu or session.mu.
type clanSubscriptions struct {
	sync.Mutex
	topics map[string]*clanSubscription
}

func clanSignature(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// clanStampMicros reads a client chat cursor. The client echoes the
// "sec.micro" keys it was sent, so seconds are expected; larger magnitudes
// are accepted as milliseconds or microseconds. Cursors in the future are
// clamped to now so that live messages still flow (PGWSD subscribes with
// last_timeStamp_service = 2147483647 to skip the service backlog).
func clanStampMicros(value any, now time.Time) int64 {
	v, ok := clanFloat(value)
	if !ok || v <= 0 {
		return 0
	}
	var micros float64
	switch {
	case v >= 1e14:
		micros = v
	case v >= 1e11:
		micros = v * 1e3
	default:
		micros = math.Round(v * 1e6)
	}
	limit := now.UnixMicro()
	if micros > float64(limit) {
		return limit
	}
	return int64(micros)
}

func (s *Server) validateClanSubscription(session *sioSession, head sioHead, req int) map[string]any {
	if _, ok := s.socketPlayerID(session); !ok {
		return socketStateFailure(req, "not_authorized")
	}
	if head.text("type") == clanTopicCores {
		if _, ok := clanIDList(head["list"], clanCoresLimit); !ok {
			return clanFailure(req, store.ErrClanInvalid)
		}
	}
	return nil
}

func (s *Server) pushClanSubscription(session *sioSession, namespace, transport string, codec *sio.Codec, head sioHead) {
	topic := head.text("type")
	if session == nil || !isClanTopic(topic) {
		return
	}
	actor, ok := s.socketPlayerID(session)
	if !ok || s.validateClanSubscription(session, head, 0) != nil {
		return
	}
	now := time.Now()
	session.clans.Lock()
	defer session.clans.Unlock()
	if session.clans.topics == nil {
		session.clans.topics = map[string]*clanSubscription{}
	}
	sub := &clanSubscription{owner: actor, namespace: namespace, codec: codec, sigs: map[string]string{}}
	switch topic {
	case clanTopicLobby, clanTopicStart, clanTopicBuilds:
		sub.clanID = s.st.PlayerClanID(actor)
		sub.logoHash = head.text("lh")
		if topic == clanTopicStart {
			sub.afterMessage = clanStampMicros(head["last_timeStamp"], now)
			sub.afterService = clanStampMicros(head["last_timeStamp_service"], now)
		}
	case clanTopicCores:
		sub.list, _ = clanIDList(head["list"], clanCoresLimit)
		if existing := session.clans.topics[topic]; existing != nil && existing.owner == actor {
			for _, id := range existing.list {
				if !hasSocialID(sub.list, id) && len(sub.list) < clanCoresLimit {
					sub.list = append(sub.list, id)
				}
			}
		}
	}
	session.clans.topics[topic] = sub
	s.pushClanLocked(session, topic, sub, transport)
}

func (s *Server) pushClanLocked(session *sioSession, topic string, sub *clanSubscription, transport string) {
	actor, ok := s.socketPlayerID(session)
	if !ok || actor != sub.owner {
		return
	}
	switch topic {
	case clanTopicState:
		s.pushClanStateLocked(session, sub, actor, transport)
	case clanTopicInvites:
		body := map[string]any{"invites": s.st.ClanInvitesFor(actor)}
		sig := clanSignature(body)
		if sub.initialized && sub.sigs["invites"] == sig {
			return
		}
		if s.replyEncrypted(session, sub.namespace, transport, sub.codec, topic, 0, body) {
			sub.sigs["invites"] = sig
			sub.initialized = true
		}
	case clanTopicCores:
		s.pushClanCoresLocked(session, sub, actor, transport)
	default:
		s.pushClanSectionsLocked(session, topic, sub, actor, transport)
	}
}

func (s *Server) pushClanStateLocked(session *sioSession, sub *clanSubscription, actor, transport string) {
	current := s.st.PlayerClanID(actor)
	if !sub.initialized {
		body := map[string]any{"existed_clan": 0}
		if current != "" {
			body["existed_clan"] = 1
			body["clan_id"] = current
		}
		if s.replyEncrypted(session, sub.namespace, transport, sub.codec, clanTopicState, 0, body) {
			sub.initialized = true
			sub.clanID = current
		}
		return
	}
	if current == sub.clanID {
		return
	}
	if sub.clanID != "" {
		if !s.replyEncrypted(session, sub.namespace, transport, sub.codec, clanTopicState, 0, map[string]any{"removed_clan": sub.clanID}) {
			return
		}
		sub.clanID = ""
	}
	if current != "" && s.replyEncrypted(session, sub.namespace, transport, sub.codec, clanTopicState, 0, map[string]any{"added_clan": current}) {
		sub.clanID = current
	}
}

// pushClanSectionsLocked sends only the sections whose content changed since
// the last delivered push. ir/or/up are add-if-absent lists on the client,
// so removals are sent explicitly with delta -1.
func (s *Server) pushClanSectionsLocked(session *sioSession, topic string, sub *clanSubscription, actor, transport string) {
	if sub.clanID == "" {
		return
	}
	v, ok := s.st.ClanByID(sub.clanID, actor)
	if !ok || v.Role(actor) == store.ClanRoleNone {
		return
	}
	body := map[string]any{}
	sigs := map[string]string{}
	logoHash := sub.logoHash
	afterMessage, afterService := sub.afterMessage, sub.afterService
	var requests, invites map[string]bool
	var upgrades map[int]string
	full := func(name string, value any) {
		sig := clanSignature(value)
		if sub.sigs[name] != sig {
			body[name] = value
			sigs[name] = sig
		}
	}
	for _, name := range clanTopicSections[topic] {
		switch name {
		case "cr":
			core := clanSectionCore(v, false)
			sig := clanSignature(core)
			withLogo := v.LogoHash != sub.logoHash && v.Logo != ""
			if sub.sigs[name] == sig && !withLogo {
				continue
			}
			if withLogo {
				core["lg"] = v.Logo
				logoHash = v.LogoHash
			}
			body[name], sigs[name] = core, sig
		case "mb":
			full(name, clanSectionMembers(v))
		case "it":
			full(name, clanSectionItems(v))
		case "wm":
			full(name, clanWarMarksWire(v))
		case "fl":
			full(name, clanFieldWire(v))
		case "fd":
			full(name, clanDecorWire(v))
		case "tn":
			full(name, clanTankWire(v))
		case "bl":
			full(name, clanSectionLevels(v))
		case "bn":
			full(name, clanSectionBuildings(v))
		case "ir":
			var rows []any
			rows, requests = clanPendingDiff(sub.requests, v.Requests, nil)
			if len(rows) > 0 {
				body[name] = rows
			}
		case "or":
			var rows []any
			rows, invites = clanPendingDiff(sub.invites, nil, v.Invites)
			if len(rows) > 0 {
				body[name] = rows
			}
		case "up":
			var rows []any
			rows, upgrades = clanUpgradeDiff(sub.upgrades, v.Upgrades)
			if len(rows) > 0 {
				body[name] = map[string]any{"it": rows}
			}
		case "ms":
			skip, limit := actor, clanChatLiveLimit
			if !sub.initialized {
				skip, limit = "", clanChatBacklog
			}
			var messages map[string]any
			messages, afterMessage, afterService = clanSectionMessages(v, sub.afterMessage, sub.afterService, skip, limit)
			if len(messages["people_messages"].([]any))+len(messages["service_messages"].([]any)) > 0 {
				body[name] = messages
			}
		}
	}
	if len(body) > 0 && !s.replyEncrypted(session, sub.namespace, transport, sub.codec, topic, 0, body) {
		return
	}
	for name, sig := range sigs {
		sub.sigs[name] = sig
	}
	sub.logoHash = logoHash
	sub.afterMessage, sub.afterService = afterMessage, afterService
	if requests != nil {
		sub.requests = requests
	}
	if invites != nil {
		sub.invites = invites
	}
	if upgrades != nil {
		sub.upgrades = upgrades
	}
	sub.initialized = true
}

// clanPendingDiff renders ir ({id,m}) or or ({id}) rows against what the
// client was last sent and returns the new known set.
func clanPendingDiff(known map[string]bool, requests []store.ClanRequest, invites []store.ClanInvite) ([]any, map[string]bool) {
	current := map[string]bool{}
	rows := []any{}
	for _, r := range requests {
		current[r.Player] = true
		if !known[r.Player] {
			rows = append(rows, map[string]any{"id": r.Player, "m": r.Message})
		}
	}
	for _, r := range invites {
		current[r.Player] = true
		if !known[r.Player] {
			rows = append(rows, map[string]any{"id": r.Player})
		}
	}
	gone := []string{}
	for id := range known {
		if !current[id] {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	for _, id := range gone {
		rows = append(rows, map[string]any{"id": id, "delta": -1})
	}
	return rows, current
}

func clanUpgradeDiff(known map[int]string, upgrades []store.ClanUpgrade) ([]any, map[int]string) {
	current := map[int]string{}
	rows := []any{}
	for _, up := range upgrades {
		row := map[string]any{"i": up.Item, "f": up.From, "fl": up.FinishAt}
		sig := clanSignature(row)
		current[up.Item] = sig
		if known[up.Item] != sig {
			rows = append(rows, row)
		}
	}
	gone := []int{}
	for item := range known {
		if _, ok := current[item]; !ok {
			gone = append(gone, item)
		}
	}
	sort.Ints(gone)
	for _, item := range gone {
		rows = append(rows, map[string]any{"i": item, "delta": -1})
	}
	return rows, current
}

// pushClanCoresLocked serves fort/tank snapshots of other clans (lobby fort
// visits and war opponents). Unknown or deleted clans are skipped.
func (s *Server) pushClanCoresLocked(session *sioSession, sub *clanSubscription, actor, transport string) {
	body := map[string]any{}
	sigs := map[string]string{}
	for _, id := range sub.list {
		v, ok := s.st.ClanByID(id, actor)
		if !ok {
			continue
		}
		sections := clanSections(v, clanCoreSections, false)
		sig := clanSignature(sections)
		if sub.sigs[id] == sig {
			continue
		}
		body[id], sigs[id] = sections, sig
	}
	if len(body) == 0 {
		sub.initialized = true
		return
	}
	if s.replyEncrypted(session, sub.namespace, transport, sub.codec, clanTopicCores, 0, body) {
		for id, sig := range sigs {
			sub.sigs[id] = sig
		}
		sub.initialized = true
	}
}

// refreshClans re-evaluates every clan topic of one session.
func (s *Server) refreshClans(session *sioSession) {
	if session == nil {
		return
	}
	session.clans.Lock()
	defer session.clans.Unlock()
	topics := make([]string, 0, len(session.clans.topics))
	for topic := range session.clans.topics {
		topics = append(topics, topic)
	}
	// change_clan_state first: it is what makes the client (re)subscribe.
	sort.Slice(topics, func(i, j int) bool {
		if (topics[i] == clanTopicState) != (topics[j] == clanTopicState) {
			return topics[i] == clanTopicState
		}
		return topics[i] < topics[j]
	})
	for _, topic := range topics {
		s.pushClanLocked(session, topic, session.clans.topics[topic], clanPushTransport)
	}
}

func (s *Server) refreshClansAll() {
	s.sio.mu.Lock()
	sessions := make([]*sioSession, 0, len(s.sio.sessions))
	for _, v := range s.sio.sessions {
		sessions = append(sessions, v)
	}
	s.sio.mu.Unlock()
	for _, session := range sessions {
		s.refreshClans(session)
	}
}

func (s *Server) unsubscribeClans(session *sioSession, head sioHead) {
	if session == nil {
		return
	}
	topic := head.text("type")
	if !isClanTopic(topic) {
		return
	}
	session.clans.Lock()
	defer session.clans.Unlock()
	sub := session.clans.topics[topic]
	if sub == nil {
		return
	}
	if topic == clanTopicCores {
		if ids, ok := clanIDList(head["list"], math.MaxInt32); ok && len(ids) > 0 {
			kept := []string{}
			for _, id := range sub.list {
				if !hasSocialID(ids, id) {
					kept = append(kept, id)
				} else {
					delete(sub.sigs, id)
				}
			}
			sub.list = kept
			if len(kept) > 0 {
				return
			}
		}
	}
	delete(session.clans.topics, topic)
}
