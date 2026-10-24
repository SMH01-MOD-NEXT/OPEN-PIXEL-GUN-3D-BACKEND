// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
)

// Models the native social preview selector (0x1FFE0E0) and texture helper
// (0x3F9C014); not an execution of the Android client or texture dimension check.
func assertSocialPreviewDecodable(t *testing.T, info map[string]any) {
	t.Helper()
	skin, skinOK := info["s"].(string)
	avatar, avatarOK := info["a"].(string)
	if !skinOK || !avatarOK {
		t.Fatal("appearance keys must be explicit strings")
	}
	selected := avatar
	if selected == "" {
		selected = skin
	}
	if selected == "" || selected == "empty" || selected == "EMPTY_ROYALE_AVATAR" {
		return
	}
	if strings.Contains(selected, "avatar") {
		return
	}
	if _, err := base64.StdEncoding.DecodeString(selected); err != nil {
		t.Fatal("native social preview would throw", err)
	}
}

func TestSocialAppearanceAcrossEncryptedFriendLifecycle(t *testing.T) {
	s, a, b, _, codec := socialAPIFixture(t)
	reqID := 0
	dispatch := func(session *sioSession, event string, head sioHead) {
		reqID++
		head["req_id"] = float64(reqID)
		socialDispatch(t, s, session, event, head)
	}
	for _, session := range []*sioSession{a, b} {
		for _, topic := range []string{"friends_list", "friend_invites"} {
			dispatch(session, "subscribe", sioHead{"type": topic})
			socialFrames(t, session, codec)
		}
	}
	if _, err := s.st.MergeProfileClientState(a.playerID, map[string]any{"skin": "avatar_phoenix_warrior"}); err != nil {
		t.Fatal(err)
	}
	const custom = "aW1hZ2U="
	if _, err := s.st.MergeProfileClientState(b.playerID, map[string]any{"skin": custom}); err != nil {
		t.Fatal(err)
	}
	checkProfiles := func() {
		t.Helper()
		for _, pair := range [][2]*sioSession{{a, b}, {b, a}} {
			viewer, peer := pair[0], pair[1]
			dispatch(viewer, "get_all_short_info_by_id_v3", sioHead{"ids_v2": []any{map[string]any{"id": peer.playerID, "s_h": ""}}})
			body := socialEvent(t, socialFrames(t, viewer, codec), "get_all_short_info_by_id_v3")
			info := body["info"].(map[string]any)[peer.playerID].(map[string]any)
			assertSocialPreviewDecodable(t, info)
			if peer == b {
				if info["s"] != custom || info["a"] != "" {
					t.Fatal("custom skin masked by fallback avatar")
				}
				// Native merge copies present keys even when empty. Verify wire data
				// can replace the old bad cached avatar instead of retaining it.
				cached := map[string]any{"a": "multi_skin_1"}
				for k, v := range info {
					cached[k] = v
				}
				assertSocialPreviewDecodable(t, cached)
				if cached["a"] != "" {
					t.Fatal("stale avatar not cleared")
				}
			} else if info["a"] != "avatar_phoenix_warrior" || info["s"] != "" {
				t.Fatal("built-in avatar lost")
			}
		}
	}
	checkProfiles()
	dispatch(a, "friend_request", sioHead{"whom": b.playerID})
	socialFrames(t, a, codec)
	if got := socialEvent(t, socialFrames(t, b, codec), "friend_invites")["incoming"]; !reflect.DeepEqual(got, []any{a.playerID}) {
		t.Fatal("incoming request missing", got)
	}
	checkProfiles()
	dispatch(b, "accept_friend", sioHead{"acceptee_id": a.playerID})
	for _, pair := range [][2]*sioSession{{a, b}, {b, a}} {
		got := socialEvent(t, socialFrames(t, pair[0], codec), "friends_list")["friends"]
		if !reflect.DeepEqual(got, []any{pair[1].playerID}) {
			t.Fatal("asymmetric friendship", got)
		}
	}
	checkProfiles()
	dispatch(a, "reject_friendship", sioHead{"rejectee_id": b.playerID})
	for _, session := range []*sioSession{a, b} {
		got := socialEvent(t, socialFrames(t, session, codec), "friends_list")["friends"]
		if !reflect.DeepEqual(got, []any{}) {
			t.Fatal("removed friendship retained", got)
		}
	}
	dispatch(b, "friend_request", sioHead{"whom": a.playerID})
	socialFrames(t, b, codec)
	if got := socialEvent(t, socialFrames(t, a, codec), "friend_invites")["incoming"]; !reflect.DeepEqual(got, []any{b.playerID}) {
		t.Fatal("reverse request missing", got)
	}
	checkProfiles()
	// Fresh authenticated session must receive the same pending request, not
	// manufacture a friendship to compensate for a missing client-side row.
	fresh := authorisedSession(a.playerID, a.playerID)
	fresh.out = make(chan sioFrame, 128)
	dispatch(fresh, "subscribe", sioHead{"type": "friend_invites"})
	if got := socialEvent(t, socialFrames(t, fresh, codec), "friend_invites")["incoming"]; !reflect.DeepEqual(got, []any{b.playerID}) {
		t.Fatal("pending request lost on reconnect", got)
	}
}
