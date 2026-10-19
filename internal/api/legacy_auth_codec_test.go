// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestLegacyAuthBinaryFixture(t *testing.T) {
	// Independent hand-encoded pair: key="url", value="x", shift=37.
	// The final value byte count is four (UTF-16 x + two-byte trailer).
	raw, _ := hex.DecodeString("060004009a00970091009d002500")
	fields, err := decodeLegacyAuth(raw)
	if err != nil || !reflect.DeepEqual(fields, map[string]string{"url": "x"}) {
		t.Fatalf("fixture decode failed: %v", err)
	}
	got, err := encodeLegacyAuth(fields, 37)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("fixture encoding mismatch")
	}
}

func TestLegacyAuthUnicodeAndMalformed(t *testing.T) {
	fields := map[string]string{"url": "http://192.0.2.20:8080/socket.io/", "empty": "", "nick": "Игрок 🐈"}
	for _, shift := range []byte{0, 1, 37, 255} {
		raw, err := encodeLegacyAuth(fields, shift)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeLegacyAuth(raw)
		if err != nil || !reflect.DeepEqual(got, fields) {
			t.Fatal("round trip failed")
		}
	}
	for _, raw := range [][]byte{nil, {0, 0}, {2, 0, 2, 0, 0, 0}, {255, 127, 0, 0, 0, 0}, make([]byte, maxLegacyAuthBody+2)} {
		if _, err := decodeLegacyAuth(raw); err == nil {
			t.Fatal("malformed packet accepted")
		}
	}
}

func TestLegacyAuthBinaryRequestReachesSocketContract(t *testing.T) {
	s, _ := newTestServer(t)
	account, _, err := s.st.RegisterOrLogin("native-device", "Tester", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	body, err := encodeLegacyAuth(map[string]string{"device_id": "managed-device", "uniq_id": "", "version": "23.1.3"}, 77)
	if err != nil {
		t.Fatal(err)
	}
	q := url.Values{"opg3d_player": {account.PlayerID}, "opg3d_session": {account.Session}}
	req := httptest.NewRequest(http.MethodPost, "http://192.0.2.20:8080/@server-v2.pixelgun3dserver.com/auth_v2/?"+q.Encode(), bytes.NewReader(body))
	req.RemoteAddr = "192.0.2.10:5555"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("status/type=%d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	fields, err := decodeLegacyAuth(rec.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if fields["player_id"] != account.PlayerID || fields["token"] != account.Session || fields["wait"] != "0" {
		t.Fatal("binary auth contract mismatch")
	}
	if _, present := fields["err_code"]; present {
		t.Fatal("err_code would select error branch")
	}
	if len(s.st.Accounts()) != 1 {
		t.Fatal("managed auth minted duplicate account")
	}
	for _, entry := range s.log.Recent(0) {
		if entry.Kind == "http.response" && (strings.Contains(entry.Line, account.Session) || strings.Contains(entry.Line, "body=hex:")) {
			t.Fatal("auth reply reached trace")
		}
	}
}

func FuzzDecodeLegacyAuth(f *testing.F) {
	seed, _ := encodeLegacyAuth(map[string]string{"url": "test", "wait": "0"}, 37)
	f.Add(seed)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		fields, err := decodeLegacyAuth(raw)
		if err == nil {
			encoded, err := encodeLegacyAuth(fields, 37)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := decodeLegacyAuth(encoded)
			if err != nil || !reflect.DeepEqual(fields, decoded) {
				t.Fatal("round trip mismatch")
			}
		}
	})
}
