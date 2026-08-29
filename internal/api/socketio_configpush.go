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
	"sort"
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

// The push spells its two keys in full.
//
// h, c and v are the client's own cache vocabulary: it uses them in the poll
// it sends and this server echoes them in the answer to that poll. The config
// system's push handler reads different names, and run 49 got it to say the
// first one out loud once the reply envelope stopped hiding the config name:
//
//	[ConfigSystem.OnWebS_ocketCallback] Key 'val' not found: CommandsWithFullSlots
//
// That warning is raised from RVA 0x24A2850, inside the handler at 0x24A1E1C,
// which places the failing lookup at 0x24A261C. The three messages that sit
// beside it in global-metadata.dat spell out the rest of the contract:
//
//	[ConfigSystem.OnWebS_ocketCallback] Key 'hash' not found:
//	[ConfigSystem.OnWebS_ocketCallback] An issue processing 'val' for key:
//	[ConfigSystem.OnWebS_ocketCallback] save incoming config:
//
// val is not the body. At 0x24A26AC the handler hands the value to a converter
// that ends in a change of type to Int32, bounds-checks the result against the
// length of the attachment list it was called with, and reads that element as
// the document. So val is the attachment index and hash identifies the body,
// which is the pair this server already computes.
const (
	sioPushBodyKey = "val"
	sioPushHashKey = "hash"
)

// configPushBatch is one push: the map the client iterates, and the bodies its
// entries index by position.
type configPushBatch struct {
	entries   map[string]any
	documents [][]byte
}

// configPushDocumentLimit is how many documents one push can carry. Each push
// spends two attachments on the encrypted event name and argument, and the
// rest are bodies.
const configPushDocumentLimit = sioMaxAttachments - sioReplyAttachments

// configPushBatches builds the pushes that hand every hosted document the
// client polled for over to it.
//
// Only polled names are pushed, because a document the client never asked
// about would be an unsolicited entry in a list it built itself. Only hosted
// names carry a body index, because an index into an attachment that was not
// sent is how a managed handler throws inside the client rather than here.
//
// A single push cannot carry more bodies than the attachment cap allows, and
// the client polls once per boot, so the documents that do not fit are not
// dropped: they go out in further pushes. Names are ordered so that the
// batching is reproducible between runs rather than following Go's map order.
func (s *Server) configPushBatches(list map[string]any) []configPushBatch {
	names := make([]string, 0, len(list))
	for name := range list {
		names = append(names, name)
	}
	sort.Strings(names)

	batches := make([]configPushBatch, 0, 1)
	current := configPushBatch{}
	for _, name := range names {
		hosted, ok := s.hostedConfigDocument(name)
		if !ok {
			continue
		}
		if current.entries == nil {
			current = configPushBatch{
				entries:   make(map[string]any, configPushDocumentLimit),
				documents: make([][]byte, 0, configPushDocumentLimit),
			}
		}
		current.entries[name] = map[string]any{
			sioPushBodyKey: len(current.documents),
			sioPushHashKey: hosted.hash,
		}
		current.documents = append(current.documents, hosted.body)
		if len(current.documents) == configPushDocumentLimit {
			batches = append(batches, current)
			current = configPushBatch{}
		}
	}
	if len(current.documents) > 0 {
		batches = append(batches, current)
	}
	return batches
}

// pushConfigDocuments hands the hosted documents to the config system on the
// event that system actually subscribes to.
func (s *Server) pushConfigDocuments(session *sioSession, namespace, transport string, codec *sio.Codec, head sioHead) {
	list, ok := head["list"].(map[string]any)
	if !ok {
		return
	}

	batches := s.configPushBatches(list)
	if len(batches) == 0 {
		return
	}

	for number, batch := range batches {
		// The config system iterates the top level of this argument and
		// reads every key as a config name. Run 48 wrapped the names in the
		// reply envelope this server uses everywhere else, and the client
		// named all three wrapper keys back as configs it had never heard
		// of:
		//
		//	[ConfigSystem.OnWebS_ocketCallback] Unknown configId: None, Key: error
		//	[ConfigSystem.OnWebS_ocketCallback] Unknown configId: None, Key: list
		//	[ConfigSystem.OnWebS_ocketCallback] Unknown configId: None, Key: result
		//
		// The gate still opened, so the push looked healthy in this
		// server's log while not one document was ingested. A push is not a
		// reply: nothing correlates it, so it carries no req_id and no
		// envelope.
		frames, err := encodeEncryptedConfigPush(codec, namespace, sioConfigPushEvent, batch.entries, batch.documents)
		if err != nil {
			s.log.Event("socket.config_push_unencodable", map[string]any{
				"sid":     session.sid,
				"event":   sioConfigPushEvent,
				"batch":   number,
				"batches": len(batches),
				"err":     err.Error(),
			})
			return
		}

		for i, frame := range frames {
			if session.send(frame) {
				continue
			}
			s.log.Event("socket.config_push_undelivered", map[string]any{
				"sid":     session.sid,
				"event":   sioConfigPushEvent,
				"batch":   number,
				"batches": len(batches),
				"frame":   i,
				"frames":  len(frames),
				"note":    "the session send queue is full; the push is incomplete",
			})
			return
		}

		s.log.Event("socket.config_pushed", map[string]any{
			"sid":         session.sid,
			"event":       sioConfigPushEvent,
			"transport":   transport,
			"batch":       number,
			"batches":     len(batches),
			"documents":   len(batch.documents),
			"attachments": len(batch.documents) + sioReplyAttachments,
			"names":       len(batch.entries),
			"note":        "bodies travel as attachments 0..n-3; the name and argument are the last two",
		})
	}
}
