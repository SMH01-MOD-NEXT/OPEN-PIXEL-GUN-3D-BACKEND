// SPDX-License-Identifier: GPL-3.0-or-later

package api

// The boot stops on the documents nobody hosts.
//
// Run 56, device 001691527000747, logs/backend-2026-08-29.log: every poll of
// the day carries "h":"" for TrophyRoad, TopUp, ManualOffers,
// ConfrontationEvent and Boosters - 84 polls each with an empty hash, 8 with a
// fingerprint - while CombatTokens, ExpOpen and WearSkins arrive with a 16-hex
// fingerprint in all 100. An empty hash is the client saying it holds no copy
// of that document. This server hosted exactly one document, so those names
// were answered with their own empty cache echoed back, and their consumers
// said so on screen:
//
//	[TopUp] ---- _config == null
//	[ManualOffers] ---- _config == null
//	[ConfrontationEvent] ---- _config == null
//	[Boosters] ---- _config == null
//	[TrophyRoad] ---- parse error, message below
//
// TrophyRoad differs because it has a second source: the literal
// [TrophyRoad] ---- parse from prefs error, message below sits beside it in
// global-metadata.dat, so a missing document reaches that parser as empty text
// rather than as a null reference.
//
// The documents are read from files rather than compiled in, because their
// shape is a measurement and measurements change. Run 56 pulled the documents
// the client's own parsers received from
// /sdcard/Android/data/com.pixel.gun3d/files/opg3d-doc-*.json: every one of
// them is a JSON object whose first member is configId naming the document,
// which is the same object the client's config cache stores as the plaintext
// h_<16 hex>_c_<object>. A file per name lets the next logcat correct a
// document without a rebuild, and keeps this server from inventing members it
// has not measured.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	// hostedConfigDir is the directory under ContentDir that holds one file
	// per hosted document, named <ConfigName>.json after the name the client
	// polls it under.
	hostedConfigDir = "sio-configs"

	// hostedConfigVersion is the document version every hosted reply states.
	// A body sent without it was read and discarded (measured 2026-08-29
	// 09:24:35.790), and the one document whose version was read off the
	// device is version 1, so the two stay the same constant until a
	// measurement separates them.
	hostedConfigVersion = fullSlotsVersion

	// hostedConfigNameLimit bounds a polled name before it is used to address
	// a file. The longest name in the captured 153-name poll is 27 characters.
	hostedConfigNameLimit = 64
)

// hostedConfig is one document this server can hand over, in the two spellings
// its two delivery paths need: the poll answer carries the parsed object, the
// push carries the bytes as an attachment.
type hostedConfig struct {
	document map[string]any
	body     []byte
	hash     string
}

// hostedConfigFile maps a polled config name onto the file that would hold its
// document, or reports that the name cannot address a file.
//
// Every name arrives from the client, so anything that is not a plain
// identifier is refused rather than joined onto a path.
func hostedConfigFile(name string) (string, bool) {
	if name == "" || len(name) > hostedConfigNameLimit {
		return "", false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_', r == '-':
		default:
			return "", false
		}
	}
	return name + ".json", true
}

// hostedConfigFromFile reads the document hosted for one config name.
//
// A name with no file is the ordinary case - the client polls 153 names and
// this server hosts a handful - so it is not logged. A file that exists and
// cannot be used is logged, because that is an operator mistake which would
// otherwise look exactly like a document nobody hosts.
func (s *Server) hostedConfigFromFile(name string) (hostedConfig, bool) {
	file, ok := hostedConfigFile(name)
	if !ok {
		return hostedConfig{}, false
	}
	path := filepath.Join(s.cfg.ContentDir, hostedConfigDir, file)
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Event("config.document_unreadable", map[string]any{
				"config": name,
				"path":   path,
				"error":  err.Error(),
			})
		}
		return hostedConfig{}, false
	}

	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		s.log.Event("config.document_unparsable", map[string]any{
			"config": name,
			"path":   path,
			"error":  err.Error(),
			"note":   "a hosted document is a JSON object; the client reads every one of its members as config content",
		})
		return hostedConfig{}, false
	}

	// The hash is derived from the re-encoded document rather than from the
	// file's bytes, so reindenting a file does not tell every client its copy
	// went stale.
	body, err := json.Marshal(document)
	if err != nil {
		s.log.Event("config.document_unencodable", map[string]any{
			"config": name,
			"path":   path,
			"error":  err.Error(),
		})
		return hostedConfig{}, false
	}
	return hostedConfig{
		document: document,
		body:     body,
		hash:     s.hostedConfigHash(body),
	}, true
}

// newConfigNonce returns the salt one run of this server states in its
// fingerprints. The value only has to differ between runs, so the clock is
// enough.
func newConfigNonce() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

// hostedConfigHash is the fingerprint this server states for a document body.
//
// Run 64 measured what the fingerprint decides, by reading the client's own
// dictionary as the push was handled. The handler looks the polled name up in
// the dictionary its readers consult, and:
//
//	store id=167 ... embedded set count 80  -> 80   (Boosters, held, skipped)
//	store id=69  ... embedded set count 80  -> 81   (ClanCommon, new, inserted)
//	store id=7   ... embedded set count 81  -> 81   (held, skipped)
//	store id=4/38/88/47                     -> 82/83/84/85 (new, inserted)
//
// The five names the client held no copy of were inserted; the six it had
// stored on an earlier run were not. The difference is the fingerprint: the
// handler compares the one it already holds against the one being delivered
// and skips the body when they match. That dictionary is rebuilt at boot with
// fingerprints but without texts - which is why the client polls with this
// server's own hashes and still answers get-text with nothing - so a document
// it stored once can never be delivered again while the fingerprint is
// honest about the bytes.
//
// With ConfigHashNonce on, the fingerprint is that digest salted with a value
// fixed for this run, so it cannot equal anything the client stored earlier
// and the handler takes the branch that writes the text. Nothing verifies the
// fingerprint against the body: the client uses it as the name of a cache
// entry and as the equality test above, both of which a salted digest
// satisfies.
func (s *Server) hostedConfigHash(body []byte) string {
	sum := sha256.Sum256(body)
	if !s.cfg.ConfigHashNonce {
		return hex.EncodeToString(sum[:8])
	}
	salted := sha256.Sum256(append(sum[:], s.configNonce...))
	return hex.EncodeToString(salted[:8])
}

// hostedConfigDocument returns the document this server hosts for one polled
// name, whether it comes from a file or from the one document that is built
// in.
//
// A file wins over the built-in document, so a measurement can correct
// CommandsWithFullSlots on the device without a rebuild.
func (s *Server) hostedConfigDocument(name string) (hostedConfig, bool) {
	if hosted, ok := s.hostedConfigFromFile(name); ok {
		return hosted, true
	}
	if name != sioFullSlotsConfig {
		return hostedConfig{}, false
	}
	body, _, ok := fullSlotsPayload()
	if !ok {
		return hostedConfig{}, false
	}
	document, ok := fullSlotsObject(body)
	if !ok {
		return hostedConfig{}, false
	}
	return hostedConfig{document: document, body: []byte(body), hash: s.hostedConfigHash([]byte(body))}, true
}
