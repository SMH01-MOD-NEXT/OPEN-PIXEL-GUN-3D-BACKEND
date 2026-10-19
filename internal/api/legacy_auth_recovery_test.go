// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"net/http"
	"net/url"
	"testing"
)

func TestLegacyAuthBootContractPreservesBootstrap(t *testing.T) {
	s, _ := newTestServer(t)
	bootstrap := decode(t, do(t, s, http.MethodPost, "/api/v1/accounts/register", map[string]string{"device_id": "recovery-device"}, nil))
	q := url.Values{"opg3d_player": {bootstrap["player_id"].(string)}, "opg3d_session": {bootstrap["session"].(string)}, "id_player": {"1"}}
	for i := 0; i < 2; i++ {
		rec := do(t, s, http.MethodGet, "http://192.0.2.20:8080/@server-v2.pixelgun3dserver.com/auth_v2/?"+q.Encode(), nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("auth status=%d", rec.Code)
		}
		body := decode(t, rec)
		if body["player_id"] != bootstrap["player_id"] || body["token"] != bootstrap["session"] {
			t.Fatal("bootstrap identity/session changed")
		}
		if _, exists := body["err_code"]; exists {
			t.Fatal("success must omit err_code")
		}
		u, err := url.Parse(body["url"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if u.Scheme != "http" || u.Host != "192.0.2.20:8080" || u.Path != "/socket.io/" {
			t.Fatalf("socket origin/path=%s://%s%s", u.Scheme, u.Host, u.Path)
		}
		if u.Query().Get("opg3d_player") != bootstrap["player_id"] || u.Query().Get("opg3d_session") != bootstrap["session"] {
			t.Fatal("socket bootstrap credentials missing")
		}
		if body["wait"] != float64(0) || body["mm_url"] != "" {
			t.Fatal("stock callback defaults missing")
		}
	}
	if len(s.st.Accounts()) != 1 {
		t.Fatal("duplicate account created by legacy auth")
	}
	if _, ok := s.st.Authenticate(q.Get("opg3d_player"), q.Get("opg3d_session"), ""); !ok {
		t.Fatal("native session invalidated")
	}
}

func TestLegacyAuthRejectsInvalidBootstrap(t *testing.T) {
	for _, query := range []string{"opg3d_player=12345", "opg3d_session=bad", "opg3d_player=12345&opg3d_session=bad"} {
		s, _ := newTestServer(t)
		rec := do(t, s, http.MethodGet, "/auth_v2/?"+query, nil, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d, want401", rec.Code)
		}
		if len(s.st.Accounts()) != 0 {
			t.Fatal("invalid bootstrap created account")
		}
	}
}
