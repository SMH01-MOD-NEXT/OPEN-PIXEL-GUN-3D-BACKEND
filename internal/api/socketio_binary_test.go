// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The constants below are the verbatim 23.1.3 capture from log_back.txt,
// 2026-08-27T20:08:34Z: the socket.io packet the client sent after connecting
// the /sio namespace, and the two binary frames that followed it. Everything in
// this file is a regression test against that exact exchange, because it is the
// only evidence of what the channel carries.
const (
	// The body of the packet, i.e. "452-..." without the engine.io message type
	// and the socket.io BINARY_EVENT type.
	capturedBinaryBody = `2-/sio,[{"_placeholder":true, "num":0}, {"_placeholder":true, "num":1}]`

	// Attachment 0: 17 bytes on the wire, the first being the engine.io v3
	// binary marker.
	capturedAttachment0 = "04374bc0db20874c6c117e89ac1853e730"

	// Attachment 1: 609 bytes on the wire, same marker.
	capturedAttachment1 = "042d1cc1dd1680210c7c55ba87226fde0e2704aeca5cad65f660391e5eef05d6a4f9ac3d5e643b178dfc2d8bc169c968e2d7f8a420505b7cf9c114f9531d91929572e620ec24fcdcc42ad12b4f58a9783ce23a476cf84481b62ba0a8baf559847aab14382d3caba2bf9e3cea75fbc3277b73257b1f8f6830fc8484e823dbd01c8684eda3ac03272b41c2abbc1b2bfb581b7d1b2be01cdaa3d8cb3abffed42a0a28926657380a5a8a3a118255538f65ccb717673d40cf63929ecce75350a77c330473bae2e4c13a17ea696afbf6850c2140979c14b4ee44343eb51a0bc1b9834e36498afe90fd2ec87ec654b47da156c5be7bfb3084550e4d57e735f510ec65b29f166a2c641118c848813a414adee481081f309e9386cf2fb4e5c780582e009634a843cd4120830be7cacb0b1a157cf62dca870b4182d335cfc71b9dbc1bec0fa62c5c427a413f08daf640359edd6033ce801cdb53e83d51b54ce51522b4afa0462649cb8b3f9fad99c1beb8a8a57ef276d2d5f4e0fd9e027f938594c7f2b137900b0a5bf52108eee905616d19ddb60ea109e602bca87697c11e602113f038e57499932e8f7c4d8515bfc7c64e0a43b4cc9d21cb536fba89ee9cbbeb6defad2ab1f38d6298c2cc7a5205e44cf06e781e517e089e83da90851a617f47d2b62d4386b787f90ea28e60e2fcc8f8b2cba882c858a96eb158581551632ada3c6a64d2539efd09da662085cb5c524f17490fcf98c1eb1ee309acff5311f1cabb5ced6fdc9f9d38f328c31698f1623038b2cdea5b5c14576b1d21eec8e61f9c7c1f7a163a84c9516da235fff4c6d34641d0d763d8e7d46237de995ab6297455fb4083263c"
)

func mustDecodeHex(t *testing.T, text string) []byte {
	t.Helper()
	data, err := hex.DecodeString(text)
	if err != nil {
		t.Fatalf("decode captured hex: %v", err)
	}
	return data
}

func TestParseBinaryHeader(t *testing.T) {
	count, rest, err := parseBinaryHeader(capturedBinaryBody)
	if err != nil {
		t.Fatalf("parse captured header: %v", err)
	}
	if count != 2 {
		t.Fatalf("attachment count = %d, want 2", count)
	}
	if !strings.HasPrefix(rest, "/sio,[") {
		t.Fatalf("rest = %q, want the namespaced tail", rest)
	}

	if _, _, err := parseBinaryHeader(`["plain"]`); err == nil {
		t.Fatal("a body with no attachment count must be reported, not assumed to be zero")
	}
	if _, _, err := parseBinaryHeader(`x-["plain"]`); err == nil {
		t.Fatal("a non-numeric attachment count must be reported")
	}
	// A declared count is an instruction to buffer, so it has to be bounded.
	if _, _, err := parseBinaryHeader(`999-["plain"]`); err == nil {
		t.Fatal("an attachment count above the cap must be refused")
	}
}

func TestSplitEventTail(t *testing.T) {
	namespace, ack, args := splitEventTail(`/sio,[{"_placeholder":true, "num":0}]`)
	if namespace != "/sio" || ack != "" || !strings.HasPrefix(args, "[{") {
		t.Fatalf("namespaced tail = %q/%q/%q", namespace, ack, args)
	}

	namespace, ack, args = splitEventTail(`/live,17["subscribe"]`)
	if namespace != "/live" || ack != "17" || args != `["subscribe"]` {
		t.Fatalf("acked tail = %q/%q/%q", namespace, ack, args)
	}

	namespace, ack, args = splitEventTail(`["config"]`)
	if namespace != "" || ack != "" || args != `["config"]` {
		t.Fatalf("bare tail = %q/%q/%q", namespace, ack, args)
	}
}

func TestBinaryPrefixIsStrippedAndRestored(t *testing.T) {
	raw := mustDecodeHex(t, capturedAttachment0)
	if len(raw) != 17 {
		t.Fatalf("captured attachment 0 = %d bytes, want 17", len(raw))
	}

	payload, prefixed := stripBinaryPrefix(raw)
	if !prefixed {
		t.Fatal("the captured attachment carries the engine.io v3 marker")
	}
	if len(payload) != 16 {
		t.Fatalf("payload = %d bytes, want 16", len(payload))
	}

	restored := withBinaryPrefix(payload)
	if hex.EncodeToString(restored) != capturedAttachment0 {
		t.Fatalf("restored = %s, want the captured bytes back", hex.EncodeToString(restored))
	}

	// An attachment that does not carry the marker must be left alone rather
	// than losing its first byte.
	plain := []byte{0x81, 0xa1, 0x61}
	payload, prefixed = stripBinaryPrefix(plain)
	if prefixed || len(payload) != 3 {
		t.Fatalf("unprefixed attachment = %v prefixed=%t", payload, prefixed)
	}
	if payload, prefixed := stripBinaryPrefix(nil); prefixed || payload != nil {
		t.Fatalf("empty attachment = %v prefixed=%t", payload, prefixed)
	}
}

// The whole point of the change: the two binary frames belong to the packet
// that preceded them, and the event cannot be named until they are bound to it.
func TestBinaryEventIsAssembledFromTheCapture(t *testing.T) {
	hub := newSioHub(false, nil)
	session, err := hub.create(eioV4)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	count, tail, err := parseBinaryHeader(capturedBinaryBody)
	if err != nil {
		t.Fatalf("parse header: %v", err)
	}
	namespace, ack, args := splitEventTail(tail)
	if abandoned := session.beginAssembly(&sioBinaryAssembly{
		kind:      sioBinaryEvent,
		namespace: namespace,
		ack:       ack,
		args:      args,
		expected:  count,
		started:   time.Now(),
		transport: "websocket",
	}); abandoned != nil {
		t.Fatal("a fresh session cannot already have a pending event")
	}

	first, firstPrefixed := stripBinaryPrefix(mustDecodeHex(t, capturedAttachment0))
	if got, outcome := session.addAttachment(sioAttachment{data: first, prefixed: firstPrefixed}); outcome != attachmentBuffered || got != nil {
		t.Fatalf("first attachment outcome = %d, want it buffered and withheld", outcome)
	}

	second, secondPrefixed := stripBinaryPrefix(mustDecodeHex(t, capturedAttachment1))
	if len(second) != 608 {
		t.Fatalf("captured attachment 1 payload = %d bytes, want 608", len(second))
	}
	done, outcome := session.addAttachment(sioAttachment{data: second, prefixed: secondPrefixed})
	if outcome != attachmentCompleted {
		t.Fatalf("second attachment outcome = %d, want the event completed", outcome)
	}
	if done == nil {
		t.Fatal("a completed event must be handed back to the caller")
	}
	if done.namespace != "/sio" {
		t.Fatalf("namespace = %q, want /sio", done.namespace)
	}
	if len(done.attachments) != 2 {
		t.Fatalf("attachments = %d, want 2", len(done.attachments))
	}
	// The session must not keep holding a completed event.
	if leftover := session.takeAssembly(); leftover != nil {
		t.Fatal("a completed event must be cleared from the session")
	}

	name, resolved, nameInAttachment := done.resolveEvent()
	if !nameInAttachment {
		t.Fatalf("name = %q; this capture carries the event name in attachment 0", name)
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved args = %v, want two", resolved)
	}
	// Both payloads are block-aligned, which is the observation that says the
	// attachments are encrypted rather than plain MessagePack.
	for i, want := range []string{
		"16 bytes prefixed=true aes_block_aligned=true",
		"608 bytes prefixed=true aes_block_aligned=true",
	} {
		if resolved[i] != want {
			t.Fatalf("resolved[%d] = %q, want %q", i, resolved[i], want)
		}
	}
}

// A plain, non-binary event still has to report its name from the JSON.
func TestResolveEventReadsAPlainName(t *testing.T) {
	assembly := &sioBinaryAssembly{
		args:     `["config",{"_placeholder":true,"num":0}]`,
		expected: 1,
		attachments: []sioAttachment{
			{data: make([]byte, 32), prefixed: false},
		},
	}
	name, resolved, nameInAttachment := assembly.resolveEvent()
	if name != "config" || nameInAttachment {
		t.Fatalf("name = %q nameInAttachment=%t, want config/false", name, nameInAttachment)
	}
	if len(resolved) != 2 || resolved[1] != "32 bytes prefixed=false aes_block_aligned=true" {
		t.Fatalf("resolved = %v", resolved)
	}

	// A placeholder pointing past the attachments must be reported as missing,
	// not silently resolved to an empty payload.
	missing := &sioBinaryAssembly{args: `["config",{"_placeholder":true,"num":3}]`}
	if _, resolved, _ := missing.resolveEvent(); len(resolved) != 2 || resolved[1] != "<attachment 3 missing>" {
		t.Fatalf("resolved = %v", resolved)
	}

	unparsed := &sioBinaryAssembly{args: "not json"}
	if name, _, _ := unparsed.resolveEvent(); name != "<unparsed>" {
		t.Fatalf("name = %q, want <unparsed>", name)
	}
}

func TestAttachmentWithoutAWaitingEventIsReported(t *testing.T) {
	hub := newSioHub(false, nil)
	session, err := hub.create(eioV4)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, outcome := session.addAttachment(sioAttachment{data: []byte{1, 2, 3}}); outcome != attachmentUnexpected {
		t.Fatalf("outcome = %d, want attachmentUnexpected", outcome)
	}
}

func TestStaleAssemblyIsDroppedRatherThanFilled(t *testing.T) {
	hub := newSioHub(false, nil)
	session, err := hub.create(eioV4)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session.beginAssembly(&sioBinaryAssembly{
		expected: 2,
		args:     `[{"_placeholder":true,"num":0}]`,
		started:  time.Now().Add(-2 * sioAssemblyTimeout),
	})

	stale, outcome := session.addAttachment(sioAttachment{data: []byte{1}})
	if outcome != attachmentExpired {
		t.Fatalf("outcome = %d, want attachmentExpired", outcome)
	}
	if stale == nil || stale.expected != 2 {
		t.Fatal("the expired assembly must be handed back for the trace")
	}
	if leftover := session.takeAssembly(); leftover != nil {
		t.Fatal("an expired assembly must not stay pending")
	}
}

// A binary event that declares no attachments is already whole.
func TestBinaryEventWithNoAttachmentsCompletesImmediately(t *testing.T) {
	assembly := &sioBinaryAssembly{expected: 0}
	if !assembly.complete() {
		t.Fatal("an event declaring zero attachments is complete on arrival")
	}
}

func TestEncodeBinaryEventCarriesThePrefixAndPlaceholders(t *testing.T) {
	frames, err := encodeBinaryEvent("/sio", "config", [][]byte{{0xde, 0xad}, {0xbe, 0xef}}, true)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want a header and two attachments", len(frames))
	}

	wantHeader := `452-/sio,["config",{"_placeholder":true,"num":0},{"_placeholder":true,"num":1}]`
	if frames[0].text != wantHeader {
		t.Fatalf("header = %q, want %q", frames[0].text, wantHeader)
	}
	if frames[0].binary {
		t.Fatal("the header is a text packet")
	}

	for i, want := range []string{"04dead", "04beef"} {
		frame := frames[i+1]
		if !frame.binary {
			t.Fatalf("frame %d must be binary", i+1)
		}
		if got := hex.EncodeToString(frame.data); got != want {
			t.Fatalf("attachment %d = %s, want %s", i, got, want)
		}
	}

	// Without the marker the bytes must go out untouched, because socket.io v4
	// proper does not use it.
	plain, err := encodeBinaryEvent("", "config", [][]byte{{0xde, 0xad}}, false)
	if err != nil {
		t.Fatalf("encode without prefix: %v", err)
	}
	if plain[0].text != `451-["config",{"_placeholder":true,"num":0}]` {
		t.Fatalf("header = %q", plain[0].text)
	}
	if hex.EncodeToString(plain[1].data) != "dead" {
		t.Fatalf("attachment = %s, want dead", hex.EncodeToString(plain[1].data))
	}

	if _, err := encodeBinaryEvent("", "config", make([][]byte, sioMaxAttachments+1), true); err == nil {
		t.Fatal("more attachments than the cap must be refused")
	}
}

// The XHR transport cannot carry a binary frame, so an attachment travels
// base64-encoded behind a "b" marker in both directions.
func TestPollingAttachmentRoundTrip(t *testing.T) {
	raw := mustDecodeHex(t, capturedAttachment0)
	encoded := encodePollingAttachment(raw)
	if !strings.HasPrefix(encoded, "b") {
		t.Fatalf("encoded = %q, want a leading b", encoded)
	}

	decoded, err := decodePollingAttachment(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if hex.EncodeToString(decoded) != capturedAttachment0 {
		t.Fatalf("decoded = %s", hex.EncodeToString(decoded))
	}

	if _, err := decodePollingAttachment("40"); err == nil {
		t.Fatal("a text packet must not be accepted as an attachment")
	}
	if _, err := decodePollingAttachment("b!!!not base64"); err == nil {
		t.Fatal("invalid base64 must be reported")
	}
}

// Binary frames must survive the polling writer instead of being dropped, which
// is what the previous revision did.
func TestWritePollingEncodesBinaryFrames(t *testing.T) {
	s, _ := newTestServer(t)

	for _, version := range []eioVersion{eioV3, eioV4} {
		rec := httptest.NewRecorder()
		s.writePolling(rec, version, []sioFrame{
			{text: "40"},
			{binary: true, data: []byte{0xde, 0xad}},
		})
		body := rec.Body.String()
		if !strings.Contains(body, encodePollingAttachment([]byte{0xde, 0xad})) {
			t.Fatalf("EIO=%d body = %q, want the base64 attachment", version, body)
		}
	}
}
