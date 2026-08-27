// SPDX-License-Identifier: GPL-3.0-or-later

// Package secrets holds the authenticated-encryption primitives used to hand
// credentials (currently the Photon AppIDs) to the game client.
//
// SECURITY: the AppID never travels in clear text and is never written to a
// log. It is sealed with AES-256-GCM under a key that only the server and the
// shipped client know, and the player id is bound in as additional
// authenticated data, so an envelope minted for one account cannot be replayed
// for another.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Alg is the wire name of the envelope algorithm. The client refuses anything
// it does not implement instead of guessing.
const Alg = "AES-256-GCM"

var (
	// ErrNoKey is returned when no key material was configured.
	ErrNoKey = errors.New("secrets: no key material configured")
	// ErrAlg is returned when an envelope names an unsupported algorithm.
	ErrAlg = errors.New("secrets: unsupported envelope algorithm")
)

// Envelope is the JSON shape returned to the client.
type Envelope struct {
	Alg        string `json:"alg"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
	AAD        string `json:"aad"`
}

// DeriveKey turns configured key material into a 32-byte AES key. 64 hex
// characters are used verbatim; anything else is treated as a passphrase and
// hashed, so a short human-typed secret still produces a full-length key.
func DeriveKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrNoKey
	}
	if len(raw) == 2*sha256.Size {
		if key, err := hex.DecodeString(raw); err == nil {
			return key, nil
		}
	}
	sum := sha256.Sum256([]byte(raw))
	return sum[:], nil
}

// Seal encrypts plaintext under key, binding aad into the authentication tag.
func Seal(key, plaintext, aad []byte) (Envelope, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Envelope{}, fmt.Errorf("secrets: nonce: %w", err)
	}
	return Envelope{
		Alg:        Alg,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, plaintext, aad)),
		AAD:        string(aad),
	}, nil
}

// Open reverses Seal. It exists for tests and for the reference client
// implementation; the server itself never needs it.
func Open(key []byte, env Envelope) ([]byte, error) {
	if env.Alg != Alg {
		return nil, ErrAlg
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, fmt.Errorf("secrets: nonce: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("secrets: ciphertext: %w", err)
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, errors.New("secrets: bad nonce size")
	}
	return gcm.Open(nil, nonce, ct, []byte(env.AAD))
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != sha256.Size {
		return nil, ErrNoKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: gcm: %w", err)
	}
	return gcm, nil
}

// RandomToken returns n random bytes as lowercase hex.
func RandomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
