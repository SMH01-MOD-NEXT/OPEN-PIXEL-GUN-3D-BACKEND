// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/clienterrors"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

func TestEconomyAPIWiresPersistentBoundedPrivateCoverageJournal(t *testing.T) {
	s, session, rev := newOperatorEditedProgress(t)
	completeRevisionDownload(t, s, session, rev)
	cfg := *s.cfg
	cfg.DataDir = t.TempDir()
	s = New(&cfg, s.log, s.st)
	ctx := session.progressWriteContext()
	_, err := s.st.MergeProfileClientState(revisionTestPlayerID, map[string]any{"coins": 123, "opaque": "DO-NOT-LOG-THIS-CLIENT-TEXT"}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := clienterrors.Open(s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	q := journal.Query(clienterrors.Query{Text: "economy_observed", Source: "backend"}, time.Now())
	if q.Total != 1 || q.Groups[0].Module != "economy_phase1" || !strings.Contains(q.Groups[0].Message, "mode=gradual") {
		t.Fatal(q)
	}
	raw, _ := json.Marshal(q)
	if strings.Contains(string(raw), "DO-NOT-LOG") || strings.Contains(string(raw), ctx.Device) {
		t.Fatal("client content leaked")
	}
	for i := 0; i < 650; i++ {
		s.observeEconomy(store.EconomyObservation{Player: revisionTestPlayerID, Channel: "commands", Outcome: "accepted", Rule: "coverage", Unverified: i})
	}
	q = s.clientErrors.Query(clienterrors.Query{}, time.Now())
	if q.Summary.Events > clienterrors.MaxEvents || q.Summary.Limited == 0 {
		t.Fatal("journal is not rate limited", q.Summary)
	}
}

func TestEconomyKnownMailboxAndExchangeRejectionsKeepCorrelationAndResync(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload map[string]any
	}{
		{"mailbox", map[string]any{"c": "Coins", "v": 9999999, "ca": 62}},
		{"exchange", map[string]any{"c": "GemsCurrency", "v": 9999999, "ca": 107}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, session, rev := newOperatorEditedProgress(t)
			completeRevisionDownload(t, s, session, rev)
			before, _ := s.st.Profile(revisionTestPlayerID)
			head := sioHead{"i": "outer-economy", "p": map[string]any{"c": []any{map[string]any{"id": 10, "ci": "child-economy", "p": tc.payload}}}}
			reply := s.answerUpdateProgress(session, head, 123)
			if reply["err_code"] != 1 || reply["i"] != "outer-economy" || reply["ci"] != "child-economy" || reply["result"] != "progress_resync_required" {
				t.Fatal(reply)
			}
			after, _ := s.st.Profile(revisionTestPlayerID)
			if after.ProgressRevision != rev+1 || after.Coins != before.Coins || after.Gems != before.Gems || !reflect.DeepEqual(after.ProgressCommandReceipts, before.ProgressCommandReceipts) {
				t.Fatal("rejection changed economy")
			}
			q := s.clientErrors.Query(clienterrors.Query{Text: "economy_rejected"}, time.Now())
			if q.Total != 1 || !strings.Contains(q.Groups[0].Message, "rule="+tc.name) {
				t.Fatal(q)
			}
			s.answerUpdateProgress(session, head, 124)
			again, _ := s.st.Profile(revisionTestPlayerID)
			if again.ProgressRevision != after.ProgressRevision {
				t.Fatal("rejection retry advanced revision")
			}
			fresh := revisionTestSession("economy-reload")
			fresh.setProgressWriteRevision(after.ProgressRevision)
			s.answerProgressStatus(fresh, sioHead{}, 125)
			completeRevisionDownload(t, s, fresh, after.ProgressRevision)
			status := s.answerProgressStatus(fresh, sioHead{}, 126)
			if status["opg3d_admin_resync"] != nil {
				t.Fatal("reload never retired")
			}
		})
	}
}

func TestEconomyUnavailableJournalCannotBlockCompatibleGameplay(t *testing.T) {
	s, session, rev := newOperatorEditedProgress(t)
	completeRevisionDownload(t, s, session, rev)
	// Separate diagnostic failure from the gameplay state file.
	cfg := *s.cfg
	cfg.DataDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "client-errors.json"), []byte("invalid journal"), 0600); err != nil {
		t.Fatal(err)
	}
	s = New(&cfg, s.log, s.st)
	if !s.clientErrors.Query(clienterrors.Query{}, time.Now()).Summary.Unavailable {
		t.Fatal("failed journal not observable")
	}
	before, _ := s.st.Profile(revisionTestPlayerID)
	head := sioHead{"id": 10, "i": "unverified-without-journal", "p": map[string]any{"c": "Coins", "v": 7}}
	reply := s.answerUpdateProgress(session, head, 200)
	if reply["err_code"] != sioErrNone {
		t.Fatal("diagnostic failure affected gameplay", reply)
	}
	after, _ := s.st.Profile(revisionTestPlayerID)
	if after.Coins != before.Coins+7 {
		t.Fatal("compatible grant lost")
	}
}
