// SPDX-License-Identifier: GPL-3.0-or-later

package sio

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"testing"
)

// TestPBKDF2SHA1 checks the hand-rolled PBKDF2 against the RFC 6070 test
// vectors. Without this the whole key schedule would rest on an unverified
// reimplementation.
func TestPBKDF2SHA1(t *testing.T) {
	cases := []struct {
		name       string
		password   string
		salt       string
		iterations int
		length     int
		want       string
	}{
		{"c=1", "password", "salt", 1, 20, "0c60c80f961f0e71f3a9b524af6012062fe037a6"},
		{"c=2", "password", "salt", 2, 20, "ea6c014dc72d6f8ccd1ed92ace1d41f0d8de8957"},
		{"c=4096", "password", "salt", 4096, 20, "4b007901b765489abead49d926f721d065a429c1"},
		{
			"long",
			"passwordPASSWORDpassword",
			"saltSALTsaltSALTsaltSALTsaltSALTsalt",
			4096, 25,
			"3d2eec4fe41c849b80c8d83662c0e44a8b291a964cf2f07038",
		},
		{"embedded-nul", "pass\x00word", "sa\x00lt", 4096, 16, "56fa6aa75548099dcc37d7f03425e0c3"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hex.EncodeToString(PBKDF2SHA1([]byte(tc.password), []byte(tc.salt), tc.iterations, tc.length))
			if got != tc.want {
				t.Fatalf("PBKDF2SHA1 = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestDeriveKeyIV pins the split the client performs: 48 derived bytes cut
// into a 32-byte key followed by a 16-byte IV.
func TestDeriveKeyIV(t *testing.T) {
	password := bytes.Repeat([]byte{0xa5}, PasswordLen)
	salt := []byte{1, 2, 3, 4, 5, 6, 7, 8}

	key, iv := DeriveKeyIV(password, salt)
	if len(key) != KeyLen {
		t.Fatalf("key length = %d, want %d", len(key), KeyLen)
	}
	if len(iv) != IVLen {
		t.Fatalf("iv length = %d, want %d", len(iv), IVLen)
	}

	derived := PBKDF2SHA1(password, salt, Iterations, DerivedLen)
	if !bytes.Equal(key, derived[:KeyLen]) || !bytes.Equal(iv, derived[KeyLen:]) {
		t.Fatal("DeriveKeyIV does not match a straight split of the derived bytes")
	}
}

func TestPassword(t *testing.T) {
	a := bytes.Repeat([]byte{0x0f}, PasswordLen)
	b := bytes.Repeat([]byte{0xf0}, PasswordLen)

	got, err := Password(a, b)
	if err != nil {
		t.Fatalf("Password: %v", err)
	}
	if want := bytes.Repeat([]byte{0xff}, PasswordLen); !bytes.Equal(got, want) {
		t.Fatalf("Password = %x, want %x", got, want)
	}

	if _, err := Password(a[:8], b); err == nil {
		t.Fatal("Password accepted a short half")
	}
}

func testCodec(t *testing.T, mode Mode) *Codec {
	t.Helper()
	codec, err := New(bytes.Repeat([]byte{0x5a}, PasswordLen), []byte("8bytesal"), mode)
	if err != nil {
		t.Fatalf("New(%s): %v", mode, err)
	}
	return codec
}

// TestRoundTrip exercises every chaining variant across lengths that straddle
// the block boundary, including the exact sizes seen on the wire.
func TestRoundTrip(t *testing.T) {
	for _, mode := range Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			codec := testCodec(t, mode)
			for _, size := range []int{0, 1, 7, 15, 16, 17, 63, 608} {
				plain := make([]byte, size)
				for i := range plain {
					plain[i] = byte(i * 7)
				}
				ciphertext := codec.Encrypt(plain)
				if len(ciphertext)%BlockLen != 0 {
					t.Fatalf("size %d: ciphertext %d bytes is not block aligned", size, len(ciphertext))
				}
				got, err := codec.Decrypt(ciphertext)
				if err != nil {
					t.Fatalf("size %d: Decrypt: %v", size, err)
				}
				if !bytes.Equal(got, plain) {
					t.Fatalf("size %d: round trip mismatch", size)
				}
			}
		})
	}
}

// TestEventNameCiphertextLength ties the implementation to the capture: the
// client sends "auth_v2" as a 16-byte attachment, which is exactly what PKCS#7
// padding of a 7-byte string produces.
func TestEventNameCiphertextLength(t *testing.T) {
	for _, mode := range Modes() {
		codec := testCodec(t, mode)
		if got := len(codec.Encrypt([]byte("auth_v2"))); got != 16 {
			t.Fatalf("%s: Encrypt(\"auth_v2\") = %d bytes, want 16", mode, got)
		}
	}
}

// TestEncryptDeterministic guards the property the whole key-recovery plan
// rests on: no IV is generated per call, so identical plaintext must produce
// identical ciphertext.
func TestEncryptDeterministic(t *testing.T) {
	for _, mode := range Modes() {
		codec := testCodec(t, mode)
		first := codec.Encrypt([]byte("auth_v2"))
		second := codec.Encrypt([]byte("auth_v2"))
		if !bytes.Equal(first, second) {
			t.Fatalf("%s: encryption is not deterministic", mode)
		}
	}
}

// TestCFB128MatchesStdlib cross-checks the full-block feedback implementation
// against the standard library, so a bug in the hand-rolled loop cannot hide.
func TestCFB128MatchesStdlib(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, KeyLen)
	iv := bytes.Repeat([]byte{0x22}, IVLen)

	codec, err := NewFromKeyIV(key, iv, ModeCFB128)
	if err != nil {
		t.Fatalf("NewFromKeyIV: %v", err)
	}
	plain := pkcs7Pad([]byte("the quick brown fox jumps over the lazy dog"), BlockLen)

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	want := make([]byte, len(plain))
	//nolint:staticcheck // CFB is required to match the client, not chosen.
	cipher.NewCFBEncrypter(block, iv).XORKeyStream(want, plain)

	got := make([]byte, len(plain))
	codec.cfb128(plain, got, true)

	if !bytes.Equal(got, want) {
		t.Fatalf("cfb128 = %x, want %x", got, want)
	}
}

// TestCFB8FirstByteMatchesCFB128 checks the 8-bit variant against a property
// it must share with the full-block one: both start from the same keystream
// byte, so the first ciphertext byte is identical.
func TestCFB8FirstByteMatchesCFB128(t *testing.T) {
	key := bytes.Repeat([]byte{0x33}, KeyLen)
	iv := bytes.Repeat([]byte{0x44}, IVLen)
	plain := pkcs7Pad([]byte("auth_v2"), BlockLen)

	wide, err := NewFromKeyIV(key, iv, ModeCFB128)
	if err != nil {
		t.Fatalf("NewFromKeyIV(cfb128): %v", err)
	}
	narrow, err := NewFromKeyIV(key, iv, ModeCFB8)
	if err != nil {
		t.Fatalf("NewFromKeyIV(cfb8): %v", err)
	}

	if a, b := wide.Encrypt([]byte("auth_v2"))[0], narrow.Encrypt([]byte("auth_v2"))[0]; a != b {
		t.Fatalf("first ciphertext byte differs: cfb128 %#x, cfb8 %#x", a, b)
	}

	// The 8-bit variant must genuinely differ from the wide one overall,
	// otherwise the sweep would be testing the same thing twice.
	wideOut := make([]byte, len(plain))
	narrowOut := make([]byte, len(plain))
	wide.cfb128(plain, wideOut, true)
	narrow.cfb8(plain, narrowOut, true)
	if bytes.Equal(wideOut, narrowOut) {
		t.Fatal("cfb8 and cfb128 produced identical output")
	}
}

func TestPKCS7(t *testing.T) {
	if got := pkcs7Pad([]byte("auth_v2"), BlockLen); len(got) != 16 || got[15] != 9 {
		t.Fatalf("pkcs7Pad = %x", got)
	}
	// A full block must gain a whole block of padding.
	if got := pkcs7Pad(bytes.Repeat([]byte{0}, 16), BlockLen); len(got) != 32 {
		t.Fatalf("pkcs7Pad on a full block = %d bytes, want 32", len(got))
	}

	bad := [][]byte{
		{},
		bytes.Repeat([]byte{0x00}, 16),
		append(bytes.Repeat([]byte{0x01}, 15), 0x11),
		append(bytes.Repeat([]byte{0x01}, 14), 0x03, 0x03),
	}
	for i, buf := range bad {
		if _, err := pkcs7Unpad(buf, BlockLen); err == nil {
			t.Fatalf("case %d: pkcs7Unpad accepted bad padding %x", i, buf)
		}
		if ValidPKCS7(buf) {
			t.Fatalf("case %d: ValidPKCS7 accepted bad padding %x", i, buf)
		}
	}
}

func TestDecryptRejectsUnalignedCiphertext(t *testing.T) {
	codec := testCodec(t, ModeCFB8)
	if _, err := codec.Decrypt(make([]byte, 17)); err == nil {
		t.Fatal("Decrypt accepted a ciphertext that is not block aligned")
	}
	if _, err := codec.Decrypt(nil); err == nil {
		t.Fatal("Decrypt accepted an empty ciphertext")
	}
}

func TestParseMode(t *testing.T) {
	for _, mode := range Modes() {
		got, err := ParseMode(mode.String())
		if err != nil {
			t.Fatalf("ParseMode(%q): %v", mode.String(), err)
		}
		if got != mode {
			t.Fatalf("ParseMode(%q) = %v", mode.String(), got)
		}
	}
	if _, err := ParseMode("ecb"); err == nil {
		t.Fatal("ParseMode accepted an unsupported mode")
	}
}
