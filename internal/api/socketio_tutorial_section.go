// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "strconv"

// The tutorial section of the progress document, and why this server owns it.
//
// The 23.1.3 boot asks its tutorial manager whether tutorial 1100
// (RanchoTutorial) is completed at two decisive places:
//
//	libil2cpp.so+0x1F21708  Switcher.LoadMainMenu   -> reported as ranchoOk
//	libil2cpp.so+0x3DC9770  AuthScene finalauth SM  -> the 90% ladder
//
// The answer comes from progress-document section 56. The section id is not a
// guess: the section object's own slot 7 is `mov w0, #0x38 ; ret` and the
// registry getter at 0x26FEEDC looks it up with the same constant. Runs 106 and
// 107 read the section's three static readonly strings out of the live process:
//
//	key@0x00  Tutorial_Slot
//	key@0x08  Tutorial_Slot_Key_JUL_2020
//	key@0x10  t
//
// Until run 108 the yes came from a frontend hook in the client's analysis
// sandbox (`sandbox_2313.h`, `kForceRanchoComplete`). That hook is a temporary
// probe and has to disappear, so the section is authored here instead: a server
// that owns progress is the only thing that can legitimately say a tutorial is
// done.
//
// Which channel carries it was measured, not chosen:
//
//	run 108  init_progress document        the manager never sees it (1200 no's)
//	run 110  get_progress_slots answer     answered yes, but a non-empty slots
//	                                       member also tells the client its set
//	                                       is already uploaded, so it skips
//	                                       create_slots and stalls at 87%
//	run 111  create_slots acknowledgement  answered yes at +2.9s with the upload
//	                                       branch intact; the client persisted
//	                                       the section and uploaded it back
//
// Runs 111-113 then exposed the cost of writing the section as a whole value.
// The client does not merge what arrives into its local tutorial history, it
// replaces it: run 107 answered `IsCompleted(1500) -> 1` from the device's own
// history, and after a `{"t": [1100]}` section landed, run 113 answered
// `IsCompleted(1500) -> 0`. The boot then took the tutorial-step branch (extra
// ladder writes at 0x3DC4BF8, 0x3DC42EC and 0x3DC890C for 81%, 87% and 88%) and
// never started the BalanceController coroutine that writes 95%, so it sat at
// 90% - the exact wall this seed was meant to remove.
//
// Therefore the seed unions: whatever the account already carries stays, and
// only missing ids are appended. The default set is the two ids the boot is
// measured to need, and it is an operator switch so a run can widen or narrow
// it without a rebuild. A full set is known to be wrong: run 102 answered every
// tutorial id as completed and the boot left the ladder for 75 seconds.

const (
	// sioTutorialSectionID is section 56, the tutorials section, keyed the way
	// the client's applier parses document members: the decimal section id.
	sioTutorialSectionID = "56"

	// sioTutorialSectionKey is the section's own payload member, read from the
	// live class as the third static readonly string.
	sioTutorialSectionKey = "t"

	// sioRanchoTutorialID is the tutorial both boot gates ask about.
	sioRanchoTutorialID = 1100

	// sioIntroTutorialID is the id run 107 saw the device answer yes to on its
	// own, and run 113 saw turn into a no once the section was replaced.
	sioIntroTutorialID = 1500
)

// tutorialSectionIDs returns the completed tutorial ids this server publishes,
// or nil when the operator has switched the seed off.
func (s *Server) tutorialSectionIDs() []int {
	if !s.cfg.TutorialSectionSeed {
		return nil
	}
	return s.cfg.TutorialSectionIDs
}

// applyTutorialSection merges this server's tutorial ids into the tutorials
// section of a slot document that is about to be sent.
//
// Nothing is ever removed. The client replaces its local history with what
// arrives, so dropping an id here retracts a tutorial the player really has
// finished, which is what walled the boot at 90% in runs 111-113. The function
// reports whether it changed the document, so the log can say so rather than
// leaving an unexplained extra member.
func (s *Server) applyTutorialSection(document map[string]any) bool {
	if document == nil {
		return false
	}
	seeds := s.tutorialSectionIDs()
	if len(seeds) == 0 {
		return false
	}

	section, _ := document[sioTutorialSectionID].(map[string]any)
	completed := tutorialCompletedIDs(section)
	added := false
	for _, id := range seeds {
		if containsTutorialID(completed, id) {
			continue
		}
		completed = append(completed, id)
		added = true
	}
	if !added {
		return false
	}

	list := make([]any, 0, len(completed))
	for _, id := range completed {
		list = append(list, id)
	}
	merged := make(map[string]any, len(section)+1)
	for key, value := range section {
		merged[key] = value
	}
	merged[sioTutorialSectionKey] = list
	document[sioTutorialSectionID] = merged
	return true
}

// tutorialCompletedIDs reads the section's list of completed ids. Values that
// arrive over the wire are JSON numbers, values built in this process are ints,
// and a stored document can hold either, so all of them are accepted.
func tutorialCompletedIDs(section map[string]any) []int {
	if section == nil {
		return nil
	}
	raw, ok := section[sioTutorialSectionKey].([]any)
	if !ok {
		return nil
	}
	ids := make([]int, 0, len(raw))
	for _, value := range raw {
		switch typed := value.(type) {
		case int:
			ids = append(ids, typed)
		case int64:
			ids = append(ids, int(typed))
		case float64:
			ids = append(ids, int(typed))
		case string:
			if parsed, err := strconv.Atoi(typed); err == nil {
				ids = append(ids, parsed)
			}
		}
	}
	return ids
}

func containsTutorialID(ids []int, id int) bool {
	for _, held := range ids {
		if held == id {
			return true
		}
	}
	return false
}
