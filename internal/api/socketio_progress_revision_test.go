// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

const revisionTestPlayerID = "918930694"

func newOperatorEditedProgress(t *testing.T) (*Server, *sioSession, uint64) {
	t.Helper()
	s, _ := newTestServer(t)
	if _, err := s.st.EnsureProfile(revisionTestPlayerID, "Veteran"); err != nil {
		t.Fatal(err)
	}
	profile, err := s.st.SetProfile(revisionTestPlayerID, "Veteran", 32, 20000, 123123123, 123123123)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ProgressRevision == 0 {
		t.Fatal("operator edit did not create a progress revision")
	}
	return s, authorisedSession("revision-download", revisionTestPlayerID), profile.ProgressRevision
}

func revisionTestSession(sid string) *sioSession {
	return authorisedSession(sid, revisionTestPlayerID)
}

func wrapRevisionEcho(slots map[string]any) map[string]any {
	wire := make(map[string]any, len(slots))
	for id, value := range slots {
		wire[id] = map[string]any{sioProgressSlotDataKey: value, "h": "device-echo"}
	}
	return wire
}

func stageRevisionForRawEcho(t *testing.T, s *Server, session *sioSession, revision uint64) {
	t.Helper()
	s.answerLastCommand(session, sioHead{}, 8)
	syncReply := s.answerSyncFull(session, sioHead{sioSlotsKey: map[string]any{"4": "client-md5"}}, 17)
	if _, ok := syncReply[sioSlotsKey].(map[string]any); !ok {
		t.Fatalf("correlated sync_full did not return authoritative slots: %#v", syncReply)
	}
	if pending, served := session.pendingProgressState(); pending != revision || !served {
		t.Fatalf("download evidence = (%d, %t), want (%d, true)", pending, served, revision)
	}
	s.answerPlayerSuccessInit(session, sioHead{}, 31)
	if !s.progressRevisionWasDelivered(revisionTestPlayerID, "device", revision) {
		t.Fatal("state-9 Login did not stage the raw-echo boot")
	}
	profile, err := s.st.Profile(revisionTestPlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if got, pending := profile.PendingProgressRevision("device"); !pending || got != revision {
		t.Fatalf("download without raw echo was acknowledged: (%d, %t)", got, pending)
	}
}

func completeStagedRevision(t *testing.T, s *Server, revision uint64) *sioSession {
	t.Helper()
	session := revisionTestSession("revision-raw-echo")
	status := s.answerProgressStatus(session, sioHead{}, 3)
	if got := status[sioProgressEnabledKey]; got != sioProgressSynchronize {
		t.Fatalf("staged revision answers enabled=%v, want state-13 code %d", got, sioProgressSynchronize)
	}
	last := s.answerLastCommand(session, sioHead{}, 8)
	if last["index"] != "" || last["device_id"] != "" {
		t.Fatalf("raw-echo boot advertised a chooser cursor: %#v", last)
	}
	if pending, served := session.pendingProgressState(); pending != revision || !served {
		t.Fatalf("staged evidence = (%d, %t), want (%d, true)", pending, served, revision)
	}
	profile, err := s.st.Profile(revisionTestPlayerID)
	if err != nil {
		t.Fatal(err)
	}
	replace := s.answerReplaceProgress(session, sioHead{
		sioReplaceProgressIndexKey: "",
		sioSlotsKey:                wrapRevisionEcho(profile.ProgressSlots),
	}, 19)
	if replace["error"] != sioErrNone {
		t.Fatalf("matching raw echo failed: %#v", replace)
	}
	if gotRevision, served, echoed := session.progressRevisionEvidence(); gotRevision != revision || !served || !echoed {
		t.Fatalf("raw evidence = (%d, %t, %t), want (%d, true, true)", gotRevision, served, echoed, revision)
	}
	s.answerPlayerSuccessInit(session, sioHead{}, 31)
	return session
}

func TestProgressRevisionUsesStateNineThenRawEchoAndIsNotReplayed(t *testing.T) {
	s, download, revision := newOperatorEditedProgress(t)

	status := s.answerProgressStatus(download, sioHead{}, 3)
	if got := status[sioProgressEnabledKey]; got != sioProgressOpen {
		t.Fatalf("new revision answers enabled=%v, want state-9 code %d", got, sioProgressOpen)
	}
	first := s.answerLastCommand(download, sioHead{}, 8)
	if first["command"] != "" || first["command_id"] != 0 || first["index"] != "" || first["device_id"] != "" {
		t.Fatalf("download boot lost the safe empty-cursor contract: %#v", first)
	}
	if pending, served := download.pendingProgressState(); pending != revision || served {
		t.Fatalf("initial evidence = (%d, %t), want (%d, false)", pending, served, revision)
	}

	stageRevisionForRawEcho(t, s, download, revision)
	// A staged revision must use code 2 even if the broad stored-profile mode
	// is disabled; otherwise there is no stock path that uploads the raw slots.
	s.cfg.ProgressSynchronizeMode = false
	completeStagedRevision(t, s, revision)

	profile, err := s.st.Profile(revisionTestPlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if got, pending := profile.PendingProgressRevision("device"); pending || got != revision {
		t.Fatalf("confirmed revision remains pending: (%d, %t)", got, pending)
	}
	if s.progressRevisionWasDelivered(revisionTestPlayerID, "device", revision) {
		t.Fatal("final acknowledgement left a transient delivery stage")
	}

	// The acknowledged device is never armed again. With the broad mode still
	// disabled this also proves that code 1 here is ordinary, not a revision
	// replay; enabling the broad mode would produce its normal code 2 instead.
	restart := revisionTestSession("revision-restart")
	restartStatus := s.answerProgressStatus(restart, sioHead{}, 43)
	if got := restartStatus[sioProgressEnabledKey]; got != sioProgressOpen {
		t.Fatalf("acknowledged restart answers enabled=%v, want ordinary code %d", got, sioProgressOpen)
	}
	second := s.answerLastCommand(restart, sioHead{}, 44)
	if second["index"] != "" || second["device_id"] != "" {
		t.Fatalf("ordinary reply lost the safe empty cursor: %#v", second)
	}
	if pending, served := restart.pendingProgressState(); pending != 0 || served {
		t.Fatalf("acknowledged revision was armed again: (%d, %t)", pending, served)
	}
}

func TestProgressRevisionAcknowledgementIsPerDevice(t *testing.T) {
	s, firstDevice, revision := newOperatorEditedProgress(t)
	stageRevisionForRawEcho(t, s, firstDevice, revision)
	completeStagedRevision(t, s, revision)

	secondDevice := &sioSession{sid: "second-device"}
	secondDevice.setIdentity(revisionTestPlayerID, "another-device", revisionTestPlayerID)
	status := s.answerProgressStatus(secondDevice, sioHead{}, 3)
	if got := status[sioProgressEnabledKey]; got != sioProgressOpen {
		t.Fatalf("unacknowledged second device answers enabled=%v, want %d", got, sioProgressOpen)
	}
	s.answerLastCommand(secondDevice, sioHead{}, 8)
	if pending, served := secondDevice.pendingProgressState(); pending != revision || served {
		t.Fatalf("second-device evidence = (%d, %t), want (%d, false)", pending, served, revision)
	}

	firstRestart := revisionTestSession("first-device-restart")
	if got := s.answerProgressStatus(firstRestart, sioHead{}, 9)[sioProgressEnabledKey]; got != sioProgressSynchronize {
		t.Fatalf("acknowledged first device lost ordinary stored-profile mode: %v", got)
	}
	s.answerLastCommand(firstRestart, sioHead{}, 10)
	if pending, served := firstRestart.pendingProgressState(); pending != 0 || served {
		t.Fatalf("first device was made pending again: (%d, %t)", pending, served)
	}
}

func TestMismatchingRawEchoReturnsToStateNineDownload(t *testing.T) {
	s, download, revision := newOperatorEditedProgress(t)
	stageRevisionForRawEcho(t, s, download, revision)

	echo := revisionTestSession("mismatching-raw-echo")
	if got := s.answerProgressStatus(echo, sioHead{}, 3)[sioProgressEnabledKey]; got != sioProgressSynchronize {
		t.Fatalf("staged route = %v, want %d", got, sioProgressSynchronize)
	}
	s.answerLastCommand(echo, sioHead{}, 8)
	mismatch := map[string]any{
		"3": map[string]any{"Coins": float64(7), "GemsCurrency": float64(8)},
		"4": map[string]any{"l": float64(1), "e": float64(0)},
	}
	s.answerReplaceProgress(echo, sioHead{sioSlotsKey: wrapRevisionEcho(mismatch)}, 19)
	if s.progressRevisionWasDelivered(revisionTestPlayerID, "device", revision) {
		t.Fatal("mismatching raw echo left the state-13 stage armed")
	}
	if pending, served := echo.pendingProgressState(); pending != revision || served {
		t.Fatalf("mismatch evidence = (%d, %t), want (%d, false)", pending, served, revision)
	}
	s.answerPlayerSuccessInit(echo, sioHead{}, 31)

	profile, err := s.st.Profile(revisionTestPlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if got, pending := profile.PendingProgressRevision("device"); !pending || got != revision {
		t.Fatalf("mismatch acknowledged revision: (%d, %t)", got, pending)
	}
	retry := revisionTestSession("redownload-after-mismatch")
	if got := s.answerProgressStatus(retry, sioHead{}, 32)[sioProgressEnabledKey]; got != sioProgressOpen {
		t.Fatalf("mismatch did not return to state-9 code: %v", got)
	}
}

func TestSyncFullCannotStageOrAcknowledgeANewerOperatorEdit(t *testing.T) {
	s, session, oldRevision := newOperatorEditedProgress(t)
	s.answerLastCommand(session, sioHead{}, 8)

	newProfile, err := s.st.SetProfile(revisionTestPlayerID, "Veteran", 32, 470, 123123123, 123123124)
	if err != nil {
		t.Fatal(err)
	}
	if newProfile.ProgressRevision == oldRevision {
		t.Fatal("second operator edit did not advance the revision")
	}
	s.answerSyncFull(session, sioHead{sioSlotsKey: map[string]any{}}, 17)
	s.answerPlayerSuccessInit(session, sioHead{}, 31)
	if s.progressRevisionWasDelivered(revisionTestPlayerID, "device", oldRevision) ||
		s.progressRevisionWasDelivered(revisionTestPlayerID, "device", newProfile.ProgressRevision) {
		t.Fatal("stale state-9 session staged a revision")
	}

	profile, err := s.st.Profile(revisionTestPlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if got, pending := profile.PendingProgressRevision("device"); !pending || got != newProfile.ProgressRevision {
		t.Fatalf("new revision was acknowledged: (%d, %t)", got, pending)
	}
	latest := revisionTestSession("new-revision")
	if got := s.answerProgressStatus(latest, sioHead{}, 32)[sioProgressEnabledKey]; got != sioProgressOpen {
		t.Fatalf("new revision answers enabled=%v, want %d", got, sioProgressOpen)
	}
	s.answerLastCommand(latest, sioHead{}, 33)
	if pending, served := latest.pendingProgressState(); pending != newProfile.ProgressRevision || served {
		t.Fatalf("new revision evidence = (%d, %t)", pending, served)
	}
}

func TestAuthoritativeProgressRejectsNonBlankClientCoreRewrite(t *testing.T) {
	s, session, revision := newOperatorEditedProgress(t)
	body := s.answerReplaceProgress(session, sioHead{
		sioReplaceProgressIndexKey: "local-index",
		sioSlotsKey: map[string]any{
			"3": map[string]any{sioProgressSlotDataKey: map[string]any{"Coins": float64(7), "GemsCurrency": float64(8)}, "h": "currency-md5"},
			"4": map[string]any{sioProgressSlotDataKey: map[string]any{"l": float64(32), "e": float64(470)}, "h": "level-md5"},
		},
	}, 41)
	if body["error"] != sioErrNone {
		t.Fatalf("replace_progress failed: %#v", body)
	}

	profile, err := s.st.Profile(revisionTestPlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ProgressRevision != revision {
		t.Fatalf("client upload changed operator revision: %d -> %d", revision, profile.ProgressRevision)
	}
	level, experience, coins, gems := profile.Counters()
	if level != 32 || experience != 20000 || coins != 123123123 || gems != 123123123 {
		t.Fatalf("client rewrote authority: level=%d exp=%d coins=%d gems=%d", level, experience, coins, gems)
	}
}

func TestMatchingReplaceWithoutDownloadOrStageCannotAcknowledge(t *testing.T) {
	s, session, revision := newOperatorEditedProgress(t)
	s.answerLastCommand(session, sioHead{}, 8)
	profile, err := s.st.Profile(revisionTestPlayerID)
	if err != nil {
		t.Fatal(err)
	}
	s.answerReplaceProgress(session, sioHead{sioSlotsKey: wrapRevisionEcho(profile.ProgressSlots)}, 41)
	if session.progressRevisionConfirmed(revision) {
		t.Fatal("raw values without delivery proof became confirmable")
	}
	s.answerPlayerSuccessInit(session, sioHead{}, 42)
	profile, err = s.st.Profile(revisionTestPlayerID)
	if err != nil {
		t.Fatal(err)
	}
	if got, pending := profile.PendingProgressRevision("device"); !pending || got != revision {
		t.Fatalf("raw values without delivery proof were acknowledged: (%d, %t)", got, pending)
	}
}

func TestLastCommandRejectsUnboundSession(t *testing.T) {
	s, _ := newTestServer(t)
	body := s.answerLastCommand(&sioSession{sid: "unbound"}, sioHead{}, 8)
	if body["error"] != sioErrNotAuthorized {
		t.Fatalf("unbound get_last_command = %#v, want authorization error", body)
	}
}

func TestGetProgressAlwaysReturnsLiveSlotsDictionary(t *testing.T) {
	s, _ := newTestServer(t)
	body := s.answerGetProgress(&sioSession{sid: "unbound"}, sioHead{}, 9)
	if _, ok := body[sioSlotsKey].(map[string]any); !ok {
		t.Fatalf("empty get_progress slots = %T, want a dictionary", body[sioSlotsKey])
	}
}
