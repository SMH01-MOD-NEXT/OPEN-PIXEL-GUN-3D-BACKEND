// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"github.com/fxamacker/cbor/v2"
	"net/http"
	"testing"
)

func b64Hub(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func hubOptions(t *testing.T, wBody []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if json.Unmarshal(wBody, &out) != nil {
		t.Fatal("options JSON")
	}
	p, ok := out["publicKey"].(map[string]any)
	if !ok {
		t.Fatal("missing publicKey")
	}
	return p
}
func TestHubRealPasskeyRegistrationAndSignedLogin(t *testing.T) {
	s := hubTest(t)
	seedSocketAccount(t, s, "123456789", "owner")
	a, _ := s.st.Account("123456789")
	login, _ := s.webAuth.Issue(webIdentity(a))
	session := &http.Cookie{Name: sessionCookie, Value: login.Token}
	w := hubCall(t, s, "POST", "/api/v1/hub/auth/passkey/register/begin", map[string]any{}, login.CSRF, session)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	options := hubOptions(t, w.Body.Bytes())
	flow := namedCookie(t, w, hubFlowCookie)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	credentialID := make([]byte, 32)
	rand.Read(credentialID)
	cose, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: key.X.FillBytes(make([]byte, 32)), -3: key.Y.FillBytes(make([]byte, 32))})
	rp := sha256.Sum256([]byte("game.example"))
	authData := append([]byte{}, rp[:]...)
	authData = append(authData, 0x45, 0, 0, 0, 0)
	authData = append(authData, make([]byte, 16)...)
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(credentialID)))
	authData = append(authData, credentialID...)
	authData = append(authData, cose...)
	attestation, _ := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
	client, _ := json.Marshal(map[string]string{"type": "webauthn.create", "challenge": options["challenge"].(string), "origin": "https://game.example"})
	registration := map[string]any{"id": b64Hub(credentialID), "rawId": b64Hub(credentialID), "type": "public-key", "response": map[string]any{"clientDataJSON": b64Hub(client), "attestationObject": b64Hub(attestation), "transports": []string{"internal"}}, "clientExtensionResults": map[string]any{}}
	w = hubCall(t, s, "POST", "/api/v1/hub/auth/passkey/register/finish", registration, login.CSRF, session, flow)
	if w.Code != 200 {
		t.Fatal("valid registration", w.Code, w.Body.String())
	}
	if len(s.hubSecurity.Get(a.PlayerID, webIdentity(a).Fingerprint).Keys) != 1 {
		t.Fatal("key not durable")
	}
	if hubCall(t, s, "GET", "/api/v1/hub/account", nil, "", session).Code != 401 {
		t.Fatal("old session not revoked")
	}
	handle := options["user"].(map[string]any)["id"].(string)
	assertion := func(challenge, origin string, flags byte) map[string]any {
		client, _ := json.Marshal(map[string]string{"type": "webauthn.get", "challenge": challenge, "origin": origin})
		ad := append([]byte{}, rp[:]...)
		ad = append(ad, flags, 0, 0, 0, 1)
		clientHash := sha256.Sum256(client)
		message := append(append([]byte{}, ad...), clientHash[:]...)
		digest := sha256.Sum256(message)
		sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
		return map[string]any{"id": b64Hub(credentialID), "rawId": b64Hub(credentialID), "type": "public-key", "response": map[string]any{"authenticatorData": b64Hub(ad), "clientDataJSON": b64Hub(client), "signature": b64Hub(sig), "userHandle": handle}, "clientExtensionResults": map[string]any{}}
	}
	for _, tc := range []struct {
		origin string
		flags  byte
		status int
	}{{"https://evil.example", 5, 401}, {"https://game.example", 1, 401}, {"https://game.example", 5, 200}} {
		w = hubCall(t, s, "POST", "/api/v1/hub/auth/passkey/login/begin", map[string]any{}, "")
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		challenge := hubOptions(t, w.Body.Bytes())["challenge"].(string)
		flow = namedCookie(t, w, hubFlowCookie)
		proof := assertion(challenge, tc.origin, tc.flags)
		w = hubCall(t, s, "POST", "/api/v1/hub/auth/passkey/login/finish", proof, "", flow)
		if w.Code != tc.status {
			t.Fatal("assertion", tc.origin, tc.flags, w.Code, w.Body.String())
		}
		if w.Code == 200 {
			full := namedCookie(t, w, sessionCookie)
			if hubCall(t, s, "GET", "/api/v1/hub/account", nil, "", full).Code != 200 {
				t.Fatal("direct passkey login")
			}
		}
		if hubCall(t, s, "POST", "/api/v1/hub/auth/passkey/login/finish", proof, "", flow).Code != 401 {
			t.Fatal("assertion replay")
		}
	}
}
