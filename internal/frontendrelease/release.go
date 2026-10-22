// Package frontendrelease validates immutable, RSA-signed native releases.
package frontendrelease

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const MaxSize = 32 << 20
const GameVersion = "23.1.3"

type Manifest struct {
	Schema      int    `json:"schema"`
	LoaderMin   int    `json:"loader_min"`
	GameVersion string `json:"game_version"`
	ABI         string `json:"abi"`
	Build       int64  `json:"build"`
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
}
type Envelope struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}
type Release struct {
	Manifest Manifest
	Envelope []byte
	Binary   []byte
}

func Read(dir string) (*Release, error) {
	b, err := os.ReadFile(filepath.Join(dir, "active.json"))
	if err != nil {
		return nil, err
	}
	if len(b) > 16384 {
		return nil, errors.New("manifest too large")
	}
	var env Envelope
	if err = json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signature)
	if err != nil {
		return nil, err
	}
	der, err := os.ReadFile(filepath.Join(dir, "public.der"))
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	pub, ok := key.(*rsa.PublicKey)
	if !ok || pub.N.BitLen() < 3072 {
		return nil, errors.New("invalid signing key")
	}
	digest := sha256.Sum256(raw)
	if err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return nil, err
	}
	var m Manifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.Schema != 1 || m.LoaderMin != 1 || m.GameVersion != GameVersion || m.ABI != "arm64-v8a" || m.Build <= 0 || m.Version == "" || m.Size < 64 || m.Size > MaxSize || len(m.SHA256) != 64 {
		return nil, errors.New("incompatible native release")
	}
	hash, err := hex.DecodeString(m.SHA256)
	if err != nil || len(hash) != 32 {
		return nil, errors.New("invalid hash")
	}
	f, err := os.Open(filepath.Join(dir, m.SHA256+".so"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() != m.Size {
		return nil, errors.New("invalid native file size")
	}
	bin := make([]byte, m.Size)
	n, err := f.ReadAt(bin, 0)
	if err != nil || int64(n) != m.Size {
		return nil, errors.New("native read failed")
	}
	h := sha256.Sum256(bin)
	if hex.EncodeToString(h[:]) != m.SHA256 {
		return nil, errors.New("native hash mismatch")
	}
	if string(bin[:4]) != "\x7fELF" || bin[4] != 2 || bin[5] != 1 || bin[18] != 183 || bin[19] != 0 {
		return nil, errors.New("not AArch64 ELF")
	}
	return &Release{m, b, bin}, nil
}

// ReadBinary keeps content-addressed URLs valid when active.json advances while
// a client is downloading. Execution trust still comes from the signed manifest.
func ReadBinary(dir, hash string) ([]byte, error) {
	if len(hash) != 64 || strings.ToLower(hash) != hash {
		return nil, errors.New("invalid artifact hash")
	}
	decoded, err := hex.DecodeString(hash)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("invalid artifact hash")
	}
	path := filepath.Join(dir, hash+".so")
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() < 64 || st.Size() > MaxSize {
		return nil, errors.New("invalid artifact")
	}
	bin, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(bin)
	if hex.EncodeToString(digest[:]) != hash {
		return nil, errors.New("artifact hash mismatch")
	}
	return bin, nil
}
