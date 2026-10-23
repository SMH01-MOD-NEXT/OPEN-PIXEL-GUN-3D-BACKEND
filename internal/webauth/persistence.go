// SPDX-License-Identifier: GPL-3.0-or-later
package webauth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/securefile"
	"time"
)

type savedSession struct {
	Identity Identity  `json:"identity"`
	Created  time.Time `json:"created"`
	Seen     time.Time `json:"seen"`
}

func Open(path string, key []byte) (*Service, error) {
	if len(key) != 32 {
		return nil, errors.New("invalid session key")
	}
	s, e := New()
	if e != nil {
		return nil, e
	}
	s.path = path
	copy(s.storageKey[:], key)
	s.key = sha256.Sum256(append(append([]byte{}, key...), []byte("OPG3D-session-signing-v1")...))
	rows := map[string]savedSession{}
	if e = securefile.Read(path, key, "OPG3D-SESSIONS-v1", &rows); e != nil {
		return nil, e
	}
	if rows == nil || len(rows) > maxRecords {
		return nil, ErrLimited
	}
	for id, r := range rows {
		raw, e := hex.DecodeString(id)
		if e != nil || len(raw) != 32 {
			return nil, ErrInvalid
		}
		var k [32]byte
		copy(k[:], raw)
		if r.Identity.PlayerID == "" || r.Created.IsZero() || r.Created.After(time.Now().Add(time.Minute)) {
			return nil, ErrInvalid
		}
		s.sessions[k] = &session{identity: r.Identity, created: r.Created, seen: r.Seen}
	}
	s.prune(s.now())
	return s, nil
}
func (s *Service) persist() error {
	rows := map[string]savedSession{}
	for k, r := range s.sessions {
		rows[hex.EncodeToString(k[:])] = savedSession{r.identity, r.created, r.seen}
	}
	return securefile.Write(s.path, s.storageKey[:], "OPG3D-SESSIONS-v1", rows)
}
