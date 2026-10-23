// SPDX-License-Identifier: GPL-3.0-or-later
// Package hubsecurity persists encrypted account security state separately from
// gameplay. Every mutation is clone -> encrypt -> fsync -> rename -> commit.
package hubsecurity

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	wa "github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

var ErrInvalid = errors.New("security: invalid proof or operation")
var ErrUnavailable = errors.New("security: persistence unavailable")

type Passkey struct {
	Credential wa.Credential `json:"credential"`
	Name       string        `json:"name"`
	CreatedAt  time.Time     `json:"created_at"`
}
type Record struct {
	Fingerprint string    `json:"fingerprint"`
	Handle      []byte    `json:"handle"`
	Secret      string    `json:"totp_secret,omitempty"`
	LastStep    int64     `json:"last_step"`
	Recovery    []string  `json:"recovery,omitempty"`
	Keys        []Passkey `json:"keys,omitempty"`
}
type User struct {
	ID     string
	Record Record
}

func (u User) WebAuthnID() []byte          { return u.Record.Handle }
func (u User) WebAuthnName() string        { return "OPG3D " + u.ID }
func (u User) WebAuthnDisplayName() string { return "OPG3D " + u.ID }
func (u User) WebAuthnCredentials() []wa.Credential {
	out := []wa.Credential{}
	for _, k := range u.Record.Keys {
		out = append(out, k.Credential)
	}
	return out
}

type Store struct {
	mu      sync.Mutex
	path    string
	aead    cipher.AEAD
	records map[string]Record
	now     func() time.Time
}

func Open(path string, key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, ErrUnavailable
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, aead: aead, records: map[string]Record{}, now: time.Now}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil || len(b) < aead.NonceSize() {
		return nil, ErrUnavailable
	}
	raw, err := aead.Open(nil, b[:aead.NonceSize()], b[aead.NonceSize():], []byte("OPG3D-HUB-SECURITY-v1"))
	if err != nil {
		return nil, ErrUnavailable
	}
	if json.Unmarshal(raw, &s.records) != nil || s.records == nil {
		return nil, ErrUnavailable
	}
	return s, nil
}
func copyRecord(r Record) Record {
	b, _ := json.Marshal(r)
	var out Record
	json.Unmarshal(b, &out)
	return out
}
func (s *Store) Get(id, fingerprint string) Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[id]
	if r.Fingerprint != fingerprint {
		return Record{Fingerprint: fingerprint}
	}
	return copyRecord(r)
}
func (s *Store) persist(id string, r Record) error {
	all := make(map[string]Record, len(s.records)+1)
	for k, v := range s.records {
		all[k] = v
	}
	all[id] = r
	if s.path != "" {
		raw, err := json.Marshal(all)
		if err != nil {
			return ErrUnavailable
		}
		nonce := make([]byte, s.aead.NonceSize())
		if _, err = rand.Read(nonce); err != nil {
			return ErrUnavailable
		}
		sealed := s.aead.Seal(nonce, nonce, raw, []byte("OPG3D-HUB-SECURITY-v1"))
		dir := filepath.Dir(s.path)
		if os.MkdirAll(dir, 0700) != nil {
			return ErrUnavailable
		}
		f, err := os.CreateTemp(dir, ".hub-security-*")
		if err != nil {
			return ErrUnavailable
		}
		name := f.Name()
		defer os.Remove(name)
		if f.Chmod(0600) != nil {
			f.Close()
			return ErrUnavailable
		}
		if _, err = f.Write(sealed); err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return ErrUnavailable
		}
		if os.Rename(name, s.path) != nil {
			return ErrUnavailable
		}
		// A directory sync failure cannot roll back an already committed rename.
		if d, e := os.Open(dir); e == nil {
			d.Sync()
			d.Close()
		}
	}
	s.records = all
	return nil
}
func (s *Store) Ensure(id, fp string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[id]
	if r.Fingerprint != fp {
		r = Record{Fingerprint: fp}
	}
	if len(r.Handle) == 0 {
		r.Handle = make([]byte, 32)
		if _, err := rand.Read(r.Handle); err != nil {
			return User{}, ErrUnavailable
		}
		if err := s.persist(id, r); err != nil {
			return User{}, err
		}
	}
	return User{ID: id, Record: copyRecord(r)}, nil
}
func NewTOTP(id string) (string, string, error) {
	k, err := totp.Generate(totp.GenerateOpts{Issuer: "OPG3D HUB", AccountName: id, SecretSize: 20, Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		return "", "", err
	}
	return k.Secret(), k.URL(), nil
}
func recoveryHash(code string) string {
	h := sha256.Sum256([]byte("OPG3D-recovery-v1:" + code))
	return hex.EncodeToString(h[:])
}
func newRecovery() ([]string, []string, error) {
	codes, hashes := []string{}, []string{}
	for i := 0; i < 10; i++ {
		var b [16]byte
		if _, e := rand.Read(b[:]); e != nil {
			return nil, nil, e
		}
		c := hex.EncodeToString(b[:])
		codes = append(codes, c)
		hashes = append(hashes, recoveryHash(c))
	}
	return codes, hashes, nil
}
func validStep(secret, code string, last int64, now time.Time) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	base := now.Unix() / 30
	for _, step := range []int64{base, base - 1, base + 1} {
		if step <= last {
			continue
		}
		v, e := totp.GenerateCodeCustom(secret, time.Unix(step*30, 0), totp.ValidateOpts{Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if e == nil && subtle.ConstantTimeCompare([]byte(v), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}
func (s *Store) Enable(id, fp, secret, code string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[id]
	if r.Fingerprint != fp || r.Secret != "" {
		return nil, ErrInvalid
	}
	step, ok := validStep(secret, code, 0, s.now())
	if !ok {
		return nil, ErrInvalid
	}
	codes, hashes, err := newRecovery()
	if err != nil {
		return nil, ErrUnavailable
	}
	r = copyRecord(r)
	r.Secret = secret
	r.LastStep = step
	r.Recovery = hashes
	if err := s.persist(id, r); err != nil {
		return nil, err
	}
	return codes, nil
}

// Check consumes a TOTP step or a single-use high-entropy recovery code and
// durably commits it BEFORE the caller grants authentication.
func (s *Store) Check(id, fp, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[id]
	if r.Fingerprint != fp || r.Secret == "" {
		return ErrInvalid
	}
	r = copyRecord(r)
	if step, ok := validStep(r.Secret, code, r.LastStep, s.now()); ok {
		r.LastStep = step
		return s.persist(id, r)
	}
	if len(code) == 32 {
		hash := recoveryHash(code)
		for i, h := range r.Recovery {
			if subtle.ConstantTimeCompare([]byte(h), []byte(hash)) == 1 {
				r.Recovery = append(r.Recovery[:i], r.Recovery[i+1:]...)
				return s.persist(id, r)
			}
		}
	}
	return ErrInvalid
}
func (s *Store) Disable(id, fp string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[id]
	if r.Fingerprint != fp || r.Secret == "" {
		return ErrInvalid
	}
	r = copyRecord(r)
	r.Secret = ""
	r.LastStep = 0
	r.Recovery = nil
	return s.persist(id, r)
}
func (s *Store) Recovery(id, fp string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[id]
	if r.Fingerprint != fp || r.Secret == "" {
		return nil, ErrInvalid
	}
	codes, hashes, err := newRecovery()
	if err != nil {
		return nil, ErrUnavailable
	}
	r = copyRecord(r)
	r.Recovery = hashes
	return codes, s.persist(id, r)
}
func keyID(c wa.Credential) string { return base64.RawURLEncoding.EncodeToString(c.ID) }
func KeyID(c wa.Credential) string { return keyID(c) }
func (s *Store) AddKey(id, fp, name string, c wa.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[id]
	if r.Fingerprint != fp || len(r.Handle) == 0 || len(r.Keys) >= 8 || len(name) > 64 {
		return ErrInvalid
	}
	for _, user := range s.records {
		for _, k := range user.Keys {
			if bytes.Equal(k.Credential.ID, c.ID) {
				return ErrInvalid
			}
		}
	}
	r = copyRecord(r)
	r.Keys = append(r.Keys, Passkey{Credential: c, Name: name, CreatedAt: s.now()})
	return s.persist(id, r)
}
func (s *Store) FindUser(rawID, handle []byte) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.records {
		if bytes.Equal(r.Handle, handle) {
			for _, k := range r.Keys {
				if bytes.Equal(k.Credential.ID, rawID) {
					return User{ID: id, Record: copyRecord(r)}, nil
				}
			}
		}
	}
	return User{}, ErrInvalid
}
func (s *Store) UpdateKey(user User, c wa.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[user.ID]
	if r.Fingerprint != user.Record.Fingerprint || c.Authenticator.CloneWarning {
		return ErrInvalid
	}
	r = copyRecord(r)
	for i, k := range r.Keys {
		if bytes.Equal(k.Credential.ID, c.ID) {
			if c.Authenticator.SignCount < k.Credential.Authenticator.SignCount {
				return ErrInvalid
			}
			r.Keys[i].Credential = c
			return s.persist(user.ID, r)
		}
	}
	return ErrInvalid
}
func (s *Store) RemoveKey(id, fp, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[id]
	if r.Fingerprint != fp {
		return ErrInvalid
	}
	r = copyRecord(r)
	for i, k := range r.Keys {
		if keyID(k.Credential) == key {
			r.Keys = append(r.Keys[:i], r.Keys[i+1:]...)
			return s.persist(id, r)
		}
	}
	return ErrInvalid
}
func (s *Store) String() string { return fmt.Sprintf("hubsecurity.Store(%s)", filepath.Base(s.path)) }
