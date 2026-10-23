// SPDX-License-Identifier: GPL-3.0-or-later
package securefile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedAtomicPurposeAndTampering(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	p := filepath.Join(t.TempDir(), "state.enc")
	v := map[string]string{"secret": "private sentinel"}
	if e := Write(p, key, "purpose", v); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(p)
	if bytes.Contains(b, []byte(v["secret"])) {
		t.Fatal("plaintext")
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	var out map[string]string
	if e := Read(p, key, "purpose", &out); e != nil || out["secret"] != v["secret"] {
		t.Fatal(e)
	}
	if Read(p, key, "other", &out) == nil || Read(p, bytes.Repeat([]byte{8}, 32), "purpose", &out) == nil {
		t.Fatal("authentication bypass")
	}
	b[len(b)-1] ^= 1
	os.WriteFile(p, b, 0600)
	if Read(p, key, "purpose", &out) == nil {
		t.Fatal("tampering")
	}
	blocked := filepath.Join(t.TempDir(), "block")
	os.WriteFile(blocked, []byte("x"), 0600)
	if Write(filepath.Join(blocked, "x"), key, "purpose", v) == nil {
		t.Fatal("write should fail")
	}
}
