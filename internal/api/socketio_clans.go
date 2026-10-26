// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

// Stock 23.1.3 clan requests (PGCompany.Clans.Network). Contracts were read
// from the client parsers; docs/CLANS_23_1_3.md lists every address.
//
// Errors are reported with the native numeric err_code. The callers that
// branch on a specific code: 1702 member limit (join/accept, Key_5938), 1705
// already in a clan (invite Key_4253, request accept drops the row), 1708 name
// taken, 1710 cooldown with `time` seconds (client fallback 1200), 1711
// upgrade mismatch, 1740 request refused (client forgets its sent request),
// 1400 invalid. Anything else renders the generic failure.
const (
	clanErrGeneric   = 1
	clanErrInvalid   = 1400
	clanErrFull      = 1702
	clanErrMember    = 1705
	clanErrNameTaken = 1708
	clanErrCooldown  = 1710
	clanErrUpgrade   = 1711
	clanErrPromo     = 1730
	clanErrRefused   = 1740
)

func clanFailure(req int, err error) map[string]any {
	body := map[string]any{sioRequestIDKey: req, "error": 1}
	code, result := clanErrGeneric, "clan_failed"
	var cooldown *store.ClanCooldownError
	switch {
	case errors.As(err, &cooldown):
		code, result = clanErrCooldown, "clan_cooldown"
		body["time"] = cooldown.Seconds()
	case errors.Is(err, store.ErrClanNameTaken):
		code, result = clanErrNameTaken, "clan_name_taken"
	case errors.Is(err, store.ErrClanMember):
		code, result = clanErrMember, "already_in_clan"
	case errors.Is(err, store.ErrClanFull):
		code, result = clanErrFull, "clan_full"
	case errors.Is(err, store.ErrClanUpgrade):
		code, result = clanErrUpgrade, "clan_upgrade_mismatch"
	case errors.Is(err, store.ErrClanInvalid), errors.Is(err, store.ErrClanNotFound):
		code, result = clanErrInvalid, "invalid_clan_request"
	case errors.Is(err, store.ErrClanForbidden), errors.Is(err, store.ErrClanNotMember):
		result = "clan_forbidden"
	case errors.Is(err, store.ErrClanRate):
		result = "rate_limited"
	case errors.Is(err, store.ErrClanRank):
		result = "clan_rank_too_low"
	case errors.Is(err, store.ErrClanClosed):
		result = "clan_private"
	case errors.Is(err, store.ErrClanPending):
		result = "clan_pending_limit"
	case errors.Is(err, store.ErrNotFound):
		result = "not_found"
	}
	body["err_code"] = code
	body["result"] = result
	return body
}

// isClanMutation names requests after which subscribed clan views refresh.
func isClanMutation(event string) bool {
	switch event {
	case "create_clan_v2", "change_clan_params", "join_clan", "accept_invite_to_join_clan",
		"reject_invite_to_join_clan", "ask_to_join_clan", "accept_request_to_join_clan",
		"reject_request_to_join_clan", "reject_request_to_join_clan_all", "invite_to_clan",
		"change_clan_role", "kick_from_clan", "leave_clan", "delete_clan", "send_clan_message",
		"update_clan_field", "update_clan_field_decor", "update_clan_tank", "set_clan_building_levels",
		"add_upgrading_item", "finish_upgrading_item", "create_clan_building", "update_clan_war_mark",
		"activate_clan_promo":
		return true
	}
	return false
}

// clanAnswerFor routes every clan request this server owns.
func clanAnswerFor(event string) sioAnswer {
	switch event {
	case "create_clan_v2":
		return (*Server).answerCreateClan
	case "change_clan_params":
		return (*Server).answerChangeClanParams
	case "get_possible_clans":
		return (*Server).answerPossibleClans
	case "get_clan_info_by_param":
		return (*Server).answerSearchClans
	case "get_clan_info_v2":
		return (*Server).answerClanInfoV2
	case "get_clan_data":
		return (*Server).answerClanData
	case "join_clan":
		return (*Server).answerJoinClan
	case "accept_invite_to_join_clan":
		return clanInviteAnswer(true)
	case "reject_invite_to_join_clan":
		return clanInviteAnswer(false)
	case "ask_to_join_clan":
		return (*Server).answerAskToJoinClan
	case "accept_request_to_join_clan":
		return clanRequestAnswer(true)
	case "reject_request_to_join_clan":
		return clanRequestAnswer(false)
	case "reject_request_to_join_clan_all":
		return (*Server).answerRejectAllClanRequests
	case "invite_to_clan":
		return (*Server).answerInviteToClan
	case "change_clan_role":
		return (*Server).answerChangeClanRole
	case "kick_from_clan":
		return (*Server).answerKickFromClan
	case "leave_clan":
		return (*Server).answerLeaveClan
	case "delete_clan":
		return (*Server).answerDeleteClan
	case "get_clan_status":
		return (*Server).answerClanStatus
	case "send_clan_message":
		return (*Server).answerSendClanMessage
	case "send_clan_chat_event":
		return (*Server).answerClanChatEvent
	case "update_clan_field":
		return (*Server).answerUpdateClanField
	case "update_clan_field_decor":
		return (*Server).answerUpdateClanDecor
	case "update_clan_tank":
		return (*Server).answerUpdateClanTank
	case "set_clan_building_levels":
		return (*Server).answerSetClanBuildingLevels
	case "add_upgrading_item":
		return (*Server).answerAddUpgradingItem
	case "finish_upgrading_item":
		return (*Server).answerFinishUpgradingItem
	case "create_clan_building":
		return (*Server).answerCreateClanBuilding
	case "update_clan_war_mark":
		return (*Server).answerUpdateClanWarMark
	case "activate_clan_promo":
		return (*Server).answerActivateClanPromo
	}
	return clanWarAnswerFor(event)
}

// clanInt reads an integral JSON number, numeric string or bool.
func clanInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || math.Abs(v) > math.MaxInt32 {
			return 0, false
		}
		return int(v), true
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return clanInt(n)
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	case int:
		return v, true
	case int64:
		if v > math.MaxInt32 || v < math.MinInt32 {
			return 0, false
		}
		return int(v), true
	}
	return 0, false
}

func clanFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(v, ",", ".")), 64)
		return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
	case int:
		return float64(v), true
	}
	return 0, false
}

func headClanInt(head sioHead, key string) (*int, bool) {
	raw, present := head[key]
	if !present || raw == nil {
		return nil, true
	}
	n, ok := clanInt(raw)
	if !ok {
		return nil, false
	}
	return &n, true
}

func headClanText(head sioHead, key string) (*string, bool) {
	raw, present := head[key]
	if !present || raw == nil {
		return nil, true
	}
	text, ok := raw.(string)
	return &text, ok
}

func headClanID(head sioHead, key string) string {
	id := strings.TrimSpace(head.text(key))
	if !store.ValidClanID(id) {
		return ""
	}
	return id
}

func headPlayerID(head sioHead, key string) string {
	id := strings.TrimSpace(head.text(key))
	if !store.ValidPlayerID(id) {
		return ""
	}
	return id
}

// clanType accepts the boxed enum as number or name.
func clanType(value any) (int, bool) {
	if value == nil {
		return store.ClanTypePublic, true
	}
	if text, ok := value.(string); ok {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "public", "open":
			return store.ClanTypePublic, true
		case "private", "closed":
			return store.ClanTypePrivate, true
		}
	}
	n, ok := clanInt(value)
	if !ok || n < 0 || n > store.ClanTypePrivate {
		return 0, false
	}
	if n == 0 {
		n = store.ClanTypePublic
	}
	return n, true
}

// clanRole accepts the boxed ClanRole as number or name.
func clanRole(value any) (int, bool) {
	if text, ok := value.(string); ok {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "soldier":
			return store.ClanRoleSoldier, true
		case "leader":
			return store.ClanRoleLeader, true
		case "officer":
			return store.ClanRoleOfficer, true
		}
	}
	return clanInt(value)
}

// ---- wire projections ----------------------------------------------------

func clanCoreFull(v store.ClanView) map[string]any {
	return map[string]any{
		"id": v.ID, "type": v.Type, "name": v.Name, "creator_id": v.Creator,
		"sieges_won": v.SiegesWon, "siege_party": false, "shields": v.Shields,
		"division": v.Division, "members_cnt": len(v.Members), "slogan": v.Slogan,
		"logo": v.Logo, "logo_hash": v.LogoHash, "leader_message": v.LeaderMessage,
		"clan_war_wins": v.WarWins, "continent": v.Continent, "min_rank": v.MinRank,
		"pr": clanPromo(v),
	}
}

func clanPromo(v store.ClanView) int64 {
	if v.PromoUntil > time.Now().Unix() {
		return v.PromoUntil
	}
	return 0
}

func clanMembersFull(v store.ClanView) []any {
	out := make([]any, 0, len(v.MemberViews))
	for _, m := range v.MemberViews {
		out = append(out, map[string]any{
			"id": m.ID, "nick": m.Nick, "role": m.Role, "details": m.Details,
			"war_score": m.WarScore, "rank": m.Rank, "clan_rank": m.ClanRank,
			"gems_up": m.GemsUp, "join_time": m.JoinedAt, "re": m.RankExp,
		})
	}
	return out
}

func clanItemsWire(v store.ClanView) []any {
	out := make([]any, 0, len(v.Items))
	for _, it := range v.Items {
		out = append(out, map[string]any{"i": it.Index, "c": it.Count, "l": it.Level})
	}
	return out
}

func clanFieldWire(v store.ClanView) []any {
	out := make([]any, 0, len(v.Field))
	for _, c := range v.Field {
		out = append(out, map[string]any{"p": map[string]any{"x": c.X, "y": c.Y, "z": c.Z}, "i": c.Item, "r": c.Rotation, "s": c.Size})
	}
	return out
}

func clanDecorWire(v store.ClanView) []any {
	out := make([]any, 0, len(v.Decor))
	for _, d := range v.Decor {
		out = append(out, d)
	}
	return out
}

func clanTankWire(v store.ClanView) []any {
	out := make([]any, 0, len(v.Tank))
	for _, t := range v.Tank {
		out = append(out, map[string]any{"p": t.Place, "in": t.Item})
	}
	return out
}

func clanBuildingsWire(v store.ClanView) []any {
	out := make([]any, 0, len(v.Buildings))
	for _, b := range v.Buildings {
		points := map[string]any{}
		for id, n := range b.Points {
			points[id] = n
		}
		out = append(out, map[string]any{"i": b.Index, "if": map[string]any{"c": b.Count, "l": b.Level, "bc": b.Built, "pp": points}})
	}
	return out
}

func clanWarMarksWire(v store.ClanView) []any {
	out := make([]any, 0, len(v.WarMarks))
	for _, m := range v.WarMarks {
		out = append(out, m)
	}
	return out
}

func clanRequestsFull(v store.ClanView) ([]any, []any) {
	incoming := make([]any, 0, len(v.Requests))
	for _, r := range v.Requests {
		incoming = append(incoming, map[string]any{"id": r.Player, "message": r.Message})
	}
	outgoing := make([]any, 0, len(v.Invites))
	for _, r := range v.Invites {
		outgoing = append(outgoing, r.Player)
	}
	return incoming, outgoing
}

// clanFull is the root `clan` object parsed by 0x283D050 (join/accept).
func clanFull(v store.ClanView) map[string]any {
	incoming, outgoing := clanRequestsFull(v)
	upgrades := make([]any, 0, len(v.Upgrades))
	for _, up := range v.Upgrades {
		upgrades = append(upgrades, map[string]any{"item_index": up.Item, "from_level": up.From, "finish_at": up.FinishAt})
	}
	return map[string]any{
		"clan_core":               clanCoreFull(v),
		"clan_members":            clanMembersFull(v),
		"clan_incoming_requests":  incoming,
		"clan_outcoming_requests": outgoing,
		"clan_messages":           []any{},
		"clan_items":              clanItemsWire(v),
		"clan_field":              clanFieldWire(v),
		"clan_field_decor":        clanDecorWire(v),
		"clan_tank":               clanTankWire(v),
		"clan_building_levels": map[string]any{
			"field_level": v.FieldLevel, "field_points": v.FieldPoints, "field_energy": v.FieldEnergy,
			"tank_level": v.TankLevel, "tank_points": v.TankPoints, "tank_energy": v.TankEnergy,
		},
		"clan_current_upgrades": map[string]any{"items": upgrades},
		"war_marks":             clanWarMarksWire(v),
		"clan_buildings":        clanBuildingsWire(v),
	}
}

// clanListItem is one entry of a search/info `data` list (0x283D5F4).
func clanListItem(v store.ClanView) map[string]any {
	return map[string]any{
		"clan_core":        clanCoreFull(v),
		"clan_members":     clanMembersFull(v),
		"clan_items":       clanItemsWire(v),
		"clan_field":       clanFieldWire(v),
		"clan_field_decor": clanDecorWire(v),
		"clan_tank":        clanTankWire(v),
	}
}

// Compact sections merged by ParseSections 0x3DB785C.
func clanSectionCore(v store.ClanView, withLogo bool) map[string]any {
	core := map[string]any{
		"id": v.ID, "ty": v.Type, "na": v.Name, "cr": v.Creator, "sw": v.SiegesWon,
		"sp": false, "sh": v.Shields, "dv": v.Division, "sl": v.Slogan, "lh": v.LogoHash,
		"lm": v.LeaderMessage, "cw": v.WarWins, "cn": v.Continent, "mr": v.MinRank,
		"fp": v.FieldPower, "fe": v.FieldEnergy, "tp": v.TankPower, "te": v.TankEnergy,
		"pr": clanPromo(v),
	}
	if withLogo {
		core["lg"] = v.Logo
	}
	return core
}

func clanSectionMembers(v store.ClanView) []any {
	out := make([]any, 0, len(v.MemberViews))
	for _, m := range v.MemberViews {
		out = append(out, map[string]any{
			"id": m.ID, "nc": m.Nick, "rl": m.Role, "dt": m.Details, "ws": m.WarScore,
			"rn": m.Rank, "cr": m.ClanRank, "gu": m.GemsUp, "jt": m.JoinedAt,
			"lo": m.LastOnline - m.LastOnline%60, "re": m.RankExp,
		})
	}
	return out
}

func clanSectionItems(v store.ClanView) map[string]any {
	return map[string]any{"it": clanItemsWire(v)}
}

func clanSectionLevels(v store.ClanView) map[string]any {
	return map[string]any{"tl": v.TankLevel, "tp": v.TankPoints, "fl": v.FieldLevel, "fp": v.FieldPoints}
}

func clanSectionUpgrades(v store.ClanView) map[string]any {
	rows := make([]any, 0, len(v.Upgrades))
	for _, up := range v.Upgrades {
		rows = append(rows, map[string]any{"i": up.Item, "f": up.From, "fl": up.FinishAt})
	}
	return map[string]any{"it": rows}
}

func clanSectionBuildings(v store.ClanView) map[string]any {
	return map[string]any{"delta": false, "bu": clanBuildingsWire(v)}
}

func clanSectionRequests(v store.ClanView) ([]any, []any) {
	incoming := make([]any, 0, len(v.Requests))
	for _, r := range v.Requests {
		incoming = append(incoming, map[string]any{"id": r.Player, "m": r.Message})
	}
	outgoing := make([]any, 0, len(v.Invites))
	for _, r := range v.Invites {
		outgoing = append(outgoing, map[string]any{"id": r.Player})
	}
	return incoming, outgoing
}

func clanServiceWire(m store.ClanServiceMessage) map[string]any {
	return map[string]any{
		"action": m.Action, "target": m.Target, "chest_index": m.ChestIndex,
		"item_index": m.ItemIndex, "details": m.Details, "item_level": m.ItemLevel,
		"power": m.Power, "price_string": m.Price, "currency": m.Currency,
		"info": m.Info, "from": m.From,
	}
}

// clanSectionMessages renders chat after the given stamps (µs). skipFrom
// omits a player's own lines (the native outbox is optimistic and local).
func clanSectionMessages(v store.ClanView, afterMessage, afterService int64, skipFrom string, limit int) (map[string]any, int64, int64) {
	people, service := []any{}, []any{}
	messages := v.Messages
	if limit > 0 && len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	for _, m := range messages {
		if m.Stamp <= afterMessage {
			continue
		}
		afterMessage = m.Stamp
		if skipFrom != "" && m.From == skipFrom {
			continue
		}
		people = append(people, map[string]any{store.ClanStampString(m.Stamp): map[string]any{"from": m.From, "text": m.Text}})
	}
	notices := v.Service
	if limit > 0 && len(notices) > limit {
		notices = notices[len(notices)-limit:]
	}
	for _, m := range notices {
		if m.Stamp <= afterService {
			continue
		}
		afterService = m.Stamp
		service = append(service, map[string]any{store.ClanStampString(m.Stamp): clanServiceWire(m)})
	}
	return map[string]any{"people_messages": people, "service_messages": service}, afterMessage, afterService
}

var clanSectionNames = map[string]bool{"cr": true, "mb": true, "ms": true, "ir": true, "or": true, "it": true, "fl": true, "fd": true, "tn": true, "bl": true, "up": true, "wm": true, "bn": true}

// clanSections renders full (non-delta) sections; private ones only for members.
func clanSections(v store.ClanView, names []string, member bool) map[string]any {
	out := map[string]any{}
	for _, name := range names {
		switch name {
		case "cr":
			out[name] = clanSectionCore(v, true)
		case "mb":
			out[name] = clanSectionMembers(v)
		case "it":
			out[name] = clanSectionItems(v)
		case "fl":
			out[name] = clanFieldWire(v)
		case "fd":
			out[name] = clanDecorWire(v)
		case "tn":
			out[name] = clanTankWire(v)
		case "bl":
			out[name] = clanSectionLevels(v)
		case "up":
			out[name] = clanSectionUpgrades(v)
		case "wm":
			out[name] = clanWarMarksWire(v)
		case "bn":
			out[name] = clanSectionBuildings(v)
		case "ir", "or":
			if member {
				incoming, outgoing := clanSectionRequests(v)
				if name == "ir" {
					out[name] = incoming
				} else {
					out[name] = outgoing
				}
			}
		case "ms":
			if member {
				out[name], _, _ = clanSectionMessages(v, 0, 0, "", 50)
			}
		}
	}
	return out
}

// ---- request handlers ----------------------------------------------------

func (s *Server) clanActor(session *sioSession, req int) (string, map[string]any) {
	actor, ok := s.socketPlayerID(session)
	if !ok {
		return "", socketStateFailure(req, "not_authorized")
	}
	return actor, nil
}

func (s *Server) logClan(event, actor string, req int, err error, fields map[string]any) {
	entry := map[string]any{"event": event, "player_id": actor, "req_id": req}
	for k, v := range fields {
		entry[k] = v
	}
	if err != nil {
		entry["err"] = err.Error()
		s.log.Event("socket.clan_refused", entry)
		return
	}
	s.log.Event("socket.clan_answered", entry)
}

func (s *Server) answerCreateClan(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	kind, okType := clanType(head["type"])
	minRank, okRank := headClanInt(head, "min_rank")
	name, okName := head["name"].(string)
	logo, okLogo := headClanText(head, "logo")
	slogan, okSlogan := headClanText(head, "slogan")
	continent, okContinent := headClanText(head, "continent")
	if !okType || !okRank || !okName || !okLogo || !okSlogan || !okContinent {
		return clanFailure(req, store.ErrClanInvalid)
	}
	in := store.ClanCreate{Name: name, Type: kind}
	if minRank != nil {
		in.MinRank = *minRank
	}
	if logo != nil {
		in.Logo = *logo
	}
	if slogan != nil {
		in.Slogan = *slogan
	}
	if continent != nil {
		in.Continent = *continent
	}
	id, err := s.st.CreateClan(actor, in)
	s.logClan("create_clan_v2", actor, req, err, map[string]any{"clan_id": id})
	if err != nil {
		return clanFailure(req, err)
	}
	body := sioOK(req)
	body["data"] = map[string]any{"clan_id": id}
	body["clan_id"] = id
	return body
}

func (s *Server) answerChangeClanParams(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	var in store.ClanParams
	valid := true
	texts := []struct {
		key string
		dst **string
	}{{"name", &in.Name}, {"slogan", &in.Slogan}, {"logo", &in.Logo}, {"leader_message", &in.LeaderMessage}, {"continent", &in.Continent}}
	for _, t := range texts {
		value, ok := headClanText(head, t.key)
		valid = valid && ok
		*t.dst = value
	}
	var ok bool
	if in.MinRank, ok = headClanInt(head, "min_rank"); !ok {
		valid = false
	}
	if in.Limit, ok = headClanInt(head, "limit"); !ok {
		valid = false
	}
	if raw, present := head["type"]; present && raw != nil {
		kind, ok := clanType(raw)
		valid = valid && ok
		in.Type = &kind
	}
	clanID := headClanID(head, "clan_id")
	if !valid {
		return clanFailure(req, store.ErrClanInvalid)
	}
	if clanID == "" {
		clanID = s.st.PlayerClanID(actor)
	}
	err := s.st.ChangeClanParams(actor, clanID, in)
	s.logClan("change_clan_params", actor, req, err, map[string]any{"clan_id": clanID})
	if err != nil {
		return clanFailure(req, err)
	}
	body := sioOK(req)
	body["clan_id"] = clanID
	if in.Limit != nil {
		body["limit"] = *in.Limit
	}
	return body
}

func clanListBody(req int, views []store.ClanView, withRequest bool) map[string]any {
	data := make([]any, 0, len(views))
	for _, v := range views {
		item := clanListItem(v)
		if withRequest {
			item["has_request"] = 0
			if v.HasRequest {
				item["has_request"] = 1
			}
		}
		data = append(data, item)
	}
	body := sioOK(req)
	body["data"] = data
	return body
}

func (s *Server) answerPossibleClans(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	views, err := s.st.PossibleClans(actor, head.text("continent"))
	if err != nil {
		return clanFailure(req, err)
	}
	return clanListBody(req, views, true)
}

func (s *Server) answerSearchClans(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	views, err := s.st.SearchClans(actor, head.text("param"))
	if err != nil {
		return clanFailure(req, err)
	}
	return clanListBody(req, views, true)
}

func clanIDList(value any, limit int) ([]string, bool) {
	if value == nil {
		return nil, true
	}
	raw, ok := value.([]any)
	if !ok || len(raw) > limit {
		return nil, false
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, v := range raw {
		id := strings.TrimSpace(sioHead{"v": v}.text("v"))
		if id == "" || id == "0" || id == "-1" {
			continue
		}
		if !store.ValidClanID(id) {
			return nil, false
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, true
}

func (s *Server) answerClanInfoV2(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	ids, ok := clanIDList(head["clan_ids"], 100)
	if !ok {
		return clanFailure(req, store.ErrClanInvalid)
	}
	if id := headClanID(head, "clan_id"); id != "" && !hasSocialID(ids, id) {
		ids = append(ids, id)
	}
	views, err := s.st.ClanInfos(actor, ids)
	if err != nil {
		return clanFailure(req, err)
	}
	return clanListBody(req, views, true)
}

func clanParamNames(value any) ([]string, bool) {
	all := []string{"cr", "mb", "ms", "ir", "or", "it", "fl", "fd", "tn", "bl", "up", "wm", "bn"}
	if value == nil {
		return all, true
	}
	raw, ok := value.([]any)
	if !ok || len(raw) > 32 {
		return nil, false
	}
	names := []string{}
	for _, v := range raw {
		name, ok := v.(string)
		if !ok {
			return nil, false
		}
		if clanSectionNames[name] {
			names = append(names, name)
		}
	}
	return names, true
}

func (s *Server) answerClanData(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := headClanID(head, "clan_id")
	if clanID == "" {
		clanID = s.st.PlayerClanID(actor)
	}
	names, ok := clanParamNames(head["params"])
	if !ok || clanID == "" {
		return clanFailure(req, store.ErrClanInvalid)
	}
	view, found := s.st.ClanByID(clanID, actor)
	if !found {
		return clanFailure(req, store.ErrClanNotFound)
	}
	body := sioOK(req)
	for k, v := range clanSections(view, names, view.Role(actor) != store.ClanRoleNone) {
		body[k] = v
	}
	body["clan_id"] = clanID
	return body
}

func (s *Server) clanJoinedBody(req int, actor string) map[string]any {
	body := sioOK(req)
	if view, ok := s.st.PlayerClan(actor); ok {
		body["clan"] = clanFull(view)
		body["clan_id"] = view.ID
	}
	return body
}

func (s *Server) answerJoinClan(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := headClanID(head, "clan_id")
	err := s.st.JoinClan(actor, clanID)
	s.logClan("join_clan", actor, req, err, map[string]any{"clan_id": clanID})
	if err != nil {
		return clanFailure(req, err)
	}
	return s.clanJoinedBody(req, actor)
}

func clanInviteAnswer(accept bool) sioAnswer {
	return func(s *Server, session *sioSession, head sioHead, req int) map[string]any {
		actor, denied := s.clanActor(session, req)
		if denied != nil {
			return denied
		}
		clanID := headClanID(head, "clan_id")
		err := s.st.AnswerClanInvite(actor, clanID, accept)
		s.logClan("answer_clan_invite", actor, req, err, map[string]any{"clan_id": clanID, "accept": accept})
		if err != nil {
			if !accept && errors.Is(err, store.ErrClanInvalid) {
				// Rejecting an invitation that already expired is a no-op.
				body := sioOK(req)
				body["clan_id"] = clanID
				return body
			}
			return clanFailure(req, err)
		}
		if accept {
			return s.clanJoinedBody(req, actor)
		}
		body := sioOK(req)
		body["clan_id"] = clanID
		return body
	}
}

func (s *Server) answerAskToJoinClan(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := headClanID(head, "clan_id")
	text, _ := head["text"].(string)
	err := s.st.AskToJoinClan(actor, clanID, text)
	s.logClan("ask_to_join_clan", actor, req, err, map[string]any{"clan_id": clanID})
	if err != nil {
		body := clanFailure(req, err)
		var cooldown *store.ClanCooldownError
		if !errors.As(err, &cooldown) && !errors.Is(err, store.ErrClanRate) {
			// 1740: the clan refused the request; the client forgets it.
			body["err_code"] = clanErrRefused
		}
		return body
	}
	body := sioOK(req)
	body["clan_id"] = clanID
	return body
}

func clanRequestAnswer(accept bool) sioAnswer {
	return func(s *Server, session *sioSession, head sioHead, req int) map[string]any {
		actor, denied := s.clanActor(session, req)
		if denied != nil {
			return denied
		}
		clanID := headClanID(head, "clan_id")
		if clanID == "" {
			clanID = s.st.PlayerClanID(actor)
		}
		receiver := headPlayerID(head, "receiver_id")
		err := s.st.AnswerClanRequest(actor, clanID, receiver, accept)
		s.logClan("answer_clan_request", actor, req, err, map[string]any{"clan_id": clanID, "receiver_id": receiver, "accept": accept})
		var body map[string]any
		if err != nil {
			body = clanFailure(req, err)
		} else {
			body = sioOK(req)
		}
		body["receiver_id"] = receiver
		body["clan_id"] = clanID
		return body
	}
}

func (s *Server) answerRejectAllClanRequests(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := headClanID(head, "clan_id")
	if clanID == "" {
		clanID = s.st.PlayerClanID(actor)
	}
	err := s.st.RejectAllClanRequests(actor, clanID)
	s.logClan("reject_request_to_join_clan_all", actor, req, err, map[string]any{"clan_id": clanID})
	if err != nil {
		return clanFailure(req, err)
	}
	body := sioOK(req)
	body["clan_id"] = clanID
	return body
}

func (s *Server) answerInviteToClan(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := headClanID(head, "clan_id")
	if clanID == "" {
		clanID = s.st.PlayerClanID(actor)
	}
	receiver := headPlayerID(head, "receiver_id")
	err := s.st.InviteToClan(actor, clanID, receiver)
	s.logClan("invite_to_clan", actor, req, err, map[string]any{"clan_id": clanID, "receiver_id": receiver})
	var body map[string]any
	if err != nil {
		body = clanFailure(req, err)
	} else {
		body = sioOK(req)
	}
	body["receiver_id"] = receiver
	return body
}

func (s *Server) answerChangeClanRole(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := headClanID(head, "clan_id")
	if clanID == "" {
		clanID = s.st.PlayerClanID(actor)
	}
	player := headPlayerID(head, "player_id")
	role, ok := clanRole(head["role_id"])
	var err error
	if !ok {
		err = store.ErrClanInvalid
	} else {
		err = s.st.ChangeClanRole(actor, clanID, player, role)
	}
	s.logClan("change_clan_role", actor, req, err, map[string]any{"clan_id": clanID, "target": player, "role": role})
	var body map[string]any
	if err != nil {
		body = clanFailure(req, err)
	} else {
		body = sioOK(req)
	}
	body["player_id"] = player
	body["role_id"] = role
	return body
}

func (s *Server) answerKickFromClan(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := headClanID(head, "clan_id")
	if clanID == "" {
		clanID = s.st.PlayerClanID(actor)
	}
	player := headPlayerID(head, "player_id")
	err := s.st.KickFromClan(actor, clanID, player)
	s.logClan("kick_from_clan", actor, req, err, map[string]any{"clan_id": clanID, "target": player})
	var body map[string]any
	if err != nil {
		body = clanFailure(req, err)
	} else {
		body = sioOK(req)
	}
	body["player_id"] = player
	return body
}

func (s *Server) answerLeaveClan(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := headClanID(head, "clan_id")
	if clanID == "" {
		clanID = s.st.PlayerClanID(actor)
	}
	err := s.st.LeaveClan(actor, clanID)
	s.logClan("leave_clan", actor, req, err, map[string]any{"clan_id": clanID})
	if err != nil {
		return clanFailure(req, err)
	}
	body := sioOK(req)
	body["clan_id"] = clanID
	return body
}

func (s *Server) answerDeleteClan(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := headClanID(head, "clan_id")
	if clanID == "" {
		clanID = s.st.PlayerClanID(actor)
	}
	err := s.st.DeleteClan(actor, clanID)
	s.logClan("delete_clan", actor, req, err, map[string]any{"clan_id": clanID})
	if err != nil {
		return clanFailure(req, err)
	}
	body := sioOK(req)
	body["clan_id"] = clanID
	body["player_id"] = actor
	return body
}

// get_clan_status reader 0x294DD50: err_code, then clan_status and
// clan_war_status (1 = available) and poll intervals clamped by the client to
// 120 s. Note the native spelling clan_war_timout.
func (s *Server) answerClanStatus(session *sioSession, _ sioHead, req int) map[string]any {
	if _, denied := s.clanActor(session, req); denied != nil {
		return denied
	}
	body := sioOK(req)
	body["err_code"] = sioErrNone
	body["clan_status"] = 1
	body["clan_war_status"] = 1
	body["clan_timeout"] = 60
	body["clan_war_timout"] = 60
	body["clan_war_timeout"] = 60
	return body
}

func (s *Server) answerSendClanMessage(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	text, ok := head["text"].(string)
	stamp, valid := socialNumber(head["timeStamp"])
	if !ok || !valid {
		return clanFailure(req, store.ErrClanInvalid)
	}
	if from := head.text("from"); from != "" {
		if resolved, _ := s.st.ResolvePlayerID(from); resolved != actor {
			return clanFailure(req, store.ErrClanForbidden)
		}
	}
	token := head.text("timeStamp")
	if _, isString := head["timeStamp"].(string); !isString {
		token = strconv.FormatFloat(stamp, 'f', -1, 64)
	}
	m, clanID, err := s.st.SendClanMessage(actor, text, token)
	s.logClan("send_clan_message", actor, req, err, map[string]any{"clan_id": clanID})
	if err != nil {
		return clanFailure(req, err)
	}
	body := sioOK(req)
	body["timestamp"] = store.ClanStampString(m.Stamp)
	body["client_timestamp"] = token
	return body
}

func (s *Server) answerClanChatEvent(session *sioSession, _ sioHead, req int) map[string]any {
	if _, denied := s.clanActor(session, req); denied != nil {
		return denied
	}
	return sioOK(req)
}

func clanFieldCells(value any) ([]store.ClanFieldCell, bool) {
	raw, ok := value.([]any)
	if value == nil {
		return []store.ClanFieldCell{}, true
	}
	if !ok || len(raw) > store.ClanFieldCellLimit {
		return nil, false
	}
	cells := make([]store.ClanFieldCell, 0, len(raw))
	for _, entry := range raw {
		cell, ok := entry.(map[string]any)
		if !ok {
			return nil, false
		}
		pos, ok := cell["p"].(map[string]any)
		if !ok {
			return nil, false
		}
		x, okX := clanFloat(pos["x"])
		y, okY := clanFloat(pos["y"])
		z, okZ := clanFloat(pos["z"])
		size, okS := clanFieldSize(cell["s"])
		rotation, okR := 0, true
		if cell["r"] != nil {
			rotation, okR = clanInt(cell["r"])
		}
		item, okI := 0, true
		if cell["i"] != nil {
			item, okI = clanInt(cell["i"])
		}
		if !okX || !okY || !okZ || !okS || !okR || !okI {
			return nil, false
		}
		cells = append(cells, store.ClanFieldCell{X: x, Y: y, Z: z, Item: item, Rotation: rotation, Size: size})
	}
	return cells, true
}

// clanFieldSize accepts SquareFieldCellSize as a number or enum name.
func clanFieldSize(value any) (int, bool) {
	if value == nil {
		return 1, true
	}
	if text, ok := value.(string); ok {
		names := map[string]int{"Nullable": 0, "_1x1": 1, "_2x2": 2, "_2x1": 21, "_3x3": 3, "_3x1": 31, "_3x2": 32, "_4x4": 4, "_4x1": 41, "_4x2": 42, "_4x3": 43}
		if n, ok := names[strings.TrimSpace(text)]; ok {
			return n, true
		}
	}
	return clanInt(value)
}

func clanPower(head sioHead) (int, bool) {
	if head["power"] == nil {
		return 0, true
	}
	f, ok := clanFloat(head["power"])
	if !ok || f < 0 || f > 1e9 {
		return 0, false
	}
	return int(math.Round(f)), true
}

func (s *Server) clanEditBody(event, actor, clanID string, req int, err error) map[string]any {
	s.logClan(event, actor, req, err, map[string]any{"clan_id": clanID})
	if err != nil {
		return clanFailure(req, err)
	}
	body := sioOK(req)
	body["clan_id"] = clanID
	return body
}

func (s *Server) editClanID(head sioHead, actor string) string {
	if id := headClanID(head, "clan_id"); id != "" {
		return id
	}
	return s.st.PlayerClanID(actor)
}

func (s *Server) answerUpdateClanField(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := s.editClanID(head, actor)
	cells, okCells := clanFieldCells(head["field_cells"])
	power, okPower := clanPower(head)
	var err error = store.ErrClanInvalid
	if okCells && okPower {
		err = s.st.UpdateClanField(actor, clanID, cells, power)
	}
	return s.clanEditBody("update_clan_field", actor, clanID, req, err)
}

func clanIntList(value any, limit int) ([]int, bool) {
	if value == nil {
		return []int{}, true
	}
	raw, ok := value.([]any)
	if !ok || len(raw) > limit {
		return nil, false
	}
	out := make([]int, 0, len(raw))
	for _, v := range raw {
		n, ok := clanInt(v)
		if !ok {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func (s *Server) answerUpdateClanDecor(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := s.editClanID(head, actor)
	decor, ok := clanIntList(head["field_decor"], store.ClanDecorLimit)
	var err error = store.ErrClanInvalid
	if ok {
		err = s.st.UpdateClanDecor(actor, clanID, decor)
	}
	return s.clanEditBody("update_clan_field_decor", actor, clanID, req, err)
}

func clanTankSlots(value any) ([]store.ClanTankSlot, bool) {
	if value == nil {
		return []store.ClanTankSlot{}, true
	}
	raw, ok := value.([]any)
	if !ok || len(raw) > store.ClanTankSlotLimit {
		return nil, false
	}
	slots := make([]store.ClanTankSlot, 0, len(raw))
	for _, entry := range raw {
		slot, ok := entry.(map[string]any)
		if !ok {
			return nil, false
		}
		place, okP := clanInt(slot["p"])
		item, okI := clanInt(slot["in"])
		if !okP || !okI {
			return nil, false
		}
		slots = append(slots, store.ClanTankSlot{Place: place, Item: item})
	}
	return slots, true
}

func (s *Server) answerUpdateClanTank(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := s.editClanID(head, actor)
	slots, okSlots := clanTankSlots(head["clan_tank"])
	power, okPower := clanPower(head)
	var err error = store.ErrClanInvalid
	if okSlots && okPower {
		err = s.st.UpdateClanTank(actor, clanID, slots, power)
	}
	return s.clanEditBody("update_clan_tank", actor, clanID, req, err)
}

func (s *Server) answerSetClanBuildingLevels(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := s.editClanID(head, actor)
	var in store.ClanBuildingLevels
	valid := true
	for key, dst := range map[string]**int{"field_level": &in.FieldLevel, "field_points": &in.FieldPoints, "tank_level": &in.TankLevel, "tank_points": &in.TankPoints} {
		value, ok := headClanInt(head, key)
		valid = valid && ok
		*dst = value
	}
	var err error = store.ErrClanInvalid
	if valid {
		err = s.st.SetClanBuildingLevels(actor, clanID, in)
	}
	return s.clanEditBody("set_clan_building_levels", actor, clanID, req, err)
}

func (s *Server) answerAddUpgradingItem(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := s.editClanID(head, actor)
	item, okI := clanInt(head["item_index"])
	from, okF := clanInt(head["from_level"])
	to, okT := clanInt(head["to_level"])
	finish, okA := clanFloat(head["finish_at"])
	kind, okK := 0, true
	if head["item_type"] != nil {
		kind, okK = clanInt(head["item_type"])
	}
	var err error = store.ErrClanInvalid
	if okI && okF && okT && okA && okK {
		err = s.st.AddClanUpgrade(actor, clanID, store.ClanUpgrade{Item: item, From: from, To: to, FinishAt: int64(finish), Type: kind})
	}
	return s.clanEditBody("add_upgrading_item", actor, clanID, req, err)
}

func (s *Server) answerFinishUpgradingItem(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := s.editClanID(head, actor)
	item, okI := clanInt(head["item_index"])
	from, okF := clanInt(head["from_level"])
	to, okT := clanInt(head["to_level"])
	price := 0.0
	okP := true
	if head["price"] != nil {
		price, okP = clanFloat(head["price"])
	}
	level := 0
	var err error = store.ErrClanInvalid
	if okI && okF && okT && okP {
		level, err = s.st.FinishClanUpgrade(actor, clanID, item, from, to, price > 0)
	}
	body := s.clanEditBody("finish_upgrading_item", actor, clanID, req, err)
	if data, ok := head["upgrade_data"].(map[string]any); ok {
		body["upgrade_data"] = data
	}
	if err == nil {
		body["current_level"] = level
	}
	return body
}

func (s *Server) answerCreateClanBuilding(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := s.editClanID(head, actor)
	building, okB := clanInt(head["building_id"])
	level, okU := 0, true
	if head["up"] != nil {
		level, okU = clanInt(head["up"])
	}
	var err error = store.ErrClanInvalid
	if okB && okU {
		err = s.st.CreateClanBuilding(actor, clanID, building, level)
	}
	body := s.clanEditBody("create_clan_building", actor, clanID, req, err)
	body["building_id"] = building
	return body
}

func (s *Server) answerUpdateClanWarMark(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	marks, ok := clanIntList(head["unset"], store.ClanWarMarkLimit)
	var err error = store.ErrClanInvalid
	if ok {
		err = s.st.UnsetClanWarMarks(actor, marks)
	}
	return s.clanEditBody("update_clan_war_mark", actor, s.st.PlayerClanID(actor), req, err)
}

// ClanPromoDuration is how long one activate_clan_promo advertises a clan in
// get_possible_clans.
const ClanPromoDuration = 24 * time.Hour

func (s *Server) answerActivateClanPromo(session *sioSession, head sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	clanID := s.editClanID(head, actor)
	until, err := s.st.ActivateClanPromo(actor, clanID, ClanPromoDuration)
	s.logClan("activate_clan_promo", actor, req, err, map[string]any{"clan_id": clanID, "until": until})
	if err != nil {
		body := clanFailure(req, err)
		if errors.Is(err, store.ErrClanPending) {
			body["err_code"] = clanErrPromo
		}
		return body
	}
	body := sioOK(req)
	body["player_id"] = actor
	body["clan_id"] = clanID
	body["pr"] = until
	return body
}

// answerLeaderClan feeds the lobby ClanLeaderBunner (0x27A5D5C): {id,name,logo}
// of the leading clan. Without a clan war winner the reply carries no id,
// which the client logs and ignores.
func (s *Server) answerLeaderClan(_ *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body["clan_id"] = head.text("clan_id")
	body["leader_id"] = "0"
	body["leader"] = map[string]any{}
	if leader, ok := s.st.LeadingClan(); ok {
		body["id"] = leader.ID
		body["name"] = leader.Name
		body["logo"] = leader.Logo
		body["leader_id"] = leader.ID
	}
	return body
}

// ---- update_progress clan commands ----------------------------------------

// clanProgressCommands extracts CreateClanItems (182) and
// ClanBuildingsAddItems (206) from an update_progress batch.
func clanProgressCommands(head sioHead) []store.ClanProgressCommand {
	var source []any
	if payload, ok := head["p"].(map[string]any); ok {
		source, _ = payload["c"].([]any)
	}
	if source == nil {
		source = []any{map[string]any(head)}
	}
	out := []store.ClanProgressCommand{}
	for _, raw := range source {
		command, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := clanInt(command["id"])
		if id != 182 && id != 206 {
			continue
		}
		data, _ := command["p"].(map[string]any)
		items, _ := data["items"].([]any)
		cmd := store.ClanProgressCommand{Receipt: sioHead(command).text("ci")}
		if cmd.Receipt == "" {
			cmd.Receipt = head.text("i")
		}
		if cmd.Receipt != "" {
			cmd.Receipt = strconv.Itoa(id) + ":" + cmd.Receipt
		}
		for _, rawItem := range items {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			get := func(key string) int {
				n, _ := clanInt(item[key])
				return n
			}
			if id == 182 {
				buy, _ := item["buy"].(bool)
				if !buy {
					buy = get("buy") == 1
				}
				cmd.Items = append(cmd.Items, store.ClanItemGrant{
					UniqueID: sioHead(item).text("id"), Clan: strings.TrimSpace(sioHead(item).text("clan")),
					Index: get("item_index"), Count: get("count"), Level: get("level"), Buy: buy, FromChest: get("from_chest"),
				})
			} else {
				cmd.Buildings = append(cmd.Buildings, store.ClanBuildingGrant{
					Clan: strings.TrimSpace(sioHead(item).text("ci")), Index: get("i"), Count: get("c"),
					Spent: get("sp"), Upgrade: get("up"), Multiplier: get("mu"),
				})
			}
		}
		if len(cmd.Items)+len(cmd.Buildings) > 0 {
			out = append(out, cmd)
		}
	}
	return out
}

// applyClanProgress shares crafted clan items with the player's clan. It
// never fails the player's own update_progress acknowledgement.
func (s *Server) applyClanProgress(playerID string, head sioHead) {
	commands := clanProgressCommands(head)
	if len(commands) == 0 {
		return
	}
	applied, err := s.st.ApplyClanProgress(playerID, commands)
	fields := map[string]any{"player_id": playerID, "commands": len(commands), "applied": applied}
	if err != nil {
		fields["err"] = err.Error()
		s.log.Event("socket.clan_progress_refused", fields)
		return
	}
	s.log.Event("socket.clan_progress_applied", fields)
	if applied > 0 {
		s.refreshClansAll()
	}
}

// clanPlayerInfo adds the PlayerData clan badge (cn/cl/clh/ic/cci/crl/crn).
func (s *Server) clanPlayerInfo(playerID string, clientState map[string]any, info map[string]any) {
	if rank, ok := clanInt(clientState["clan_rank"]); ok && rank >= 0 {
		info["crn"] = rank
	}
	summary, ok := s.st.PlayerClanSummary(playerID)
	if !ok {
		return
	}
	info["cn"] = summary.Name
	if summary.Logo != "" {
		info["cl"] = summary.Logo
		info["clh"] = summary.LogoHash
	}
	if id, err := strconv.Atoi(summary.ID); err == nil {
		info["ic"] = id
	}
	if creator, err := strconv.Atoi(summary.Creator); err == nil {
		info["cci"] = creator
	}
	info["crl"] = summary.Role
}

// sortedClanIDs is a deterministic helper for pushes keyed by clan id.
func sortedClanIDs(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}
