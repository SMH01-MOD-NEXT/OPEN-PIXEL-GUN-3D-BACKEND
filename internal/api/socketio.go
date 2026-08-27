// SPDX-License-Identifier: GPL-3.0-or-later

package api

// The live config channel.
//
// The 23.1.3 client does not fetch its live configuration over HTTP. It opens a
// socket.io channel and waits for the server to push it, which is why the boot
// used to stall with "Start wait config response" even though every REST route
// answered. The client-side trace reported the address it builds:
//
//   https://server-v2.pixelgun3dserver.com/socket.io/
//
// and the URL rewriter now points that at this server. This file answers it.
//
// Protocol version
// ----------------
// The client is BestHTTP's socket.io implementation, and the query templates
// compiled into the game are
//
//   {0}?EIO={1}&transport=polling&t={2}-{3}&sid={4}{5}
//   {0}?EIO={1}&transport=websocket{3}
//
// The "t=<timestamp>-<counter>" cache buster and the polling-then-upgrade order
// are engine.io v3, so v3 framing is what is implemented: length-prefixed
// packets on XHR polling, one packet per frame on WebSocket. The EIO value the
// client actually sends is logged, so a mismatch is visible rather than silent.
//
// What this stage does
// --------------------
// It completes the handshake, survives the upgrade to WebSocket, keeps the
// channel alive through ping/pong, accepts the namespace connect, and logs
// every packet the client sends - including the events it subscribes with.
// Those events are the last unknown: they say which config documents the client
// asks for and under which keys it expects them. No config is pushed yet, on
// purpose - the payload will be written against the logged subscription rather
// than against a guess.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/secrets"
)

// sioPath is the channel's namespace on this server. The client-side rewriter
// keeps the original path, so it arrives exactly as the retired service had it.
const sioPath = "/socket.io/"

const (
	// Handshake values handed to the client. pingInterval is how often the
	// client is expected to ping; pingTimeout is how long it may wait for the
	// answer. These are engine.io v3 defaults, which BestHTTP is written for.
	sioPingInterval = 25 * time.Second
	sioPingTimeout  = 60 * time.Second

	// How long a polling GET is held open waiting for something to send. It
	// must stay below pingInterval so the client's own cycle is never starved.
	sioPollHold = 20 * time.Second

	// A session with no traffic for this long is forgotten. The client re-opens
	// one immediately, so this only bounds memory.
	sioSessionIdle = 2 * time.Minute

	// Outgoing queue depth per session. Deep enough for a burst of config
	// packets, shallow enough that a dead peer cannot be used to grow memory.
	sioQueueDepth = 64
)

// Engine.io packet types (the first character of a packet).
const (
	eioOpen    = '0'
	eioClose   = '1'
	eioPing    = '2'
	eioPong    = '3'
	eioMessage = '4'
	eioUpgrade = '5'
	eioNoop    = '6'
)

// Socket.io packet types (the character after an engine.io message type).
const (
	sioConnect     = '0'
	sioDisconnect  = '1'
	sioEvent       = '2'
	sioAck         = '3'
	sioError       = '4'
	sioBinaryEvent = '5'
	sioBinaryAck   = '6'
)

// sioFrame is one thing to send to the client: either an engine.io packet as
// text, or a raw binary attachment.
type sioFrame struct {
	binary bool
	text   string
	data   []byte
}

// sioSession is one engine.io session, identified by its sid and outliving the
// individual HTTP requests that carry it.
type sioSession struct {
	sid     string
	created time.Time
	out     chan sioFrame

	mu        sync.Mutex
	lastSeen  time.Time
	transport string
	upgraded  bool
	packets   uint64
}

func (s *sioSession) touch() {
	s.mu.Lock()
	s.lastSeen = time.Now()
	s.packets++
	s.mu.Unlock()
}

func (s *sioSession) idleFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastSeen)
}

func (s *sioSession) setTransport(name string) {
	s.mu.Lock()
	s.transport = name
	s.mu.Unlock()
}

func (s *sioSession) markUpgraded() {
	s.mu.Lock()
	s.upgraded = true
	s.transport = "websocket"
	s.mu.Unlock()
}

// send queues a frame. It never blocks: a client that stopped reading must not
// be able to stall the server.
func (s *sioSession) send(frame sioFrame) bool {
	select {
	case s.out <- frame:
		return true
	default:
		return false
	}
}

// sioHub owns the live sessions.
type sioHub struct {
	mu       sync.Mutex
	sessions map[string]*sioSession
}

func newSioHub() *sioHub {
	return &sioHub{sessions: make(map[string]*sioSession)}
}

func (h *sioHub) create() (*sioSession, error) {
	sid, err := secrets.RandomToken(12)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	session := &sioSession{
		sid:       sid,
		created:   now,
		lastSeen:  now,
		out:       make(chan sioFrame, sioQueueDepth),
		transport: "polling",
	}
	h.mu.Lock()
	h.sessions[sid] = session
	h.mu.Unlock()
	return session, nil
}

func (h *sioHub) get(sid string) *sioSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[sid]
}

func (h *sioHub) drop(sid string) {
	h.mu.Lock()
	delete(h.sessions, sid)
	h.mu.Unlock()
}

// reap forgets sessions the client abandoned without closing them, which is
// what a killed app or a lost network looks like from here.
func (h *sioHub) reap() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var dropped []string
	for sid, session := range h.sessions {
		if session.idleFor() > sioSessionIdle {
			delete(h.sessions, sid)
			dropped = append(dropped, sid)
		}
	}
	return dropped
}

// handleSocketIO is the single entry point for the channel: the handshake, the
// polling transport and the WebSocket upgrade all arrive on this path.
func (s *Server) handleSocketIO(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	eio := query.Get("EIO")
	transport := query.Get("transport")
	sid := query.Get("sid")

	if dropped := s.sio.reap(); len(dropped) > 0 {
		s.log.Event("socket.reaped", map[string]any{"sids": dropped})
	}
	if eio != "" && eio != "3" {
		s.log.Event("socket.protocol_mismatch", map[string]any{
			"client_eio": eio,
			"served_eio": "3",
			"hint":       "this server speaks engine.io v3 framing; the client asked for another version",
		})
	}

	switch transport {
	case "websocket":
		s.socketWebSocket(w, r, sid, eio)
	case "polling", "":
		s.socketPolling(w, r, sid, eio)
	default:
		s.log.Event("socket.bad_transport", map[string]any{"transport": transport, "sid": sid})
		s.writeError(w, http.StatusBadRequest, "bad_transport", transport)
	}
}

// socketPolling serves the XHR transport: the handshake, the long-poll GET that
// carries server packets, and the POST that carries client packets.
func (s *Server) socketPolling(w http.ResponseWriter, r *http.Request, sid, eio string) {
	if sid == "" {
		s.socketHandshake(w, r, eio)
		return
	}

	session := s.sio.get(sid)
	if session == nil {
		s.log.Event("socket.unknown_session", map[string]any{"sid": sid, "method": r.Method})
		// The shape engine.io itself uses for this, so the client's own error
		// path handles it instead of parsing a surprise.
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"code": 1, "message": "Session ID unknown"})
		return
	}
	session.touch()
	session.setTransport("polling")

	switch r.Method {
	case http.MethodGet:
		s.socketPollRead(w, session)
	case http.MethodPost:
		s.socketPollWrite(w, r, session)
	case http.MethodOptions:
		w.WriteHeader(http.StatusOK)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "bad_method", r.Method)
	}
}

// socketHandshake opens a session and answers with the engine.io open packet.
func (s *Server) socketHandshake(w http.ResponseWriter, r *http.Request, eio string) {
	session, err := s.sio.create()
	if err != nil {
		s.log.Event("socket.handshake_failed", map[string]any{"err": err.Error()})
		s.writeError(w, http.StatusInternalServerError, "handshake", err.Error())
		return
	}

	open, err := json.Marshal(map[string]any{
		"sid":          session.sid,
		"upgrades":     []string{"websocket"},
		"pingInterval": sioPingInterval.Milliseconds(),
		"pingTimeout":  sioPingTimeout.Milliseconds(),
	})
	if err != nil {
		s.log.Event("socket.handshake_failed", map[string]any{"err": err.Error()})
		s.writeError(w, http.StatusInternalServerError, "handshake", err.Error())
		return
	}

	s.log.Event("socket.handshake", map[string]any{
		"sid":       session.sid,
		"eio":       eio,
		"ip":        clientIP(r),
		"raw_path":  r.URL.Path,
		"transport": "polling",
	})
	s.writePolling(w, []sioFrame{{text: string(eioOpen) + string(open)}})
}

// socketPollRead holds the GET open until there is something to send, then
// flushes everything queued. A poll that times out answers with a noop, which
// is how engine.io keeps the cycle going without pretending to be idle.
func (s *Server) socketPollRead(w http.ResponseWriter, session *sioSession) {
	timer := time.NewTimer(sioPollHold)
	defer timer.Stop()

	var frames []sioFrame
	select {
	case frame := <-session.out:
		frames = append(frames, frame)
	case <-timer.C:
		s.writePolling(w, []sioFrame{{text: string(eioNoop)}})
		return
	}
	for {
		select {
		case frame := <-session.out:
			frames = append(frames, frame)
			continue
		default:
		}
		break
	}
	s.writePolling(w, frames)
}

// socketPollWrite decodes the packets a POST carries and handles each one.
func (s *Server) socketPollWrite(w http.ResponseWriter, r *http.Request, session *sioSession) {
	raw, err := readAll(r)
	if err != nil {
		s.log.Event("socket.read_failed", map[string]any{"sid": session.sid, "err": err.Error()})
		s.writeError(w, http.StatusBadRequest, "read", err.Error())
		return
	}

	packets, err := decodePollingPayload(string(raw))
	if err != nil {
		s.log.Event("socket.bad_payload", map[string]any{
			"sid":  session.sid,
			"err":  err.Error(),
			"body": s.log.Body(raw),
		})
		s.writeError(w, http.StatusBadRequest, "bad_payload", err.Error())
		return
	}
	for _, packet := range packets {
		s.handleEnginePacket(session, packet, "polling")
	}

	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// socketWebSocket serves the upgraded transport. The client may either upgrade
// an existing polling session (sid given) or open a new one directly.
func (s *Server) socketWebSocket(w http.ResponseWriter, r *http.Request, sid, eio string) {
	session := s.sio.get(sid)
	fresh := false
	if session == nil {
		if sid != "" {
			s.log.Event("socket.unknown_session", map[string]any{"sid": sid, "transport": "websocket"})
			s.writeJSON(w, http.StatusBadRequest, map[string]any{"code": 1, "message": "Session ID unknown"})
			return
		}
		var err error
		session, err = s.sio.create()
		if err != nil {
			s.log.Event("socket.handshake_failed", map[string]any{"err": err.Error()})
			s.writeError(w, http.StatusInternalServerError, "handshake", err.Error())
			return
		}
		fresh = true
	}

	conn, err := wsUpgrade(w, r)
	if err != nil {
		s.log.Event("socket.ws_upgrade_failed", map[string]any{"sid": session.sid, "err": err.Error()})
		s.writeError(w, http.StatusBadRequest, "ws_upgrade", err.Error())
		return
	}
	defer conn.Close()

	s.log.Event("socket.ws_open", map[string]any{
		"sid":   session.sid,
		"eio":   eio,
		"ip":    clientIP(r),
		"fresh": fresh,
	})
	session.setTransport("websocket")
	session.touch()

	if fresh {
		open, err := json.Marshal(map[string]any{
			"sid":          session.sid,
			"upgrades":     []string{},
			"pingInterval": sioPingInterval.Milliseconds(),
			"pingTimeout":  sioPingTimeout.Milliseconds(),
		})
		if err == nil {
			conn.WriteText(string(eioOpen) + string(open))
		}
	}

	// One writer goroutine drains the session queue; the read loop below owns
	// the connection's lifetime and stops the writer when it returns.
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case frame := <-session.out:
				var writeErr error
				if frame.binary {
					writeErr = conn.WriteBinary(frame.data)
				} else {
					writeErr = conn.WriteText(frame.text)
				}
				if writeErr != nil {
					s.log.Event("socket.ws_write_failed", map[string]any{
						"sid": session.sid,
						"err": writeErr.Error(),
					})
					return
				}
			}
		}
	}()

	for {
		opcode, payload, err := conn.ReadMessage(sioPingTimeout + sioPingInterval)
		if err != nil {
			s.log.Event("socket.ws_closed", map[string]any{
				"sid":     session.sid,
				"err":     err.Error(),
				"alive_s": int64(time.Since(session.created).Seconds()),
			})
			return
		}
		session.touch()

		if opcode == wsOpBinary {
			// Binary attachments carry the MessagePack documents this channel
			// exists for, in both directions.
			s.log.Event("socket.binary", map[string]any{
				"sid":   session.sid,
				"bytes": len(payload),
				"body":  s.log.Body(payload),
			})
			continue
		}
		s.handleEnginePacketWS(session, conn, string(payload))
	}
}

// handleEnginePacketWS answers on the connection directly, so a pong or a probe
// reply cannot be delayed behind the session queue.
func (s *Server) handleEnginePacketWS(session *sioSession, conn *wsConn, packet string) {
	if packet == "" {
		return
	}
	switch packet[0] {
	case eioPing:
		// "2probe" during the upgrade must be answered with "3probe"; a bare
		// "2" is the keepalive. Echoing the body covers both.
		conn.WriteText(string(eioPong) + packet[1:])
		return
	case eioUpgrade:
		session.markUpgraded()
		s.log.Event("socket.upgraded", map[string]any{"sid": session.sid, "transport": "websocket"})
		return
	}
	s.handleEnginePacket(session, packet, "websocket")
}

// handleEnginePacket is the transport-independent packet handler. Replies are
// queued on the session, so they reach the client over whichever transport is
// current at the time.
func (s *Server) handleEnginePacket(session *sioSession, packet, transport string) {
	if packet == "" {
		return
	}
	body := packet[1:]
	switch packet[0] {
	case eioPing:
		session.send(sioFrame{text: string(eioPong) + body})
	case eioPong:
		// Nothing to do; the timestamp is already in the trace.
	case eioClose:
		s.log.Event("socket.closed", map[string]any{
			"sid":       session.sid,
			"transport": transport,
			"alive_s":   int64(time.Since(session.created).Seconds()),
		})
		s.sio.drop(session.sid)
	case eioUpgrade:
		session.markUpgraded()
		s.log.Event("socket.upgraded", map[string]any{"sid": session.sid, "transport": transport})
	case eioNoop:
		// Keepalive only.
	case eioMessage:
		s.handleSocketMessage(session, body, transport)
	default:
		s.log.Event("socket.unknown_packet", map[string]any{
			"sid":       session.sid,
			"transport": transport,
			"packet":    s.log.Body([]byte(packet)),
		})
	}
}

// handleSocketMessage handles the socket.io layer that rides on engine.io
// messages.
func (s *Server) handleSocketMessage(session *sioSession, body, transport string) {
	if body == "" {
		return
	}
	kind := body[0]
	rest := body[1:]

	switch kind {
	case sioConnect:
		// The namespace, when the client uses one, is the text up to the first
		// comma; it must be echoed back or the client waits forever.
		namespace := rest
		if comma := strings.Index(rest, ","); comma >= 0 {
			namespace = rest[:comma]
		}
		s.log.Event("socket.connect", map[string]any{
			"sid":       session.sid,
			"namespace": namespace,
			"transport": transport,
		})
		session.send(sioFrame{text: string(eioMessage) + string(sioConnect) + namespace})
	case sioDisconnect:
		s.log.Event("socket.disconnect", map[string]any{"sid": session.sid, "namespace": rest})
	case sioEvent, sioBinaryEvent:
		// This is the interesting one: the client names the config documents it
		// wants here. The payload is recorded verbatim so the push side can be
		// written against it.
		name, ackID, namespace := describeSocketEvent(rest)
		s.log.Event("socket.event", map[string]any{
			"sid":       session.sid,
			"transport": transport,
			"binary":    kind == sioBinaryEvent,
			"namespace": namespace,
			"ack":       ackID,
			"event":     name,
			"payload":   s.log.Body([]byte(rest)),
			"note":      "no answer is pushed yet; the config payload is the next step",
		})
	case sioAck, sioBinaryAck:
		s.log.Event("socket.ack", map[string]any{"sid": session.sid, "payload": s.log.Body([]byte(rest))})
	case sioError:
		s.log.Event("socket.client_error", map[string]any{"sid": session.sid, "payload": s.log.Body([]byte(rest))})
	default:
		s.log.Event("socket.unknown_message", map[string]any{
			"sid":     session.sid,
			"payload": s.log.Body([]byte(body)),
		})
	}
}

// describeSocketEvent pulls the readable parts out of a socket.io event body,
// which looks like [namespace,][attachments-][ack]["name",args...]. Anything
// that cannot be parsed is reported as unknown rather than guessed at, and the
// verbatim payload is logged alongside.
func describeSocketEvent(body string) (name, ack, namespace string) {
	name = "<unparsed>"
	rest := body

	if dash := strings.Index(rest, "-"); dash > 0 && isAllDigits(rest[:dash]) {
		rest = rest[dash+1:]
	}
	if comma := strings.Index(rest, ","); comma >= 0 && strings.HasPrefix(rest, "/") {
		namespace = rest[:comma]
		rest = rest[comma+1:]
	}
	if bracket := strings.Index(rest, "["); bracket > 0 && isAllDigits(rest[:bracket]) {
		ack = rest[:bracket]
		rest = rest[bracket:]
	}

	var args []json.RawMessage
	if err := json.Unmarshal([]byte(rest), &args); err == nil && len(args) > 0 {
		var first string
		if err := json.Unmarshal(args[0], &first); err == nil {
			name = first
		}
	}
	return name, ack, namespace
}

func isAllDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// writePolling encodes frames in engine.io v3 XHR framing: each packet is
// prefixed with its length in characters and a colon.
func (s *Server) writePolling(w http.ResponseWriter, frames []sioFrame) {
	var builder strings.Builder
	for _, frame := range frames {
		if frame.binary {
			// Binary over XHR polling needs base64 framing, which this client
			// only uses before the upgrade. Nothing binary is pushed yet, so
			// reaching this point would be a bug worth seeing.
			s.log.Event("socket.binary_polling_skipped", map[string]any{"bytes": len(frame.data)})
			continue
		}
		builder.WriteString(fmt.Sprintf("%d:%s", utf8.RuneCountInString(frame.text), frame.text))
	}

	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(builder.String()))
}

// decodePollingPayload parses engine.io v3 XHR framing into packets.
func decodePollingPayload(payload string) ([]string, error) {
	var packets []string
	rest := payload
	for rest != "" {
		colon := strings.Index(rest, ":")
		if colon <= 0 {
			return nil, fmt.Errorf("no length prefix in %q", rest)
		}
		if !isAllDigits(rest[:colon]) {
			return nil, fmt.Errorf("length prefix %q is not a number", rest[:colon])
		}
		var length int
		if _, err := fmt.Sscanf(rest[:colon], "%d", &length); err != nil {
			return nil, fmt.Errorf("length prefix %q: %w", rest[:colon], err)
		}

		runes := []rune(rest[colon+1:])
		if length > len(runes) {
			return nil, fmt.Errorf("packet claims %d characters but only %d remain", length, len(runes))
		}
		packets = append(packets, string(runes[:length]))
		rest = string(runes[length:])
	}
	return packets, nil
}
