// SPDX-License-Identifier: GPL-3.0-or-later

package api

// answerPlayerSuccessInit acknowledges the stock post-login event with the
// three integers its 23.1.3 subscriber reads. Run 174 reached 100% with zeros;
// spelling them explicitly removes the generic-fallback dependency.
func (s *Server) answerPlayerSuccessInit(session *sioSession, head sioHead, reqID int) map[string]any {
	_, _, playerID := session.identity()
	if playerID == "" {
		return sioDenied(reqID)
	}
	body := sioOK(reqID)
	body["err_code"] = sioErrNone
	body["clan_timeout"] = 0
	body["clan_war_timeout"] = 0
	s.log.Event("socket.player_success_init", map[string]any{
		"sid":       session.sid,
		"player_id": playerID,
		"req_id":    reqID,
	})
	return body
}
