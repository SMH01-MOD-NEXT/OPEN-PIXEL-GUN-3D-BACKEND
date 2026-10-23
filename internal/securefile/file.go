// SPDX-License-Identifier: GPL-3.0-or-later
// Package securefile writes purpose-bound authenticated encrypted JSON atomically.
package securefile

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func aead(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("invalid storage key")
	}
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(b)
}
func Read(path string, key []byte, purpose string, value any) error {
	if path == "" {
		return nil
	}
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	a, e := aead(key)
	if e != nil {
		return e
	}
	if len(b) < a.NonceSize() {
		return errors.New("invalid encrypted file")
	}
	raw, e := a.Open(nil, b[:a.NonceSize()], b[a.NonceSize():], []byte(purpose))
	if e != nil {
		return e
	}
	return json.Unmarshal(raw, value)
}
func Write(path string, key []byte, purpose string, value any) error {
	if path == "" {
		return nil
	}
	a, e := aead(key)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	nonce := make([]byte, a.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return e
	}
	b := a.Seal(nonce, nonce, raw, []byte(purpose))
	dir := filepath.Dir(path)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".secure-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
