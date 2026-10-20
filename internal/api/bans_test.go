// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

var banAdminHeaders = map[string]string{"Authorization": "Bearer admin-secret"}

func banAccount(t *testing.T, s *Server) store.Account {
	t.Helper()
	a, _, err := s.st.RegisterOrLogin("ban-native", "Ban Test", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	linked, err := s.st.AdoptSocketIdentityForAccount(a.PlayerID, a.PlayerID, "ban-socket", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	return linked.Account
}
func readBanEvent(t *testing.T, session *sioSession) (string, map[string]any) {
	t.Helper()
	frames := make([]sioFrame, 3)
	for i := range frames {
		select {
		case frames[i] = <-session.out:
		default:
			t.Fatal("missing event frames")
		}
	}
	codec, err := sio.NewForSid(session.sid, sio.DefaultMode)
	if err != nil {
		t.Fatal(err)
	}
	name, err := codec.Decrypt(frames[1].data[1:])
	if err != nil {
		t.Fatal(err)
	}
	var event string
	if err := json.Unmarshal(name, &event); err != nil {
		t.Fatal(err)
	}
	body, err := codec.Decrypt(frames[2].data[1:])
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if event == "expired_session" && session.authNamespace == "/sio" && frames[0].text != `452-/sio,[{"_placeholder":true,"num":0},{"_placeholder":true,"num":1}]` {
		t.Fatal("revocation sent outside the authenticated Socket.IO namespace")
	}
	return event, out
}
func TestBanPlayerEnforcesBothDevicesAndExistingSocketsWithoutProgressChanges(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.SioReply = true
	a := banAccount(t, s)
	profile, _ := s.st.Profile(a.PlayerID)
	target := addReloginTestSession(t, s, a.PlayerID, "ban-socket", &testReloginTransport{})
	target.authNamespace = "/sio"
	foreign := addReloginTestSession(t, s, "987654321", "foreign", &testReloginTransport{})
	rec := do(t, s, http.MethodPost, "/api/v1/admin/players/"+a.PlayerID+"/ban", map[string]any{"reason": "test", "minutes": 5}, banAdminHeaders)
	if rec.Code != 200 || len(s.st.Bans()) != 3 {
		t.Fatalf("ban status=%d records=%d", rec.Code, len(s.st.Bans()))
	}
	event, body := readBanEvent(t, target)
	if event != "expired_session" || body["reason"] != float64(1) {
		t.Fatal("not the stock typed revocation event")
	}
	if len(foreign.out) != 0 {
		t.Fatal("foreign session notified")
	}
	for _, device := range []string{a.DeviceID, a.SocketDevice} {
		if _, hit := s.st.MatchBan("", device, ""); !hit {
			t.Fatal("missing device ban")
		}
	}
	if _, ok := s.socketPlayerID(target); ok {
		t.Fatal("existing socket still authorized")
	}
	for _, event := range []string{"update_player", "update_progress", "replace_progress", "subscribe"} {
		codec, _ := sio.NewForSid(target.sid, sio.DefaultMode)
		s.answerDecodedEvent(target, &sioBinaryAssembly{}, codec, [][]byte{[]byte(event), []byte(`{"req_id":73,"nick":"Should not persist"}`)})
		got, reply := readBanEvent(t, target)
		if got != event || reply["err_code"] != float64(1419) || reply["req_id"] != float64(73) {
			t.Fatal("banned command not rejected/correlated")
		}
		if len(target.out) != 0 {
			t.Fatal("banned subscribe pushed configs")
		}
	}
	after, _ := s.st.Profile(a.PlayerID)
	if !reflect.DeepEqual(profile, after) {
		t.Fatal("ban or denied gameplay changed progress")
	}
	current, _ := s.st.Account(a.PlayerID)
	if current.Session != a.Session {
		t.Fatal("ban rotated native credentials")
	}
	// A later auth cannot leave a banned player bound to the session.
	reply := s.answerAuth(target, sioHead{"uniq_id": a.PlayerID, "device": "ban-socket"}, 4)
	if reply["err_code"] != 1419 {
		t.Fatal("auth ban not recognized")
	}
	if _, _, id := target.identity(); id != "" {
		t.Fatal("banned auth left authorized identity")
	}
	rec = do(t, s, http.MethodPost, "/api/v1/admin/players/"+a.PlayerID+"/unban", nil, banAdminHeaders)
	if rec.Code != 200 || len(s.st.Bans()) != 0 {
		t.Fatal("unban left device record")
	}
	reply = s.answerAuth(target, sioHead{"uniq_id": a.PlayerID, "device": "ban-socket"}, 5)
	if _, bad := reply["err_code"]; bad {
		t.Fatal("unbanned auth denied")
	}
	if id, ok := s.socketPlayerID(target); !ok || id != a.PlayerID {
		t.Fatal("unbanned session failed")
	}
}
func TestBanBootstrapAndEncryptedStockHTTPWindow(t *testing.T) {
	s, _ := newTestServer(t)
	a := banAccount(t, s)
	if err := s.st.AddBan(store.Ban{Kind: store.BanPlayer, Subject: a.PlayerID}); err != nil {
		t.Fatal(err)
	}
	for _, ua := range []string{"", "opg3d-thin-client/23.1.3"} {
		rec := do(t, s, http.MethodPost, "/api/v1/accounts/register", map[string]string{"device_id": a.DeviceID}, map[string]string{"User-Agent": ua})
		want := 403
		if ua != "" {
			want = 200
		}
		if rec.Code != want {
			t.Fatalf("bootstrap=%d want%d", rec.Code, want)
		}
		current, _ := s.st.Account(a.PlayerID)
		if current.Session != a.Session {
			t.Fatal("banned bootstrap rotated session")
		}
	}
	for _, path := range []string{"/api/v1/photon/appid?mode=pun&player_id=" + a.PlayerID, "/api/v1/sessions/refresh"} {
		method := http.MethodGet
		var request any
		if path == "/api/v1/sessions/refresh" {
			method = http.MethodPost
			request = refreshRequest{PlayerID: a.PlayerID, Session: a.Session}
		}
		rec := do(t, s, method, path, request, map[string]string{"X-OPG3D-Player": a.PlayerID, "X-OPG3D-Session": a.Session})
		if rec.Code != 403 {
			t.Fatalf("banned capability %s status=%d", path, rec.Code)
		}
	}
	q := url.Values{"opg3d_player": {a.PlayerID}, "opg3d_session": {a.Session}}
	raw, _ := encodeLegacyAuth(map[string]string{"device_id": "ban-socket", "player_id": a.PlayerID, "hash": "preserve-hash"}, 77)
	req := httptest.NewRequest(http.MethodPost, "/auth_v2/?"+q.Encode(), bytes.NewReader(raw))
	req.RemoteAddr = "192.0.2.10:5555"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	fields, err := decodeLegacyAuth(rec.Body.Bytes())
	if rec.Code != 200 || err != nil {
		t.Fatalf("stock ban status=%d decode=%v", rec.Code, err)
	}
	if fields["err_code"] != "1402" || fields["player_id"] != a.PlayerID || fields["hash"] != "preserve-hash" || fields["create_to"] != "2147483647" {
		t.Fatal("stock ban contract mismatch")
	}
	if fields["url"] != "" || fields["token"] != "" {
		t.Fatal("banned stock auth granted socket access")
	}
	if len(s.st.Accounts()) != 1 {
		t.Fatal("ban created account")
	}
}
func TestBanDurationAndSubjectsValidationAndSharedIPUnban(t *testing.T) {
	s, _ := newTestServer(t)
	a := banAccount(t, s)
	for _, minutes := range []int{-1, int(^uint(0) >> 1)} {
		for _, path := range []string{"/api/v1/admin/bans", "/api/v1/admin/players/" + a.PlayerID + "/ban"} {
			rec := do(t, s, http.MethodPost, path, map[string]any{"kind": "player", "subject": a.PlayerID, "minutes": minutes}, banAdminHeaders)
			if rec.Code != 400 || len(s.st.Bans()) != 0 {
				t.Fatal("invalid duration mutated bans")
			}
		}
	}
	for _, body := range []map[string]any{{"kind": "device", "subject": " "}, {"kind": "typo", "subject": "a"}} {
		if rec := do(t, s, http.MethodPost, "/api/v1/admin/bans", body, banAdminHeaders); rec.Code != 400 {
			t.Fatal("invalid subject accepted")
		}
	}
	if err := s.st.AddBan(store.Ban{Kind: store.BanIP, Subject: a.LastIP}); err != nil {
		t.Fatal(err)
	}
	rec := do(t, s, http.MethodPost, "/api/v1/admin/players/"+a.PlayerID+"/unban", nil, banAdminHeaders)
	if rec.Code != 200 || decode(t, rec)["still_banned"] != true {
		t.Fatal("shared IP ban not reported")
	}
	if _, hit := s.accountBan(a, a.LastIP); !hit {
		t.Fatal("unban lifted shared IP")
	}
	rec = do(t, s, http.MethodDelete, "/api/v1/admin/bans?kind=ip&subject="+url.QueryEscape(a.LastIP), nil, banAdminHeaders)
	if rec.Code != 200 {
		t.Fatal("explicit IP unban failed")
	}
	past := time.Now().Add(-time.Second)
	if err := s.st.AddBan(store.Ban{Kind: store.BanPlayer, Subject: a.PlayerID, Until: &past}); err != nil {
		t.Fatal(err)
	}
	if _, hit := s.accountBan(a, a.LastIP); hit {
		t.Fatal("expired ban active")
	}
}

// The stock client explicitly connects /sio. A push to the root namespace is
// valid Socket.IO framing but is silently routed away from these subscribers.
func TestBanPushRetainsActualAuthNamespaceAndExpiryReauthorizes(t *testing.T) {
	for _, namespace := range []string{"", "/sio", "/custom"} {
		t.Run(namespace, func(t *testing.T) {
			s, cfg := newTestServer(t)
			cfg.SioReply = true
			a := banAccount(t, s)
			target := addReloginTestSession(t, s, a.PlayerID, a.SocketDevice, &testReloginTransport{})
			codec, _ := sio.NewForSid(target.sid, sio.DefaultMode)
			head, _ := json.Marshal(map[string]any{"req_id": 1, "uniq_id": a.PlayerID, "device": a.SocketDevice})
			s.answerDecodedEvent(target, &sioBinaryAssembly{namespace: namespace}, codec, [][]byte{[]byte(sioAuthEvent), head})
			readBanEvent(t, target)
			if target.authNamespace != namespace {
				t.Fatal("auth namespace not retained")
			}
			rec := do(t, s, http.MethodPost, "/api/v1/admin/bans", map[string]any{"kind": "player", "subject": a.PlayerID, "minutes": 5}, banAdminHeaders)
			if rec.Code != 200 {
				t.Fatal("ban failed")
			}
			frames := make([]sioFrame, 3)
			for i := range frames {
				select {
				case frames[i] = <-target.out:
				default:
					t.Fatal("missing push")
				}
			}
			want := "452-"
			if namespace != "" {
				want += namespace + ","
			}
			want += `[{"_placeholder":true,"num":0},{"_placeholder":true,"num":1}]`
			if frames[0].text != want {
				t.Fatalf("wrong namespace header: %q", frames[0].text)
			}
			past := time.Now().Add(-time.Second)
			if err := s.st.AddBan(store.Ban{Kind: store.BanPlayer, Subject: a.PlayerID, Until: &past}); err != nil {
				t.Fatal(err)
			}
			if _, banned := s.socketBan(target); banned {
				t.Fatal("expired ban blocks existing socket")
			}
			reply := s.answerAuth(target, sioHead{"uniq_id": a.PlayerID, "device": a.SocketDevice}, 2)
			if _, denied := reply["err_code"]; denied {
				t.Fatal("expired ban rejects auth")
			}
		})
	}
}
func TestGenericDeviceAndIPBansIsolateLiveSessions(t *testing.T) {
	for _, kind := range []string{store.BanDevice, store.BanIP} {
		t.Run(kind, func(t *testing.T) {
			s, _ := newTestServer(t)
			a := banAccount(t, s)
			target := addReloginTestSession(t, s, a.PlayerID, a.SocketDevice, &testReloginTransport{})
			target.remoteIP = a.LastIP
			foreign := addReloginTestSession(t, s, "987654321", "foreign", &testReloginTransport{})
			foreign.remoteIP = "192.0.2.99"
			subject := a.SocketDevice
			if kind == store.BanIP {
				subject = a.LastIP
			}
			rec := do(t, s, http.MethodPost, "/api/v1/admin/bans", map[string]any{"kind": kind, "subject": subject}, banAdminHeaders)
			if rec.Code != 200 {
				t.Fatal("ban failed")
			}
			readBanEvent(t, target)
			if len(foreign.out) != 0 {
				t.Fatal("foreign session revoked")
			}
			if _, banned := s.socketBan(target); !banned {
				t.Fatal("target not blocked")
			}
			rec = do(t, s, http.MethodDelete, "/api/v1/admin/bans?kind="+kind+"&subject="+url.QueryEscape(subject), nil, banAdminHeaders)
			if rec.Code != 200 {
				t.Fatal("unban failed")
			}
			if _, banned := s.socketBan(target); banned {
				t.Fatal("unban not effective on existing session")
			}
		})
	}
}

func TestFailedBanCommitNeverRevokesOnlineSessions(t *testing.T) {
	s, _ := newTestServer(t)
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.st = st
	a := banAccount(t, s)
	transport := &testReloginTransport{}
	target := addReloginTestSession(t, s, a.PlayerID, a.SocketDevice, transport)
	profile, _ := s.st.Profile(a.PlayerID)
	path := filepath.Join(dir, "state.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	rec := do(t, s, http.MethodPost, "/api/v1/admin/players/"+a.PlayerID+"/ban", map[string]any{"minutes": 5}, banAdminHeaders)
	if rec.Code != 500 {
		t.Fatalf("failed persist reported %d", rec.Code)
	}
	if len(s.st.Bans()) != 0 || len(target.out) != 0 || transport.aborts != 0 {
		t.Fatal("failed durable commit revoked a session")
	}
	after, _ := s.st.Profile(a.PlayerID)
	if !reflect.DeepEqual(profile, after) {
		t.Fatal("failed ban touched progress")
	}
}
