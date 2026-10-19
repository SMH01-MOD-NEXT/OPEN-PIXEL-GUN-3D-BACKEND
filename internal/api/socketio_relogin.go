// SPDX-License-Identifier: GPL-3.0-or-later

package api

// sioReloginTransport is the live transport capability an authorised session
// exposes to a backend operator edit. It models abnormal transport loss, not a
// fabricated client request or an Engine.IO close packet.
type sioReloginTransport interface {
	abortForRelogin() bool
}

func (s *sioSession) attachReloginTransport(transport sioReloginTransport) {
	s.mu.Lock()
	s.reloginTransport = transport
	s.mu.Unlock()
}

func (s *sioSession) detachReloginTransport(transport sioReloginTransport) {
	s.mu.Lock()
	if s.reloginTransport == transport {
		s.reloginTransport = nil
	}
	s.mu.Unlock()
}

// abortReloginTransportForPlayer atomically checks the canonical auth_v2
// identity and consumes only this session's live transport.
func (s *sioSession) abortReloginTransportForPlayer(playerID string) bool {
	if playerID == "" {
		return false
	}
	s.mu.Lock()
	if s.playerID != playerID || s.reloginTransport == nil {
		s.mu.Unlock()
		return false
	}
	transport := s.reloginTransport
	s.reloginTransport = nil
	s.mu.Unlock()
	return transport.abortForRelogin()
}

// reloginPlayer aborts every currently live transport for one canonical player
// and no other identity. Multiple devices of the same account are eligible;
// foreign, stale and polling-only sessions are untouched.
func (h *sioHub) reloginPlayer(playerID string) int {
	h.mu.Lock()
	sessions := make([]*sioSession, 0, len(h.sessions))
	for _, session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.mu.Unlock()

	aborted := 0
	for _, session := range sessions {
		if session.abortReloginTransportForPlayer(playerID) {
			aborted++
		}
	}
	return aborted
}

// reloginOnlinePlayerAfterProgressEdit is called only after the edit is durable
// and its HTTP success body has been written. The durable per-device revision
// is already pending before transport loss, including after a process restart.
func (s *Server) reloginOnlinePlayerAfterProgressEdit(playerID string, revision uint64) {
	canonicalID := playerID
	if account, err := s.st.Account(playerID); err == nil {
		canonicalID = account.PlayerID
	}
	aborted := s.sio.reloginPlayer(canonicalID)
	s.log.Event("admin.progress_relogin", map[string]any{
		"player_id": canonicalID,
		"revision":  revision,
		"sessions":  aborted,
		"online":    aborted != 0,
		"method":    "transport_abort_force_server_sync",
	})
}
