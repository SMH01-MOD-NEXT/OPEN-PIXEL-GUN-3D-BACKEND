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
// How the session id reaches the password is now recovered. A[i] is the array
// get_Extras overwrites at run time and B[i] is a static base (passwordBase
// below); the client fills A with a per-connection mask taken from the
// socket.io session id, one byte per sid character:
//
//	A[i]        = 0x30 + nibble(sid[i])   for i < len(sid)   (the sid is 24 hex chars)
//	A[i]        = 0                        for the remaining bytes
//	password[i] = passwordBase[i] ^ A[i]
//
// so the password is passwordBase with its first 24 bytes XORed against the sid
// mask and its last 8 left untouched. Two traced launches on different sids fix
// it beyond doubt: deriving passwordBase from one sid reproduces the other
// sid's password byte for byte, and both then reproduce their captured key, IV
// and auth_v2 attachment. See PasswordForSid and NewForSid.
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

// salt is the PBKDF2 salt the client's cipher constructor passes to
// Rfc2898DeriveBytes. It is a build-time constant shared by every instance of
// the class, not a per-user or per-connection value.
//
// SECURITY-SENSITIVE, and recovered rather than guessed. get_Extras hands the
// constructor a password and nothing else, so the salt had to be static data
// owned by the cipher class. It was found by sliding an 8-byte window over the
// client's shipped metadata and testing each window against one captured
// (password, Key) pair; see ScanSalt for why a candidate costs a single HMAC.
//
//	analys2313/global-metadata.dat, offset 0x00BF92B8
//
// The hit is not an artefact of a single sample. These 8 bytes reproduce both
// Key and IV for all four cipher instances the traced process constructed,
// each from a different password:
//
//	socket   .ctor pc=0x3ecf360  password a6aa0aee...588b (32 B)
//	prefs    .ctor pc=0x3b692b0  password 555e0803...a19b (40 B)
//	prefs    .ctor pc=0x3b692b0  password 34b33f7d...a19b (40 B)
//	cfgcache .ctor pc=0x249fdfc  password 509dc635...79e5 (32 B)
//
// One false match has probability 2^-160; four independent passwords agreeing
// settles it. TestRecoveredSaltDerivesEveryCapturedInstance pins this.
var salt = [SaltLen]byte{0x4c, 0x82, 0xa1, 0x18, 0x24, 0x64, 0x15, 0x96}

// SaltMetadataOffset is where salt was found in the client's
// global-metadata.dat, so the constant can be re-derived from the shipped file
// instead of being trusted.
const SaltMetadataOffset = 0x00BF92B8

// Salt returns a copy of the recovered PBKDF2 salt.
func Salt() []byte {
	out := make([]byte, SaltLen)
	copy(out, salt[:])
	return out
}

// NewFromPassword derives Key and IV from password using the recovered salt.
// This mirrors the client, which only ever supplies a password.
func NewFromPassword(password []byte, mode Mode) (*Codec, error) {
	return New(password, Salt(), mode)
}

// passwordBase is the static half of the /sio password: the array get_Extras
// XORs against the per-connection sid mask (see the package comment). It is B
// in the A ^ B decomposition.
//
// SECURITY-SENSITIVE, and recovered rather than guessed. It was read off two
// traced launches whose socket.io session ids differed: deriving these bytes
// from the pinned-sid password and re-applying them to the random-sid password
// reproduces it exactly, and every derived key, IV and auth_v2 attachment then
// matches the capture. See PasswordForSid and the sid vectors in the tests.
var passwordBase = [PasswordLen]byte{
	0x96, 0x9b, 0x38, 0xdd, 0x35, 0xc9, 0xb6, 0xc3,
	0x75, 0xfd, 0x1f, 0x1b, 0x45, 0x7a, 0x6a, 0x5a,
	0xd2, 0x81, 0x46, 0x7b, 0xa3, 0x70, 0xa1, 0x95,
	0x5a, 0x24, 0x73, 0xa3, 0x42, 0xb3, 0x58, 0x8b,
}

// sidMaskByte maps one hex character of the session id to the byte the client
// writes into the mask array: the character's nibble value biased by 0x30. For
// a digit that is the character itself; for a-f it lands at 0x3a..0x3f, the
// off-by-bias the client's own conversion produces and the reason the mask is
// not simply the sid's ASCII.
func sidMaskByte(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return 0x30 + (c - '0'), true
	case c >= 'a' && c <= 'f':
		return 0x30 + (c - 'a') + 0x0a, true
	case c >= 'A' && c <= 'F':
		return 0x30 + (c - 'A') + 0x0a, true
	}
	return 0, false
}

// PasswordForSid computes the /sio codec password for a socket.io session id.
//
// The password is passwordBase with a per-connection mask XORed over its
// leading bytes, one mask byte per sid character. This is what lets the server
// reproduce the key the client derives for a session instead of only fitting a
// single captured sid.
func PasswordForSid(sid string) ([]byte, error) {
	if len(sid) > PasswordLen {
		return nil, fmt.Errorf("sio: session id is %d bytes, longer than the %d-byte password", len(sid), PasswordLen)
	}
	out := make([]byte, PasswordLen)
	copy(out, passwordBase[:])
	for i := 0; i < len(sid); i++ {
		mask, ok := sidMaskByte(sid[i])
		if !ok {
			return nil, fmt.Errorf("sio: session id byte %d = %q is not a hex character", i, sid[i])
		}
		out[i] ^= mask
	}
	return out, nil
}

// NewForSid derives the codec for a session straight from its sid, using the
// recovered salt and the client's real chaining mode.
func NewForSid(sid string, mode Mode) (*Codec, error) {
	password, err := PasswordForSid(sid)
	if err != nil {
		return nil, err
	}
	return NewFromPassword(password, mode)
}

// ScanSalt returns every offset in blob whose SaltLen-byte window derives
// wantKey from password.
//
// This is the routine that recovered salt, kept so the constant stays
// reproducible from the shipped metadata. It exploits Iterations == 1: the
// first PBKDF2 output block is then exactly HMAC-SHA1(password, window ||
// uint32be(1)), and Key is the leading KeyLen bytes of that stream, so
// comparing the first sha1.Size bytes of wantKey decides a window in one HMAC.
// A false positive per window has probability 2^-160.
//
// wantKey must hold at least sha1.Size bytes; only that prefix is used.
func ScanSalt(blob, password, wantKey []byte) []int {
	if len(blob) < SaltLen || len(wantKey) < sha1.Size {
		return nil
	}
	want := wantKey[:sha1.Size]
	mac := hmac.New(sha1.New, password)
	msg := make([]byte, SaltLen+4)
	binary.BigEndian.PutUint32(msg[SaltLen:], 1)
	sum := make([]byte, 0, sha1.Size)
	var hits []int
	for off := 0; off+SaltLen <= len(blob); off++ {
		copy(msg, blob[off:off+SaltLen])
		mac.Reset()
		mac.Write(msg)
		sum = mac.Sum(sum[:0])
		if hmac.Equal(sum, want) {
			hits = append(hits, off)
		}
	}
	return hits
}
