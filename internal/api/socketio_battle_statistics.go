// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
)

// 23.1.3 registers 0x2501514 for info_by_id_v3, separately from the
// subscribe acknowledgement. Its argument is {id:playerID,info:PlayerData}.
// 0x2500D28 is the DIFFERENT bulk all_short_info_by_id_v3 callback.
// Presence of err_code (even zero) aborts either native callback.
func (s *Server) nativePlayerInfo(playerID string) (map[string]any, bool) {
	id, ok := s.st.ResolvePlayerID(strings.TrimSpace(playerID))
	if !ok {
		return nil, false
	}
	p, err := s.st.Profile(id)
	if err != nil {
		return nil, false
	}
	info := p.NativePlayerStatistics(time.Now().UTC())
	info["n"], info["lv"] = p.Nick, p.Level
	// The stock LastOnlineTime field is nullable Int32 Unix seconds, not ms.
	// HTTP/web authentication does not advance game-specific activity.
	if account, err := s.st.Account(id); err == nil {
		last := account.GameLastSeenAt
		if last.IsZero() {
			last = account.LastSeenAt
		} // legacy records only
		if stamp := last.Unix(); !last.IsZero() && stamp > 0 && stamp <= 2147483647 {
			info["ldt"] = stamp
		}
	}
	// PlayerData.s is base64 image data; a is the built-in avatar resource.
	// Passing avatar_phoenix_warrior as s throws in 0x4024CD4 before nickname
	// and statistics render. v is independently required by the league view.
	info["s"], info["a"], info["sh"] = nativeAppearance(p.ClientState)
	info["v"] = "23.1.3" // this backend supports stock 23.1.3; old profiles have no version
	if version, ok := p.ClientState["version"].(string); ok && nativeVersionPattern.MatchString(version) {
		info["v"] = version
	}
	for src, dst := range map[string]string{"weapons_c": "wc", "gadgets_c": "gc", "pets_c": "pc", "avatars_c": "avc", "sets": "sc", "league_id": "li", "rating": "c", "is_season_lobby": "isl"} {
		if v, ok := p.ClientState[src]; ok {
			info[dst] = v
		}
	}
	// update_player sends these as inline JSON; PlayerData expects composites.
	var lobby []any
	switch v := p.ClientState["lobby"].(type) {
	case []any:
		lobby = v
	case string:
		_ = json.Unmarshal([]byte(v), &lobby)
	}
	if lobby != nil {
		info["l"] = lobby
	}
	scores := map[string]any{}
	var scoreRows []map[string]any
	if raw, ok := p.ClientState["scores"].(string); ok {
		_ = json.Unmarshal([]byte(raw), &scoreRows)
	}
	for _, row := range scoreRows {
		game, ok := row["game"].(string)
		value, valid := row["max_score"].(string)
		if !ok || !valid {
			continue
		}
		n, err := strconv.ParseInt(value, 10, 32)
		if err == nil {
			scores[game] = n
		}
	}
	if len(scores) > 0 {
		info["scs"] = scores
	}
	loadout, _ := p.ClientState["w"].(map[string]any)
	for key, value := range loadout {
		switch key {
		case "pw", "bw", "mw", "spw", "snw", "prw", "pt":
			info[key] = value
		}
	}
	// Accessory reports can contain inline JSON text; PlayerData.asc is a list.
	var accessories []any
	switch v := p.ClientState["accessories"].(type) {
	case []any:
		accessories = v
	case string:
		_ = json.Unmarshal([]byte(v), &accessories)
	}
	if accessories != nil {
		info["asc"] = accessories
	}
	s.clanPlayerInfo(id, p.ClientState, info)
	return info, true
}
func (s *Server) answerNativePlayerInfo(session *sioSession, head sioHead, reqID int) map[string]any {
	if _, ok := s.socketPlayerID(session); !ok {
		return socketStateFailure(reqID, "not_authorized")
	}
	id := head.text("id")
	if id == "" {
		if list, ok := head["list_v2"].([]any); ok && len(list) > 0 {
			if len(list) > 100 {
				return socketStateFailure(reqID, "too_many_players")
			}
			if entry, ok := list[0].(map[string]any); ok {
				id, _ = entry["id"].(string)
			}
		}
	}
	info, ok := s.nativePlayerInfo(id)
	if !ok {
		return socketStateFailure(reqID, "not_found")
	}
	body := sioOK(reqID)
	body["id"] = id
	body["info"] = info
	return body
}
func (s *Server) pushSubscribedPlayerInfo(session *sioSession, namespace, transport string, codec *sio.Codec, head sioHead, reqID int) {
	if head.text("type") != "info_by_id_v3" {
		return
	}
	list, ok := head["list_v2"].([]any)
	if !ok || len(list) > 100 {
		return
	}
	for _, raw := range list {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, ok := entry["id"].(string)
		if !ok {
			continue
		}
		body := s.answerNativePlayerInfo(session, sioHead{"id": id}, reqID)
		if _, failed := body["err_code"]; failed {
			continue
		}
		// An unsolicited subscription update has no pending-request identifier.
		delete(body, sioRequestIDKey)
		if !s.replyEncrypted(session, namespace, transport, codec, "info_by_id_v3", 0, body) {
			return
		}
	}
}
func (s *Server) answerUpdatePlayerModesStat(session *sioSession, head sioHead, reqID int) map[string]any {
	playerID, ok := s.socketPlayerID(session)
	if !ok {
		return socketStateFailure(reqID, "not_authorized")
	}
	mods, ok := head["mods"].(map[string]any)
	if !ok {
		return socketStateFailure(reqID, "invalid_modes")
	}
	if err := s.st.MergeBattleModes(playerID, mods, session.progressWriteContext()); err != nil {
		s.log.Event("socket.battle_modes_failed", map[string]any{"player_id": playerID, "err": err.Error()})
		return socketStateFailure(reqID, "store_failed")
	}
	body := sioOK(reqID)
	body["status"] = "ok"
	return body
}

// Kept here to document the public boundary: no raw Profile, currency ledger,
// command receipts, device identifiers or session secrets enter PlayerData.

var nativeAvatarPattern = regexp.MustCompile(`^avatar_[A-Za-z0-9_]{1,100}(?:\.txt)?$`)
var nativeVersionPattern = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$`)

func nativeAppearance(state map[string]any) (skin, avatar, hash string) {
	// Social previews prefer nonempty a over s, then decode non-avatar values as
	// Base64. multi_skin_1 is an internal fallback asset, not a valid wire avatar.
	// Send a explicitly empty so cache merges also clear old invalid values.
	if v, ok := state["avatar"].(string); ok && nativeAvatarPattern.MatchString(v) {
		avatar = strings.TrimSuffix(v, ".txt")
	}
	v, _ := state["skin"].(string)
	if nativeAvatarPattern.MatchString(v) {
		avatar = strings.TrimSuffix(v, ".txt")
	} else if len(v) <= 256*1024 {
		if data, err := base64.StdEncoding.DecodeString(v); err == nil && len(data) > 0 {
			skin = v
		}
	}
	return skin, avatar, fmt.Sprintf("%x", sha256.Sum256([]byte(skin+":"+avatar)))
}
