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

// Captured from a live 23.1.3 handshake on 2026-08-28 with the socket.io sid
// pinned to 0123456789abcdef01234567 by OPG3D_STABLE_SESSIONS.
//
// These are not reconstructed values. The client's own cipher constructor was
// traced, so the key and IV below are exactly what the client used, and the
// two ciphertexts are the two attachments the server received for the same
// auth_v2 emit: attachment 0 is Encrypt(UTF8(eventName)) and attachment 1 is
// Encrypt(UTF8(jsonArgs)).
//
// This vector is what settles FeedbackSize. The constructor never writes it,
// so the effective variant had to be measured rather than read, and only
// full-block feedback reproduces the capture.
const (
	capturedKey             = "d670c4e97d6aafcdc9940cfb1f411df8c08b8fc480e50b239fe852a1f6ddd6d2"
	capturedIV              = "c72cc19a7fb79ef6e6d7a6f3944514bd"
	capturedPassword        = "a6aa0aee01fc80f44dc4252079475465e2b07448974597a25a2473a342b3588b"
	capturedEvent           = "auth_v2"
	capturedEventCiphertext = "74a1de5b1e2465e4529d219ac23400fe"
	capturedArgsCiphertext  = "6ef6df5d282308843fb612b1f80839c046751ab76e16e82f596b37130e4d088ab2ab5c4bd8ce71fa513f326c9e41df864e8c0cb7d9037529821cd18157cd36d12e009a26ec0e3a4ccbbe1892ed63ffe0c303d80fb4f455b95b1ab58075c09cb7887e01958b28908919517c04f244ed63cbfb299e7f9edf52a36bbdf8b4c3be37c59a1aaae878fc2834c4a394e1b54cd80a0c1adb8dcf69c83100156ec7c9babeda22f5084991fe03c5fa46de506f84d2e057acd490848409a5cd126513051536a34bdf52c505eed1e9fe43970eb0dfce88d666ed94190bb113b7a561649f86bb618819e7fe66b687b00b7ee981ba6af4b74a48038408b904d80c1eb7eedb6ca4fd64bd6ec2f33d2fa518e86088bfa6d1e89587890c0e9a0187e740fa8803e1e5ace6d9fd5328d8e7f364f88d068f8258ca012f5fe2f667a99ff4236fe71461a51e3dc92c090557eabf9322af17e00a709882039ec0d908650f2265d817c0f665ffbbe9c7f10c580015fd3f96f88ebba3a18021ffd776f14152adb5a6e8b51b44946db4599fce386bb4b718fe322f8a86b767b6efcaae7488dd9b107e483e49ff714e98b34df4b9f181c0cab23d7639dddeb753ebb70fb8606478ca91bf17ef8861eac34c1f84d026fbd87459b0f48d69b84345d6aa00f6e4cdf98ee011692c57d83827a9e202090ca651057e5837903907c21c1538c912d50768b1bfe2f70a90822f8143f790e5300ebde9add05cf08b26d18c1ac63868474c33923f42328c1d66acd819cb4799e876ec1ec36fc8a8f675f8c558dbc390197e7ed6abeb87b4eac55c9bf21f77d2b7fc2a79c39dab454d1d5e057f12042d320a09bbc1aa3a88c9"
)

// capturedArgs is the plaintext of attachment 1. The client builds it with a
// space after every comma, so it is stored verbatim rather than re-marshalled:
// a byte-exact plaintext is what makes the re-encryption check meaningful.
const capturedArgs = `{"uniq_id":"350713954", "hash":"afd8f926-88c0-49d7-9a90-3f6d662fcd28", "device":"918ad658854434c89292b80c5e573229", "token":"2690ea3d61c0ae48fb958bbadcd076aeee6fd722b01f8097240cd9a6c7749b48870bd5a7441895fe4cda772276ba133c25706be288dd16c7cc7dd0252807db645851a9c63ffa9540be947b46165b380484d79576bed98317692e39de067a914e8fc4b69693fd3f6193ef0cab3caca75cc66c5bda44c38d96525284c0a07f3b44", "return":"1510171621262214222614262242213712121715203430121632302611223", "platform":"1", "device_model":"Nothing A059P", "type_device":2, "abuse_method":1, "block_param":1, "req_id":1, "sid":"0123456789abcdef01234567"}`

func mustDecodeHex(t *testing.T, in string) []byte {
	t.Helper()
	out, err := hex.DecodeString(in)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	return out
}

// TestCapturedVectorPinsFullBlockCFB is the end-to-end check against real
// traffic. It proves three separate things at once: the chaining mode is
// full-block CFB, encryption is deterministic across a 38-block payload, and
// this package can both read and produce what the client accepts.
func TestCapturedVectorPinsFullBlockCFB(t *testing.T) {
	key := mustDecodeHex(t, capturedKey)
	iv := mustDecodeHex(t, capturedIV)
	event := mustDecodeHex(t, capturedEventCiphertext)
	args := mustDecodeHex(t, capturedArgsCiphertext)

	if got := len(mustDecodeHex(t, capturedPassword)); got != PasswordLen {
		t.Fatalf("captured password is %d bytes, want PasswordLen=%d", got, PasswordLen)
	}
	if len(event) != BlockLen {
		t.Fatalf("captured event ciphertext is %d bytes, want %d", len(event), BlockLen)
	}
	if len(args) != 608 {
		t.Fatalf("captured payload ciphertext is %d bytes, want 608", len(args))
	}

	codec, err := NewFromKeyIV(key, iv, ModeCFB128)
	if err != nil {
		t.Fatalf("NewFromKeyIV: %v", err)
	}

	name, err := codec.Decrypt(event)
	if err != nil {
		t.Fatalf("Decrypt(attachment 0): %v", err)
	}
	if string(name) != capturedEvent {
		t.Fatalf("attachment 0 = %q, want %q", name, capturedEvent)
	}
	if got := hex.EncodeToString(codec.Encrypt([]byte(capturedEvent))); got != capturedEventCiphertext {
		t.Fatalf("Encrypt(%q) = %s, want %s", capturedEvent, got, capturedEventCiphertext)
	}

	payload, err := codec.Decrypt(args)
	if err != nil {
		t.Fatalf("Decrypt(attachment 1): %v", err)
	}
	if string(payload) != capturedArgs {
		t.Fatalf("attachment 1 plaintext mismatch:\n got %q\nwant %q", payload, capturedArgs)
	}
	if got := hex.EncodeToString(codec.Encrypt(payload)); got != capturedArgsCiphertext {
		t.Fatal("re-encrypting attachment 1 did not reproduce the captured ciphertext")
	}

	// The other two variants are controls. If either ever reproduced the
	// event name, the mode would be ambiguous and every conclusion drawn
	// from this vector would need revisiting.
	for _, mode := range []Mode{ModeCFB8, ModeCBC} {
		control, err := NewFromKeyIV(key, iv, mode)
		if err != nil {
			t.Fatalf("NewFromKeyIV(%s): %v", mode, err)
		}
		if plain, err := control.Decrypt(event); err == nil && string(plain) == capturedEvent {
			t.Fatalf("%s also decrypted the capture; the mode is not pinned", mode)
		}
	}
}
