// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"errors"
	"testing"
	"time"
)

func mustOpenIdentityStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestReconcileSocketIdentityMergesBootstrapAndGameAccounts(t *testing.T) {
	st := mustOpenIdentityStore(t)
	bootstrap, _, err := st.RegisterOrLogin("native-device", "Bootstrap", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	game, _, err := st.AdoptSocketIdentity("612672163", "socket-device", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeProfileProgress(game.PlayerID, map[string]any{"level": 7, "coins": 33}); err != nil {
		t.Fatal(err)
	}

	result, err := st.AdoptSocketIdentityForAccount(bootstrap.PlayerID, game.PlayerID, "socket-device", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Account.PlayerID != game.PlayerID {
		t.Fatalf("result = %#v", result)
	}
	if got := len(st.Accounts()); got != 1 {
		t.Fatalf("accounts = %d, want 1", got)
	}
	if result.Account.DeviceID != "native-device" || result.Account.SocketDevice != "socket-device" {
		t.Fatalf("devices not preserved: %#v", result.Account)
	}
	if result.Account.Session != bootstrap.Session {
		t.Fatal("bootstrap session was not preserved")
	}
	canonical, ok := st.ResolvePlayerID(bootstrap.PlayerID)
	if !ok || canonical != game.PlayerID {
		t.Fatalf("alias = %q %v", canonical, ok)
	}
	if account, ok := st.Authenticate(bootstrap.PlayerID, bootstrap.Session, ""); !ok || account.PlayerID != game.PlayerID {
		t.Fatalf("alias auth = %#v %v", account, ok)
	}
	profile, err := st.Profile(game.PlayerID)
	if err != nil || profile.Level != 7 || profile.Coins != 33 {
		t.Fatalf("profile = %#v, %v", profile, err)
	}
}

func TestReconcileSocketIdentityReusesBootstrapWhenUniqIDEmpty(t *testing.T) {
	st := mustOpenIdentityStore(t)
	bootstrap, _, err := st.RegisterOrLogin("native-device", "Bootstrap", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	result, err := st.AdoptSocketIdentityForAccount(bootstrap.PlayerID, "", "socket-device", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || result.Account.PlayerID != bootstrap.PlayerID {
		t.Fatalf("result = %#v", result)
	}
	if got := len(st.Accounts()); got != 1 {
		t.Fatalf("accounts = %d, want 1", got)
	}
	if result.Account.SocketDevice != "socket-device" {
		t.Fatalf("socket device = %q", result.Account.SocketDevice)
	}
}

func TestReconcileSocketIdentityRejectsTwoPopulatedProfiles(t *testing.T) {
	st := mustOpenIdentityStore(t)
	bootstrap, _, err := st.RegisterOrLogin("native-device", "Bootstrap", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeProfileProgress(bootstrap.PlayerID, map[string]any{"coins": 1}); err != nil {
		t.Fatal(err)
	}
	game, _, err := st.AdoptSocketIdentity("612672163", "socket-device", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeProfileProgress(game.PlayerID, map[string]any{"coins": 2}); err != nil {
		t.Fatal(err)
	}
	_, err = st.AdoptSocketIdentityForAccount(bootstrap.PlayerID, game.PlayerID, "socket-device", "10.0.0.1")
	if !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("err = %v", err)
	}
	if got := len(st.Accounts()); got != 2 {
		t.Fatalf("accounts = %d, want 2", got)
	}
}

func TestReconcileAuthenticatedIdentityRejectsWrongSession(t *testing.T) {
	st := mustOpenIdentityStore(t)
	bootstrap, _, err := st.RegisterOrLogin("native-device", "Bootstrap", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.ReconcileAuthenticatedIdentity(bootstrap.PlayerID, "wrong", "612672163", "socket-device", "10.0.0.1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestReconcileRecentHTTPIdentityClaimsOnlyCandidate(t *testing.T) {
	st := mustOpenIdentityStore(t)
	bootstrap, _, err := st.RegisterOrLogin("native-device", "Bootstrap", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	result, candidates, err := st.ReconcileRecentHTTPIdentity("612672163", "socket-device", "10.0.0.1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if candidates != 1 || result.Account.PlayerID != "612672163" || !result.Changed {
		t.Fatalf("result=%#v candidates=%d", result, candidates)
	}
	if got := len(st.Accounts()); got != 1 {
		t.Fatalf("accounts=%d, want 1", got)
	}
	if canonical, ok := st.ResolvePlayerID(bootstrap.PlayerID); !ok || canonical != "612672163" {
		t.Fatalf("alias=%q %v", canonical, ok)
	}
}

func TestReconcileRecentHTTPIdentityReusesBootstrapOnFirstRun(t *testing.T) {
	st := mustOpenIdentityStore(t)
	bootstrap, _, err := st.RegisterOrLogin("native-device", "Bootstrap", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	result, candidates, err := st.ReconcileRecentHTTPIdentity("", "socket-device", "10.0.0.1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if candidates != 1 || result.Account.PlayerID != bootstrap.PlayerID || result.Changed {
		t.Fatalf("result=%#v candidates=%d", result, candidates)
	}
	if result.Account.SocketDevice != "socket-device" {
		t.Fatalf("socket device=%q", result.Account.SocketDevice)
	}
}

func TestReconcileRecentHTTPIdentitySkipsAmbiguousAndStaleCandidates(t *testing.T) {
	st := mustOpenIdentityStore(t)
	if _, _, err := st.RegisterOrLogin("native-one", "One", "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RegisterOrLogin("native-two", "Two", "10.0.0.1"); err != nil {
		t.Fatal(err)
	}

	if result, candidates, err := st.ReconcileRecentHTTPIdentity("612672163", "socket-device", "10.0.0.1", time.Minute); err != nil || candidates != 2 || result.Account.PlayerID != "" {
		t.Fatalf("ambiguous result=%#v candidates=%d err=%v", result, candidates, err)
	}
	if got := len(st.Accounts()); got != 2 {
		t.Fatalf("ambiguous match changed accounts: %d", got)
	}
	if result, candidates, err := st.ReconcileRecentHTTPIdentity("612672163", "socket-device", "10.0.0.1", time.Nanosecond); err != nil || candidates != 0 || result.Account.PlayerID != "" {
		t.Fatalf("stale result=%#v candidates=%d err=%v", result, candidates, err)
	}
}

func TestReconciledAliasWorksForBanAndDeletePaths(t *testing.T) {
	st := mustOpenIdentityStore(t)
	source, _, err := st.RegisterOrLogin("native-device", "Bootstrap", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := st.AdoptSocketIdentity("612672163", "socket-device", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddBan(Ban{Kind: BanPlayer, Subject: source.PlayerID, Reason: "support test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReconcileAccounts(source.PlayerID, target.PlayerID); err != nil {
		t.Fatal(err)
	}
	if ban, ok := st.MatchBan(source.PlayerID, "", ""); !ok || ban.Subject != target.PlayerID {
		t.Fatalf("alias did not reach rewritten ban: %#v %v", ban, ok)
	}
	if ban, ok := st.MatchBan(target.PlayerID, "", ""); !ok || ban.Subject != target.PlayerID {
		t.Fatalf("canonical id did not keep ban: %#v %v", ban, ok)
	}
	if err := st.DeleteAccount(source.PlayerID); err != nil {
		t.Fatalf("delete by alias: %v", err)
	}
	if _, ok := st.ResolvePlayerID(source.PlayerID); ok {
		t.Fatal("deleting canonical account left retired alias live")
	}
	if _, err := st.Account(target.PlayerID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("canonical account survived delete by alias: %v", err)
	}
}

func TestPlayerBanOperationsResolveRetiredAlias(t *testing.T) {
	st := mustOpenIdentityStore(t)
	source, _, err := st.RegisterOrLogin("native-device", "Bootstrap", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := st.AdoptSocketIdentity("612672163", "socket-device", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReconcileAccounts(source.PlayerID, target.PlayerID); err != nil {
		t.Fatal(err)
	}
	if err := st.AddBan(Ban{Kind: BanPlayer, Subject: source.PlayerID, Reason: "alias test"}); err != nil {
		t.Fatal(err)
	}
	if ban, ok := st.MatchBan(target.PlayerID, "", ""); !ok || ban.Subject != target.PlayerID {
		t.Fatalf("ban filed by alias did not match canonical account: %#v %v", ban, ok)
	}
	removed, err := st.RemoveBans(BanPlayer, source.PlayerID)
	if err != nil || removed != 1 {
		t.Fatalf("remove by alias: removed=%d err=%v", removed, err)
	}
	if _, ok := st.MatchBan(target.PlayerID, "", ""); ok {
		t.Fatal("ban removed through alias still matches canonical account")
	}
}
