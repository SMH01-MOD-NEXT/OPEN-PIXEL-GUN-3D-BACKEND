// SPDX-License-Identifier: GPL-3.0-or-later

package api

// The get_inapps answer.
//
// Run 140 measured a boot that parks in AuthSceneState.SendProgress (13) with
// every request answered and none refused. Exactly one request is re-asked, on
// a thirty second timer:
//
//	+013974ms  get_inapps  req_id 15
//	+044121ms  get_inapps  req_id 60
//	+074258ms  get_inapps  req_id 61
//
// A retry on a fixed timer is the signature of an answer the reply reader
// could not use, not of a missing answer: the fallback did reply to all three,
// and the client asked again anyway.
//
// The request carries only the ids it wants, measured from the client own
// encrypted frame in run 140:
//
//	{"ids":["918930694"], "req_id":15, "sid":"bfbaa3c9c8767f069ca55aed"}
//
// The reply reader is 丈三丞丘且且丗下专.丌专东业下丟七丝丆 at RVA 0x294DF50, reached from
// the handler at 0x294E108. It indexes three keys and nothing else, at
// +0xDC, +0xF0 and +0x104:
//
//	err_code
//	clan_timeout
//	clan_war_timeout
//
// So those three are what this answer has to contain. Nothing beyond them is
// invented: run 36 measured that err_code on every reply silences the boot, so
// it goes only where a reader was measured indexing it, and a member of the
// wrong type throws inside the client handler, which is indistinguishable from
// no answer at all.
const (
	// sioInappErrCodeKey is the member the reply reader indexes first.
	sioInappErrCodeKey = "err_code"
	// sioInappClanTimeoutKey and sioInappClanWarTimeoutKey are the other two
	// members that reader indexes, both read as numbers.
	sioInappClanTimeoutKey    = "clan_timeout"
	sioInappClanWarTimeoutKey = "clan_war_timeout"
	// sioInappIDsKey is the member the request carries. It is echoed so the
	// client can match the answer to the ids it asked about.
	sioInappIDsKey = "ids"
)

// answerGetInapps answers the purchase query with the three members its reply
// reader indexes, plus the ids it asked about echoed back unchanged.
//
// The ids are echoed rather than rebuilt: they arrive as the client own JSON
// array and are handed back with the same element types, so no conversion in
// the reader can fail on them. This server sells nothing yet, so both timeouts
// are zero, which is a real answer rather than a placeholder: it says there is
// no clan purchase cooldown outstanding for this account.
func (s *Server) answerGetInapps(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	body[sioInappErrCodeKey] = 0
	body[sioInappClanTimeoutKey] = 0
	body[sioInappClanWarTimeoutKey] = 0

	asked := 0
	if ids, ok := head[sioInappIDsKey].([]any); ok {
		body[sioInappIDsKey] = ids
		asked = len(ids)
	}

	_, _, playerID := session.identity()
	s.log.Event("socket.inapps_asked", map[string]any{
		"sid":       session.sid,
		"req_id":    reqID,
		"player_id": playerID,
		"asked":     asked,
		"note":      "answered with err_code, clan_timeout and clan_war_timeout, the three keys the reader at 0x294DF50 indexes; the fallback ok lacked all three and the client re-asked every thirty seconds",
	})
	return body
}
