// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativePlayerStatisticsSubscriptionEnvelope(t *testing.T) {
	s, _ := newTestServer(t)
	const id = "918930694"
	seedSocketAccount(t, s, id, "stats-device")
	session := authorisedSession("stats", id)
	if _, err := s.st.MergeProfileProgressSlots(id, map[string]any{"57": map[string]any{"h": []any{map[string]any{"m": 2, "r": 1, "t": 1792587593}}}}); err != nil {
		t.Fatal(err)
	}
	if got := s.answerUpdatePlayerModesStat(session, sioHead{"mods": map[string]any{}}, 11); got["error"] != sioErrNone {
		t.Fatal(got)
	}
	head := sioHead{"type": "info_by_id_v3", "list_v2": []any{map[string]any{"id": id, "s_h": "", "cl_h": ""}}}
	body := s.answerNativePlayerInfo(session, head, 12)
	if _, bad := body["err_code"]; bad {
		t.Fatal("err_code presence aborts native success")
	}
	player := body["info"].(map[string]any)
	if body["id"] != id {
		t.Fatal("missing id")
	}
	wins := player["w"].(map[string]any)
	if wins["2"] != int64(1) {
		t.Fatal("missing real history wins", body)
	}
	encoded, _ := json.Marshal(body)
	for _, private := range []string{"coins", "gems", "battle_statistics_ledger", "receipts", "device", "prefs"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("private profile disclosure", private)
		}
	}
	if got := s.answerNativePlayerInfo(&sioSession{}, head, 13); got["err_code"] == nil {
		t.Fatal("unauthenticated lookup")
	}
}
func TestBattleModesUsesSocketOwner(t *testing.T) {
	s, _ := newTestServer(t)
	seedSocketAccount(t, s, "918930694", "owner")
	seedSocketAccount(t, s, "111111111", "other")
	session := authorisedSession("stats-owner", "918930694")
	body := s.answerUpdatePlayerModesStat(session, sioHead{"player_id": "111111111", "mods": map[string]any{"2": float64(5)}}, 14)
	if body["error"] != sioErrNone {
		t.Fatal(body)
	}
	a, _ := s.st.Profile("918930694")
	b, _ := s.st.Profile("111111111")
	if a.BattleWins()["2"] != int64(5) || b.BattleWins()["2"] != nil {
		t.Fatal("cross-account mutation")
	}
}
func TestNativePlayerInfoIsSeparateEncryptedPush(t *testing.T) {
	s, _ := newTestServer(t)
	seedSocketAccount(t, s, "918930694", "stats-push")
	session := authorisedSession("stats-push", "918930694")
	session.out = make(chan sioFrame, 8)
	codec := codecForPinnedSid(t)
	head := sioHead{"type": "info_by_id_v3", "list_v2": []any{map[string]any{"id": "918930694"}}}
	s.pushSubscribedPlayerInfo(session, "/sio", "websocket", codec, head, 25)
	if len(session.out) != 3 {
		t.Fatal("missing push frames", len(session.out))
	}
	<-session.out
	name, _ := stripBinaryPrefix((<-session.out).data)
	plain, err := codec.Decrypt(name)
	if err != nil || string(plain) != `"info_by_id_v3"` {
		t.Fatal("wrong subscription event", string(plain), err)
	}
	arg, _ := stripBinaryPrefix((<-session.out).data)
	plain, err = codec.Decrypt(arg)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if json.Unmarshal(plain, &body) != nil || body["info"] == nil || body["err_code"] != nil {
		t.Fatal("invalid native payload", string(plain))
	}
	s.pushSubscribedPlayerInfo(session, "/sio", "websocket", codec, sioHead{"type": "friends_list"}, 26)
	if len(session.out) != 0 {
		t.Fatal("unrelated subscription leaked profile")
	}
}
func TestNativeSubscriptionPushHasPerPlayerShape(t *testing.T) {
	s, _ := newTestServer(t)
	seedSocketAccount(t, s, "918930694", "stats-multi")
	seedSocketAccount(t, s, "111111111", "stats-multi-other")
	session := authorisedSession("stats-multi", "918930694")
	session.out = make(chan sioFrame, 12)
	codec := codecForPinnedSid(t)
	s.pushSubscribedPlayerInfo(session, "/sio", "websocket", codec, sioHead{"type": "info_by_id_v3", "list_v2": []any{map[string]any{"id": "918930694"}, map[string]any{"id": "111111111"}}}, 30)
	if len(session.out) != 6 {
		t.Fatal("expected one native push per subscribed id", len(session.out))
	}
	for _, id := range []string{"918930694", "111111111"} {
		<-session.out
		<-session.out
		raw, _ := stripBinaryPrefix((<-session.out).data)
		plain, err := codec.Decrypt(raw)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if json.Unmarshal(plain, &body) != nil {
			t.Fatal("bad push JSON")
		}
		info := body["info"].(map[string]any)
		if body["id"] != id || body["req_id"] != nil || info["w"] == nil || info[id] != nil {
			t.Fatal("bulk envelope would bypass 0x2501514", body)
		}
	}
}

func TestNativeAppearanceAndVersionCompatibility(t *testing.T) {
	for _, tc := range []struct{ skin, avatar, wantSkin, wantAvatar string }{
		{"avatar_phoenix_warrior", "", "", "avatar_phoenix_warrior"},
		{"aW1hZ2U=", "", "aW1hZ2U=", ""},
		{"aW1hZ2U=", "multi_skin_1", "aW1hZ2U=", ""},
		{"", "", "", ""},
		{"multi_skin_1", "multi_skin_1", "", ""},
		{"multi_skin_1.txt", "", "", ""},
		{"aW1hZ2U=", "avatar_phoenix_warrior", "aW1hZ2U=", "avatar_phoenix_warrior"},
		{"not base64!", "../../private", "", ""},
		{"", "avatar_phoenix_warrior.txt", "", "avatar_phoenix_warrior"},
		{strings.Repeat("A", 256*1024+4), "", "", ""},
	} {
		skin, avatar, hash := nativeAppearance(map[string]any{"skin": tc.skin, "avatar": tc.avatar})
		if skin != tc.wantSkin || avatar != tc.wantAvatar || len(hash) != 64 {
			t.Fatal("invalid appearance conversion", avatar)
		}
		_, _, again := nativeAppearance(map[string]any{"skin": tc.skin, "avatar": tc.avatar})
		if hash != again {
			t.Fatal("unstable cache hash")
		}
	}
	s, a, b, _, codec := socialAPIFixture(t)
	if _, err := s.st.MergeProfileClientState(b.playerID, map[string]any{"skin": "avatar_phoenix_warrior", "version": "invalid", "device": "secret-device"}); err != nil {
		t.Fatal(err)
	}
	if got := s.answerUpdatePlayerModesStat(b, sioHead{"mods": map[string]any{"2": float64(5)}}, 1); got["err_code"] != nil {
		t.Fatal(got)
	}
	socialDispatch(t, s, a, "subscribe", sioHead{"type": "info_by_id_v3", "list_v2": []any{map[string]any{"id": b.playerID}}, "req_id": 2})
	body := socialEvent(t, socialFrames(t, a, codec), "info_by_id_v3")
	info := body["info"].(map[string]any)
	p, _ := s.st.Profile(b.playerID)
	if info["n"] != p.Nick || info["lv"] != float64(p.Level) || info["v"] != "23.1.3" || info["s"] != "" || info["a"] != "avatar_phoenix_warrior" {
		t.Fatal("broken public PlayerData", info)
	}
	if info["w"].(map[string]any)["2"] != float64(5) {
		t.Fatal("real statistics lost")
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "secret-device") {
		t.Fatal("private disclosure")
	}
}
