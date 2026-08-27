// SPDX-License-Identifier: GPL-3.0-or-later

package api

// A minimal RFC 6455 server, written here rather than pulled in as a
// dependency, because this project deliberately depends on the standard
// library only and needs exactly one thing from WebSocket: to carry engine.io
// frames for the game's live config channel.
//
// What is implemented: the opening handshake, text and binary messages
// including continuation frames, ping/pong, and close. What is not: extensions
// (permessage-deflate is never negotiated, so it is never used), subprotocols,
// and client-side masking of outgoing frames, which a server must not do.

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// wsGUID is the constant every WebSocket handshake mixes into the client key
// (RFC 6455 section 4.2.2).
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsMaxMessage caps a single reassembled message, so a broken or hostile peer
// cannot make the server buffer without bound.
const wsMaxMessage = 1 << 20

// Opcodes used by this server.
const (
	wsOpContinuation byte = 0x0
	wsOpText         byte = 0x1
	wsOpBinary       byte = 0x2
	wsOpClose        byte = 0x8
	wsOpPing         byte = 0x9
	wsOpPong         byte = 0xA
)

// errWSClosed is returned by ReadMessage once the peer closed the connection.
var errWSClosed = errors.New("websocket closed by peer")

// wsConn is a hijacked WebSocket connection. Writes are serialised, because
// engine.io writes from both the read loop (pong, probe) and the session's
// outgoing queue.
type wsConn struct {
	conn net.Conn
	brw  *bufio.ReadWriter

	writeMu sync.Mutex
	closed  bool
}

// wsUpgrade completes the opening handshake and takes the connection over from
// net/http. The caller owns the returned connection and must Close it.
func wsUpgrade(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, errors.New("not a websocket upgrade request")
	}
	if !headerHasToken(r.Header.Get("Connection"), "upgrade") {
		return nil, errors.New("missing Connection: Upgrade")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("missing Sec-WebSocket-Key")
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("response writer cannot be hijacked")
	}
	conn, brw, err := hijacker.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack: %w", err)
	}

	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + wsAcceptKey(key) + "\r\n\r\n"
	if _, err := brw.WriteString(response); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write handshake: %w", err)
	}
	if err := brw.Flush(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("flush handshake: %w", err)
	}
	return &wsConn{conn: conn, brw: brw}, nil
}

// wsAcceptKey derives the Sec-WebSocket-Accept value from the client key.
func wsAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// headerHasToken reports whether a comma-separated header value contains a
// token, case-insensitively.
func headerHasToken(value, token string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

// ReadMessage returns the next data message, reassembling continuation frames
// and answering ping frames on the way. Control traffic is never returned to
// the caller.
func (c *wsConn) ReadMessage(idleTimeout time.Duration) (opcode byte, payload []byte, err error) {
	var (
		message    []byte
		dataOpcode byte
		inMessage  bool
	)
	for {
		if idleTimeout > 0 {
			if err := c.conn.SetReadDeadline(time.Now().Add(idleTimeout)); err != nil {
				return 0, nil, err
			}
		}
		fin, frameOpcode, frame, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}

		switch frameOpcode {
		case wsOpPing:
			if err := c.writeFrame(wsOpPong, frame); err != nil {
				return 0, nil, err
			}
			continue
		case wsOpPong:
			continue
		case wsOpClose:
			c.writeFrame(wsOpClose, frame)
			return 0, nil, errWSClosed
		case wsOpText, wsOpBinary:
			if inMessage {
				return 0, nil, errors.New("new data frame inside an unfinished message")
			}
			dataOpcode = frameOpcode
			message = frame
			inMessage = true
		case wsOpContinuation:
			if !inMessage {
				return 0, nil, errors.New("continuation frame without a message")
			}
			message = append(message, frame...)
		default:
			return 0, nil, fmt.Errorf("unsupported websocket opcode 0x%x", frameOpcode)
		}

		if len(message) > wsMaxMessage {
			return 0, nil, errors.New("websocket message too large")
		}
		if fin {
			return dataOpcode, message, nil
		}
	}
}

// readFrame reads one frame and unmasks its payload.
func (c *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.brw, header); err != nil {
		return false, 0, nil, err
	}
	fin = header[0]&0x80 != 0
	opcode = header[0] & 0x0F
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7F)

	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(c.brw, extended[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(c.brw, extended[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(extended[:])
	}
	if length > wsMaxMessage {
		return false, 0, nil, fmt.Errorf("websocket frame of %d bytes is too large", length)
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.brw, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}

	payload = make([]byte, length)
	if _, err := io.ReadFull(c.brw, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}

// WriteText sends one text message.
func (c *wsConn) WriteText(text string) error {
	return c.writeFrame(wsOpText, []byte(text))
}

// WriteBinary sends one binary message.
func (c *wsConn) WriteBinary(data []byte) error {
	return c.writeFrame(wsOpBinary, data)
}

// writeFrame sends a complete, unmasked frame. Server frames are never masked.
func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return errWSClosed
	}

	header := []byte{0x80 | opcode}
	switch length := len(payload); {
	case length < 126:
		header = append(header, byte(length))
	case length <= 0xFFFF:
		header = append(header, 126, 0, 0)
		binary.BigEndian.PutUint16(header[2:], uint16(length))
	default:
		header = append(header, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[2:], uint64(length))
	}

	if err := c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	if _, err := c.brw.Write(header); err != nil {
		return err
	}
	if _, err := c.brw.Write(payload); err != nil {
		return err
	}
	return c.brw.Flush()
}

// Close sends a close frame, best effort, and releases the socket.
func (c *wsConn) Close() error {
	c.writeMu.Lock()
	if c.closed {
		c.writeMu.Unlock()
		return nil
	}
	c.closed = true
	c.writeMu.Unlock()

	c.conn.SetWriteDeadline(time.Now().Add(time.Second))
	c.brw.Write([]byte{0x80 | wsOpClose, 0})
	c.brw.Flush()
	return c.conn.Close()
}
