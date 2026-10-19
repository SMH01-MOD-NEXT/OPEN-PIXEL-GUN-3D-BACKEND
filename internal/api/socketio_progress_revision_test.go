// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"strconv"
	"testing"
)

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
	session := authorisedSession("revision-download", revisionTestPlayerID)
	session.setProgressWriteRevision(profile.ProgressRevision)
	return s, session, profile.ProgressRevision
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

func completeRevisionDownload(t *testing.T, s *Server, session *sioSession, revision uint64) {
	t.Helper()
	s.answerLastCommand(session, sioHead{}, 8)
	body := s.answerSyncFull(session, sioHead{sioSlotsKey: map[string]any{}}, 17)
	if _, ok := body[sioSlotsKey].(map[string]any); !ok {
		t.Fatal("full snapshot not served")
	}
	s.answerPlayerSuccessInit(session, sioHead{}, 31)
	profile, _ := s.st.Profile(revisionTestPlayerID)
	_, device, _ := session.identity()
	if got, pending := profile.PendingProgressRevision(device); pending || got != revision {
		t.Fatal("Login after correlated full sync did not acknowledge revision")
	}
}

func TestProgressRevisionAppliesInOneBootAndIsNotReplayed(t *testing.T) {
	s, session, revision := newOperatorEditedProgress(t)
	if got := s.answerProgressStatus(session, sioHead{}, 3)[sioProgressEnabledKey]; got != sioProgressOpen {
		t.Fatalf("download route = %v", got)
	}
	completeRevisionDownload(t, s, session, revision)
	if pending, served := session.pendingProgressState(); pending != 0 || served {
		t.Fatal("applied revision remained armed")
	}
	s.cfg.ProgressSynchronizeMode = false
	restart := revisionTestSession("restart")
	if got := s.answerProgressStatus(restart, sioHead{}, 43)[sioProgressEnabledKey]; got != sioProgressOpen {
		t.Fatalf("ordinary route = %v", got)
	}
	s.answerLastCommand(restart, sioHead{}, 44)
	if pending, _ := restart.pendingProgressState(); pending != 0 {
		t.Fatal("admin edit replayed")
	}
}

func TestProgressRevisionAcknowledgementIsPerDevice(t *testing.T) {
	s, first, revision := newOperatorEditedProgress(t)
	completeRevisionDownload(t, s, first, revision)
	second := authorisedSession("second-device", revisionTestPlayerID)
	second.setIdentity(revisionTestPlayerID, "another-device", revisionTestPlayerID)
	second.setProgressWriteRevision(revision)
	s.answerLastCommand(second, sioHead{}, 8)
	if pending, served := second.pendingProgressState(); pending != revision || served {
		t.Fatal("second device lost pending flag")
	}
	completeRevisionDownload(t, s, second, revision)
	profile, _ := s.st.Profile(revisionTestPlayerID)
	if _, pending := profile.PendingProgressRevision("device"); pending {
		t.Fatal("second device reset first acknowledgement")
	}
}

func TestLoginWithoutFullDownloadCannotAcknowledge(t *testing.T) {
	s, session, revision := newOperatorEditedProgress(t)
	s.answerLastCommand(session, sioHead{}, 8)
	s.answerPlayerSuccessInit(session, sioHead{}, 31)
	profile, _ := s.st.Profile(revisionTestPlayerID)
	if got, pending := profile.PendingProgressRevision("device"); !pending || got != revision {
		t.Fatal("Login alone cleared offline resync flag")
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

func TestAdminResyncMarkerIsPendingPerAuthenticatedDeviceOnly(t *testing.T) {
	s, session, revision := newOperatorEditedProgress(t)
	body := s.answerProgressStatus(session, sioHead{}, 3)
	if got := body["opg3d_admin_resync"]; got != revisionTestPlayerID+":"+strconv.FormatUint(revision, 10) {
		t.Fatalf("pending marker = %v", got)
	}
	foreign := authorisedSession("foreign-marker", "foreign-player")
	if _, err := s.st.EnsureProfile("foreign-player", "Other"); err != nil {
		t.Fatal(err)
	}
	if body := s.answerProgressStatus(foreign, sioHead{"player_id": revisionTestPlayerID}, 3); body["opg3d_admin_resync"] != nil {
		t.Fatal("marker leaked across players")
	}
	completeRevisionDownload(t, s, session, revision)
	if got := s.answerProgressStatus(session, sioHead{}, 4)["opg3d_admin_resync"]; got != nil {
		t.Fatal("ACKed revision still requests reload")
	}
	otherDevice := authorisedSession("other-device-marker", revisionTestPlayerID)
	otherDevice.setIdentity(revisionTestPlayerID, "another-device", revisionTestPlayerID)
	if got := s.answerProgressStatus(otherDevice, sioHead{}, 5)["opg3d_admin_resync"]; got != revisionTestPlayerID+":"+strconv.FormatUint(revision, 10) {
		t.Fatal("offline device lost reload flag")
	}
}

func TestOldAuthenticatedEpochCannotRearmAndAcknowledgeNewRevision(t *testing.T) {
	s, old, revision := newOperatorEditedProgress(t)
	completeRevisionDownload(t, s, old, revision)
	current, err := s.st.SetProfile(revisionTestPlayerID, "Veteran", 32, 501, 50, 60)
	if err != nil {
		t.Fatal(err)
	}
	s.answerLastCommand(old, sioHead{}, 8)
	s.answerSyncFull(old, sioHead{sioSlotsKey: map[string]any{}}, 17)
	s.answerPlayerSuccessInit(old, sioHead{}, 31)
	p, _ := s.st.Profile(revisionTestPlayerID)
	if got, pending := p.PendingProgressRevision("device"); !pending || got != current.ProgressRevision {
		t.Fatal("old auth acknowledged new revision")
	}
}
