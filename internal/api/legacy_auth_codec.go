// SPDX-License-Identifier: GPL-3.0-or-later
package api

// 23.1.3 auth_v2 is NOT JSON. RVA 0x3C1554C reads little-endian
// int16 byte lengths + shifted UTF-16LE key/value pairs. The final value's
// length includes the two-byte shift trailer; its low byte is the shift.
// This is vendor obfuscation, not encryption/authentication. Native bootstrap
// credentials still need independent verification by the account store.
import (
	"encoding/binary"
	"errors"
	"sort"
	"unicode/utf16"
)

const maxLegacyAuthBody = 64 << 10

func decodeLegacyAuth(data []byte) (map[string]string, error) {
	bad := errors.New("invalid legacy auth packet")
	if len(data) < 6 || len(data) > maxLegacyAuthBody || len(data)%2 != 0 || data[len(data)-1] != 0 {
		return nil, bad
	}
	shift := uint16(data[len(data)-2])
	decode := func(b []byte) string {
		chars := make([]uint16, len(b)/2)
		for i := range chars {
			chars[i] = binary.LittleEndian.Uint16(b[i*2:]) - shift
		}
		return string(utf16.Decode(chars))
	}
	fields := make(map[string]string)
	for pos := 0; pos < len(data)-2; {
		if len(fields) >= 128 || pos+4 > len(data)-2 {
			return nil, bad
		}
		keyLen := int(binary.LittleEndian.Uint16(data[pos:]))
		valueLen := int(binary.LittleEndian.Uint16(data[pos+2:]))
		pos += 4
		if keyLen == 0 || keyLen > 32767 || valueLen > 32767 || (keyLen|valueLen)&1 != 0 || pos+keyLen+valueLen > len(data) {
			return nil, bad
		}
		key := decode(data[pos : pos+keyLen])
		pos += keyLen
		final := pos+valueLen == len(data)
		actual := valueLen
		if final {
			actual -= 2
		}
		if actual < 0 || pos+actual > len(data)-2 {
			return nil, bad
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, bad
		}
		fields[key] = decode(data[pos : pos+actual])
		pos += valueLen
		if final {
			return fields, nil
		}
	}
	return nil, bad
}

func encodeLegacyAuth(fields map[string]string, shift byte) ([]byte, error) {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 || len(keys) > 128 {
		return nil, errors.New("invalid legacy auth fields")
	}
	var out []byte
	encode := func(text string) []byte {
		chars := utf16.Encode([]rune(text))
		raw := make([]byte, len(chars)*2)
		for i, c := range chars {
			binary.LittleEndian.PutUint16(raw[i*2:], c+uint16(shift))
		}
		return raw
	}
	for i, key := range keys {
		k, v := encode(key), encode(fields[key])
		size := len(v)
		if i == len(keys)-1 {
			size += 2
		}
		if len(k) == 0 || len(k) > 32767 || size > 32767 || len(out)+4+len(k)+size > maxLegacyAuthBody {
			return nil, errors.New("legacy auth fields too large")
		}
		out = binary.LittleEndian.AppendUint16(out, uint16(len(k)))
		out = binary.LittleEndian.AppendUint16(out, uint16(size))
		out = append(out, k...)
		out = append(out, v...)
	}
	return append(out, shift, 0), nil
}
