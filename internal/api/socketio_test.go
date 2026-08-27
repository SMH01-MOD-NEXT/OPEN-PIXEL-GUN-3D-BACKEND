// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// socketRequest issues a request against the channel without JSON-encoding the
// body, because engine.io payloads are plain text with a length prefix.
func socketRequest(t *testing.T, s *Server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("Content-Type", "text/plain; charset=UTF-8")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// The client always handshakes over polling first, so that path has to work on
// its own even when the upgrade never happens.
func TestSocketHandshakeAndNamespaceConnectOverPolling(t *testing.T) {
	s, _ := newTestServer(t)

	rec := socketRequest(t, s, http.MethodGet, "/socket.io/?EIO=3&transport=polling&t=1-0", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("handshake status = %d, body = %s", rec.Code, rec.Body)
	}
	packets, err := decodePollingPayload(rec.Body.String())
	if err != nil {
		t.Fatalf("decode handshake %q: %v", rec.Body.String(), err)
	}
	if len(packets) != 1 || packets[0] == "" || packets[0][0] != eioOpen {
		t.Fatalf("handshake packets = %q, want one open packet", packets)
	}

	var open struct {
		SID          string   `json:"sid"`
		Upgrades     []string `json:"upgrades"`
		PingInterval int64    `json:"pingInterval"`
		PingTimeout  int64    `json:"pingTimeout"`
	}
	if err := json.Unmarshal([]byte(packets[0][1:]), &open); err != nil {
		t.Fatalf("decode open payload %q: %v", packets[0], err)
	}
	if open.SID == "" {
		t.Fatal("handshake returned no sid")
	}
	if len(open.Upgrades) != 1 || open.Upgrades[0] != "websocket" {
		t.Fatalf("upgrades = %v, want [websocket]", open.Upgrades)
	}
	if open.PingInterval != sioPingInterval.Milliseconds() || open.PingTimeout != sioPingTimeout.Milliseconds() {
		t.Fatalf("ping timings = %d/%d", open.PingInterval, open.PingTimeout)
	}

	session := "/socket.io/?EIO=3&transport=polling&sid=" + url.QueryEscape(open.SID)

	// "40" is an engine.io message carrying a socket.io namespace connect. It
	// must be answered or the client waits for its connect callback forever.
	if post := socketRequest(t, s, http.MethodPost, session, "2:40"); post.Code != http.StatusOK {
		t.Fatalf("post status = %d, body = %s", post.Code, post.Body)
	}
	poll := socketRequest(t, s, http.MethodGet, session, "")
	if poll.Code != http.StatusOK {
		t.Fatalf("poll status = %d, body = %s", poll.Code, poll.Body)
	}
	queued, err := decodePollingPayload(poll.Body.String())
	if err != nil {
		t.Fatalf("decode poll %q: %v", poll.Body.String(), err)
	}
	if len(queued) != 1 || queued[0] != "40" {
		t.Fatalf("poll packets = %q, want [40]", queued)
	}

	// A ping must come back as a pong, or the client tears the channel down.
	if post := socketRequest(t, s, http.MethodPost, session, "1:2"); post.Code != http.StatusOK {
		t.Fatalf("ping status = %d", post.Code)
	}
	pong, err := decodePollingPayload(socketRequest(t, s, http.MethodGet, session, "").Body.String())
	if err != nil {
		t.Fatalf("decode pong: %v", err)
	}
	if len(pong) != 1 || pong[0] != "3" {
		t.Fatalf("pong packets = %q, want [3]", pong)
	}
}

func TestSocketUnknownSessionIsRejected(t *testing.T) {
	s, _ := newTestServer(t)
	rec := socketRequest(t, s, http.MethodGet, "/socket.io/?EIO=3&transport=polling&sid=nope", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Session ID unknown") {
		t.Fatalf("body = %s", rec.Body)
	}
}

// The upgrade is the part that cannot be tested through httptest.ResponseRecorder:
// it needs a real socket, a real hijack, and real frames.
func TestSocketWebSocketUpgradeAnswersProbeAndConnect(t *testing.T) {
	s, _ := newTestServer(t)
	server := httptest.NewServer(s)
	defer server.Close()

	sid := decodeHandshake(t, s)
	address := strings.TrimPrefix(server.URL, "http://")

	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	request := "GET /socket.io/?EIO=3&transport=websocket&sid=" + url.QueryEscape(sid) + " HTTP/1.1\r\n" +
		"Host: " + address + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write upgrade request: %v", err)
	}

	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read upgrade response: %v", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", response.StatusCode)
	}
	// The sample key from RFC 6455 has a known answer.
	if got := response.Header.Get("Sec-WebSocket-Accept"); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("Sec-WebSocket-Accept = %q", got)
	}

	// The upgrade probe: engine.io only switches transports once this is
	// answered with "3probe".
	writeClientFrame(t, conn, wsOpText, []byte("2probe"))
	if reply := readServerFrame(t, reader); reply != "3probe" {
		t.Fatalf("probe reply = %q, want 3probe", reply)
	}

	writeClientFrame(t, conn, wsOpText, []byte("5"))
	writeClientFrame(t, conn, wsOpText, []byte("40"))
	if reply := readServerFrame(t, reader); reply != "40" {
		t.Fatalf("connect reply = %q, want 40", reply)
	}
}

// decodeHandshake opens a session over polling and returns its sid.
func decodeHandshake(t *testing.T, s *Server) string {
	t.Helper()
	packets, err := decodePollingPayload(
		socketRequest(t, s, http.MethodGet, "/socket.io/?EIO=3&transport=polling&t=1-0", "").Body.String())
	if err != nil || len(packets) != 1 {
		t.Fatalf("handshake failed: %v (%q)", err, packets)
	}
	var open struct {
		SID string `json:"sid"`
	}
	if err := json.Unmarshal([]byte(packets[0][1:]), &open); err != nil {
		t.Fatalf("decode open: %v", err)
	}
	return open.SID
}

// writeClientFrame sends a masked frame, which is what a client must always do.
func writeClientFrame(t *testing.T, conn net.Conn, opcode byte, payload []byte) {
	t.Helper()
	if len(payload) >= 126 {
		t.Fatalf("test frames stay short; got %d bytes", len(payload))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		t.Fatalf("mask: %v", err)
	}
	frame := []byte{0x80 | opcode, 0x80 | byte(len(payload))}
	frame = append(frame, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// readServerFrame reads one unmasked server frame and returns its payload.
func readServerFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		t.Fatalf("read frame header: %v", err)
	}
	if header[1]&0x80 != 0 {
		t.Fatal("server frames must not be masked")
	}
	length := int(header[1] & 0x7F)
	if length >= 126 {
		t.Fatalf("unexpected long frame of %d bytes", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatalf("read frame payload: %v", err)
	}
	return string(payload)
}

func TestDecodePollingPayload(t *testing.T) {
	packets, err := decodePollingPayload("1:2" + "4:4abc")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(packets) != 2 || packets[0] != "2" || packets[1] != "4abc" {
		t.Fatalf("packets = %q", packets)
	}
	if _, err := decodePollingPayload("9:4ab"); err == nil {
		t.Fatal("a truncated packet must be reported, not padded")
	}
	if _, err := decodePollingPayload("nope"); err == nil {
		t.Fatal("a payload without a length prefix must be reported")
	}
}

func TestDescribeSocketEvent(t *testing.T) {
	name, ack, namespace := describeSocketEvent(`["config",{"version":"23.1.3"}]`)
	if name != "config" || ack != "" || namespace != "" {
		t.Fatalf("plain event = %q/%q/%q", name, ack, namespace)
	}
	name, ack, namespace = describeSocketEvent(`/live,17["subscribe","ClanCommon"]`)
	if name != "subscribe" || ack != "17" || namespace != "/live" {
		t.Fatalf("namespaced event = %q/%q/%q", name, ack, namespace)
	}
	if name, _, _ := describeSocketEvent("not json at all"); name != "<unparsed>" {
		t.Fatalf("unparsed event = %q", name)
	}
}
