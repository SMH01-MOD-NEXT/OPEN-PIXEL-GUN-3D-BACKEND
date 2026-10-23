package clienterrors

// Diagnostics credentials are purpose-scoped: never accepted by gameplay or
// admin authentication. A separate persistent key survives backend restarts.
import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Credentials struct{ key []byte }

func OpenCredentials(dir string) (*Credentials, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if dir != "" {
		path := filepath.Join(dir, "client-diagnostics.key")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, err = f.Write(key)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				os.Remove(path)
				return nil, err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		key, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(key) != 32 {
			return nil, errors.New("invalid diagnostics key")
		}
	}
	return &Credentials{key: key}, nil
}

type credentialPayload struct {
	Player  string `json:"p"`
	Expires int64  `json:"e"`
}

func (c *Credentials) Mint(player string, now time.Time) string {
	if c == nil {
		return ""
	}
	raw, _ := json.Marshal(credentialPayload{player, now.Add(24 * time.Hour).Unix()})
	payload := base64.RawURLEncoding.EncodeToString(raw)
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte("opg3d-client-errors-v1:" + payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func (c *Credentials) Valid(token, player string, now time.Time) bool {
	if c == nil || len(token) > 512 || player == "" {
		return false
	}
	p, s, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte("opg3d-client-errors-v1:" + p))
	if !hmac.Equal(sig, m.Sum(nil)) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return false
	}
	var data credentialPayload
	return json.Unmarshal(raw, &data) == nil && data.Player == player && data.Expires > now.Unix() && data.Expires <= now.Add(24*time.Hour).Unix()
}
