// SPDX-License-Identifier: GPL-3.0-or-later

package sio

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"
)

// Recipe names how a candidate password was assembled. Knowing which recipe
// won matters as much as the key itself: it tells us whether the password is a
// build-time constant or is mixed with a per-session value.
const (
	RecipeStaticPair    = "staticA^staticB"
	RecipeStaticOnly    = "staticA"
	RecipeSessionOnly   = "session"
	RecipeStaticSession = "staticA^session"
)

// Candidate is one password/salt/mode combination that decrypts the sample to
// the expected plaintext.
type Candidate struct {
	Recipe   string
	Mode     Mode
	Salt     []byte
	Password []byte
	Key      []byte
	IV       []byte
	// Left and Right are the halves that produced Password, when applicable.
	Left  []byte
	Right []byte
}

// String renders a candidate for a log line or console output.
func (c Candidate) String() string {
	return fmt.Sprintf("recipe=%s mode=%s salt=%x password=%x key=%x iv=%x",
		c.Recipe, c.Mode, c.Salt, c.Password, c.Key, c.IV)
}

// Sample is one captured attachment together with the plaintext it must
// decrypt to. Attachment 0 of every outgoing event is Encrypt(UTF8(eventName)),
// which makes it an exact known-plaintext oracle.
type Sample struct {
	// Ciphertext is the captured attachment, padding included.
	Ciphertext []byte
	// Plaintext is what it must decrypt to, for example []byte("auth_v2").
	Plaintext []byte
	// Session is the raw session token handed to the client before this
	// capture, or nil when the capture predates session tracking. Only used
	// by the session-based recipes.
	Session []byte
}

// SearchInput configures a sweep.
type SearchInput struct {
	// Sample is the capture to crack.
	Sample Sample
	// Salts are candidate PBKDF2 salts, normally every 8-byte constant
	// found in the client metadata.
	Salts [][]byte
	// Halves are candidate static password halves, normally every 32-byte
	// constant found in the client metadata.
	Halves [][]byte
	// Modes limits the chaining variants tried. Empty means all of them.
	Modes []Mode
	// Pairs enables the quadratic staticA^staticB sweep. It is the most
	// expensive recipe, so callers can switch it off while probing the
	// cheaper session-based ones.
	Pairs bool
	// StopAtFirst returns as soon as one candidate matches.
	StopAtFirst bool
}

// Stats reports how much work a sweep did.
type Stats struct {
	Passwords   int
	Salts       int
	Derivations int
	Trials      int
}

type labelledPassword struct {
	bytes  []byte
	recipe string
	left   []byte
	right  []byte
}

// Search sweeps every password recipe, salt and mode and returns the
// combinations that decrypt the sample to the expected plaintext.
//
// The key schedule depends only on the password and salt, so the derivation is
// done once per pair and then reused across modes.
func Search(in SearchInput) ([]Candidate, Stats, error) {
	if len(in.Sample.Ciphertext) == 0 || len(in.Sample.Ciphertext)%BlockLen != 0 {
		return nil, Stats{}, fmt.Errorf("%w: %d bytes", ErrCiphertextSize, len(in.Sample.Ciphertext))
	}
	if len(in.Sample.Plaintext) == 0 {
		return nil, Stats{}, fmt.Errorf("sio: search needs the expected plaintext")
	}
	if len(in.Salts) == 0 {
		return nil, Stats{}, fmt.Errorf("sio: search needs at least one candidate salt")
	}

	modes := in.Modes
	if len(modes) == 0 {
		modes = Modes()
	}
	passwords := buildPasswords(in)
	if len(passwords) == 0 {
		return nil, Stats{}, fmt.Errorf("sio: search produced no candidate passwords")
	}

	stats := Stats{Passwords: len(passwords), Salts: len(in.Salts)}
	var hits []Candidate

	for _, salt := range in.Salts {
		for _, pw := range passwords {
			derived := PBKDF2SHA1(pw.bytes, salt, Iterations, DerivedLen)
			stats.Derivations++
			key, iv := derived[:KeyLen], derived[KeyLen:]

			for _, mode := range modes {
				stats.Trials++
				codec, err := NewFromKeyIV(key, iv, mode)
				if err != nil {
					return nil, stats, err
				}
				plain, err := codec.Decrypt(in.Sample.Ciphertext)
				if err != nil || !bytes.Equal(plain, in.Sample.Plaintext) {
					continue
				}
				hits = append(hits, Candidate{
					Recipe:   pw.recipe,
					Mode:     mode,
					Salt:     append([]byte(nil), salt...),
					Password: append([]byte(nil), pw.bytes...),
					Key:      append([]byte(nil), key...),
					IV:       append([]byte(nil), iv...),
					Left:     pw.left,
					Right:    pw.right,
				})
				if in.StopAtFirst {
					return hits, stats, nil
				}
			}
		}
	}
	return hits, stats, nil
}

// buildPasswords assembles the candidate passwords. get_Extras XORs two
// byte[32] arrays, so every password is exactly PasswordLen bytes; anything
// else would not match the disassembly.
func buildPasswords(in SearchInput) []labelledPassword {
	seen := map[string]bool{}
	var out []labelledPassword

	add := func(pw []byte, recipe string, left, right []byte) {
		if len(pw) != PasswordLen {
			return
		}
		key := recipe + "|" + hex.EncodeToString(pw)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, labelledPassword{bytes: pw, recipe: recipe, left: left, right: right})
	}

	halves := make([][]byte, 0, len(in.Halves))
	for _, half := range in.Halves {
		if len(half) == PasswordLen {
			halves = append(halves, half)
		}
	}

	// A lone static array covers the case where the second half is all zeros.
	for _, half := range halves {
		add(append([]byte(nil), half...), RecipeStaticOnly, half, nil)
	}

	session := in.Sample.Session
	if len(session) == PasswordLen {
		add(append([]byte(nil), session...), RecipeSessionOnly, nil, session)
		for _, half := range halves {
			add(xor(half, session), RecipeStaticSession, half, session)
		}
	}

	if in.Pairs {
		for i := 0; i < len(halves); i++ {
			for j := i + 1; j < len(halves); j++ {
				add(xor(halves[i], halves[j]), RecipeStaticPair, halves[i], halves[j])
			}
		}
	}
	return out
}

func xor(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// Verify re-checks a candidate against further captures. A recipe that only
// fits one sample is a coincidence; one that fits several is the real key.
// Session-based recipes are re-derived per sample, because the session changes
// between captures.
func Verify(c Candidate, samples []Sample) (int, error) {
	matched := 0
	for i, sample := range samples {
		password := c.Password
		switch c.Recipe {
		case RecipeSessionOnly:
			if len(sample.Session) != PasswordLen {
				continue
			}
			password = sample.Session
		case RecipeStaticSession:
			if len(sample.Session) != PasswordLen || len(c.Left) != PasswordLen {
				continue
			}
			password = xor(c.Left, sample.Session)
		}

		key, iv := DeriveKeyIV(password, c.Salt)
		codec, err := NewFromKeyIV(key, iv, c.Mode)
		if err != nil {
			return matched, fmt.Errorf("sio: verify sample %d: %w", i, err)
		}
		plain, err := codec.Decrypt(sample.Ciphertext)
		if err != nil || !bytes.Equal(plain, sample.Plaintext) {
			continue
		}
		matched++
	}
	return matched, nil
}

// DedupeBlobs drops duplicates and sorts, so a candidate list assembled from
// several passes over the client metadata does not inflate the sweep.
func DedupeBlobs(blobs [][]byte) [][]byte {
	seen := map[string]bool{}
	out := make([][]byte, 0, len(blobs))
	for _, blob := range blobs {
		key := hex.EncodeToString(blob)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, blob)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i], out[j]) < 0 })
	return out
}
