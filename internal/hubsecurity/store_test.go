// SPDX-License-Identifier: GPL-3.0-or-later
package hubsecurity

import (
	"bytes"
	wa "github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp/totp"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEncryptedStateTOTPRecoveryAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "security.enc")
	key := bytes.Repeat([]byte{7}, 32)
	s, e := Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Unix(1792750000, 0)
	s.now = func() time.Time { return now }
	u, e := s.Ensure("123", "owner")
	if e != nil || len(u.Record.Handle) != 32 {
		t.Fatal(e)
	}
	secret, _, e := NewTOTP("123")
	if e != nil {
		t.Fatal(e)
	}
	code, _ := totp.GenerateCode(secret, now)
	codes, e := s.Enable("123", "owner", secret, code)
	if e != nil || len(codes) != 10 {
		t.Fatal(e)
	}
	if s.Check("123", "owner", code) == nil {
		t.Fatal("TOTP step replay")
	}
	if s.Check("123", "other", codes[0]) == nil {
		t.Fatal("owner bypass")
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(codes[0])) {
		t.Fatal("plaintext secret")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("file permissions")
	}
	if _, e := Open(path, bytes.Repeat([]byte{8}, 32)); e == nil {
		t.Fatal("wrong encryption key")
	}
	s, e = Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return now }
	if s.Check("123", "owner", codes[0]) != nil || s.Check("123", "owner", codes[0]) == nil {
		t.Fatal("recovery not single use")
	}
	now = now.Add(30 * time.Second)
	code, _ = totp.GenerateCode(secret, now)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.Check("123", "owner", code) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatal("parallel replay", accepted.Load())
	}
	if len(s.Get("123", "other").Recovery) != 0 {
		t.Fatal("account incarnation leak")
	}
	raw[20] ^= 1
	os.WriteFile(path, raw, 0600)
	if _, e := Open(path, key); e == nil {
		t.Fatal("tampered encryption")
	}
}
func TestKeyOwnershipCounterAndPersistenceFailure(t *testing.T) {
	s, _ := Open("", bytes.Repeat([]byte{1}, 32))
	u, _ := s.Ensure("1", "one")
	s.Ensure("2", "two")
	c := wa.Credential{ID: []byte("id"), PublicKey: []byte("key")}
	c.Authenticator.SignCount = 10
	if s.AddKey("1", "one", "Passkey", c) != nil {
		t.Fatal("add")
	}
	if s.AddKey("2", "two", "Passkey", c) == nil {
		t.Fatal("duplicate global credential")
	}
	if _, e := s.FindUser(c.ID, []byte("wrong")); e == nil {
		t.Fatal("handle binding")
	}
	if _, e := s.FindUser(c.ID, u.Record.Handle); e != nil {
		t.Fatal(e)
	}
	c.Authenticator.SignCount = 9
	if s.UpdateKey(u, c) == nil {
		t.Fatal("counter rollback")
	}
	c.Authenticator.SignCount = 11
	c.Authenticator.CloneWarning = true
	if s.UpdateKey(u, c) == nil {
		t.Fatal("clone")
	}
	c.Authenticator.CloneWarning = false
	if s.UpdateKey(u, c) != nil {
		t.Fatal("counter advance")
	}
	if s.RemoveKey("1", "other", KeyID(c)) == nil {
		t.Fatal("owner")
	}
	blocked := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocked, []byte("x"), 0600)
	s.path = filepath.Join(blocked, "fail")
	if s.RemoveKey("1", "one", KeyID(c)) == nil {
		t.Fatal("persist failure")
	}
	if len(s.Get("1", "one").Keys) != 1 {
		t.Fatal("failed write mutated memory")
	}
	s.path = ""
	if s.RemoveKey("1", "one", KeyID(c)) != nil {
		t.Fatal("remove")
	}
	if _, e := s.FindUser(c.ID, u.Record.Handle); e == nil {
		t.Fatal("removed key accepted")
	}
}
