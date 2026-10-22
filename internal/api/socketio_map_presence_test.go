// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

const stockTestCatalog = `{"params":{"2":{"16.6.0mm_v2":{"0":["1001","1035"]}}}}`

func stockTestSession(t *testing.T, s *Server, id string) *sioSession {
	t.Helper()
	account, _, err := s.st.AdoptSocketIdentity(id, "map-device-"+id, "")
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.sio.create(eioV4)
	if err != nil {
		t.Fatal(err)
	}
	session.setIdentity(id, "map-device-"+id, account.PlayerID)
	s.retireOlderMapPresence(session)
	s.answerMapPopularity(session, popularityParams(t, stockTestCatalog), 1)
	return session
}
func stockTestJoin(s *Server, session *sioSession) {
	s.answerPlayerOnline(session, sioHead{"version": "23.1.3", "game_mode": "110002", "room_name": "real-online-room", "map": "1001", "protocol": "16.6.0mm_v2", "mmrank": float64(0)}, 2)
}
func stockTestCounts(t *testing.T, s *Server) mapPopularityData {
	t.Helper()
	return s.answerMapPopularity(nil, popularityParams(t, stockTestCatalog), 3)["data"].(mapPopularityData)
}
func stockTestWant(t *testing.T, s *Server, want int) {
	t.Helper()
	if got := stockTestCounts(t, s)["2"]["0"]["1001"]; got != want {
		t.Fatalf("count=%d want=%d", got, want)
	}
}
func TestStockMapPresenceTwoAccountsJoinMoveLeaveDrop(t *testing.T) {
	s, _ := newTestServer(t)
	a := stockTestSession(t, s, "123456701")
	b := stockTestSession(t, s, "123456702")
	before, _ := s.st.Profile(a.playerID)
	stockTestWant(t, s, 0)
	stockTestJoin(s, a)
	stockTestWant(t, s, 1)
	stockTestJoin(s, a)
	stockTestWant(t, s, 1) // repeated delta is not another actor
	stockTestJoin(s, b)
	stockTestWant(t, s, 2)
	s.answerPlayerOnline(a, sioHead{"rank": 65}, 4)
	stockTestWant(t, s, 2) // missing fields survive
	s.answerPlayerOnline(a, sioHead{"map": "1035", "room_name": "other-room"}, 5)
	got := stockTestCounts(t, s)
	if got["2"]["0"]["1001"] != 1 || got["2"]["0"]["1035"] != 1 {
		t.Fatalf("move=%v", got)
	}
	s.answerPlayerOnline(a, sioHead{"game_mode": "100006", "room_name": "", "map": "-1", "protocol": ""}, 6)
	stockTestWant(t, s, 1)
	s.sio.drop(b.sid)
	stockTestWant(t, s, 0)
	after, _ := s.st.Profile(a.playerID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("presence changed durable progress")
	}
}
func TestStockMapPresenceRejectsUnverifiedOrMalformedReports(t *testing.T) {
	cases := []struct {
		name  string
		patch sioHead
	}{
		{"offline", sioHead{"room_name": "offline"}}, {"offline-case", sioHead{"room_name": "OFFLINE"}},
		{"lobby", sioHead{"game_mode": "100006"}}, {"mode-only-campaign", sioHead{"game_mode": "100011"}},
		{"variant", sioHead{"game_mode": "110102"}}, {"other-platform", sioHead{"game_mode": "120002"}},
		{"negative-mode", sioHead{"game_mode": "-110002"}}, {"alias-mode", sioHead{"game_mode": "0110002"}},
		{"unmeasured-client", sioHead{"version": "99.0"}}, {"empty-protocol", sioHead{"protocol": ""}},
		{"unknown-protocol", sioHead{"protocol": "future"}}, {"empty-room", sioHead{"room_name": ""}},
		{"object-room", sioHead{"room_name": map[string]any{"x": 1}}}, {"long-room", sioHead{"room_name": strings.Repeat("x", 129)}},
		{"whitespace-room", sioHead{"room_name": " room "}}, {"control-room", sioHead{"room_name": "room\n"}},
		{"missing-map", sioHead{"map": "-1"}}, {"unknown-map", sioHead{"map": "9999"}},
		{"malformed-rank", sioHead{"mmrank": nil}}, {"ranked", sioHead{"mmrank": 1}},
		{"conditional-ranked", sioHead{"tier_mm": 1}},
		{"malformed-conditional-rank", sioHead{"tier_mm": nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServer(t)
			a := stockTestSession(t, s, "123456703")
			stockTestJoin(s, a)
			s.answerPlayerOnline(a, tc.patch, 3)
			stockTestWant(t, s, 0)
		})
	}
	s, _ := newTestServer(t)
	a := stockTestSession(t, s, "123456704")
	s.answerPlayerOnline(a, sioHead{"game_mode": "110002", "map": "1001", "room_name": "room", "protocol": "16.6.0mm_v2"}, 2)
	stockTestWant(t, s, 0) // without authenticated full version/rank, do not guess defaults
	unauth, _ := s.sio.create(eioV4)
	stockTestJoin(s, unauth)
	stockTestWant(t, s, 0)
	if unauth.mapPresence.fields != nil {
		t.Fatal("unauthenticated report retained")
	}
}
func TestStockMapPresenceCatalogAndProtocolIsolation(t *testing.T) {
	s, _ := newTestServer(t)
	a := stockTestSession(t, s, "123456705")
	stockTestJoin(s, a)
	for _, raw := range []string{
		`{"params":{"2":{"future":{"0":["1001"]}}}}`,
		`{"params":{"0":{"16.6.0mm_v2":{"0":["1001"]}}}}`,
		`{"params":{"2":{"16.6.0mm_v2":{"1":["1001"]}}}}`,
	} {
		body := s.answerMapPopularity(nil, popularityParams(t, raw), 8)
		for _, groups := range body["data"].(mapPopularityData) {
			for _, maps := range groups {
				for _, n := range maps {
					if n != 0 {
						t.Fatal("cross-partition leak")
					}
				}
			}
		}
	}
	// A three-level reply cannot distinguish two protocols for the same map.
	// Fail closed instead of combining incompatible pools.
	raw := `{"params":{"2":{"future":{"0":["1001"]},"16.6.0mm_v2":{"0":["1001"]}}}}`
	if n := s.answerMapPopularity(nil, popularityParams(t, raw), 9)["data"].(mapPopularityData)["2"]["0"]["1001"]; n != 0 {
		t.Fatalf("ambiguous protocol union=%d", n)
	}
	// Ambiguous source catalogs and platform group=1 must never produce counts.
	s.answerMapPopularity(a, popularityParams(t, `{"params":{"2":{"16.6.0mm_v2":{"0":["1001"],"101":["1001"]}}}}`), 10)
	stockTestWant(t, s, 0)
	s.answerMapPopularity(a, popularityParams(t, stockTestCatalog), 11)
	s.answerPlayerOnline(a, sioHead{"group": 1, "tier": 6}, 12)
	stockTestWant(t, s, 1)
	s.answerMapPopularity(a, popularityParams(t, `{}`), 13)
	stockTestWant(t, s, 1)
	s.answerMapPopularity(a, popularityParams(t, `{"params":{"2":{"v":{"0":"bad"}}}}`), 14)
	stockTestWant(t, s, 1)
	// A reconnect has no inherited catalog or in-match fields.
	a.setIdentity("different", "device", "123456799")
	if a.mapPresence.fields != nil || a.mapPresence.catalog != nil {
		t.Fatal("cross-identity telemetry retained")
	}
}
func TestStockMapPresenceExpiryBanAndNoReconnectResurrection(t *testing.T) {
	s, _ := newTestServer(t)
	a := stockTestSession(t, s, "123456706")
	stockTestJoin(s, a)
	a.mu.Lock()
	a.lastSeen = time.Now().Add(-stockMapPresenceLiveness - time.Second)
	a.mu.Unlock()
	stockTestWant(t, s, 0)
	a.touch()
	stockTestWant(t, s, 1)
	a.mu.Lock()
	a.lastSeen = time.Now().Add(time.Minute)
	a.mu.Unlock()
	stockTestWant(t, s, 0)
	a.touch()
	b := stockTestSession(t, s, "123456706")
	stockTestWant(t, s, 0) // new lobby replaces old match
	stockTestJoin(s, b)
	stockTestWant(t, s, 1)
	stockTestJoin(s, a)
	stockTestWant(t, s, 1) // older socket cannot revive itself
	s.sio.drop(b.sid)
	stockTestWant(t, s, 0)
	c := stockTestSession(t, s, "123456706")
	stockTestJoin(s, c)
	stockTestWant(t, s, 1)
	if err := s.st.AddBan(store.Ban{Kind: store.BanPlayer, Subject: c.playerID}); err != nil {
		t.Fatal(err)
	}
	stockTestWant(t, s, 0)
}
func TestStockMapPresenceConcurrentRequestsAndLifecycle(t *testing.T) {
	s, _ := newTestServer(t)
	a := stockTestSession(t, s, "123456707")
	stockTestJoin(s, a)
	// The test explicitly drives all operations sharing map fields under -race.
	head := popularityParams(t, stockTestCatalog)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				a.touch()
				s.answerPlayerOnline(a, sioHead{"room_name": fmt.Sprintf("room-%d", n)}, i)
				s.answerMapPopularity(a, head, i)
				s.retireOlderMapPresence(a)
			}
		}(worker)
	}
	wg.Wait()
	stockTestWant(t, s, 1)
}

func TestStockMapPresenceTransportCloseClearsOnlyItsOwner(t *testing.T) {
	s, _ := newTestServer(t)
	a := stockTestSession(t, s, "123456708")
	stockTestJoin(s, a)
	old, current := &testReloginTransport{}, &testReloginTransport{}
	a.attachReloginTransport(old)
	a.attachReloginTransport(current)
	a.detachReloginTransport(old)
	stockTestWant(t, s, 1)
	a.detachReloginTransport(current)
	stockTestWant(t, s, 0)
	stockTestJoin(s, a)
	stockTestWant(t, s, 1)
	a.attachReloginTransport(current)
	if a.abortReloginTransportForPlayer("unrelated") {
		t.Fatal("foreign abort accepted")
	}
	stockTestWant(t, s, 1)
	if !a.abortReloginTransportForPlayer(a.playerID) {
		t.Fatal("owner abort failed")
	}
	stockTestWant(t, s, 0)
	a.detachReloginTransport(current)
	stockTestWant(t, s, 0)
}
