// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// The identity values here are the ones a real 23.1.3 client presented on the
// encrypted /sio channel, so the tests exercise the id shape the game actually
// uses rather than a convenient invention.
const (
	socketUniqID = "350713954"
	socketDevice = "918ad658854434c89292b80c5e573229"
)

func TestAdoptSocketIdentityHonoursTheClientsOwnID(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	account, created, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, "192.168.0.100")
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if !created {
		t.Fatal("the first contact did not report creation")
	}
	// The client keeps using its own uniq_id no matter what this server would
	// prefer, so the account has to exist under that id or an operator would be
	// banning and restoring a different account than the game shows.
	if account.PlayerID != socketUniqID {
		t.Fatalf("player id is %q, want the client's own %q", account.PlayerID, socketUniqID)
	}
	if account.SocketDevice != socketDevice {
		t.Fatalf("socket device is %q, want %q", account.SocketDevice, socketDevice)
	}

	again, created, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, "192.168.0.100")
	if err != nil {
		t.Fatalf("re-adopt: %v", err)
	}
	if created {
		t.Fatal("reconnecting created a second account")
	}
	if again.PlayerID != account.PlayerID {
		t.Fatalf("reconnect moved the account from %q to %q", account.PlayerID, again.PlayerID)
	}

	// Adopting also has to leave a profile behind, or the first progress sync
	// would have nowhere to land.
	if _, err := s.Profile(socketUniqID); err != nil {
		t.Fatalf("profile after adopt: %v", err)
	}
}

func TestAdoptSocketIdentityRejectsAMalformedID(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, _, err := s.AdoptSocketIdentity("not-an-id", socketDevice, ""); !errors.Is(err, ErrBadPlayerID) {
		t.Fatalf("error is %v, want ErrBadPlayerID", err)
	}
	// A socket that presents neither an id nor a device cannot be tied to any
	// account, so it has to be refused rather than handed a fresh player.
	if _, _, err := s.AdoptSocketIdentity("", "", ""); err == nil {
		t.Fatal("a socket with no id and no device was accepted")
	}
}

// TestAdoptSocketIdentityRegistersAFirstRun covers the real first launch. The
// client sends uniq_id and hash empty and identifies itself by device
// fingerprint alone, so the server has to mint the id, and every later launch
// from the same handset has to come back to that same account instead of
// minting another one.
func TestAdoptSocketIdentityRegistersAFirstRun(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	account, created, err := s.AdoptSocketIdentity("", socketDevice, "192.0.2.10")
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if !created {
		t.Fatal("the first run did not create an account")
	}
	if !ValidPlayerID(account.PlayerID) {
		t.Fatalf("minted id %q is not a valid player id", account.PlayerID)
	}
	if account.SocketDevice != socketDevice {
		t.Fatalf("socket device is %q, want %q", account.SocketDevice, socketDevice)
	}

	again, created, err := s.AdoptSocketIdentity("", socketDevice, "192.0.2.10")
	if err != nil {
		t.Fatalf("second adopt: %v", err)
	}
	if created {
		t.Fatal("the same handset was registered twice")
	}
	if again.PlayerID != account.PlayerID {
		t.Fatalf("second launch became %s, want %s", again.PlayerID, account.PlayerID)
	}

	other, _, err := s.AdoptSocketIdentity("", "0123456789abcdef0123456789abcdef", "192.0.2.11")
	if err != nil {
		t.Fatalf("other adopt: %v", err)
	}
	if other.PlayerID == account.PlayerID {
		t.Fatal("a different device reused the first account")
	}
}

func TestChangePlayerIDCarriesTheProfileAndTheBan(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, _, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, ""); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if _, err := s.MergeProfileProgress(socketUniqID, map[string]any{"kills": float64(7)}); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := s.AddBan(Ban{Kind: BanPlayer, Subject: socketUniqID, Reason: "aimbot"}); err != nil {
		t.Fatalf("ban: %v", err)
	}

	const newID = "412345678"
	moved, err := s.ChangePlayerID(socketUniqID, newID)
	if err != nil {
		t.Fatalf("change: %v", err)
	}
	if moved.PlayerID != newID {
		t.Fatalf("account id is %q, want %q", moved.PlayerID, newID)
	}

	profile, err := s.Profile(newID)
	if err != nil {
		t.Fatalf("profile under the new id: %v", err)
	}
	if got := profile.Progress["kills"]; got != float64(7) {
		t.Fatalf("progress did not follow the id: kills=%v", got)
	}
	if _, err := s.Profile(socketUniqID); !errors.Is(err, ErrNotFound) {
		t.Fatal("a profile is still filed under the vacated id")
	}
	if _, err := s.Account(socketUniqID); !errors.Is(err, ErrNotFound) {
		t.Fatal("an account is still filed under the vacated id")
	}

	// SECURITY: the whole point of rewriting the ban. Without it, handing out a
	// new id would be a one-click way out of a ban, and handing out new ids is
	// exactly what this operation is for.
	if _, banned := s.MatchBan(newID, "", ""); !banned {
		t.Fatal("the ban did not follow the new id, so a rename evades it")
	}
	if _, banned := s.MatchBan(socketUniqID, "", ""); banned {
		t.Fatal("the ban stayed behind on the vacated id")
	}
}

func TestChangePlayerIDRefusesTakenAndMalformedIDs(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	const otherID = "165330406"
	if _, _, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, ""); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if _, _, err := s.AdoptSocketIdentity(otherID, "45fdd0566d816ff910dc99a9ea672134", ""); err != nil {
		t.Fatalf("adopt other: %v", err)
	}

	if _, err := s.ChangePlayerID(socketUniqID, otherID); !errors.Is(err, ErrIDTaken) {
		t.Fatalf("error is %v, want ErrIDTaken", err)
	}
	if _, err := s.ChangePlayerID(socketUniqID, "12"); !errors.Is(err, ErrBadPlayerID) {
		t.Fatalf("error is %v, want ErrBadPlayerID", err)
	}
	if _, err := s.ChangePlayerID(socketUniqID, socketUniqID); !errors.Is(err, ErrSameAccount) {
		t.Fatalf("error is %v, want ErrSameAccount", err)
	}
	if _, err := s.ChangePlayerID("999999999", "412345678"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error is %v, want ErrNotFound", err)
	}

	// A refused change must not have moved anything.
	if _, err := s.Account(socketUniqID); err != nil {
		t.Fatalf("the original account was disturbed by a refused change: %v", err)
	}
}

func TestTransferProgressRestoresOntoAnotherAccountOnce(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	const newID = "165330406"
	if _, _, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, ""); err != nil {
		t.Fatalf("adopt old: %v", err)
	}
	if _, _, err := s.AdoptSocketIdentity(newID, "45fdd0566d816ff910dc99a9ea672134", ""); err != nil {
		t.Fatalf("adopt new: %v", err)
	}
	if _, err := s.MergeProfileProgress(socketUniqID, map[string]any{"kills": float64(7)}); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if _, err := s.MergeProfilePrefs(socketUniqID, map[string]string{"skin": "veteran"}); err != nil {
		t.Fatalf("prefs: %v", err)
	}
	if _, err := s.SetProfile(socketUniqID, "Veteran", 42, 9001, 500, 25); err != nil {
		t.Fatalf("set profile: %v", err)
	}

	restored, err := s.TransferProgress(socketUniqID, newID)
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if restored.Level != 42 || restored.Coins != 500 || restored.Gems != 25 {
		t.Fatalf("restored profile is level=%d coins=%d gems=%d", restored.Level, restored.Coins, restored.Gems)
	}
	if got := restored.Progress["kills"]; got != float64(7) {
		t.Fatalf("progress did not arrive: kills=%v", got)
	}
	if got := restored.Prefs["skin"]; got != "veteran" {
		t.Fatalf("prefs did not arrive: skin=%q", got)
	}
	if restored.PlayerID != newID {
		t.Fatalf("restored profile is filed under %q, want %q", restored.PlayerID, newID)
	}
	if restored.TransferredFrom != socketUniqID {
		t.Fatalf("audit trail says the progress came from %q", restored.TransferredFrom)
	}

	// Both logins keep working: restoring is not deleting.
	if _, err := s.Account(socketUniqID); err != nil {
		t.Fatalf("the source account was removed: %v", err)
	}

	// The source is emptied so the same progress cannot be handed out twice by
	// repeating the call, and it records where it went.
	source, err := s.Profile(socketUniqID)
	if err != nil {
		t.Fatalf("source profile: %v", err)
	}
	if source.Level != 0 || source.Coins != 0 || len(source.Progress) != 0 || len(source.Prefs) != 0 {
		t.Fatalf("the source profile kept its progress: %+v", source)
	}
	if source.TransferredTo != newID {
		t.Fatalf("audit trail says the progress went to %q", source.TransferredTo)
	}

	if _, err := s.TransferProgress(socketUniqID, newID); err != nil {
		t.Fatalf("a repeated transfer errored instead of moving nothing: %v", err)
	}
	after, err := s.Profile(newID)
	if err != nil {
		t.Fatalf("target profile: %v", err)
	}
	if after.Level != 0 {
		t.Fatalf("a repeated transfer was expected to move the now-empty source, got level=%d", after.Level)
	}
}

func TestTransferProgressValidatesItsEnds(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, _, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, ""); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if _, err := s.TransferProgress(socketUniqID, socketUniqID); !errors.Is(err, ErrSameAccount) {
		t.Fatalf("error is %v, want ErrSameAccount", err)
	}
	if _, err := s.TransferProgress(socketUniqID, "999999999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error is %v, want ErrNotFound for a missing target", err)
	}
	if _, err := s.TransferProgress("999999999", socketUniqID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error is %v, want ErrNotFound for a missing source", err)
	}
}

func TestProfileReadsAreDeepCopies(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, _, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, ""); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if _, err := s.MergeProfileProgress(socketUniqID, map[string]any{
		"weapons": map[string]any{"sniper": float64(1)},
		"maps":    []any{"pool", "parkour"},
	}); err != nil {
		t.Fatalf("progress: %v", err)
	}

	profile, err := s.Profile(socketUniqID)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	// A caller holding an interior pointer could mutate stored state outside
	// the lock, so the nested client-owned data has to be copied too.
	profile.Progress["weapons"].(map[string]any)["sniper"] = float64(999)
	profile.Progress["maps"].([]any)[0] = "tampered"

	fresh, err := s.Profile(socketUniqID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got := fresh.Progress["weapons"].(map[string]any)["sniper"]; got != float64(1) {
		t.Fatalf("a nested map was shared with the caller: sniper=%v", got)
	}
	if got := fresh.Progress["maps"].([]any)[0]; got != "pool" {
		t.Fatalf("a nested slice was shared with the caller: maps[0]=%v", got)
	}
}

func TestStatsCountsRecentActivityAndActiveBans(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, _, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, ""); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if err := s.AddBan(Ban{Kind: BanPlayer, Subject: socketUniqID, Reason: "aimbot"}); err != nil {
		t.Fatalf("ban: %v", err)
	}
	expired := time.Now().UTC().Add(-time.Hour)
	if err := s.AddBan(Ban{Kind: BanIP, Subject: "192.168.0.100", Until: &expired}); err != nil {
		t.Fatalf("expired ban: %v", err)
	}

	stats := s.Stats()
	if stats.Accounts != 1 || stats.Profiles != 1 {
		t.Fatalf("accounts=%d profiles=%d, want 1 and 1", stats.Accounts, stats.Profiles)
	}
	if stats.Bans != 2 {
		t.Fatalf("bans=%d, want both recorded", stats.Bans)
	}
	if stats.ActiveBans != 1 {
		t.Fatalf("active bans=%d, want the expired one excluded", stats.ActiveBans)
	}
	if stats.NewToday != 1 || stats.SeenLastHour != 1 {
		t.Fatalf("new today=%d seen last hour=%d, want 1 and 1", stats.NewToday, stats.SeenLastHour)
	}
}

func TestProfilesSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, _, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, ""); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if _, err := s.SetProfile(socketUniqID, "Veteran", 42, 9001, 500, 25); err != nil {
		t.Fatalf("set profile: %v", err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	profile, err := reopened.Profile(socketUniqID)
	if err != nil {
		t.Fatalf("profile after reopen: %v", err)
	}
	if profile.Level != 42 || profile.Nick != "Veteran" {
		t.Fatalf("profile did not survive the reopen: %+v", profile)
	}
}

func TestProfileProjectsKnownCountersAndPreservesUnknownProgress(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, ""); err != nil {
		t.Fatal(err)
	}
	profile, err := s.MergeProfileProgress(socketUniqID, map[string]any{
		"level": 7, "exp": float64(91), "coins": int64(33), "gems": json.Number("4"),
		"future_field": map[string]any{"enabled": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Level != 7 || profile.Experience != 91 || profile.Coins != 33 || profile.Gems != 4 {
		t.Fatalf("counters=%+v", profile)
	}
	if _, ok := profile.Progress["future_field"].(map[string]any); !ok {
		t.Fatalf("unknown field lost: %#v", profile.Progress)
	}

	profile, err = s.MergeProfileProgressDocument(socketUniqID, map[string]any{
		"4": map[string]any{"l": float64(9), "e": float64(123), "future": "kept"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Level != 9 || profile.Experience != 123 {
		t.Fatalf("document counters=%+v", profile)
	}
	if profile.ProgressDocument["4"].(map[string]any)["future"] != "kept" {
		t.Fatal("document field was lost")
	}
}

func TestSetProfileSynchronizesClientFacingProgress(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MergeProfileProgressDocument(socketUniqID, map[string]any{"4": map[string]any{"l": 1, "e": 0, "future": true}}); err != nil {
		t.Fatal(err)
	}
	profile, err := s.SetProfile(socketUniqID, "Veteran", 12, 345, 67, 8)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int64{"level": 12, "experience": 345, "coins": 67, "gems": 8} {
		got, ok := counterInt64(profile.Progress[key])
		if !ok || got != want {
			t.Fatalf("progress[%s]=%#v, want %d", key, profile.Progress[key], want)
		}
	}
	section := profile.ProgressDocument["4"].(map[string]any)
	if level, _ := counterInt64(section["l"]); level != 12 {
		t.Fatalf("section level=%#v", section["l"])
	}
	if experience, _ := counterInt64(section["e"]); experience != 345 {
		t.Fatalf("section experience=%#v", section["e"])
	}
	if section["future"] != true {
		t.Fatal("set profile dropped an unknown document member")
	}
	account, err := s.Account(socketUniqID)
	if err != nil {
		t.Fatal(err)
	}
	if account.Nick != "Veteran" {
		t.Fatalf("account nick=%q, want profile nick", account.Nick)
	}
}

func TestClientStateIsDeepCopiedAndSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, ""); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"unknown": map[string]any{"items": []any{"original", float64(2)}},
		"w":       map[string]any{"pw": float64(10001)},
	}
	stored, err := s.MergeProfileClientState(socketUniqID, input)
	if err != nil {
		t.Fatal(err)
	}
	input["unknown"].(map[string]any)["items"].([]any)[0] = "input-mutated"
	stored.ClientState["w"].(map[string]any)["pw"] = float64(999)

	fresh, err := s.Profile(socketUniqID)
	if err != nil {
		t.Fatal(err)
	}
	if got := fresh.ClientState["unknown"].(map[string]any)["items"].([]any)[0]; got != "original" {
		t.Fatalf("stored client state shared input memory: %#v", got)
	}
	if got := fresh.ClientState["w"].(map[string]any)["pw"]; got != float64(10001) {
		t.Fatalf("returned client state shared store memory: %#v", got)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.Profile(socketUniqID)
	if err != nil {
		t.Fatal(err)
	}
	if got := persisted.ClientState["unknown"].(map[string]any)["items"].([]any)[0]; got != "original" {
		t.Fatalf("client state did not survive reopen: %#v", persisted.ClientState)
	}
}

func TestMergeClientStateProjectsCountersAndNick(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, ""); err != nil {
		t.Fatal(err)
	}
	profile, err := s.MergeProfileClientState(socketUniqID, map[string]any{
		"nick": "Socket Nick", "level": json.Number("8"), "experience": float64(44),
		"coins": int64(12), "gems": uint8(3), "future": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Nick != "Socket Nick" || profile.Level != 8 || profile.Experience != 44 || profile.Coins != 12 || profile.Gems != 3 {
		t.Fatalf("profile projection = %#v", profile)
	}
	if profile.ClientState["future"] != true {
		t.Fatal("unknown client-state field was dropped")
	}
}

func TestReplaceRawStateClearsStaleProjectedCounters(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AdoptSocketIdentity(socketUniqID, socketDevice, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProfile(socketUniqID, "", 10, 200, 30, 4); err != nil {
		t.Fatal(err)
	}
	profile, err := s.ReplaceProfileRawState(socketUniqID,
		map[string]string{}, map[string]any{}, map[string]any{}, map[string]any{},
		map[string]any{"coins": -500, "future": "kept"})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Level != 0 || profile.Experience != 0 || profile.Coins != 0 || profile.Gems != 0 {
		t.Fatalf("complete raw replacement left stale counters: %#v", profile)
	}
	if profile.ClientState["coins"] != -500 || profile.ClientState["future"] != "kept" {
		t.Fatalf("invalid/unknown raw values were not preserved for support: %#v", profile.ClientState)
	}
}
