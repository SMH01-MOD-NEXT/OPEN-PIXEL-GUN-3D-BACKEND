// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"reflect"
	"strings"
	"testing"
)

func seedSocketAccount(t *testing.T, s *Server, playerID, device string) {
	t.Helper()
	if _, _, err := s.st.AdoptSocketIdentity(playerID, device, "192.0.2.10"); err != nil {
		t.Fatalf("seed socket account %s: %v", playerID, err)
	}
}

func TestPlayerStateEventsUseDedicatedHandlers(t *testing.T) {
	s, _ := newTestServer(t)
	seedSocketAccount(t, s, "918930694", "device-player-state")
	authorised := authorisedSession("state-routing", "918930694")
	unbound := &sioSession{sid: "unbound-routing"}

	readCases := []struct {
		event  string
		head   sioHead
		marker string
	}{
		{"get_info_by_id", sioHead{"id": "918930694"}, "info"},
		{"get_info_by_id_v2", sioHead{"id": "918930694"}, "info"},
		{"get_info_by_id_v3", sioHead{"id": "918930694"}, "info"},
		{"refund_get", sioHead{}, "refunds"},
		{"get_time", sioHead{}, "time"},
		{"check_binded_id", sioHead{}, "binded"},
		{"get_map_popularity", sioHead{"params": map[string]any{}}, "params"},
		{"get_leader_clan", sioHead{"clan_id": "0"}, "leader"},
		{"unsubscribe", sioHead{"list": []any{}}, "list"},
	}
	for _, tc := range readCases {
		t.Run(tc.event, func(t *testing.T) {
			body := sioAnswerFor(tc.event)(s, authorised, tc.head, 71)
			if _, ok := body[tc.marker]; !ok {
				t.Fatalf("%s reached the generic acknowledgement: %#v", tc.event, body)
			}
		})
	}

	for _, event := range []string{
		"update_player", "update_accessories", "update_loadout",
		"update_character_target_ad_consent",
	} {
		t.Run(event, func(t *testing.T) {
			body := sioAnswerFor(event)(s, unbound, sioHead{}, 72)
			if body["error"] != sioErrNotAuthorized {
				t.Fatalf("%s reached the generic acknowledgement: %#v", event, body)
			}
		})
	}
}

func TestPlayerStateMutationsBelongToAuthenticatedSocket(t *testing.T) {
	s, _ := newTestServer(t)
	const (
		ownerID = "918930694"
		otherID = "111111111"
	)
	seedSocketAccount(t, s, ownerID, "device-owner")
	seedSocketAccount(t, s, otherID, "device-other")
	session := authorisedSession("state-owner", ownerID)

	body := s.answerUpdatePlayer(session, sioHead{
		"player_id":  otherID, // data only; it must never select the write target
		"nick":       "Backend Tester",
		"level":      float64(9),
		"experience": float64(1234),
		"coins":      float64(77),
		"gems":       float64(8),
		"ad_id":      "private-advertising-id",
		"unknown":    map[string]any{"nested": []any{float64(1), "two"}},
	}, 80)
	if body["error"] != sioErrNone {
		t.Fatalf("update_player failed: %#v", body)
	}
	if got := s.answerUpdateAccessories(session, sioHead{"accessories": "exact-accessory-json"}, 81); got["error"] != sioErrNone {
		t.Fatalf("update_accessories failed: %#v", got)
	}
	loadout := map[string]any{"pw": float64(10001), "bw": float64(1001)}
	if got := s.answerUpdateLoadout(session, sioHead{"w": loadout}, 82); got["error"] != sioErrNone {
		t.Fatalf("update_loadout failed: %#v", got)
	}
	if got := s.answerUpdateTargetAdConsent(session, sioHead{"target_ad": float64(1)}, 83); got["error"] != sioErrNone {
		t.Fatalf("target-ad update failed: %#v", got)
	}

	profile, err := s.st.Profile(ownerID)
	if err != nil {
		t.Fatalf("load owner profile: %v", err)
	}
	if profile.Nick != "Backend Tester" || profile.Level != 9 || profile.Experience != 1234 || profile.Coins != 77 || profile.Gems != 8 {
		t.Fatalf("projected profile = %#v", profile)
	}
	if profile.ClientState["player_id"] != otherID || profile.ClientState["ad_id"] != "private-advertising-id" {
		t.Fatalf("unknown update_player fields were dropped: %#v", profile.ClientState)
	}
	if profile.ClientState["accessories"] != "exact-accessory-json" || profile.ClientState["target_ad"] != float64(1) {
		t.Fatalf("specialized state was not stored exactly: %#v", profile.ClientState)
	}
	if !reflect.DeepEqual(profile.ClientState["w"], loadout) {
		t.Fatalf("loadout = %#v, want %#v", profile.ClientState["w"], loadout)
	}
	account, _ := s.st.Account(ownerID)
	if account.Nick != "Backend Tester" {
		t.Fatalf("account nick = %q, want update_player nick", account.Nick)
	}

	other, err := s.st.Profile(otherID)
	if err != nil {
		t.Fatalf("load claimed profile: %v", err)
	}
	if len(other.ClientState) != 0 || other.Level != 0 || other.Coins != 0 || other.Gems != 0 {
		t.Fatalf("payload-selected account was modified: %#v", other)
	}
}

func TestPlayerStateMutationRejectsUnboundSession(t *testing.T) {
	s, _ := newTestServer(t)
	seedSocketAccount(t, s, "918930694", "device-existing")

	body := s.answerUpdatePlayer(&sioSession{sid: "unbound"}, sioHead{
		"player_id": "918930694",
		"coins":     float64(999999),
	}, 84)
	if body["error"] != sioErrNotAuthorized || body["err_code"] != sioErrNotAuthorized {
		t.Fatalf("unbound state update was not denied: %#v", body)
	}
	profile, _ := s.st.Profile("918930694")
	if profile.Coins != 0 || len(profile.ClientState) != 0 {
		t.Fatalf("unbound update reached storage: %#v", profile)
	}
}

func TestGetInfoByIDResolvesAliasAndRedactsCredentials(t *testing.T) {
	s, _ := newTestServer(t)
	const (
		oldID = "918930694"
		newID = "812345678"
	)
	seedSocketAccount(t, s, oldID, "device-info")
	session := authorisedSession("info-state", oldID)
	s.answerUpdatePlayer(session, sioHead{
		"nick":       "Public Player",
		"skin":       "skin-payload",
		"rank":       float64(4),
		"level":      float64(12),
		"experience": float64(5678),
		"coins":      float64(90),
		"gems":       float64(11),
		"ad_id":      "must-not-leak",
		"session":    "must-not-leak",
		"device_id":  "must-not-leak",
	}, 90)
	seedSocketAccount(t, s, newID, "device-info")
	if _, err := s.st.ReconcileAccounts(oldID, newID); err != nil {
		t.Fatalf("reconcile alias: %v", err)
	}

	body := s.answerGetInfoByID(nil, sioHead{"id": oldID}, 91)
	info, ok := body["info"].(map[string]any)
	if !ok {
		t.Fatalf("info is %T: %#v", body["info"], body)
	}
	if info["id"] != newID || info["player_id"] != newID {
		t.Fatalf("alias did not resolve to canonical id: %#v", info)
	}
	if info["nick"] != "Public Player" || info["skin"] != "skin-payload" || info["rank"] != float64(4) {
		t.Fatalf("public player state is incomplete: %#v", info)
	}
	if info["level"] != 12 || info["experience"] != int64(5678) || info["coins"] != int64(90) || info["gems"] != int64(11) {
		t.Fatalf("typed counters are wrong: %#v", info)
	}
	for _, private := range []string{"ad_id", "session", "device", "device_id", "socket_device"} {
		if _, leaked := info[private]; leaked {
			t.Fatalf("public info leaked %s: %#v", private, info)
		}
	}
}

func TestReadHandlersReturnStableEmptyTypes(t *testing.T) {
	s, _ := newTestServer(t)

	refunds := s.answerRefundGet(nil, sioHead{}, 100)
	for _, key := range []string{"transaction_list", "refunds", "refunds_friend", "refunds_clan", "gifts"} {
		if value, ok := refunds[key].([]any); !ok || len(value) != 0 {
			t.Fatalf("refund member %s is %T %#v, want an empty list", key, refunds[key], refunds[key])
		}
	}
	if users, ok := refunds["users"].(map[string]any); !ok || len(users) != 0 {
		t.Fatalf("refund users is %T %#v, want an empty object", refunds["users"], refunds["users"])
	}

	clock := s.answerGetTime(nil, sioHead{}, 101)
	if value, ok := clock["time"].(int64); !ok || value <= 0 {
		t.Fatalf("time is %T %#v, want Unix seconds", clock["time"], clock["time"])
	}
	if clock["timestamp"] != clock["time"] {
		t.Fatalf("time aliases differ: %#v", clock)
	}

	bind := s.answerCheckBindedID(nil, sioHead{"social_id": "", "social_name": ""}, 102)
	if bind["social_id"] != "" || bind["binded"] != 0 || bind["is_binded"] != 0 || bind["allow_bind"] != 1 {
		t.Fatalf("bind answer = %#v", bind)
	}

	params := map[string]any{"2": map[string]any{"16.6.0mm_v2": map[string]any{"0": []any{"10038"}}}}
	popularity := s.answerMapPopularity(nil, sioHead{"params": params}, 103)
	if !reflect.DeepEqual(popularity["params"], params) {
		t.Fatalf("map shape changed: %#v", popularity)
	}
	if empty, ok := popularity["popularity"].(map[string]any); !ok || len(empty) != 0 {
		t.Fatalf("popularity is %T %#v, want empty object", popularity["popularity"], popularity["popularity"])
	}

	leader := s.answerLeaderClan(nil, sioHead{"clan_id": "0"}, 104)
	if leader["clan_id"] != "0" || leader["leader_id"] != "0" {
		t.Fatalf("leader answer = %#v", leader)
	}
	if _, ok := leader["leader"].(map[string]any); !ok {
		t.Fatalf("leader is %T, want object", leader["leader"])
	}

	list := []any{"news", "offers"}
	unsubscribed := s.answerUnsubscribe(nil, sioHead{"type": "push", "list": list}, 105)
	if unsubscribed["type"] != "push" || !reflect.DeepEqual(unsubscribed["list"], list) {
		t.Fatalf("unsubscribe did not echo request identity: %#v", unsubscribed)
	}
}

func TestGenericAcknowledgementLogNamesExactEvent(t *testing.T) {
	s, _ := newTestServer(t)
	const event = "future_23_1_3_event"

	body := sioAnswerFor(event)(s, &sioSession{sid: "generic-ack-log"}, sioHead{}, 106)
	if body["req_id"] != 106 || body["error"] != sioErrNone {
		t.Fatalf("generic acknowledgement = %#v", body)
	}

	for _, entry := range s.log.Recent(0) {
		if entry.Kind != "socket.event_generic_ack" {
			continue
		}
		if !strings.Contains(entry.Line, "event="+event) {
			t.Fatalf("fallback log omitted exact event name: %s", entry.Line)
		}
		return
	}
	t.Fatal("generic acknowledgement was not logged")
}
