// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

func TestAnswerAuthCorrelatesUniqueRecentHTTPAccount(t *testing.T) {
	s, _ := newTestServer(t)
	bootstrap, _, err := s.st.RegisterOrLogin("native-device", "Bootstrap", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}

	session := &sioSession{sid: "direct-correlation"}
	session.setRemoteIP("192.0.2.10")
	body := s.answerAuth(session, sioHead{
		"uniq_id": "612672163",
		"device":  "socket-device",
	}, 3)
	if body["player_id"] != "612672163" || body["error"] != sioErrNone {
		t.Fatalf("auth reply = %#v", body)
	}
	if got := len(s.st.Accounts()); got != 1 {
		t.Fatalf("accounts=%d, want one reconciled account", got)
	}
	canonical, ok := s.st.ResolvePlayerID(bootstrap.PlayerID)
	if !ok || canonical != "612672163" {
		t.Fatalf("HTTP id resolves to %q, %v", canonical, ok)
	}
	_, _, bound := session.identity()
	if bound != "612672163" {
		t.Fatalf("session bound to %q", bound)
	}
	account, _ := s.st.Account("612672163")
	if account.DeviceID != "native-device" || account.SocketDevice != "socket-device" {
		t.Fatalf("identity fingerprints were not preserved: %#v", account)
	}
}

func TestAnswerAuthDoesNotGuessBetweenRecentHTTPAccounts(t *testing.T) {
	s, _ := newTestServer(t)
	for _, device := range []string{"native-one", "native-two"} {
		if _, _, err := s.st.RegisterOrLogin(device, device, "192.0.2.10"); err != nil {
			t.Fatal(err)
		}
	}

	session := &sioSession{sid: "ambiguous-correlation"}
	session.setRemoteIP("192.0.2.10")
	body := s.answerAuth(session, sioHead{
		"uniq_id": "612672163",
		"device":  "socket-device",
	}, 4)
	if body["player_id"] != "612672163" || body["error"] != sioErrNone {
		t.Fatalf("auth reply = %#v", body)
	}
	if got := len(s.st.Accounts()); got != 3 {
		t.Fatalf("accounts=%d, want two untouched HTTP accounts plus game account", got)
	}
	if aliases := s.st.Aliases("612672163"); len(aliases) != 0 {
		t.Fatalf("ambiguous HTTP account was guessed: %v", aliases)
	}
	_, _, bound := session.identity()
	if bound != "612672163" {
		t.Fatalf("session bound to %q", bound)
	}
}

func TestAnswerAuthFirstRunReusesUniqueHTTPAccount(t *testing.T) {
	s, _ := newTestServer(t)
	bootstrap, _, err := s.st.RegisterOrLogin("native-first-run", "Bootstrap", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}

	session := &sioSession{sid: "first-run-correlation"}
	session.setRemoteIP("192.0.2.10")
	body := s.answerAuth(session, sioHead{
		"uniq_id": "",
		"device":  "socket-first-run",
	}, 5)
	if body["player_id"] != bootstrap.PlayerID || body["uniq_id"] != bootstrap.PlayerID {
		t.Fatalf("first run minted or returned another id: %#v", body)
	}
	if got := len(s.st.Accounts()); got != 1 {
		t.Fatalf("accounts=%d, want one reused account", got)
	}
}
