// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"encoding/hex"
	"testing"
)

// TestDefaultSessionsRotate pins the production behaviour: every login mints a
// fresh token. The diagnostic below is only meaningful because this is true.
func TestDefaultSessionsRotate(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	first, created, err := s.RegisterOrLogin("device-a", "", "127.0.0.1")
	if err != nil {
		t.Fatalf("RegisterOrLogin: %v", err)
	}
	if !created {
		t.Fatal("first login did not create the account")
	}
	second, created, err := s.RegisterOrLogin("device-a", "", "127.0.0.1")
	if err != nil {
		t.Fatalf("RegisterOrLogin: %v", err)
	}
	if created {
		t.Fatal("second login created a duplicate account")
	}
	if first.Session == second.Session {
		t.Fatal("session token did not rotate on re-login")
	}
}

// TestStableSessionsRepeat covers the diagnostic: with it on, the same device
// must be handed the same token by every login and by a freshly opened store,
// which is what makes two game runs comparable.
func TestStableSessionsRepeat(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenWithOptions(dir, Options{StableSessions: true})
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}

	first, _, err := s.RegisterOrLogin("device-a", "", "127.0.0.1")
	if err != nil {
		t.Fatalf("RegisterOrLogin: %v", err)
	}
	second, _, err := s.RegisterOrLogin("device-a", "", "127.0.0.1")
	if err != nil {
		t.Fatalf("RegisterOrLogin: %v", err)
	}
	if first.Session != second.Session {
		t.Fatalf("stable session changed between logins: %s vs %s", first.Session, second.Session)
	}

	// A server restart must not change it either.
	reopened, err := OpenWithOptions(dir, Options{StableSessions: true})
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	third, _, err := reopened.RegisterOrLogin("device-a", "", "127.0.0.1")
	if err != nil {
		t.Fatalf("RegisterOrLogin: %v", err)
	}
	if third.Session != first.Session {
		t.Fatalf("stable session changed across a restart: %s vs %s", third.Session, first.Session)
	}

	// Different devices must still get different tokens, otherwise the
	// diagnostic would make every account share one credential.
	other, _, err := s.RegisterOrLogin("device-b", "", "127.0.0.1")
	if err != nil {
		t.Fatalf("RegisterOrLogin: %v", err)
	}
	if other.Session == first.Session {
		t.Fatal("two devices were handed the same stable session token")
	}
}

// TestStableSessionShape keeps the diagnostic token indistinguishable in shape
// from a minted one: 32 bytes, hex encoded. Anything else would change a second
// variable and spoil the experiment.
func TestStableSessionShape(t *testing.T) {
	token := stableSession("45fdd0566d816ff910dc99a9ea672134")
	if len(token) != 64 {
		t.Fatalf("token length = %d, want 64", len(token))
	}
	raw, err := hex.DecodeString(token)
	if err != nil {
		t.Fatalf("token is not hex: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("decoded token = %d bytes, want 32", len(raw))
	}
	if stableSession("device-a") == stableSession("device-b") {
		t.Fatal("stableSession collided on different device ids")
	}
}

// TestStableSessionAuthenticates makes sure the diagnostic token still works as
// a credential, so the rest of the boot flow is unaffected by it.
func TestStableSessionAuthenticates(t *testing.T) {
	s, err := OpenWithOptions(t.TempDir(), Options{StableSessions: true})
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	account, _, err := s.RegisterOrLogin("device-a", "", "127.0.0.1")
	if err != nil {
		t.Fatalf("RegisterOrLogin: %v", err)
	}
	if _, ok := s.Authenticate(account.PlayerID, account.Session, "127.0.0.1"); !ok {
		t.Fatal("a stable session token failed to authenticate")
	}
	if _, ok := s.Authenticate(account.PlayerID, "not-the-token", "127.0.0.1"); ok {
		t.Fatal("authentication accepted a wrong token")
	}
}
