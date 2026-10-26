// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"math"
	"strconv"
)

// Progress command 253 completes one tutorial step.
//
// Measured stock 23.1.3 children carry exactly one id, for example
// {"id":253,"ci":"adc3e4ec","p":{"t":1600},"h":{"56":"<md5>"}}: the client has
// already marked the step completed locally and reports the digest of progress
// section 56, the tutorials section {"t":[<completed ids>]} described in
// internal/api/socketio_tutorial_section.go. The ids are the stock tutorial
// enum (TypeDefIndex 12362, 1100 RanchoTutorial .. 2600 ClanEnter); the first
// battle completes 1300 (FirstMatch).
//
// Before this reducer the command was dropped, so the server copy of section 56
// stayed at the create_slots snapshot. The client replaces its local history
// with the section a later sync_full delivers, which sent players back into
// the tutorial. The reducer therefore only ever adds: held ids keep their
// stored representation and order, other section members are preserved, and a
// repeated completion is a no-op.
const (
	tutorialSectionSlot = "56"
	tutorialSectionList = "t"
	// maxTutorialSectionIDs bounds the stored history. Stock 23.1.3 defines 16
	// tutorial ids; the margin only absorbs ids already held by a document.
	maxTutorialSectionIDs = 256
)

var errTutorialCompletion = errors.New("store: invalid tutorial completion payload")

func applyTutorialCompletion(p *Profile, d map[string]any) error {
	// The client keeps tutorial history as List<int>, so an id beyond int32
	// could not be applied back. Strings are refused like every other counter.
	id, ok := counterInt64(d[tutorialSectionList])
	if !ok || id <= 0 || id > math.MaxInt32 {
		return errTutorialCompletion
	}
	section, err := ensureObject(p.ProgressSlots, tutorialSectionSlot)
	if err != nil {
		return err
	}
	var held []any
	switch list := section[tutorialSectionList].(type) {
	case nil:
	case []any:
		held = list
	default:
		// Never overwrite a history this server cannot read.
		return errors.New("store: incompatible tutorial section shape")
	}
	for _, value := range held {
		if n, ok := heldTutorialID(value); ok && n == id {
			return nil
		}
	}
	if len(held) >= maxTutorialSectionIDs {
		return errors.New("store: tutorial history limit reached")
	}
	next := make([]any, len(held), len(held)+1)
	copy(next, held)
	section[tutorialSectionList] = append(next, id)
	return nil
}

// heldTutorialID reads an id already stored in section 56 with the same
// tolerance as the API's section merge: JSON numbers, Go integers and decimal
// strings name the same tutorial, so none of them is duplicated.
func heldTutorialID(value any) (int64, bool) {
	if text, ok := value.(string); ok {
		n, err := strconv.ParseInt(text, 10, 64)
		return n, err == nil
	}
	return counterInt64(value)
}
