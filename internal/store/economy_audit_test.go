// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestEconomyGradualUnknownGrantRemainsCompatibleAndReplaySurvivesRestart(t *testing.T) {
	s, dir, ctx := syncedGameplayStore(t)
	var events []EconomyObservation
	// Re-enter the store: the observer MUST run outside the gameplay lock.
	sink := func(e EconomyObservation) {
		if _, err := s.Profile(e.Player); err != nil {
			t.Error(err)
		}
		events = append(events, e)
	}
	s.SetEconomyObserver(sink)
	batch := []any{counterCommand(10, "unverified", map[string]any{"c": "Coins", "v": 42, "arbitrary": "secret-not-for-journal"})}
	if n, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, batch); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if len(events) != 1 || events[0].Unverified != 1 || events[0].Verified != 0 || events[0].Outcome != "accepted" {
		t.Fatal(events)
	}
	p, _ := s.Profile(socketUniqID)
	if p.Coins != 1000041 {
		t.Fatal("gradual mode blocked unverified grant")
	}
	var err error
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetEconomyObserver(sink)
	if n, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, batch); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	e := events[len(events)-1]
	if e.Replayed != 1 || e.Unverified != 0 {
		t.Fatal(e)
	}
	p, _ = s.Profile(socketUniqID)
	if p.Coins != 1000041 {
		t.Fatal("replayed grant")
	}
	// Changing ci is NOT economic proof; deliberately still allowed in gradual mode.
	if _, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, []any{counterCommand(10, "changed-ci", map[string]any{"c": "Coins", "v": 42})}); err != nil {
		t.Fatal(err)
	}
	p, _ = s.Profile(socketUniqID)
	if p.Coins != 1000083 {
		t.Fatal("unknown path silently enforced")
	}
}

func TestEconomyEverySnapshotChannelObservedWithoutRejectingUnknownFields(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*Store, ProgressWriteContext) (Profile, error)
	}{
		{"prefs", func(s *Store, c ProgressWriteContext) (Profile, error) {
			return s.MergeProfilePrefs(socketUniqID, map[string]string{"opaque": "secret-client-text"}, c)
		}},
		{"progress", func(s *Store, c ProgressWriteContext) (Profile, error) {
			return s.MergeProfileProgress(socketUniqID, map[string]any{"coins": 123}, c)
		}},
		{"document", func(s *Store, c ProgressWriteContext) (Profile, error) {
			return s.MergeProfileProgressDocument(socketUniqID, map[string]any{"28": map[string]any{"unknown": "secret-client-text"}}, c)
		}},
		{"slots", func(s *Store, c ProgressWriteContext) (Profile, error) {
			return s.MergeProfileProgressSlots(socketUniqID, map[string]any{"91": map[string]any{"opaque": "secret-client-text"}}, c)
		}},
		{"replace_slots", func(s *Store, c ProgressWriteContext) (Profile, error) {
			p, _ := s.Profile(socketUniqID)
			p.ProgressSlots["3"] = map[string]any{"Coins": 123, "GemsCurrency": 456}
			return s.ReplaceProfileProgressSlots(socketUniqID, p.ProgressSlots, c)
		}},
		{"client_state", func(s *Store, c ProgressWriteContext) (Profile, error) {
			return s.MergeProfileClientState(socketUniqID, map[string]any{"coins": 123, "opaque": "secret-client-text"}, c)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _, ctx := syncedGameplayStore(t)
			var events []EconomyObservation
			s.SetEconomyObserver(func(e EconomyObservation) { events = append(events, e) })
			if _, err := tc.apply(s, ctx); err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].Channel != tc.name || events[0].Outcome != "observed" || events[0].Rule != "unverified_snapshot" {
				t.Fatal(events)
			}
			switch tc.name {
			case "progress", "replace_slots", "client_state":
				if !events[0].CountersChanged {
					t.Fatal("counter upload not observed")
				}
			case "document":
				if events[0].ChangedSlots != 1 {
					t.Fatal("inventory upload not observed")
				}
			}
		})
	}
}

func TestEconomyObserverExcludesTrustedAdminAndUnchangedSnapshots(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	n := 0
	s.SetEconomyObserver(func(EconomyObservation) { n++ })
	if _, err := s.MergeProfilePrefs(socketUniqID, map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MergeProfilePrefs(socketUniqID, map[string]string{"a": "b"}, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProfile(socketUniqID, "trusted", 65, 500, 999999, 999999); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("trusted/no-change write flagged", n)
	}
}

func TestEconomyVerifiedMailboxAndForgedClaimAreDistinguished(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	news := rewardNews(t, s, nil)
	var events []EconomyObservation
	s.SetEconomyObserver(func(e EconomyObservation) { events = append(events, e) })
	batch := mailClaim(news, "valid")
	if _, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, batch); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Verified != 4 || events[0].Unverified != 0 {
		t.Fatal(events)
	}
	before, _ := s.Profile(socketUniqID)
	bad := []any{counterCommand(10, "forged", map[string]any{"c": "Coins", "v": 99999, "ca": 62})}
	if _, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, bad); !errors.Is(err, ErrMailboxClaim) {
		t.Fatal(err)
	}
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("forged claim partially applied")
	}
	e := events[len(events)-1]
	if e.Outcome != "rejected" || e.Rule != "mailbox" {
		t.Fatal(e)
	}
}

func TestEconomyRejectedPersistenceAndConcurrentSinkChanges(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	var events []EconomyObservation
	s.SetEconomyObserver(func(e EconomyObservation) { events = append(events, e) })
	before, _ := s.Profile(socketUniqID)
	s.path = filepath.Join(t.TempDir(), "missing", "state.json")
	if _, err := s.MergeProfileProgress(socketUniqID, map[string]any{"coins": 123}, ctx); err == nil {
		t.Fatal("expected persistence failure")
	}
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed snapshot changed state")
	}
	if len(events) != 1 || events[0].Outcome != "rejected" || events[0].Rule != "validation_or_storage" {
		t.Fatal(events)
	}
	s.SetEconomyObserver(nil)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.SetEconomyObserver(func(e EconomyObservation) { _, _ = s.Profile(e.Player) })
			_, _ = s.MergeProfileClientState(socketUniqID, map[string]any{"coins": 321}, ctx)
		}()
	}
	wg.Wait()
}

func TestEconomySnapshotComparisonNormalizesJSONNumbersAndDeletions(t *testing.T) {
	p := &Profile{PlayerID: "test"}
	before := map[string]any{"28": map[string]any{"i": 1}, "91": true}
	if e := snapshotObservation(p, "slots", before, map[string]any{"28": map[string]any{"i": float64(1)}}, false); e != nil {
		t.Fatal("same numeric snapshot flagged", e)
	}
	e := snapshotObservation(p, "replace_slots", before, map[string]any{}, true)
	if e == nil || e.ChangedSlots != 1 || !e.OpaqueChanged {
		t.Fatal("deletions missed", e)
	}
}
