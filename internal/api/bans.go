// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

// Bound operator input before time.Duration multiplication. Zero means
// permanent; negative or overflowing durations must never become permanent.
func banDeadline(minutes int) (*time.Time, error) {
	const maxMinutes = int64((1<<63 - 1) / int64(time.Minute))
	if minutes < 0 || int64(minutes) > maxMinutes {
		return nil, errors.New("minutes must be zero (permanent) or a positive representable duration")
	}
	if minutes == 0 {
		return nil, nil
	}
	t := time.Now().UTC().Add(time.Duration(minutes) * time.Minute)
	return &t, nil
}

func playerBanSubjects(a store.Account) []store.Ban {
	out := []store.Ban{{Kind: store.BanPlayer, Subject: a.PlayerID}}
	seen := map[string]bool{}
	for _, device := range []string{a.DeviceID, a.SocketDevice} {
		if device != "" && !seen[device] {
			out = append(out, store.Ban{Kind: store.BanDevice, Subject: device})
			seen[device] = true
		}
	}
	return out
}
func (s *Server) accountBan(a store.Account, ip string) (store.Ban, bool) {
	if b, hit := s.st.MatchBan(a.PlayerID, a.DeviceID, ip); hit {
		return b, true
	}
	return s.st.MatchBan(a.PlayerID, a.SocketDevice, ip)
}
func (s *Server) socketBan(session *sioSession) (store.Ban, bool) {
	uniq, device, player := session.identity()
	bootstrap, ip := session.bootstrapIdentity()
	if b, hit := s.st.MatchBan(uniq, device, ip); hit {
		return b, true
	}
	for _, id := range []string{player, bootstrap, uniq} {
		if a, err := s.st.Account(id); err == nil {
			if b, hit := s.accountBan(a, ip); hit {
				return b, true
			}
		}
	}
	return store.Ban{}, false
}

// AuthScene's HTTP callback (23.1.3 RVA 0x3C1483C) only interprets
// err_code=1402 after HTTP success and decrypt. It reads player_id/hash first.
// create_to controls the destructive "start new game" countdown, NOT ban
// expiry. Keep that button disabled rather than offering to erase progress.
// Expiry and unban are checked authoritatively on every subsequent login.
func (s *Server) writeLegacyBan(w http.ResponseWriter, fields map[string]string, a store.Account, b store.Ban) {
	player := firstNonEmpty(a.PlayerID, fields["player_id"], fields["id_player"])
	if fields != nil {
		raw, err := encodeLegacyAuth(map[string]string{"err_code": "1402", "player_id": player, "hash": fields["hash"], "create_to": "2147483647", "wait": "0"}, 37)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "legacy_auth_encode_failed", "cannot encode auth response")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	} else {
		s.writeJSON(w, http.StatusOK, map[string]any{"err_code": 1402, "player_id": player, "hash": "", "create_to": 2147483647, "wait": 0})
	}
	s.log.Event("account.legacy_ban_enforced", map[string]any{"player_id": a.PlayerID, "kind": b.Kind, "err_code": 1402})
}

// Socket auth does not render the HTTP ban screen. 1419 is stock revoked
// authorization; unauthorized gameplay replies retain the same typed error.
func socketBanReply(reqID int) map[string]any {
	body := sioDenied(reqID)
	body["err_code"] = 1419
	body["reason"] = 1 // stock expired_session handlers call Convert.ToInt32
	return body
}
func (s *Server) writeBanMutationError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, store.ErrBadBanKind) || errors.Is(err, store.ErrEmptyBanSubject) {
		status = http.StatusBadRequest
	}
	s.writeError(w, status, "ban_mutation_failed", err.Error())
}

// Called only after a durable commit, while banMu excludes gameplay dispatch.
// The stock expired_session subscriber (0x3C15D1C, 0x3DCBE34) expects numeric
// reason=1. Do not emit "ban": that event is an outbound anti-cheat report.
// Retain the identity for enforcement/expiry; never touch profile revisions,
// player progress, command receipts or the account's native session token.
func (s *Server) enforceOnlineBans() {
	s.sio.mu.Lock()
	sessions := make([]*sioSession, 0, len(s.sio.sessions))
	for _, session := range s.sio.sessions {
		sessions = append(sessions, session)
	}
	s.sio.mu.Unlock()
	notified := 0
	for _, session := range sessions {
		if _, hit := s.socketBan(session); !hit {
			continue
		}
		codec := s.sio.codec
		if codec == nil {
			var err error
			codec, err = sio.NewForSid(session.sid, sio.DefaultMode)
			if err != nil {
				continue
			}
		}
		session.mu.Lock()
		namespace := session.authNamespace
		session.mu.Unlock()
		frames, err := encodeEncryptedEvent(codec, namespace, "expired_session", map[string]any{"reason": 1})
		if err != nil {
			continue
		}
		sent := true
		for _, frame := range frames {
			if !session.send(frame) {
				sent = false
				break
			}
		}
		if sent {
			notified++
		} else {
			// A full queue cannot reliably carry a multi-frame push. Abort the
			// affected transport only; the next auth is still denied durably.
			_, _, id := session.identity()
			session.abortReloginTransportForPlayer(id)
		}
	}
	s.log.Event("admin.ban_sessions_enforced", map[string]any{"notified": notified})
}
