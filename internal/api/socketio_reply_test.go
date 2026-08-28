// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
)

// pinnedSid is the socket.io session id OPG3D_STABLE_SESSIONS hands out, and
// the one every captured cipher vector in internal/sio belongs to.
const pinnedSid = "0123456789abcdef01234567"

func codecForPinnedSid(t *testing.T) *sio.Codec {
	t.Helper()
	codec, err := sio.NewForSid(pinnedSid, sio.DefaultMode)
	if err != nil {
		t.Fatalf("no codec follows from the pinned sid: %v", err)
	}
	return codec
}

// TestEncodeEncryptedEventHidesTheNameInAttachmentZero checks the reply against
// each rule the client's inbound path imposes: two attachments, an engine.io
// marker byte on both, no event name in clear text, and a quoted name as the
// plaintext of attachment 0.
func TestEncodeEncryptedEventHidesTheNameInAttachmentZero(t *testing.T) {
	codec := codecForPinnedSid(t)

	frames, err := encodeEncryptedEvent(codec, "/sio", sioAuthEvent, map[string]any{sioRequestIDKey: 1})
	if err != nil {
		t.Fatalf("encodeEncryptedEvent: %v", err)
	}
	if len(frames) != sioReplyAttachments+1 {
		t.Fatalf("got %d frames, want %d", len(frames), sioReplyAttachments+1)
	}

	wantHeader := "45" + strconv.Itoa(sioReplyAttachments) + `-/sio,[{"_placeholder":true,"num":0},{"_placeholder":true,"num":1}]`
	if frames[0].text != wantHeader {
		t.Fatalf("header is %q, want %q", frames[0].text, wantHeader)
	}
	if frames[0].binary {
		t.Fatal("the header must go out as a text frame")
	}
	if strings.Contains(frames[0].text, sioAuthEvent) {
		t.Fatalf("the event name is in clear text in the header: %q", frames[0].text)
	}

	for i, frame := range frames[1:] {
		if !frame.binary {
			t.Fatalf("attachment %d must go out as a binary frame", i)
		}
		if len(frame.data) == 0 || frame.data[0] != eioBinaryPrefix {
			t.Fatalf("attachment %d is missing the engine.io marker byte; the client copies from offset 1 and would lose a byte of ciphertext", i)
		}
		body, _ := stripBinaryPrefix(frame.data)
		if len(body) == 0 || len(body)%aesBlockSize != 0 {
			t.Fatalf("attachment %d is %d bytes, which is not a whole number of AES blocks", i, len(body))
		}
	}

	name, _ := stripBinaryPrefix(frames[1].data)
	plain, err := codec.Decrypt(name)
	if err != nil {
		t.Fatalf("attachment 0 does not open with the session codec: %v", err)
	}
	if got := string(plain); got != `"`+sioAuthEvent+`"` {
		t.Fatalf("attachment 0 is %q, want the name with its JSON quotes; the client substitutes this text verbatim", got)
	}
}

// TestEncryptedReplyReconstructsTheClientPayload replays the client's inbound
// path over a reply this server produced: strip the marker byte, decrypt, and
// substitute each plaintext into the payload text literally, the way
// ReplacePlaceholderWithNumber does. What the client is then holding has to be
// a valid two-element event.
func TestEncryptedReplyReconstructsTheClientPayload(t *testing.T) {
	codec := codecForPinnedSid(t)

	const reqID = 7
	frames, err := encodeEncryptedEvent(codec, "/sio", sioAuthEvent, map[string]any{
		sioRequestIDKey: reqID,
		"result":        "ok",
	})
	if err != nil {
		t.Fatalf("encodeEncryptedEvent: %v", err)
	}

	header := "45" + strconv.Itoa(sioReplyAttachments) + "-"
	if !strings.HasPrefix(frames[0].text, header) {
		t.Fatalf("header %q does not start with %q", frames[0].text, header)
	}
	namespace, ack, payload := splitEventTail(strings.TrimPrefix(frames[0].text, header))
	if namespace != "/sio" {
		t.Fatalf("namespace is %q, want /sio", namespace)
	}
	if ack != "" {
		t.Fatalf("an ack id of %q would make the client answer the reply", ack)
	}

	for i, frame := range frames[1:] {
		body, prefixed := stripBinaryPrefix(frame.data)
		if !prefixed {
			t.Fatalf("attachment %d carries no marker byte", i)
		}
		plain, err := codec.Decrypt(body)
		if err != nil {
			t.Fatalf("attachment %d does not open: %v", i, err)
		}
		marker, err := json.Marshal(sioPlaceholder{Placeholder: true, Num: i})
		if err != nil {
			t.Fatalf("marshal placeholder %d: %v", i, err)
		}
		if !strings.Contains(payload, string(marker)) {
			t.Fatalf("payload %q holds no placeholder for attachment %d", payload, i)
		}
		payload = strings.Replace(payload, string(marker), string(plain), 1)
	}

	var args []any
	if err := json.Unmarshal([]byte(payload), &args); err != nil {
		t.Fatalf("the payload the client would hold is not valid JSON: %v (%s)", err, payload)
	}
	if len(args) != 2 {
		t.Fatalf("the client would see %d arguments, want the name plus one object: %s", len(args), payload)
	}
	if args[0] != sioAuthEvent {
		t.Fatalf("the client would read the event name as %v, want %q", args[0], sioAuthEvent)
	}
	head, ok := args[1].(map[string]any)
	if !ok {
		t.Fatalf("args[0] reaches subscribers as %T, want a JSON object", args[1])
	}
	if got, ok := head[sioRequestIDKey].(float64); !ok || int(got) != reqID {
		t.Fatalf("the head carries %v for %s, want %d; without it the client matches the reply against nothing", head[sioRequestIDKey], sioRequestIDKey, reqID)
	}
}

// TestEncodeEncryptedEventNeedsACodec guards the one input that cannot be
// defaulted: a reply in clear text is not a reply this client can read.
func TestEncodeEncryptedEventNeedsACodec(t *testing.T) {
	if _, err := encodeEncryptedEvent(nil, "/sio", sioAuthEvent, map[string]any{}); err == nil {
		t.Fatal("encoding without a codec must fail rather than send clear text")
	}
}

// TestAuthReplyEchoesTheRequestID pins the correlation field and the identity
// echo against the captured request.
func TestAuthReplyEchoesTheRequestID(t *testing.T) {
	session := &sioSession{sid: pinnedSid}
	head := sioHead{
		"uniq_id":       "350713954",
		sioRequestIDKey: float64(3),
		"sid":           pinnedSid,
	}

	body := authReplyBody(session, head, 3)
	if body[sioRequestIDKey] != 3 {
		t.Fatalf("%s is %v, want 3", sioRequestIDKey, body[sioRequestIDKey])
	}
	if body["uniq_id"] != "350713954" {
		t.Fatalf("uniq_id is %v, want the id the client sent", body["uniq_id"])
	}
	if body["sid"] != pinnedSid {
		t.Fatalf("sid is %v, want the session's own id", body["sid"])
	}
}

// TestSioHeadReadsBothJSONSpellings covers the mixed typing in the captured
// request, where platform arrives quoted and type_device does not.
func TestSioHeadReadsBothJSONSpellings(t *testing.T) {
	head := sioHead{
		"number": float64(12),
		"quoted": "12",
		"spaced": " 12 ",
		"text":   "Nothing A059P",
		"whole":  float64(2),
	}

	for _, key := range []string{"number", "quoted", "spaced"} {
		got, ok := head.number(key)
		if !ok || got != 12 {
			t.Fatalf("number(%q) = %d, %v; want 12, true", key, got, ok)
		}
	}
	if _, ok := head.number("text"); ok {
		t.Fatal("a device model must not read as a request id")
	}
	if _, ok := head.number("missing"); ok {
		t.Fatal("an absent key must not read as a request id")
	}
	if got := head.text("whole"); got != "2" {
		t.Fatalf(`text("whole") = %q, want "2" with no decimal point`, got)
	}
	if got := head.text("missing"); got != "" {
		t.Fatalf(`text("missing") = %q, want ""`, got)
	}
}

// TestDecodedEventNameAcceptsBothDirections checks the asymmetry that the
// literal substitution forces: the client sends the name bare, this server
// sends it quoted, and both have to read back as the same event.
func TestDecodedEventNameAcceptsBothDirections(t *testing.T) {
	if got := decodedEventName([]byte(sioAuthEvent)); got != sioAuthEvent {
		t.Fatalf("bare name read as %q", got)
	}
	if got := decodedEventName([]byte(`"` + sioAuthEvent + `"`)); got != sioAuthEvent {
		t.Fatalf("quoted name read as %q", got)
	}
}
