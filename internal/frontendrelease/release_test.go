package frontendrelease

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignedReleaseValidation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := make([]byte, 64)
	copy(bin, "\x7fELF")
	bin[4] = 2
	bin[5] = 1
	bin[18] = 183
	h := sha256.Sum256(bin)
	sha := hex.EncodeToString(h[:])
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	write := func(name string, b []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := Manifest{1, 1, GameVersion, "arm64-v8a", 1, "test", sha, 64}
	sign := func(m Manifest) []byte {
		raw, _ := json.Marshal(m)
		h := sha256.Sum256(raw)
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
		if err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(Envelope{base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(sig)})
		return out
	}
	envelope := sign(m)
	write("public.der", pub)
	write(sha+".so", bin)
	write("active.json", envelope)
	if r, err := Read(dir); err != nil || r.Manifest.Build != 1 {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		m    Manifest
	}{
		{"wrong-game", func() Manifest { x := m; x.GameVersion = "23.3.2"; return x }()},
		{"wrong-abi", func() Manifest { x := m; x.ABI = "x86"; return x }()},
		{"future-loader", func() Manifest { x := m; x.LoaderMin = 2; return x }()},
		{"path-traversal", func() Manifest { x := m; x.SHA256 = "../../private"; return x }()},
		{"oversize", func() Manifest { x := m; x.Size = MaxSize + 1; return x }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write("active.json", sign(tc.m))
			if _, err := Read(dir); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	write("active.json", envelope)
	if _, err := ReadBinary(dir, sha); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "active.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBinary(dir, sha); err != nil {
		t.Fatal("immutable artifact depended on active manifest", err)
	}
	for _, hash := range []string{"../../private", strings.ToUpper(sha), strings.Repeat("a", 64)} {
		if _, err := ReadBinary(dir, hash); err == nil {
			t.Fatal("accepted invalid artifact")
		}
	}
	write("active.json", envelope)
	bad := append([]byte{}, bin...)
	bad[63] = 1
	write(sha+".so", bad)
	if _, err := Read(dir); err == nil {
		t.Fatal("tampered binary accepted")
	}
	write(sha+".so", bin)
	var env Envelope
	json.Unmarshal(envelope, &env)
	env.Payload = base64.StdEncoding.EncodeToString([]byte(`{"build":999}`))
	forged, _ := json.Marshal(env)
	write("active.json", forged)
	if _, err := Read(dir); err == nil {
		t.Fatal("tampered manifest accepted")
	}
	write("active.json", envelope)
	other, _ := rsa.GenerateKey(rand.Reader, 3072)
	wrongPub, _ := x509.MarshalPKIXPublicKey(&other.PublicKey)
	write("public.der", wrongPub)
	if _, err := Read(dir); err == nil {
		t.Fatal("wrong signing key accepted")
	}
}
