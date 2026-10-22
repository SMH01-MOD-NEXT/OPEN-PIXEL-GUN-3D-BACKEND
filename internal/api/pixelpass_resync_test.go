// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/pixelpass"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestPixelPassConflictArmsAuthoritativeReloadNotFalseGrant(t *testing.T) {
	if !pixelpass.Active(time.Now()) {
		t.Skip("frozen season; deterministic store tests cover lifecycle")
	}
	s, session, rev := newOperatorEditedProgress(t)
	completeRevisionDownload(t, s, session, rev)
	init := sioHead{"id": 322, "i": "pass-init", "p": map[string]any{"i": 1, "c": "stock-config-reference"}}
	if r := s.answerUpdateProgress(session, init, 100); r["err_code"] != sioErrNone {
		t.Fatal(r)
	}
	before, _ := s.st.Profile(revisionTestPlayerID)
	forged := sioHead{"id": 323, "i": "bad-reward", "p": map[string]any{"l": 5, "r": []any{"26015:9999"}}}
	s.answerUpdateProgress(session, forged, 101)
	p, _ := s.st.Profile(revisionTestPlayerID)
	if p.ProgressRevision != rev {
		t.Fatal("forged reward armed reload")
	}
	claim := sioHead{"id": 323, "i": "stale-level5", "p": map[string]any{"l": 5, "r": []any{"26015:25"}}}
	reply := s.answerUpdateProgress(session, claim, 102)
	if reply["err_code"] != 1 || reply["result"] != "progress_resync_required" || reply["i"] != "stale-level5" {
		t.Fatal("failed grant acknowledged or lost correlation", reply)
	}
	p, _ = s.st.Profile(revisionTestPlayerID)
	if p.ProgressRevision != rev+1 || !reflect.DeepEqual(p.PixelPass, before.PixelPass) || p.Coins != before.Coins || p.Gems != before.Gems || !reflect.DeepEqual(p.ProgressCommandReceipts, before.ProgressCommandReceipts) {
		t.Fatal("conflict changed gameplay")
	}
	status := s.answerProgressStatus(session, sioHead{}, 103)
	if status["opg3d_admin_resync"] != revisionTestPlayerID+":"+strconv.FormatUint(p.ProgressRevision, 10) {
		t.Fatal("native reload bridge not armed")
	}
	current := p.ProgressRevision
	s.answerUpdateProgress(session, claim, 104)
	p, _ = s.st.Profile(revisionTestPlayerID)
	if p.ProgressRevision != current {
		t.Fatal("repeat conflict advanced revision again")
	}
	// Simulate fresh authorization then the real correlated full-download boundary.
	fresh := revisionTestSession("pass-reload")
	fresh.setProgressWriteRevision(current)
	s.answerProgressStatus(fresh, sioHead{}, 105)
	completeRevisionDownload(t, s, fresh, current)
	p, _ = s.st.Profile(revisionTestPlayerID)
	if !reflect.DeepEqual(p.PixelPass, before.PixelPass) {
		t.Fatal("reload lost premium/crowns/claims")
	}
	if status = s.answerProgressStatus(fresh, sioHead{}, 106); status["opg3d_admin_resync"] != nil {
		t.Fatal("reload marker did not retire")
	}
}
