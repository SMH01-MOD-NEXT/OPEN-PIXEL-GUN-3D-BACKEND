package clienterrors

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCredentialsScopeExpiryAndRestart(t *testing.T) {
	dir := t.TempDir()
	c, e := OpenCredentials(dir)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	token := c.Mint("123456", now)
	if !c.Valid(token, "123456", now) || c.Valid(token, "other", now) || c.Valid(token+"x", "123456", now) || c.Valid(token, "123456", now.Add(24*time.Hour)) {
		t.Fatal("credential validation")
	}
	reopened, e := OpenCredentials(dir)
	if e != nil || !reopened.Valid(token, "123456", now) {
		t.Fatal("restart", e)
	}
	st, _ := os.Stat(filepath.Join(dir, "client-diagnostics.key"))
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	os.WriteFile(filepath.Join(dir, "client-diagnostics.key"), []byte("broken"), 0600)
	if _, e = OpenCredentials(dir); e == nil {
		t.Fatal("corrupt key accepted")
	}
}
