// SPDX-License-Identifier: GPL-3.0-or-later

package sio

import (
	"bytes"
	"testing"
)

func blob(fill byte, n int) []byte { return bytes.Repeat([]byte{fill}, n) }

// decoySalts returns a pool of wrong salts with the real one buried inside, so
// the sweep has to actually search rather than get lucky on the first entry.
func decoySalts(real []byte) [][]byte {
	pool := [][]byte{blob(0x00, SaltLen), blob(0x11, SaltLen), blob(0x22, SaltLen)}
	pool = append(pool, real)
	return append(pool, blob(0x33, SaltLen), blob(0x44, SaltLen))
}

// TestSearchFindsStaticPair covers the build-time-constant case: both halves
// come from the client metadata.
func TestSearchFindsStaticPair(t *testing.T) {
	salt := []byte("realsalt")
	left, right := blob(0xa1, PasswordLen), blob(0x5e, PasswordLen)

	password, err := Password(left, right)
	if err != nil {
		t.Fatalf("Password: %v", err)
	}
	codec, err := New(password, salt, ModeCFB8)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hits, stats, err := Search(SearchInput{
		Sample: Sample{Ciphertext: codec.Encrypt([]byte("auth_v2")), Plaintext: []byte("auth_v2")},
		Salts:  decoySalts(salt),
		Halves: [][]byte{blob(0x01, PasswordLen), left, blob(0x02, PasswordLen), right},
		Pairs:  true,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if stats.Trials == 0 {
		t.Fatal("sweep did no work")
	}
	if len(hits) == 0 {
		t.Fatal("sweep did not recover the static pair")
	}

	var found bool
	for _, hit := range hits {
		if hit.Recipe == RecipeStaticPair && hit.Mode == ModeCFB8 && bytes.Equal(hit.Salt, salt) && bytes.Equal(hit.Password, password) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no hit matched the planted key; got %v", hits)
	}
}

// TestSearchFindsSessionRecipe covers the case the capture points at: one half
// is a build-time constant and the other is the per-session token, which is
// why the same event encrypts differently in every session.
func TestSearchFindsSessionRecipe(t *testing.T) {
	salt := []byte("saltsalt")
	static := blob(0x7c, PasswordLen)
	session := blob(0x3d, PasswordLen)

	password, err := Password(static, session)
	if err != nil {
		t.Fatalf("Password: %v", err)
	}
	codec, err := New(password, salt, ModeCFB128)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sample := Sample{
		Ciphertext: codec.Encrypt([]byte("auth_v2")),
		Plaintext:  []byte("auth_v2"),
		Session:    session,
	}
	hits, _, err := Search(SearchInput{
		Sample: sample,
		Salts:  decoySalts(salt),
		Halves: [][]byte{blob(0x09, PasswordLen), static},
		Modes:  []Mode{ModeCFB128},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	var hit *Candidate
	for i := range hits {
		if hits[i].Recipe == RecipeStaticSession {
			hit = &hits[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("sweep did not recover the session recipe; got %v", hits)
	}

	// A second session must verify against the same static half, which is
	// the property that separates a real key from a coincidence.
	secondSession := blob(0x18, PasswordLen)
	secondPassword, err := Password(static, secondSession)
	if err != nil {
		t.Fatalf("Password: %v", err)
	}
	secondCodec, err := New(secondPassword, salt, ModeCFB128)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	second := Sample{
		Ciphertext: secondCodec.Encrypt([]byte("auth_v2")),
		Plaintext:  []byte("auth_v2"),
		Session:    secondSession,
	}

	matched, err := Verify(*hit, []Sample{sample, second})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if matched != 2 {
		t.Fatalf("Verify matched %d/2 samples", matched)
	}
}

// TestSearchRejectsBadInput keeps the sweep from silently doing nothing.
func TestSearchRejectsBadInput(t *testing.T) {
	good := Sample{Ciphertext: make([]byte, 16), Plaintext: []byte("auth_v2")}

	if _, _, err := Search(SearchInput{Sample: good}); err == nil {
		t.Fatal("Search accepted an empty salt pool")
	}
	if _, _, err := Search(SearchInput{
		Sample: Sample{Ciphertext: make([]byte, 17), Plaintext: []byte("x")},
		Salts:  [][]byte{blob(0, SaltLen)},
	}); err == nil {
		t.Fatal("Search accepted an unaligned ciphertext")
	}
	if _, _, err := Search(SearchInput{
		Sample: Sample{Ciphertext: make([]byte, 16)},
		Salts:  [][]byte{blob(0, SaltLen)},
	}); err == nil {
		t.Fatal("Search accepted a missing plaintext")
	}
	// Salts present but no usable halves and no session: nothing to try.
	if _, _, err := Search(SearchInput{Sample: good, Salts: [][]byte{blob(0, SaltLen)}}); err == nil {
		t.Fatal("Search accepted an empty password pool")
	}
}

func TestDedupeBlobs(t *testing.T) {
	in := [][]byte{{0x02}, {0x01}, {0x02}, {0x01}, {0x03}}
	got := DedupeBlobs(in)
	if len(got) != 3 {
		t.Fatalf("DedupeBlobs returned %d blobs, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if bytes.Compare(got[i-1], got[i]) >= 0 {
			t.Fatalf("DedupeBlobs output is not sorted: %v", got)
		}
	}
}
