// Offline native release publisher. Keep the signing key outside content/ and source control.
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/frontendrelease"
	"os"
	"path/filepath"
)

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func atomic(path string, b []byte, mode os.FileMode) {
	f, err := os.CreateTemp(filepath.Dir(path), ".release-*")
	check(err)
	name := f.Name()
	defer os.Remove(name)
	check(f.Chmod(mode))
	_, err = f.Write(b)
	check(err)
	check(f.Sync())
	check(f.Close())
	check(os.Rename(name, path))
}
func main() {
	keyPath := flag.String("key", "", "private PKCS8 PEM path (outside served directory)")
	out := flag.String("out", "", "release directory")
	lib := flag.String("library", "", "native payload .so")
	build := flag.Int64("build", 0, "monotonic build number")
	version := flag.String("version", "", "display version")
	generate := flag.Bool("generate-key", false, "create new RSA3072 key; refuses overwrite")
	flag.Parse()
	if *keyPath == "" {
		check(fmt.Errorf("-key required"))
	}
	if *generate {
		k, e := rsa.GenerateKey(rand.Reader, 3072)
		check(e)
		der, e := x509.MarshalPKCS8PrivateKey(k)
		check(e)
		f, e := os.OpenFile(*keyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		check(e)
		check(pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}))
		check(f.Sync())
		check(f.Close())
		fmt.Println("Signing key created; keep it private and backed up")
		return
	}
	if *out == "" || *lib == "" || *build <= 0 || *version == "" {
		check(fmt.Errorf("-out -library -build -version required"))
	}
	b, e := os.ReadFile(*keyPath)
	check(e)
	p, _ := pem.Decode(b)
	if p == nil {
		check(fmt.Errorf("invalid PEM"))
	}
	k, e := x509.ParsePKCS8PrivateKey(p.Bytes)
	check(e)
	key, ok := k.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() < 3072 {
		check(fmt.Errorf("RSA3072+ required"))
	}
	bin, e := os.ReadFile(*lib)
	check(e)
	if len(bin) < 64 || len(bin) > frontendrelease.MaxSize || string(bin[:4]) != "\x7fELF" || bin[4] != 2 || bin[18] != 183 {
		check(fmt.Errorf("invalid native ELF"))
	}
	check(os.MkdirAll(*out, 0755))
	if old, e := frontendrelease.Read(*out); e == nil && *build <= old.Manifest.Build {
		check(fmt.Errorf("build must increase"))
	}
	h := sha256.Sum256(bin)
	hash := hex.EncodeToString(h[:])
	m := frontendrelease.Manifest{Schema: 1, LoaderMin: 1, GameVersion: frontendrelease.GameVersion, ABI: "arm64-v8a", Build: *build, Version: *version, SHA256: hash, Size: int64(len(bin))}
	raw, e := json.Marshal(m)
	check(e)
	digest := sha256.Sum256(raw)
	sig, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	check(e)
	envelope, e := json.Marshal(frontendrelease.Envelope{Payload: base64.StdEncoding.EncodeToString(raw), Signature: base64.StdEncoding.EncodeToString(sig)})
	check(e)
	pub, e := x509.MarshalPKIXPublicKey(&key.PublicKey)
	check(e)
	atomic(filepath.Join(*out, hash+".so"), bin, 0644)
	atomic(filepath.Join(*out, "public.der"), pub, 0644)
	atomic(filepath.Join(*out, "active.json"), envelope, 0644)
	_, e = frontendrelease.Read(*out)
	check(e)
	fmt.Println("Published signed frontend", *version, "build", *build, "sha256", hash)
}
