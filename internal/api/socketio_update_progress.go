// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

// update_progress is not a generic request/reply operation. The transport
// correlates the encrypted response with req_id, but the stock progress manager
// then performs a second lookup in its own pending-command dictionary.
//
// The reader at libil2cpp.so+0x1B41B28 indexes exactly these three members:
//
//   - err_code: converted to Int32; zero follows the success path
//   - i:         the outer command id used as the pending-dictionary key
//   - ci:        the optional child command id used by command-error handling
//
// Run 158 proved that a generic {error, req_id, result} acknowledgement reaches
// this reader but leaves the AfterProgressSync waiter parked because it carries
// no i. The literals were resolved from the progress manager's static
// constructor at 0x1B66584 and 0x1B66594 rather than inferred from the request.
const (
	sioUpdateProgressErrCodeKey   = "err_code"
	sioUpdateProgressCommandIDKey = "i"
	sioUpdateProgressChildIDKey   = "ci"
	sioUpdateProgressPayloadKey   = "p"
	sioUpdateProgressCommandsKey  = "c"
)

// updateProgressChildID returns the single child correlation id carried by the
// measured command-batch shape. It deliberately omits ci for an empty or
// multi-child batch rather than pretending that one child represents all of
// them. The success path is keyed by the outer i either way.
func updateProgressChildID(head sioHead) string {
	payload, ok := head[sioUpdateProgressPayloadKey].(map[string]any)
	if !ok {
		return ""
	}
	commands, ok := payload[sioUpdateProgressCommandsKey].([]any)
	if !ok || len(commands) != 1 {
		return ""
	}
	command, ok := commands[0].(map[string]any)
	if !ok {
		return ""
	}
	return sioHead(command).text(sioUpdateProgressChildIDKey)
}

// answerUpdateProgress acknowledges one client command batch using the
// identifiers the stock manager needs to retire its pending entry. Identity is
// taken only from the authenticated session; no payload field can select a
// different profile.
func (s *Server) answerUpdateProgress(session *sioSession, head sioHead, reqID int) map[string]any {
	body := sioOK(reqID)
	commandID := head.text(sioUpdateProgressCommandIDKey)
	childID := updateProgressChildID(head)
	if commandID != "" {
		body[sioUpdateProgressCommandIDKey] = commandID
	}
	if childID != "" {
		body[sioUpdateProgressChildIDKey] = childID
	}

	_, _, playerID := session.identity()
	if playerID == "" {
		body[sioUpdateProgressErrCodeKey] = sioErrNotAuthorized
		body["error"] = sioErrNotAuthorized
		body["result"] = "not_authorized"
		s.log.Event("socket.progress_update_denied", map[string]any{
			"sid":        session.sid,
			"req_id":     reqID,
			"command_id": commandID,
			"child_id":   childID,
			"note":       "the request was not allowed to name an account; no auth_v2 identity is bound to this session",
		})
		return body
	}

	commands := progressCounterCommands(head)
	applied := 0
	if len(commands) != 0 {
		var err error
		applied, err = s.st.ApplyProfileCounterCommands(playerID, session.progressWriteContext(), commands)
		if err != nil {
			body[sioUpdateProgressErrCodeKey] = 1
			body["error"] = 1
			body["result"] = "progress_store_failed"
			// Stock reconnect alone can retain the same optimistic local economy
			// state forever. Arm the real full-download handshake before its
			// error/reconnect path; never abort the transport or grant early.
			if store.RequiresEconomyResync(err) {
				profile, syncErr := s.st.RequestProfileProgressResync(playerID, session.progressWriteContext())
				if syncErr == nil {
					body["result"] = "progress_resync_required"
					s.diagnostic(playerID, "warning", "resync_required", "Server-backed economy conflict; authoritative progress download required", profile.ProgressRevision)
					s.log.Event("socket.economy_resync_required", map[string]any{"player_id": playerID, "req_id": reqID, "revision": profile.ProgressRevision, "reason": err.Error()})
				} else {
					s.diagnostic(playerID, "error", "resync_failed", "Unable to schedule authoritative progress download", 0)
					s.log.Event("socket.economy_resync_failed", map[string]any{"player_id": playerID, "req_id": reqID, "err": syncErr.Error()})
				}
			}
			if !store.RequiresEconomyResync(err) {
				s.diagnostic(playerID, "error", "progress_update_failed", err.Error(), 0)
			}
			s.log.Event("socket.progress_update_failed", map[string]any{"player_id": playerID, "req_id": reqID, "err": err.Error()})
			return body
		}
	}
	// Crafted clan items (182) and building contributions (206) are shared
	// with the clan; this never fails the player's own acknowledgement.
	s.applyClanProgress(playerID, head)
	body[sioUpdateProgressErrCodeKey] = sioErrNone
	s.log.Event("socket.progress_update_acknowledged", map[string]any{
		"sid":                      session.sid,
		"player_id":                playerID,
		"req_id":                   reqID,
		"command_id":               commandID,
		"child_id":                 childID,
		"applied_counter_commands": applied,
		"note":                     "persisted measured counter/inventory commands before acknowledging i/ci",
	})
	return body
}

// Only measured schemas are forwarded; unknown commands keep their lossless
// snapshot path and are never interpreted using guessed inventory fields.
func progressCounterCommands(head sioHead) []any {
	var source []any
	if payload, ok := head["p"].(map[string]any); ok {
		source, _ = payload["c"].([]any)
	}
	if source == nil {
		// Native standalone SaveConfig(322), observed on the live 23.1.3
		// channel, has outer i but NO child ci. Reuse its stable outer ID
		// internally for receipts; never invent a child ID in the wire reply.
		command := make(map[string]any, len(head)+1)
		for k, v := range head {
			command[k] = v
		}
		if ci, _ := command["ci"].(string); ci == "" {
			if outer := head.text("i"); outer != "" {
				command["ci"] = "outer:" + outer
			}
		}
		source = []any{command}
	}
	var result []any
	for _, raw := range source {
		command, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, ok := slotCounter(command["id"])
		if !ok {
			continue
		}
		if store.SupportsProgressCommand(id) {
			result = append(result, command)
		}
	}
	return result
}
