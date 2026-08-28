// SPDX-License-Identifier: GPL-3.0-or-later

// Package sio implements the payload codec that the frozen 23.1.3 client
// applies to socket.io binary attachments on the /sio namespace.
//
// SECURITY-SENSITIVE. This package performs key derivation and symmetric
// encryption. It deliberately reimplements a scheme chosen by the original
// game client; the parameters below are dictated by that client and are not a
// recommendation. In particular the iteration count is 1 and the salt is a
// build-time constant, both of which would be wrong in a new design.
//
// Every constant and mode flag is transcribed from libil2cpp.so machine code,
// not guessed:
//
//	Rilisoft.<obfuscated>..ctor(byte[] password)   RVA 0x3B680F4
//	    salt    = 8 static bytes from a FieldRVA blob
//	    derived = Rfc2898DeriveBytes(password, salt, iterations: 1).GetBytes(48)
//	    Key     = derived[0:32]    (KeySize   = 256)
//	    IV      = derived[32:48]   (BlockSize = 128)
//	    Mode    = CipherMode.CFB   (enum value 4)
//	    Padding = PaddingMode.PKCS7 (enum value 2)
//
//	Encrypt(byte[] data, bool presize)             RVA 0x3B6883C
//	    Builds a fresh CreateEncryptor() on every call and never calls
//	    GenerateIV(). Key and IV are therefore stable and encryption is
//	    fully deterministic: the same plaintext always yields the same
//	    ciphertext for a given password.
//
// The password is the XOR of two static byte[32] arrays, read through
// SocketManager.Extras (RVA 0x3ECF1F4):
//
//	password[i] = A[i] ^ B[i]   for i in 0..31
//
// Socket.Emit (RVA 0x3ECE0D0) is patched so that every outgoing event runs
// through this codec, which is why the wire carries two opaque attachments
// and no readable event name:
//
//	attachment 0 = Encrypt(UTF8(eventName))
//	attachment 1 = Encrypt(UTF8(jsonArgs))
//
// FeedbackSize is never written by the constructor, so it keeps the framework
// default and had to be measured rather than read. A traced handshake settled
// it: given the client's own key and IV, only full-block feedback reproduces
// the captured attachments, so ModeCFB128 is the real mode. The 8-bit variant
// and CBC are kept as controls (see TestCapturedVectorPinsFullBlockCFB).
//
// The same trace pinned down which input varies per connection:
//
//	salt      static. Every cipher instance in a launch is built by this one
//	          constructor, which reads the salt from a FieldRVA blob, so the
//	          salt cannot be what moves.
//	password  per connection. It is the only remaining input, so the password
//	          is what carries the socket.io session id into the key schedule.
//
// How the session id reaches the password is still open. 547 candidate salts
// built from the sid and from every field of the auth_v2 payload reproduce
// neither the key nor the IV from the captured password, and the password is
// not a plain digest of any of those fields either.
package sio

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
)

// Sizes fixed by the client's constructor.
const (
	// KeyLen is the AES key length in bytes (KeySize = 256).
	KeyLen = 32
	// IVLen is the AES IV length in bytes (BlockSize = 128).
	IVLen = 16
	// DerivedLen is how many bytes the client pulls out of PBKDF2.
	DerivedLen = KeyLen + IVLen
	// Iterations is the PBKDF2 iteration count baked into the client.
	Iterations = 1
	// SaltLen is the length of the static PBKDF2 salt.
	SaltLen = 8
	// PasswordLen is the length of each static password half.
	PasswordLen = 32
	// BlockLen is the AES block size in bytes.
	BlockLen = aes.BlockSize
)

// Errors reported by this package.
var (
	ErrCiphertextSize = errors.New("sio: ciphertext is not a whole number of AES blocks")
	ErrPadding        = errors.New("sio: invalid PKCS#7 padding")
	ErrMode           = errors.New("sio: unknown cipher mode")
)

// Mode selects how the AES block cipher is chained.
//
// The client asks for CipherMode.CFB. .NET's CFB is parameterised by
// FeedbackSize, which the constructor never sets, so the effective variant had
// to be measured against real traffic rather than read out of the code. It is
// full-block: ModeCFB128. ModeCFB8 and ModeCBC stay as controls, so a future
// capture that matches under either of them is an immediate signal that this
// reading of the client is wrong.
type Mode uint8

// Supported chaining modes.
const (
	ModeCFB8 Mode = iota + 1
	ModeCFB128
	ModeCBC
)

// String implements fmt.Stringer.
func (m Mode) String() string {
	switch m {
	case ModeCFB8:
		return "cfb8"
	case ModeCFB128:
		return "cfb128"
	case ModeCBC:
		return "cbc"
	default:
		return fmt.Sprintf("mode(%d)", uint8(m))
	}
}

// ParseMode maps a mode name back to a Mode.
func ParseMode(name string) (Mode, error) {
	for _, mode := range Modes() {
		if mode.String() == name {
			return mode, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrMode, name)
}

// Modes lists every chaining variant worth sweeping, the confirmed one first.
func Modes() []Mode { return []Mode{ModeCFB128, ModeCFB8, ModeCBC} }

// DefaultMode is the chaining variant the client actually uses. Callers that
// configure a recovered key should use it instead of picking a mode by hand.
const DefaultMode = ModeCFB128

// PBKDF2SHA1 implements PBKDF2-HMAC-SHA1 (RFC 2898), which is what
// Rfc2898DeriveBytes computes. It is hand-rolled to keep this module free of
// third-party dependencies; TestPBKDF2SHA1 checks it against the RFC 6070
// vectors.
func PBKDF2SHA1(password, salt []byte, iterations, length int) []byte {
	if iterations < 1 {
		iterations = 1
	}
	if length <= 0 {
		return nil
	}
	prf := hmac.New(sha1.New, password)
	hashLen := prf.Size()
	blocks := (length + hashLen - 1) / hashLen

	out := make([]byte, 0, blocks*hashLen)
	counter := make([]byte, 4)
	u := make([]byte, 0, hashLen)
	t := make([]byte, hashLen)

	for block := 1; block <= blocks; block++ {
		binary.BigEndian.PutUint32(counter, uint32(block))
		prf.Reset()
		prf.Write(salt)
		prf.Write(counter)
		u = prf.Sum(u[:0])
		copy(t, u)
		for i := 1; i < iterations; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:length]
}

// DeriveKeyIV reproduces the client's key schedule: 48 bytes out of
// PBKDF2-HMAC-SHA1 with a single iteration, split into a 32-byte key and a
// 16-byte IV.
func DeriveKeyIV(password, salt []byte) (key, iv []byte) {
	derived := PBKDF2SHA1(password, salt, Iterations, DerivedLen)
	return derived[:KeyLen], derived[KeyLen:]
}

// Password XORs the two static halves into the codec password.
func Password(a, b []byte) ([]byte, error) {
	if len(a) != PasswordLen || len(b) != PasswordLen {
		return nil, fmt.Errorf("sio: password halves must be %d bytes, got %d and %d", PasswordLen, len(a), len(b))
	}
	out := make([]byte, PasswordLen)
	for i := range out {
		out[i] = a[i] ^ b[i]
	}
	return out, nil
}

// Codec is one configured instance of the client's payload cipher. It holds no
// mutable state, so it is safe for concurrent use.
type Codec struct {
	block cipher.Block
	iv    [IVLen]byte
	mode  Mode
}

// New derives the key and IV from a password and salt and returns a codec.
func New(password, salt []byte, mode Mode) (*Codec, error) {
	key, iv := DeriveKeyIV(password, salt)
	return NewFromKeyIV(key, iv, mode)
}

// NewFromKeyIV builds a codec from an already-derived key and IV. It exists so
// that a recovered key can be configured directly, without shipping the
// password.
func NewFromKeyIV(key, iv []byte, mode Mode) (*Codec, error) {
	if len(key) != KeyLen {
		return nil, fmt.Errorf("sio: key must be %d bytes, got %d", KeyLen, len(key))
	}
	if len(iv) != IVLen {
		return nil, fmt.Errorf("sio: iv must be %d bytes, got %d", IVLen, len(iv))
	}
	switch mode {
	case ModeCFB8, ModeCFB128, ModeCBC:
	default:
		return nil, fmt.Errorf("%w: %d", ErrMode, uint8(mode))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("sio: aes: %w", err)
	}
	c := &Codec{block: block, mode: mode}
	copy(c.iv[:], iv)
	return c, nil
}

// Mode reports how this codec chains blocks.
func (c *Codec) Mode() Mode { return c.mode }

// Encrypt pads plain with PKCS#7 and encrypts it, mirroring the client's
// Encrypt(byte[], bool). The bool argument in the client only pre-sizes a
// MemoryStream and has no effect on the output, so it has no counterpart here.
func (c *Codec) Encrypt(plain []byte) []byte {
	padded := pkcs7Pad(plain, BlockLen)
	out := make([]byte, len(padded))
	switch c.mode {
	case ModeCFB8:
		c.cfb8(padded, out, true)
	case ModeCFB128:
		c.cfb128(padded, out, true)
	case ModeCBC:
		cipher.NewCBCEncrypter(c.block, c.iv[:]).CryptBlocks(out, padded)
	}
	return out
}

// Decrypt reverses Encrypt and strips the PKCS#7 padding.
func (c *Codec) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 || len(ciphertext)%BlockLen != 0 {
		return nil, fmt.Errorf("%w: %d bytes", ErrCiphertextSize, len(ciphertext))
	}
	out := make([]byte, len(ciphertext))
	switch c.mode {
	case ModeCFB8:
		c.cfb8(ciphertext, out, false)
	case ModeCFB128:
		c.cfb128(ciphertext, out, false)
	case ModeCBC:
		cipher.NewCBCDecrypter(c.block, c.iv[:]).CryptBlocks(out, ciphertext)
	}
	return pkcs7Unpad(out, BlockLen)
}

// cfb8 runs 8-bit cipher feedback. One block encryption per plaintext byte,
// with the shift register sliding by one byte of ciphertext each step. A
// single differing plaintext byte therefore avalanches into every later byte.
func (c *Codec) cfb8(src, dst []byte, encrypt bool) {
	var shift, keystream [BlockLen]byte
	copy(shift[:], c.iv[:])
	for i := range src {
		c.block.Encrypt(keystream[:], shift[:])
		out := src[i] ^ keystream[0]
		feedback := out
		if !encrypt {
			feedback = src[i]
		}
		dst[i] = out
		copy(shift[:BlockLen-1], shift[1:])
		shift[BlockLen-1] = feedback
	}
}

// cfb128 runs full-block cipher feedback, which for a block-sized feedback
// register is the classic CFB construction.
func (c *Codec) cfb128(src, dst []byte, encrypt bool) {
	var shift, keystream [BlockLen]byte
	copy(shift[:], c.iv[:])
	for off := 0; off+BlockLen <= len(src); off += BlockLen {
		c.block.Encrypt(keystream[:], shift[:])
		in := src[off : off+BlockLen]
		out := dst[off : off+BlockLen]
		for i := 0; i < BlockLen; i++ {
			out[i] = in[i] ^ keystream[i]
		}
		if encrypt {
			copy(shift[:], out)
		} else {
			copy(shift[:], in)
		}
	}
}

func pkcs7Pad(in []byte, size int) []byte {
	n := size - len(in)%size
	out := make([]byte, len(in)+n)
	copy(out, in)
	for i := len(in); i < len(out); i++ {
		out[i] = byte(n)
	}
	return out
}

func pkcs7Unpad(in []byte, size int) ([]byte, error) {
	if len(in) == 0 || len(in)%size != 0 {
		return nil, ErrPadding
	}
	n := int(in[len(in)-1])
	if n == 0 || n > size || n > len(in) {
		return nil, ErrPadding
	}
	for _, b := range in[len(in)-n:] {
		if int(b) != n {
			return nil, ErrPadding
		}
	}
	return in[:len(in)-n], nil
}

// ValidPKCS7 reports whether buf ends in well-formed PKCS#7 padding. Sweeps
// use it as a cheap first filter before looking at the plaintext itself.
func ValidPKCS7(buf []byte) bool {
	_, err := pkcs7Unpad(buf, BlockLen)
	return err == nil
}
