// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

func (s *Server) buildSupportEvidence(a store.Account) map[string]any {
	out := map[string]any{"player_id": a.PlayerID, "nick": a.Nick, "created_at": a.CreatedAt, "last_seen_at": a.LastSeenAt, "last_ip": a.LastIP, "device_id": a.DeviceID, "socket_device": a.SocketDevice, "observations": s.st.Evidence(a.PlayerID).Observations, "wallet_history": []store.WalletEvent{}}
	if p, e := s.st.Profile(a.PlayerID); e == nil {
		out["wallet_history"] = p.WalletHistory
		out["level"] = p.Level
		out["coins"] = p.Coins
		out["gems"] = p.Gems
	}
	return out
}
