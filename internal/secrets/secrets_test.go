// SPDX-License-Identifier: GPL-3.0-or-later

package secrets

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	key, err := DeriveKey("passphrase")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	envelope, err := Seal(key, []byte("photon-app-id"), []byte("123456789|pun"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if strings.Contains(envelope.Ciphertext, "photon") {
		t.Fatal("plaintext visible in ciphertext")
	}
	plain, err := Open(key, envelope)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(plain) != "photon-app-id" {
		t.Fatalf("plain = %q", plain)
	}
}

func TestTamperedAADFails(t *testing.T) {
	key, err := DeriveKey("passphrase")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	envelope, err := Seal(key, []byte("secret"), []byte("111111111|pun"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	envelope.AAD = "222222222|pun"
	if _, err := Open(key, envelope); err == nil {
		t.Fatal("an envelope minted for another player was accepted")
	}
}

func TestDeriveKeyAcceptsHexAndRejectsEmpty(t *testing.T) {
	raw := strings.Repeat("ab", 32)
	key, err := DeriveKey(raw)
	if err != nil {
		t.Fatalf("derive hex: %v", err)
	}
	if hex.EncodeToString(key) != raw {
		t.Fatalf("hex key was not used verbatim: %x", key)
	}
	if _, err := DeriveKey("   "); err == nil {
		t.Fatal("empty key material was accepted")
	}
}

func TestUnsupportedAlgIsRejected(t *testing.T) {
	key, err := DeriveKey("passphrase")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if _, err := Open(key, Envelope{Alg: "ROT13"}); err == nil {
		t.Fatal("unsupported algorithm was accepted")
	}
}
