// SPDX-License-Identifier: GPL-3.0-or-later

package api

// Replies on the encrypted /sio channel.
//
// The framing here is transcribed from the client, not guessed. The 23.1.3
// build has patched BestHTTP so that
// BestHTTP.SocketIO.Transports.WebSocketTransport.OnBinary (RVA 0x3ED8B80)
// decrypts a packet's attachments before the packet is parsed. Reduced to what
// it does with each arriving frame:
//
//	PacketWithAttachment.AddAttachmentFromServer(frame, copyFull: false)
//	if attachments.Count != AttachmentCount { return }
//	OriginalPayload = Payload
//	n := attachments.Count
//	ReplacePlaceholderWithNumber(n-2, UTF8.GetString(Extras.Decrypt(attachments[n-2])))
//	ReplacePlaceholderWithNumber(n-1, UTF8.GetString(Extras.Decrypt(attachments[n-1])))
//	OnPacket(PacketWithAttachment)
//
// Four consequences fix the shape of every reply, and getting any one of them
// wrong makes the client drop the packet without a word:
//
//   - Exactly two attachments. The indices are computed as n-2 and n-1, so a
//     single attachment makes the client ask its list for element -1, and a
//     third would be left in the payload as an unsubstituted placeholder.
//
//   - One leading byte per attachment, which the client throws away.
//     AddAttachmentFromServer is called with copyFull: false, and that branch
//     copies from offset 1 (RVA 0x3ECCC6C). The byte is the engine.io v3 binary
//     marker the client puts in front of its own attachments.
//
//   - The substitution is literal. ReplacePlaceholderWithNumber ends in
//     Payload = Payload.Replace(placeholderJson, plaintext) and adds nothing
//     around the plaintext (RVA 0x3ECD170), so each plaintext has to be a JSON
//     fragment that leaves the payload valid. The event name therefore travels
//     as "auth_v2" with its quotes, where the client sends it bare.
//
//   - The name is hidden in attachment 0, the same way the client hides it on
//     the way out. The inbound dispatcher (RVA 0x35C396C) takes the name from
//     Packet.EventName and hands subscribers args[0] only, so the payload the
//     client has to end up holding is ["auth_v2", {...}]: two elements from two
//     attachments, and no name in clear text anywhere on the wire.
//
// One field decides whether an answer is seen at all.
// WebSocketManager.万下七世丞一丕专丝 (RVA 0x35C3D08) is
//
//	head := args[0] as Dictionary<string, object>
//	if head == null || !head.ContainsKey(K) { return }
//	pending.Complete(Convert.ToInt32(head[K]))
//
// where K is the string literal at slot 0x67C33C0 - the same literal the
// outbound request builder uses for req_id. The client keys its pending
// requests by that integer, so a reply that does not echo it is decrypted,
// parsed, dispatched, and then matched against nothing.
//
// The second dictionary a subscriber receives is not sent by the server at all:
// WebSocketManager.三丟丄下丂丏丅丏不 (RVA 0x35C3E54) looks it up in a client-local cache
// keyed by (req_id, handshake sid) and removes it. Only the head travels.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
)

const (
	// sioReplyAttachments is how many attachments an encrypted reply carries.
	// It is not a preference: the client decrypts exactly the last two
	// attachments of a packet and substitutes both.
	sioReplyAttachments = 2

	// sioAuthEvent is the event the client emits and then waits on, holding its
	// loading bar at the 78% mark until it is answered.
	sioAuthEvent = "auth_v2"

	// sioRequestIDKey correlates a reply with the request it answers.
	sioRequestIDKey = "req_id"
)

// encodeEncryptedEvent renders one encrypted binary event as the frames to
// write: a text packet declaring two attachments whose payload is nothing but
// placeholders, then the encrypted name, then the encrypted argument.
//
// SECURITY-SENSITIVE: both attachments are sealed with the session's own codec,
// which is derived from its sid. A reply encrypted under any other key is
// unreadable to the client and indistinguishable, from the outside, from a
// server that never answered.
func encodeEncryptedEvent(codec *sio.Codec, namespace, event string, args any) ([]sioFrame, error) {
	if codec == nil {
		return nil, errors.New("no /sio codec is in force for this session")
	}

	// The name is quoted here because the client substitutes the plaintext into
	// its payload text verbatim; see the note at the top of this file.
	name, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("encode the name of %q: %w", event, err)
	}
	body, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode the argument of %q: %w", event, err)
	}

	placeholders := make([]any, 0, sioReplyAttachments)
	for i := 0; i < sioReplyAttachments; i++ {
		placeholders = append(placeholders, sioPlaceholder{Placeholder: true, Num: i})
	}
	payload, err := json.Marshal(placeholders)
	if err != nil {
		return nil, fmt.Errorf("encode the placeholders of %q: %w", event, err)
	}

	var header strings.Builder
	header.WriteByte(eioMessage)
	header.WriteByte(sioBinaryEvent)
	header.WriteString(strconv.Itoa(sioReplyAttachments))
	header.WriteByte('-')
	if namespace != "" {
		header.WriteString(namespace)
		header.WriteByte(',')
	}
	header.Write(payload)

	return []sioFrame{
		{text: header.String()},
		{binary: true, data: withBinaryPrefix(codec.Encrypt(name))},
		{binary: true, data: withBinaryPrefix(codec.Encrypt(body))},
	}, nil
}

// sioHead is the single decoded argument an event carries.
//
// The client builds it as a JSON object of mixed types - the captured auth_v2
// request sends platform as "1" and type_device as 2 - so reads go through
// accessors that accept either spelling instead of a struct that would reject
// the payload outright over one field's type.
type sioHead map[string]any

// text reads a field as a string, whatever JSON type it arrived as.
func (h sioHead) text(key string) string {
	switch value := h[key].(type) {
	case nil:
		return ""
	case string:
		return value
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	default:
		return fmt.Sprint(value)
	}
}

// number reads a field as an integer, accepting both the JSON number and the
// quoted-string form. The second return reports whether the field was present
// and convertible, which for req_id is the difference between a reply the
// client can match and one it silently discards.
func (h sioHead) number(key string) (int, bool) {
	switch value := h[key].(type) {
	case float64:
		return int(value), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// decodedEventName reads the event name out of attachment 0.
//
// The client sends it bare, so "auth_v2" arrives as the seven bytes auth_v2.
// The quoted form is accepted as well, because that is what this server writes
// back and reading our own frames is how the reply path is tested.
func decodedEventName(raw []byte) string {
	name := strings.TrimSpace(string(raw))
	var quoted string
	if err := json.Unmarshal([]byte(name), &quoted); err == nil {
		return quoted
	}
	return name
}

// answerDecodedEvent replies to an event that has just been decrypted.
//
// Ban commits and dispatch are serialized: no new gameplay mutation may
// pass a successful ban response, including on an already authorized socket.
func (s *Server) answerDecodedEvent(session *sioSession, a *sioBinaryAssembly, codec *sio.Codec, decoded [][]byte) {
	if !s.cfg.SioReply || len(decoded) < 2 {
		return
	}

	event := decodedEventName(decoded[0])
	answer := sioAnswerFor(event)
	if answer == nil {
		s.log.Event("socket.event_unanswered", map[string]any{
			"sid":   session.sid,
			"event": event,
			"note":  "no answer is implemented for this event yet",
		})
		return
	}

	var head sioHead
	if err := json.Unmarshal(decoded[1], &head); err != nil {
		s.log.Event("socket.event_unreadable", map[string]any{
			"sid":   session.sid,
			"event": event,
			"err":   err.Error(),
			"note":  "the decrypted argument is not a JSON object",
		})
		return
	}

	reqID, ok := head.number(sioRequestIDKey)
	if !ok {
		s.log.Event("socket.event_uncorrelated", map[string]any{
			"sid":   session.sid,
			"event": event,
			"key":   sioRequestIDKey,
			"note":  "the request carries no integer request id, so no reply can be matched to it",
		})
		return
	}

	s.banMu.RLock()
	defer s.banMu.RUnlock()
	if event == sioAuthEvent {
		session.mu.Lock()
		session.authNamespace = a.namespace
		session.mu.Unlock()
	}
	var body map[string]any
	_, banned := s.socketBan(session)
	if banned && event != sioAuthEvent {
		body = socketBanReply(reqID)
	} else {
		body = answer(s, session, head, reqID)
	}
	if body == nil {
		// The builder declined to answer and logged why. Sending an invented
		// payload instead can hang the client harder than silence, because a
		// field of the wrong type throws inside its own handler.
		return
	}
	if !s.replyEncrypted(session, a.namespace, a.transport, codec, event, reqID, body) {
		return
	}
	if event == sioSubscribeEvent && !banned {
		// The poll is answered above for the facade that asked it; the
		// documents themselves go to the config system, which listens on
		// its own event. See socketio_configpush.go for the measurement.
		s.pushConfigDocuments(session, a.namespace, a.transport, codec, head)
		s.pushSubscribedPlayerInfo(session, a.namespace, a.transport, codec, head, reqID)
		s.pushMailboxSubscription(session, a.namespace, a.transport, codec, head)
	}
	if event == sioAuthEvent {
		// Authorisation is the one reply the loading bar waits on, so it keeps
		// a line of its own naming the identity the client presented.
		s.log.Event("socket.auth_answered", map[string]any{
			"sid":            session.sid,
			"req_id":         reqID,
			"client_uniq_id": head.text("uniq_id"),
			"client_sid":     head.text("sid"),
		})
	}
}

// authReplyBody builds the one argument an auth_v2 answer carries.
//
// req_id is the only field the transport itself requires: the client keys its
// pending requests by that integer (RVA 0x35C3D08) and matches an answer
// against nothing without it. The rest names the result the way the retired
// /auth_v2/ HTTP contract named it - see handleLegacyAuth - because the two
// endpoints answered the same question and a JSON consumer ignores members it
// does not know.
//
// The client's own identity is echoed rather than replaced. It arrives holding
// a uniq_id from its local state and will keep using it, so minting a different
// one here would only make the session disagree with itself.
//
// Nothing is written to the account store yet. The open question this reply
// exists to answer is whether the framing is accepted at all, and a store write
// on the socket path would add a second way for the attempt to fail.
func authReplyBody(session *sioSession, head sioHead, reqID int) map[string]any {
	return map[string]any{
		sioRequestIDKey: reqID,
		"result":        "ok",
		"error":         0,
		"uniq_id":       head.text("uniq_id"),
		"sid":           session.sid,
		"time":          time.Now().UTC().Unix(),
	}
}
