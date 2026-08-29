// SPDX-License-Identifier: GPL-3.0-or-later

package api

// The config system does not read the answer to its own poll.
//
// Run 34 hooked the four entry points of the client's subscriber registry
// (namespace Rilisoft.WebSocket, dump2313.cs:381487) and printed every
// registration the boot performs. The config system registers exactly one
// handler, and it registers it under a name this server had never sent:
//
//	subscribe event='config_info_versions' target=<config system singleton>
//
// The answer to `subscribe` goes to the socket API facade instead, which
// explains six runs of a poll answered perfectly and a client that kept
// waiting: the poll compares hashes, and the bodies arrive on a separate,
// server-initiated event.
//
// The handler takes (Dictionary<string, object>, Dictionary<object, object>,
// List<byte[]>), so the bodies travel as binary attachments and each entry
// references one by index - which is what the parser's `c` key has meant all
// along.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
)

// sioConfigPushEvent is the event the client's config system listens on.
const sioConfigPushEvent = "config_info_versions"

// encodeEncryptedConfigPush renders one encrypted binary event that carries
// document bodies as extra attachments.
//
// The client substitutes the last two attachments into the payload text and
// leaves the rest in the packet's attachment list, so the bodies go first and
// the encrypted name and argument stay last. The two placeholders are numbered
// accordingly; writing a placeholder for a body would leave it unsubstituted in
// the payload and break the JSON the client parses.
func encodeEncryptedConfigPush(codec *sio.Codec, namespace, event string, args any, documents [][]byte) ([]sioFrame, error) {
	if codec == nil {
		return nil, errors.New("no /sio codec is in force for this session")
	}

	total := len(documents) + sioReplyAttachments
	if total > sioMaxAttachments {
		return nil, fmt.Errorf("%d attachments exceeds the cap of %d", total, sioMaxAttachments)
	}

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
		placeholders = append(placeholders, sioPlaceholder{Placeholder: true, Num: len(documents) + i})
	}
	payload, err := json.Marshal(placeholders)
	if err != nil {
		return nil, fmt.Errorf("encode the placeholders of %q: %w", event, err)
	}

	var header strings.Builder
	header.WriteByte(eioMessage)
	header.WriteByte(sioBinaryEvent)
	header.WriteString(strconv.Itoa(total))
	header.WriteByte('-')
	if namespace != "" {
		header.WriteString(namespace)
		header.WriteByte(',')
	}
	header.Write(payload)

	frames := make([]sioFrame, 0, total+1)
	frames = append(frames, sioFrame{text: header.String()})
	for _, document := range documents {
		frames = append(frames, sioFrame{binary: true, data: withBinaryPrefix(document)})
	}
	frames = append(frames, sioFrame{binary: true, data: withBinaryPrefix(codec.Encrypt(name))})
	frames = append(frames, sioFrame{binary: true, data: withBinaryPrefix(codec.Encrypt(body))})
	return frames, nil
}

// hostedConfigDocument returns the body this server hosts for one config name.
func hostedConfigDocument(name string) ([]byte, string, bool) {
	if name != sioFullSlotsConfig {
		return nil, "", false
	}
	body, hash, ok := fullSlotsPayload()
	if !ok {
		return nil, "", false
	}
	return []byte(body), hash, true
}

// configPushArgument builds one push: the map the client iterates, and the
// bodies its entries index by position.
//
// Only polled names are pushed, because a document the client never asked
// about would be an unsolicited entry in a list it built itself. Only hosted
// names carry a body index, because an index into an attachment that was not
// sent is how a managed handler throws inside the client rather than here.
func configPushArgument(list map[string]any) (map[string]any, [][]byte) {
	documents := make([][]byte, 0, 1)
	entries := make(map[string]any, 1)
	for name := range list {
		document, hash, ok := hostedConfigDocument(name)
		if !ok {
			continue
		}
		entries[name] = map[string]any{
			sioConfigHashKey:    hash,
			sioConfigBodyKey:    len(documents),
			sioConfigVersionKey: fullSlotsVersion,
		}
		documents = append(documents, document)
	}
	return entries, documents
}

// pushConfigDocuments hands the hosted documents to the config system on the
// event that system actually subscribes to.
//
// Only polled names are pushed: a document the client never asked about would
// be an unsolicited entry in a list it built itself. Only hosted names carry a
// body index, because an index into an attachment that was not sent is how a
// managed handler throws inside the client rather than in this server.
func (s *Server) pushConfigDocuments(session *sioSession, namespace, transport string, codec *sio.Codec, head sioHead) {
	list, ok := head["list"].(map[string]any)
	if !ok {
		return
	}

	entries, documents := configPushArgument(list)
	if len(documents) == 0 {
		return
	}

	// The config system iterates the top level of this argument and reads
	// every key as a config name. Run 48 wrapped the names in the reply
	// envelope this server uses everywhere else, and the client named all
	// three wrapper keys back as configs it had never heard of:
	//
	//	[ConfigSystem.OnWebS_ocketCallback] Unknown configId: None, Key: error
	//	[ConfigSystem.OnWebS_ocketCallback] Unknown configId: None, Key: list
	//	[ConfigSystem.OnWebS_ocketCallback] Unknown configId: None, Key: result
	//
	// The gate still opened, so the push looked healthy in this server's log
	// while not one document was ingested. A push is not a reply: nothing
	// correlates it, so it carries no req_id and no envelope.
	frames, err := encodeEncryptedConfigPush(codec, namespace, sioConfigPushEvent, entries, documents)
	if err != nil {
		s.log.Event("socket.config_push_unencodable", map[string]any{
			"sid":   session.sid,
			"event": sioConfigPushEvent,
			"err":   err.Error(),
		})
		return
	}

	for i, frame := range frames {
		if session.send(frame) {
			continue
		}
		s.log.Event("socket.config_push_undelivered", map[string]any{
			"sid":    session.sid,
			"event":  sioConfigPushEvent,
			"frame":  i,
			"frames": len(frames),
			"note":   "the session send queue is full; the push is incomplete",
		})
		return
	}

	s.log.Event("socket.config_pushed", map[string]any{
		"sid":         session.sid,
		"event":       sioConfigPushEvent,
		"transport":   transport,
		"documents":   len(documents),
		"attachments": len(documents) + sioReplyAttachments,
		"names":       len(entries),
		"note":        "bodies travel as attachments 0..n-3; the name and argument are the last two",
	})
}
