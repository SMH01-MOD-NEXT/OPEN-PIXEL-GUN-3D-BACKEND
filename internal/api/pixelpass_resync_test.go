// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/clienterrors"
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
	q := s.clientErrors.Query(clienterrors.Query{Text: "resync_required"}, time.Now())
	if q.Total != 1 || q.Groups[0].Revision != p.ProgressRevision {
		t.Fatal("resync diagnostic missing", q)
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
	if q = s.clientErrors.Query(clienterrors.Query{Text: "resync_completed"}, time.Now()); q.Total != 1 {
		t.Fatal("resync completion diagnostic missing", q)
	}
	p, _ = s.st.Profile(revisionTestPlayerID)
	if !reflect.DeepEqual(p.PixelPass, before.PixelPass) {
		t.Fatal("reload lost premium/crowns/claims")
	}
	if status = s.answerProgressStatus(fresh, sioHead{}, 106); status["opg3d_admin_resync"] != nil {
		t.Fatal("reload marker did not retire")
	}
}

func TestNativeTaskCrownCollectionAcknowledgedWithoutResync(t *testing.T) {
	if !pixelpass.Active(time.Now()) {
		t.Skip("frozen season")
	}
	s, session, rev := newOperatorEditedProgress(t)
	completeRevisionDownload(t, s, session, rev)
	commands := []any{
		map[string]any{"id": 322, "ci": "init", "p": map[string]any{"i": 1, "c": "stock"}},
		map[string]any{"id": 150, "ci": "progress", "p": map[string]any{"5_1000": map[string]any{"p": 3, "c": 1}}},
	}
	if r := s.answerUpdateProgress(session, sioHead{"id": 63, "i": "setup", "p": map[string]any{"c": commands}}, 110); r["err_code"] != sioErrNone {
		t.Fatal(r)
	}
	claim := sioHead{"id": 63, "i": "native-task", "p": map[string]any{"c": []any{
		map[string]any{"id": 326, "ci": "4588e24e", "p": map[string]any{"e": 60}},
		map[string]any{"id": 152, "ci": "3b399ef3", "p": map[string]any{"5_1000": map[string]any{"r": 1}}},
	}}}
	for i := 0; i < 2; i++ {
		if r := s.answerUpdateProgress(session, claim, 111+i); r["err_code"] != sioErrNone {
			t.Fatal(r)
		}
	}
	p, _ := s.st.Profile(revisionTestPlayerID)
	if p.ProgressRevision != rev || p.PixelPass.Claimed["1000"] != true || p.PixelPass.MatchCrowns != 0 {
		t.Fatal("claim lost or reload armed", p.PixelPass)
	}
	if status := s.answerProgressStatus(session, sioHead{}, 115); status["opg3d_admin_resync"] != nil {
		t.Fatal("unnecessary reload")
	}
}
