// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"strings"
	"testing"
)

func clanSection(t *testing.T, body map[string]any, name string) any {
	t.Helper()
	value, ok := body[name]
	if !ok {
		t.Fatal("missing section", name, body)
	}
	return value
}

func TestClanSocketLifecycle(t *testing.T) {
	s, a, b, c, codec := socialAPIFixture(t)
	socialDispatch(t, s, a, "subscribe", sioHead{"req_id": float64(1), "type": "change_clan_state"})
	events := socialFrames(t, a, codec)
	if ack := socialEvent(t, events, "subscribe"); ack["err_code"] != nil {
		t.Fatal(ack)
	}
	if state := socialEvent(t, events, "change_clan_state"); state["existed_clan"] != float64(0) {
		t.Fatal("initial state", state)
	}

	socialDispatch(t, s, a, "create_clan_v2", sioHead{"req_id": float64(2), "name": "Wolves", "logo": "", "slogan": "hunt", "type": float64(0), "continent": "eu", "min_rank": float64(0)})
	events = socialFrames(t, a, codec)
	created := socialEvent(t, events, "create_clan_v2")
	data, _ := created["data"].(map[string]any)
	id, _ := data["clan_id"].(string)
	if created["err_code"] != nil || id == "" {
		t.Fatal("create", created)
	}
	if added := socialEvent(t, events, "change_clan_state"); added["added_clan"] != id {
		t.Fatal("added_clan", added)
	}

	socialDispatch(t, s, a, "subscribe", sioHead{"req_id": float64(3), "type": "get_start_clan", "lh": "", "last_timeStamp": float64(0), "last_timeStamp_service": float64(0)})
	start := socialEvent(t, socialFrames(t, a, codec), "get_start_clan")
	core := clanSection(t, start, "cr").(map[string]any)
	if core["id"] != id || core["na"] != "Wolves" || core["cr"] != a.playerID || core["sl"] != "hunt" {
		t.Fatal("core", core)
	}
	if members := clanSection(t, start, "mb").([]any); len(members) != 1 {
		t.Fatal("members", members)
	}

	socialDispatch(t, s, b, "join_clan", sioHead{"req_id": float64(4), "clan_id": id})
	joined := socialEvent(t, socialFrames(t, b, codec), "join_clan")
	if joined["err_code"] != nil || joined["clan_id"] != id {
		t.Fatal("join", joined)
	}
	if full, ok := joined["clan"].(map[string]any); !ok || full["clan_core"] == nil {
		t.Fatal("join must carry the full clan", joined)
	}
	update := socialEvent(t, socialFrames(t, a, codec), "get_start_clan")
	if members := clanSection(t, update, "mb").([]any); len(members) != 2 {
		t.Fatal("member push", update)
	}
	if _, resent := update["cr"]; resent {
		t.Fatal("unchanged core re-sent", update)
	}
	service := clanSection(t, update, "ms").(map[string]any)["service_messages"].([]any)
	if len(service) != 1 {
		t.Fatal("join notice", service)
	}

	socialDispatch(t, s, c, "create_clan_v2", sioHead{"req_id": float64(5), "name": " wolves "})
	if fail := socialEvent(t, socialFrames(t, c, codec), "create_clan_v2"); fail["err_code"] != float64(clanErrNameTaken) {
		t.Fatal("name taken", fail)
	}
	socialDispatch(t, s, c, "send_clan_message", sioHead{"req_id": float64(6), "text": "spy", "timeStamp": float64(1)})
	if fail := socialEvent(t, socialFrames(t, c, codec), "send_clan_message"); fail["err_code"] == nil {
		t.Fatal("outsider chat", fail)
	}

	const text = "CLAN-CHAT-must-not-appear-in-logs"
	socialDispatch(t, s, b, "send_clan_message", sioHead{"req_id": float64(7), "text": text, "timeStamp": float64(1234.5), "from": c.playerID})
	if fail := socialEvent(t, socialFrames(t, b, codec), "send_clan_message"); fail["err_code"] == nil {
		t.Fatal("spoofed sender accepted", fail)
	}
	socialDispatch(t, s, b, "send_clan_message", sioHead{"req_id": float64(8), "text": text, "timeStamp": float64(1234.5)})
	ack := socialEvent(t, socialFrames(t, b, codec), "send_clan_message")
	if ack["err_code"] != nil || ack["timestamp"] == nil || ack["client_timestamp"] != "1234.5" {
		t.Fatal("chat ack", ack)
	}
	chat := clanSection(t, socialEvent(t, socialFrames(t, a, codec), "get_start_clan"), "ms").(map[string]any)["people_messages"].([]any)
	if len(chat) != 1 {
		t.Fatal("chat push", chat)
	}
	for _, value := range chat[0].(map[string]any) {
		if m := value.(map[string]any); m["from"] != b.playerID || m["text"] != text {
			t.Fatal("chat row", m)
		}
	}
	if len(c.out) != 0 {
		t.Fatal("outsider received a clan push")
	}
	for _, entry := range s.log.Recent(0) {
		if strings.Contains(entry.Line, text) {
			t.Fatal("plaintext clan chat in diagnostic log")
		}
	}

	socialDispatch(t, s, b, "leave_clan", sioHead{"req_id": float64(9), "clan_id": id})
	if ack := socialEvent(t, socialFrames(t, b, codec), "leave_clan"); ack["err_code"] != nil {
		t.Fatal("leave", ack)
	}
	socialFrames(t, a, codec)
	socialDispatch(t, s, b, "create_clan_v2", sioHead{"req_id": float64(10), "name": "Lone"})
	fail := socialEvent(t, socialFrames(t, b, codec), "create_clan_v2")
	if seconds, _ := fail["time"].(float64); fail["err_code"] != float64(clanErrCooldown) || seconds <= 0 {
		t.Fatal("cooldown", fail)
	}

	socialDispatch(t, s, a, "delete_clan", sioHead{"req_id": float64(11), "clan_id": id, "player_id": a.playerID})
	events = socialFrames(t, a, codec)
	if ack := socialEvent(t, events, "delete_clan"); ack["err_code"] != nil {
		t.Fatal("delete", ack)
	}
	if removed := socialEvent(t, events, "change_clan_state"); removed["removed_clan"] != id {
		t.Fatal("removed_clan", removed)
	}
}

func TestClanSocketSearchRequestsAndCores(t *testing.T) {
	s, a, b, c, codec := socialAPIFixture(t)
	socialDispatch(t, s, a, "create_clan_v2", sioHead{"req_id": float64(1), "name": "Closed Ring", "type": float64(2), "min_rank": float64(0)})
	created := socialEvent(t, socialFrames(t, a, codec), "create_clan_v2")
	id, _ := created["clan_id"].(string)
	if id == "" {
		t.Fatal("create", created)
	}
	socialDispatch(t, s, a, "subscribe", sioHead{"req_id": float64(2), "type": "get_start_clan"})
	socialFrames(t, a, codec)

	socialDispatch(t, s, c, "get_clan_info_by_param", sioHead{"req_id": float64(3), "param": "ring"})
	found := socialEvent(t, socialFrames(t, c, codec), "get_clan_info_by_param")["data"].([]any)
	if len(found) != 1 {
		t.Fatal("search", found)
	}
	row := found[0].(map[string]any)
	if row["clan_core"].(map[string]any)["id"] != id || row["has_request"] != float64(0) {
		t.Fatal("search row", row)
	}

	socialDispatch(t, s, b, "join_clan", sioHead{"req_id": float64(4), "clan_id": id})
	if fail := socialEvent(t, socialFrames(t, b, codec), "join_clan"); fail["err_code"] == nil {
		t.Fatal("private clan joined directly", fail)
	}
	socialDispatch(t, s, b, "ask_to_join_clan", sioHead{"req_id": float64(5), "clan_id": id, "text": "let me in"})
	if ack := socialEvent(t, socialFrames(t, b, codec), "ask_to_join_clan"); ack["err_code"] != nil {
		t.Fatal("request", ack)
	}
	incoming := clanSection(t, socialEvent(t, socialFrames(t, a, codec), "get_start_clan"), "ir").([]any)
	if len(incoming) != 1 || incoming[0].(map[string]any)["id"] != b.playerID || incoming[0].(map[string]any)["m"] != "let me in" {
		t.Fatal("incoming request push", incoming)
	}
	socialDispatch(t, s, a, "accept_request_to_join_clan", sioHead{"req_id": float64(6), "clan_id": id, "receiver_id": b.playerID})
	events := socialFrames(t, a, codec)
	if ack := socialEvent(t, events, "accept_request_to_join_clan"); ack["err_code"] != nil || ack["receiver_id"] != b.playerID {
		t.Fatal("accept", ack)
	}
	update := socialEvent(t, events, "get_start_clan")
	removed := clanSection(t, update, "ir").([]any)
	if len(removed) != 1 || removed[0].(map[string]any)["delta"] != float64(-1) {
		t.Fatal("request removal must be explicit", removed)
	}
	if members := clanSection(t, update, "mb").([]any); len(members) != 2 {
		t.Fatal("accepted member", members)
	}

	socialDispatch(t, s, b, "change_clan_role", sioHead{"req_id": float64(7), "clan_id": id, "player_id": b.playerID, "role_id": float64(2)})
	if fail := socialEvent(t, socialFrames(t, b, codec), "change_clan_role"); fail["err_code"] == nil {
		t.Fatal("soldier promoted itself", fail)
	}

	if s.answerSubscribe(c, sioHead{"type": "get_clans_core", "list": []any{"not-a-clan"}}, 8)["err_code"] == nil {
		t.Fatal("invalid core list accepted")
	}
	socialDispatch(t, s, c, "subscribe", sioHead{"req_id": float64(9), "type": "get_clans_core", "list": []any{id}})
	cores := socialEvent(t, socialFrames(t, c, codec), "get_clans_core")
	entry, ok := cores[id].(map[string]any)
	if !ok || entry["cr"] == nil || entry["mb"] == nil {
		t.Fatal("cores push", cores)
	}
	for _, private := range []string{"ir", "or", "ms", "it"} {
		if _, leaked := entry[private]; leaked {
			t.Fatal("private section in public core", private)
		}
	}

	for _, event := range []string{"create_clan_v2", "join_clan", "get_clan_info_by_param", "send_clan_message", "update_clan_field", "get_clan_status"} {
		if body := sioAnswerFor(event)(s, &sioSession{}, sioHead{}, 1); body["err_code"] == nil {
			t.Fatal("anonymous", event)
		}
	}
}
