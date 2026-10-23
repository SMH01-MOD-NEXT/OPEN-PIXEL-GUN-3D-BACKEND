// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEvidenceBoundsProvenanceRestartAndOwnership(t *testing.T) {
	s, dir, _ := passSetup(t)
	a, _ := s.Account(socketUniqID)
	for n := 0; n < 30; n++ {
		if e := s.ObserveDevice(a, DeviceObservation{IP: fmt.Sprintf("192.0.2.%d", n), Model: "Model\n", Source: "authenticated_game_client"}); e != nil {
			t.Fatal(e)
		}
	}
	before := s.Evidence(a.PlayerID)
	if len(before.Observations) != 24 || before.Observations[0].Model != "Model" {
		t.Fatal("bounds/sanitize")
	}
	last := before.Observations[23]
	if e := s.ObserveDevice(a, last); e != nil || len(s.Evidence(a.PlayerID).Observations) != 24 {
		t.Fatal("dedup")
	}
	copy := s.Evidence(a.PlayerID)
	copy.Observations[0].Model = "changed"
	if s.Evidence(a.PlayerID).Observations[0].Model == "changed" {
		t.Fatal("alias")
	}
	bad := a
	bad.DeviceID = "other"
	if s.ObserveDevice(bad, last) == nil {
		t.Fatal("ownership")
	}
	s, e := Open(dir)
	if e != nil || len(s.Evidence(a.PlayerID).Observations) != 24 {
		t.Fatal("restart", e)
	}
	blocked := filepath.Join(t.TempDir(), "block")
	os.WriteFile(blocked, []byte("x"), 0600)
	s.path = filepath.Join(blocked, "state.json")
	if s.ObserveDevice(a, DeviceObservation{Model: "must not save"}) == nil {
		t.Fatal("write failure")
	}
	if s.Evidence(a.PlayerID).Observations[23].Model == "must not save" {
		t.Fatal("partial write")
	}
}
func TestWalletEvidenceOnlyActualDeltasBounded(t *testing.T) {
	before := Profile{Coins: 200, Gems: 100}
	after := before.clone()
	recordWalletEvent(before, &after, "accepted_game_commands", 0, time.Now())
	if len(after.WalletHistory) != 0 {
		t.Fatal("noop history")
	}
	for n := 0; n < 110; n++ {
		before = after.clone()
		after.Coins--
		recordWalletEvent(before, &after, "accepted_game_commands", 0, time.Now())
	}
	if len(after.WalletHistory) != 100 || after.WalletHistory[0].Delta["Coins"] != -1 {
		t.Fatal("delta/cap")
	}
	copy := after.clone()
	copy.WalletHistory[0].Delta["Coins"] = 123
	if after.WalletHistory[0].Delta["Coins"] == 123 {
		t.Fatal("clone alias")
	}
}
