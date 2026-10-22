// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"strings"
	"time"
)

// playerInfoFields is the public subset of update_player. Device, socket and
// advertising identifiers are intentionally not included: get_info_by_id is a
// player lookup, not an account/session introspection endpoint.
var playerInfoFields = []string{
	"nick", "skin", "rank", "clan_rank", "tier", "rating", "league_id",
	"weapons_c", "gadgets_c", "avatars_c", "pets_c", "sets", "country",
	"continent", "lobby", "is_season_lobby", "paying", "kill_cnt",
	"death_cnt", "kill_cnt_month", "death_cnt_month", "mode", "accessories",
	"scores", "accuracy",
}

func (s *Server) socketPlayerID(session *sioSession) (string, bool) {
	if session == nil {
		return "", false
	}
	if _, banned := s.socketBan(session); banned {
		return "", false
	}
	_, _, playerID := session.identity()
	if strings.TrimSpace(playerID) == "" {
		return "", false
	}
	return s.st.ResolvePlayerID(playerID)
}

func socketStateFailure(reqID int, result string) map[string]any {
	return map[string]any{
		sioRequestIDKey: reqID,
		"result":        result,
		"error":         1,
		"err_code":      1,
	}
}

func (s *Server) persistSocketClientState(session *sioSession, reqID int, event string, values map[string]any) map[string]any {
	playerID, ok := s.socketPlayerID(session)
	if !ok {
		s.log.Event("socket.state_rejected", map[string]any{
			"event": event, "reason": "unauthenticated", "req_id": reqID,
		})
		body := sioDenied(reqID)
		body["err_code"] = sioErrNotAuthorized
		return body
	}
	if _, err := s.st.MergeProfileClientState(playerID, values, session.progressWriteContext()); err != nil {
		s.log.Event("socket.state_store_failed", map[string]any{
			"event": event, "player_id": playerID, "req_id": reqID, "err": err.Error(),
		})
		return socketStateFailure(reqID, "store_failed")
	}
	s.log.Event("socket.state_saved", map[string]any{
		"event": event, "player_id": playerID, "req_id": reqID,
	})
	return sioOK(reqID)
}

func socketEventFields(head sioHead) map[string]any {
	values := make(map[string]any, len(head))
	for key, value := range head {
		switch key {
		case "sid", sioRequestIDKey:
			continue
		default:
			values[key] = value
		}
	}
	return values
}

func (s *Server) answerUpdatePlayer(session *sioSession, head sioHead, reqID int) map[string]any {
	values := socketEventFields(head)
	return s.persistSocketClientState(session, reqID, "update_player", values)
}

func (s *Server) answerUpdateAccessories(session *sioSession, head sioHead, reqID int) map[string]any {
	return s.persistSocketClientState(session, reqID, "update_accessories", map[string]any{
		"accessories": head["accessories"],
	})
}

func (s *Server) answerUpdateLoadout(session *sioSession, head sioHead, reqID int) map[string]any {
	return s.persistSocketClientState(session, reqID, "update_loadout", map[string]any{
		"w": head["w"],
	})
}

func (s *Server) answerUpdateTargetAdConsent(session *sioSession, head sioHead, reqID int) map[string]any {
	return s.persistSocketClientState(session, reqID, "update_character_target_ad_consent", map[string]any{
		"target_ad": head["target_ad"],
	})
}

func (s *Server) answerGetInfoByID(_ *sioSession, head sioHead, reqID int) map[string]any {
	requestedID := strings.TrimSpace(head.text("id"))
	playerID, ok := s.st.ResolvePlayerID(requestedID)
	if !ok {
		s.log.Event("socket.player_info_not_found", map[string]any{
			"requested_id": requestedID, "req_id": reqID,
		})
		return socketStateFailure(reqID, "not_found")
	}
	profile, err := s.st.Profile(playerID)
	if err != nil {
		s.log.Event("socket.player_info_failed", map[string]any{
			"player_id": playerID, "req_id": reqID, "err": err.Error(),
		})
		return socketStateFailure(reqID, "not_found")
	}
	account, err := s.st.Account(playerID)
	if err != nil {
		return socketStateFailure(reqID, "not_found")
	}

	info := make(map[string]any, len(playerInfoFields)+8)
	for _, key := range playerInfoFields {
		if value, exists := profile.ClientState[key]; exists {
			info[key] = value
		}
	}
	info["id"] = playerID
	info["player_id"] = playerID
	info["nick"] = profile.Nick
	if strings.TrimSpace(profile.Nick) == "" {
		info["nick"] = account.Nick
	}
	info["level"] = profile.Level
	info["experience"] = profile.Experience
	info["exp"] = profile.Experience
	info["coins"] = profile.Coins
	info["gems"] = profile.Gems

	body := sioOK(reqID)
	body["info"] = info
	s.log.Event("socket.player_info_answered", map[string]any{
		"requested_id": requestedID, "player_id": playerID, "req_id": reqID,
	})
	return body
}

func (s *Server) answerRefundGet(_ *sioSession, _ sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body["transaction_list"] = []any{}
	body["refunds"] = []any{}
	body["refunds_friend"] = []any{}
	body["refunds_clan"] = []any{}
	body["users"] = map[string]any{}
	body["gifts"] = []any{}
	return body
}

func (s *Server) answerGetTime(_ *sioSession, _ sioHead, reqID int) map[string]any {
	now := time.Now().UTC().Unix()
	body := sioOK(reqID)
	body["time"] = now
	body["timestamp"] = now
	return body
}

func (s *Server) answerCheckBindedID(_ *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body["social_id"] = head.text("social_id")
	body["social_name"] = head.text("social_name")
	body["binded"] = 0
	body["is_binded"] = 0
	body["allow_bind"] = 1
	return body
}

func (s *Server) answerLeaderClan(_ *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body["clan_id"] = head.text("clan_id")
	body["leader_id"] = "0"
	body["leader"] = map[string]any{}
	return body
}

func (s *Server) answerUnsubscribe(_ *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	if value, ok := head["type"]; ok {
		body["type"] = value
	}
	if value, ok := head["list"]; ok {
		body["list"] = value
	}
	return body
}
