// SPDX-License-Identifier: GPL-3.0-or-later

package api

// Binary attachment framing for the live config channel.
//
// A socket.io binary event does not travel as one packet. The client first
// sends a text packet declaring how many attachments follow, carrying the JSON
// arguments with one placeholder object per attachment, and then sends that
// many binary frames. The 2026-08-27 capture shows exactly this:
//
//	40/sio
//	452-/sio,[{"_placeholder":true, "num":0}, {"_placeholder":true, "num":1}]
//	<binary 17 bytes>   04 37 4b c0 ...
//	<binary 609 bytes>  04 2d 1c c1 ...
//
// The event is meaningless until the halves are put back together, and this
// client puts the *event name itself* in attachment 0 rather than in the JSON.
// The previous revision logged each binary frame and dropped it, so every
// capture reported `event=<unparsed>`: not a parser bug but a framing one.
//
// Two properties of the capture are worth stating, because the rest of the
// channel work depends on them:
//
//   - The leading 0x04 is the engine.io v3 binary marker. BestHTTP sends it on
//     the WebSocket transport even though it asks for EIO=4, which is the same
//     v2-client behaviour that made the namespace auto-connect necessary. It is
//     stripped on the way in and restored on the way out, and recorded per
//     attachment rather than assumed.
//   - Once the marker is removed both attachments are AES-block-aligned
//     (16 bytes and 608 = 38 x 16). The payload is encrypted, so it is traced
//     verbatim and never parsed as MessagePack here. Reading it needs the codec
//     task, not a guess in this file.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// eioBinaryPrefix is the engine.io v3 binary marker that precedes an
	// attachment on the WebSocket transport.
	eioBinaryPrefix = 0x04

	// sioMaxAttachments caps how many attachments one event may declare, so a
	// broken or hostile peer cannot make the server hold frames without bound.
	sioMaxAttachments = 16

	// sioAssemblyTimeout bounds how long an event waits for its attachments.
	// A client that declares two and sends one must not pin the buffer for the
	// life of the session.
	sioAssemblyTimeout = 30 * time.Second

	// aesBlockSize is used only as a heuristic in traces: an attachment whose
	// length is a multiple of it is consistent with block-cipher output.
	aesBlockSize = 16
)

// sioPlaceholder is the marker socket.io substitutes for each attachment in a
// binary event's JSON arguments.
type sioPlaceholder struct {
	Placeholder bool `json:"_placeholder"`
	Num         int  `json:"num"`
}

// sioAttachment is one reassembled binary attachment.
type sioAttachment struct {
	data []byte
	// prefixed records whether the engine.io v3 marker was present and removed.
	// It is kept rather than normalised away because the reply framing has to
	// match whatever the client actually uses.
	prefixed bool
}

// sioBinaryAssembly is a BINARY_EVENT or BINARY_ACK whose text header has
// arrived and whose attachments are still being collected.
type sioBinaryAssembly struct {
	kind        byte
	namespace   string
	ack         string
	args        string
	expected    int
	attachments []sioAttachment
	started     time.Time
	transport   string
}

func (a *sioBinaryAssembly) complete() bool {
	return len(a.attachments) >= a.expected
}

func (a *sioBinaryAssembly) kindName() string {
	if a.kind == sioBinaryAck {
		return "binary_ack"
	}
	return "binary_event"
}

func (a *sioBinaryAssembly) attachment(num int) (sioAttachment, bool) {
	if num < 0 || num >= len(a.attachments) {
		return sioAttachment{}, false
	}
	return a.attachments[num], true
}

// attachmentOutcome reports what happened to an incoming binary frame.
type attachmentOutcome int

const (
	// attachmentUnexpected: no binary event was waiting for it.
	attachmentUnexpected attachmentOutcome = iota
	// attachmentBuffered: stored, more attachments are still expected.
	attachmentBuffered
	// attachmentCompleted: stored, and the event is now whole.
	attachmentCompleted
	// attachmentExpired: the waiting event outlived sioAssemblyTimeout.
	attachmentExpired
)

// parseBinaryHeader splits the body of a BINARY_EVENT / BINARY_ACK packet into
// the declared attachment count and the ordinary
// "[/namespace,][ack][json]" tail that follows it.
func parseBinaryHeader(body string) (count int, rest string, err error) {
	dash := strings.Index(body, "-")
	if dash <= 0 || !isAllDigits(body[:dash]) {
		return 0, body, fmt.Errorf("no attachment count in %q", body)
	}
	count, convErr := strconv.Atoi(body[:dash])
	if convErr != nil {
		return 0, body, fmt.Errorf("attachment count %q: %w", body[:dash], convErr)
	}
	if count < 0 || count > sioMaxAttachments {
		return 0, body, fmt.Errorf("attachment count %d is outside 0..%d", count, sioMaxAttachments)
	}
	return count, body[dash+1:], nil
}

// splitEventTail pulls the namespace and ack id off the front of an event tail,
// leaving the JSON argument array.
func splitEventTail(tail string) (namespace, ack, args string) {
	args = tail
	if strings.HasPrefix(args, "/") {
		if comma := strings.Index(args, ","); comma >= 0 {
			namespace = args[:comma]
			args = args[comma+1:]
		}
	}
	if bracket := strings.Index(args, "["); bracket > 0 && isAllDigits(args[:bracket]) {
		ack = args[:bracket]
		args = args[bracket:]
	}
	return namespace, ack, args
}

// stripBinaryPrefix removes the engine.io v3 binary marker when it is present.
func stripBinaryPrefix(data []byte) (payload []byte, prefixed bool) {
	if len(data) > 0 && data[0] == eioBinaryPrefix {
		return data[1:], true
	}
	return data, false
}

// withBinaryPrefix returns the attachment with the engine.io v3 binary marker
// in front, which is the framing this client sends and therefore parses.
func withBinaryPrefix(data []byte) []byte {
	out := make([]byte, 0, len(data)+1)
	out = append(out, eioBinaryPrefix)
	return append(out, data...)
}

// encodePollingAttachment renders an attachment for the XHR transport, which
// has no binary frame: engine.io base64-encodes it and marks it with "b".
func encodePollingAttachment(data []byte) string {
	return "b" + base64.StdEncoding.EncodeToString(data)
}

// decodePollingAttachment decodes a "b"-marked base64 attachment from the XHR
// transport.
func decodePollingAttachment(packet string) ([]byte, error) {
	if packet == "" || packet[0] != 'b' {
		return nil, fmt.Errorf("packet %q is not a base64 attachment", packet)
	}
	data, err := base64.StdEncoding.DecodeString(packet[1:])
	if err != nil {
		return nil, fmt.Errorf("decode base64 attachment: %w", err)
	}
	return data, nil
}

// beginAssembly stores a binary event that is waiting for attachments and
// returns any assembly it displaced, which is an abandoned event worth logging.
func (s *sioSession) beginAssembly(a *sioBinaryAssembly) *sioBinaryAssembly {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.pending
	s.pending = a
	return previous
}

// addAttachment appends one attachment to the pending event and reports what
// that meant. A completed or expired assembly is handed back and forgotten, so
// the caller owns it and no lock is held while it is logged.
func (s *sioSession) addAttachment(att sioAttachment) (*sioBinaryAssembly, attachmentOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return nil, attachmentUnexpected
	}
	if time.Since(s.pending.started) > sioAssemblyTimeout {
		stale := s.pending
		s.pending = nil
		return stale, attachmentExpired
	}
	s.pending.attachments = append(s.pending.attachments, att)
	if !s.pending.complete() {
		return nil, attachmentBuffered
	}
	done := s.pending
	s.pending = nil
	return done, attachmentCompleted
}

// takeAssembly removes and returns the pending event, if there is one.
func (s *sioSession) takeAssembly() *sioBinaryAssembly {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := s.pending
	s.pending = nil
	return pending
}

// describeAttachment summarises an attachment for a log line. Block alignment
// is included because it is the cheapest available evidence about what the
// bytes are, and it is the observation that sends the next step to the codec
// rather than to a MessagePack decoder.
func describeAttachment(att sioAttachment) string {
	aligned := len(att.data) > 0 && len(att.data)%aesBlockSize == 0
	return fmt.Sprintf("%d bytes prefixed=%t aes_block_aligned=%t", len(att.data), att.prefixed, aligned)
}

// resolveEvent substitutes the attachments back into the JSON arguments and
// reports what the event is called.
//
// The name is arg[0]. When that argument is a placeholder rather than a string,
// the name arrived inside an attachment and cannot be read here; it is then
// reported by size and block alignment instead of being guessed at, and
// nameInAttachment is set so the caller can say so in the trace.
func (a *sioBinaryAssembly) resolveEvent() (name string, resolved []string, nameInAttachment bool) {
	name = "<unparsed>"
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(a.args), &raw); err != nil {
		return name, nil, false
	}

	resolved = make([]string, 0, len(raw))
	for i, arg := range raw {
		var placeholder sioPlaceholder
		if err := json.Unmarshal(arg, &placeholder); err == nil && placeholder.Placeholder {
			description := fmt.Sprintf("<attachment %d missing>", placeholder.Num)
			if att, ok := a.attachment(placeholder.Num); ok {
				description = describeAttachment(att)
			}
			resolved = append(resolved, description)
			if i == 0 {
				name = "<binary:" + description + ">"
				nameInAttachment = true
			}
			continue
		}
		resolved = append(resolved, string(arg))
		if i == 0 {
			var text string
			if err := json.Unmarshal(arg, &text); err == nil {
				name = text
			}
		}
	}
	return name, resolved, nameInAttachment
}

// beginBinaryEvent records the text half of a binary event and starts waiting
// for the attachments the client is about to send.
func (s *Server) beginBinaryEvent(session *sioSession, kind byte, body, transport string) {
	count, tail, err := parseBinaryHeader(body)
	if err != nil {
		s.log.Event("socket.binary_header_bad", map[string]any{
			"sid":       session.sid,
			"transport": transport,
			"err":       err.Error(),
			"payload":   s.log.Body([]byte(body)),
		})
		return
	}

	namespace, ack, args := splitEventTail(tail)
	assembly := &sioBinaryAssembly{
		kind:      kind,
		namespace: namespace,
		ack:       ack,
		args:      args,
		expected:  count,
		started:   time.Now(),
		transport: transport,
	}
	if abandoned := session.beginAssembly(assembly); abandoned != nil {
		s.log.Event("socket.binary_abandoned", map[string]any{
			"sid":      session.sid,
			"expected": abandoned.expected,
			"received": len(abandoned.attachments),
			"reason":   "a new binary event arrived before its attachments",
		})
	}

	s.log.Event("socket.binary_header", map[string]any{
		"sid":         session.sid,
		"transport":   transport,
		"kind":        assembly.kindName(),
		"namespace":   namespace,
		"ack":         ack,
		"attachments": count,
		"args":        s.log.Body([]byte(args)),
	})

	// A binary event may legitimately declare no attachments, in which case it
	// is already whole and there is nothing to wait for.
	if count == 0 {
		if done := session.takeAssembly(); done != nil {
			s.logBinaryEvent(session, done)
		}
	}
}

// acceptAttachment feeds one binary frame into the pending binary event.
func (s *Server) acceptAttachment(session *sioSession, raw []byte, transport string) {
	payload, prefixed := stripBinaryPrefix(raw)
	assembly, outcome := session.addAttachment(sioAttachment{data: payload, prefixed: prefixed})

	switch outcome {
	case attachmentCompleted:
		s.logBinaryEvent(session, assembly)
	case attachmentBuffered:
		s.log.Event("socket.attachment_buffered", map[string]any{
			"sid":       session.sid,
			"transport": transport,
			"bytes":     len(payload),
			"prefixed":  prefixed,
		})
	case attachmentExpired:
		s.log.Event("socket.binary_abandoned", map[string]any{
			"sid":      session.sid,
			"expected": assembly.expected,
			"received": len(assembly.attachments),
			"reason":   "the attachments did not arrive within the assembly timeout",
		})
	case attachmentUnexpected:
		s.log.Event("socket.attachment_unexpected", map[string]any{
			"sid":       session.sid,
			"transport": transport,
			"bytes":     len(payload),
			"prefixed":  prefixed,
			"body":      s.log.Body(payload),
			"note":      "a binary frame arrived with no binary event waiting for it",
		})
	}
}

// logBinaryEvent records a reassembled binary event and every attachment that
// completed it. The attachment bodies are the payload this whole stage exists
// to capture, so they are traced verbatim through the logger's hex fallback.
//
// When a payload codec is pinned the attachments are decrypted as well, which
// is what turns "<binary:16 bytes ...>" in the trace above into a real event
// name and its arguments.
func (s *Server) logBinaryEvent(session *sioSession, a *sioBinaryAssembly) {
	name, resolved, nameInAttachment := a.resolveEvent()

	fields := map[string]any{
		"sid":         session.sid,
		"transport":   a.transport,
		"kind":        a.kindName(),
		"namespace":   a.namespace,
		"ack":         a.ack,
		"event":       name,
		"attachments": len(a.attachments),
		"args":        strings.Join(resolved, " | "),
		"json":        s.log.Body([]byte(a.args)),
		"ms":          time.Since(a.started).Milliseconds(),
	}
	if nameInAttachment {
		fields["note"] = "the event name is inside attachment 0; it needs the payload codec before it can be read"
	}
	s.log.Event("socket.binary_event", fields)

	for i, att := range a.attachments {
		s.log.Event("socket.attachment", map[string]any{
			"sid":               session.sid,
			"num":               i,
			"bytes":             len(att.data),
			"prefixed":          att.prefixed,
			"aes_block_aligned": len(att.data) > 0 && len(att.data)%aesBlockSize == 0,
			"body":              s.log.Body(att.data),
		})
	}

	s.decodeBinaryEvent(session, a)
}

// decodeBinaryEvent decrypts a completed binary event with the pinned payload
// codec and logs what it says.
//
// Attachment 0 carries the event name and the remaining ones carry the JSON
// arguments, which is the layout the traced client uses for every emit.
//
// A failure here is an expected outcome rather than an error in the server: the
// client derives its key per connection, so a pinned key only fits the session
// it was captured from. It is recorded and the verbatim trace above still
// stands on its own, so nothing is lost when the key does not match.
func (s *Server) decodeBinaryEvent(session *sioSession, a *sioBinaryAssembly) {
	if s.sio == nil || s.sio.codec == nil || len(a.attachments) == 0 {
		return
	}

	decoded := make([][]byte, 0, len(a.attachments))
	for i, att := range a.attachments {
		plain, err := s.sio.codec.Decrypt(att.data)
		if err != nil {
			s.log.Event("socket.attachment_undecodable", map[string]any{
				"sid":   session.sid,
				"num":   i,
				"bytes": len(att.data),
				"mode":  s.sio.codec.Mode().String(),
				"err":   err.Error(),
				"note":  "the pinned key does not fit this session; the client re-derives it per connection",
			})
			return
		}
		decoded = append(decoded, plain)
	}

	fields := map[string]any{
		"sid":         session.sid,
		"transport":   a.transport,
		"kind":        a.kindName(),
		"namespace":   a.namespace,
		"ack":         a.ack,
		"attachments": len(decoded),
		"mode":        s.sio.codec.Mode().String(),
		"event":       s.log.Body(decoded[0]),
	}
	for i, plain := range decoded[1:] {
		fields[fmt.Sprintf("arg%d", i+1)] = s.log.Body(plain)
	}
	s.log.Event("socket.event_decoded", fields)
}

// encodeBinaryEvent builds the frames of an outgoing socket.io BINARY_EVENT:
// one text packet declaring the attachment count and naming the event, then one
// frame per attachment.
//
// prefixed adds the engine.io v3 marker, which is required on the WebSocket
// transport for this client because that is the framing it sends itself.
//
// This writes the documented socket.io shape, with the event name as a plain
// string in arg[0]. Whether a reply must instead hide its name inside
// attachment 0, the way the client's own requests do, cannot be settled until
// the capture is decrypted, so nothing calls this on the live path yet.
func encodeBinaryEvent(namespace, event string, attachments [][]byte, prefixed bool) ([]sioFrame, error) {
	if len(attachments) > sioMaxAttachments {
		return nil, fmt.Errorf("%d attachments exceeds the cap of %d", len(attachments), sioMaxAttachments)
	}

	args := make([]any, 0, len(attachments)+1)
	args = append(args, event)
	for i := range attachments {
		args = append(args, sioPlaceholder{Placeholder: true, Num: i})
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode binary event %q: %w", event, err)
	}

	var header strings.Builder
	header.WriteByte(eioMessage)
	header.WriteByte(sioBinaryEvent)
	header.WriteString(strconv.Itoa(len(attachments)))
	header.WriteByte('-')
	if namespace != "" {
		header.WriteString(namespace)
		header.WriteByte(',')
	}
	header.Write(payload)

	frames := make([]sioFrame, 0, len(attachments)+1)
	frames = append(frames, sioFrame{text: header.String()})
	for _, data := range attachments {
		out := data
		if prefixed {
			out = withBinaryPrefix(data)
		}
		frames = append(frames, sioFrame{binary: true, data: out})
	}
	return frames, nil
}
