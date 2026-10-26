// SPDX-License-Identifier: GPL-3.0-or-later
package api

// Clan wars are not run by this server, and say so explicitly.
//
// Stock 23.1.3 has no war season to restore: ClanWar_0/_1 (ConfigId 67/71)
// and ClanWarV2Even/Odd (165/166) are empty on the client, and no original
// map, event, raid, tribute or division reward table was recovered. Running
// a war would mean inventing all of it and matching clans against each
// other, so instead every war request gets one consistent answer: there is
// no season, nobody is registered and nothing can be scored.
//
// The common-data reader (且丏丙丘专丆业业丕 0x3D2ED1C, "OnGetClanWarCommonData")
// reads enable/war/reg/seed/start/end/pad/room_id/season_id/clans through
// defaulting getters and enters its reward branch only when `reward` is
// present, so the info answer states the empty season and omits `reward`.
// Successes omit err_code, whose mere presence the client's reply handlers
// treat as failure. Reads that only fetch state (tribute, war log) succeed
// empty; writes (registration, event start/progress/finish, map progress,
// reward clears) are refused so a client can never believe it joined or
// scored a war that does not exist. docs/CLANS_23_1_3.md records what a real
// season would need.

const clanWarUnavailable = "clan_war_unavailable"

// clanWarAnswerFor routes the V1 and V2 clan war requests.
func clanWarAnswerFor(event string) sioAnswer {
	switch event {
	case "get_info_clan_war":
		return clanWarInfoAnswer(false)
	case "get_info_clan_warV2":
		return clanWarInfoAnswer(true)
	case "get_tribute", "get_auto_tribute", "log_war":
		return (*Server).answerClanWarRead
	case "register_clan_war", "register_clan_warV2", "start_event", "warV2_event_start",
		"update_progress_fort_raid", "finish_event_fort_raid", "update_progress_monster_siege",
		"update_war_progress_v2", "clear_war_reward", "clear_warV2_reward":
		return (*Server).answerClanWarClosed
	}
	return nil
}

// clanWarInfoAnswer reports the empty season for either war version.
func clanWarInfoAnswer(v2 bool) sioAnswer {
	return func(s *Server, session *sioSession, head sioHead, req int) map[string]any {
		actor, denied := s.clanActor(session, req)
		if denied != nil {
			return denied
		}
		body := sioOK(req)
		if v2 {
			body["enable"] = false
		}
		body["war"] = 0
		body["reg"] = false
		body["seed"] = 0
		body["start"] = 0
		body["end"] = 0
		body["pad"] = 0
		body["room_id"] = 0
		body["season_id"] = 0
		body["clans"] = []any{}
		s.logClan("clan_war_info", actor, req, nil, map[string]any{"v2": v2, "clan_id": headClanID(head, "clan_id")})
		return body
	}
}

// answerClanWarRead acknowledges a state read with nothing to report.
func (s *Server) answerClanWarRead(session *sioSession, _ sioHead, req int) map[string]any {
	if _, denied := s.clanActor(session, req); denied != nil {
		return denied
	}
	return sioOK(req)
}

// answerClanWarClosed refuses a war write: there is no season to change.
func (s *Server) answerClanWarClosed(session *sioSession, _ sioHead, req int) map[string]any {
	actor, denied := s.clanActor(session, req)
	if denied != nil {
		return denied
	}
	s.logClan("clan_war_closed", actor, req, nil, nil)
	body := map[string]any{sioRequestIDKey: req, "error": 1}
	body["err_code"] = clanErrGeneric
	body["result"] = clanWarUnavailable
	return body
}
