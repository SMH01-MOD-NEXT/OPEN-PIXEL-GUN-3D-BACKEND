// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/pixelpass"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPixelPassPaidProgressConflictReloadAndClaim(t *testing.T) {
	s, dir, ctx := passSetup(t)
	passApply(t, s, ctx, counterCommand(326, "match-initial", map[string]any{"e": 20}))
	var paid []any
	for _, ci := range []string{"paid-a", "paid-b", "paid-c", "paid-d"} {
		paid = append(paid, counterCommand(66, ci+"debit", map[string]any{"c": pixelpass.Currency, "v": 20}), counterCommand(326, ci+"crowns", map[string]any{"e": 100}))
	}
	paid = append(paid, counterCommand(66, "pack-debit", map[string]any{"c": pixelpass.Currency, "v": 1200}), counterCommand(326, "pack-crowns", map[string]any{"e": 1000}), counterCommand(324, "pack-premium", map[string]any{}))
	passApply(t, s, ctx, paid...)
	for _, ci := range []string{"match1", "match2", "match3", "match4", "match5"} {
		passApply(t, s, ctx, counterCommand(326, ci, map[string]any{"e": 20}))
	}
	before, _ := s.Profile(socketUniqID)
	if ppInt(before.PixelPass.State, "e") != 1520 || ppInt(before.PixelPass.State, "p") != 1 {
		t.Fatal("paid progression contract", before.PixelPass.State)
	}
	// Reproduces the observed native level17/135-coins claim: client ahead by120.
	claim := []any{counterCommand(10, "level17-echo", map[string]any{"c": "Coins", "v": 135, "ca": 103}), claimCell(17, "level17-claim")}
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, claim, passNow); !errors.Is(err, ErrPixelPassStateConflict) {
		t.Fatal("expected typed conflict", err)
	}
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected claim partially granted")
	}
	pending, err := s.RequestProfileProgressResync(socketUniqID, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending.ProgressRevision != before.ProgressRevision+1 || !reflect.DeepEqual(before.PixelPass, pending.PixelPass) || !reflect.DeepEqual(before.ProgressCommandReceipts, pending.ProgressCommandReceipts) || before.Coins != pending.Coins || before.Gems != pending.Gems || !reflect.DeepEqual(before.ProgressSlots, pending.ProgressSlots) {
		t.Fatal("reload edited gameplay/receipts")
	}
	again, err := s.RequestProfileProgressResync(socketUniqID, ctx)
	if err != nil || again.ProgressRevision != pending.ProgressRevision {
		t.Fatal("resync loop", err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.Profile(socketUniqID)
	if _, waiting := p.PendingProgressRevision(ctx.Device); !waiting {
		t.Fatal("pending reload lost on restart")
	}
	if ok, err := s.AcknowledgeProfileProgressRevision(socketUniqID, ctx.Device, p.ProgressRevision); err != nil || !ok {
		t.Fatal(err)
	}
	ctx.Revision = p.ProgressRevision
	passApply(t, s, ctx, paid...) // Paid retries after reload/restart must not charge again.
	p, _ = s.Profile(socketUniqID)
	beforeJSON, _ := json.Marshal(before.PixelPass)
	afterJSON, _ := json.Marshal(p.PixelPass)
	if !bytes.Equal(beforeJSON, afterJSON) || ppInt(objectAt(before.ProgressSlots, "3"), pixelpass.Currency) != ppInt(objectAt(p.ProgressSlots, "3"), pixelpass.Currency) {
		t.Fatal("reload recharged purchases")
	}
	// Buy the actual missing level, then catalog-valid reward grants exactly once.
	passApply(t, s, ctx, counterCommand(66, "next-level-pay", map[string]any{"c": pixelpass.Currency, "v": 20}), counterCommand(326, "next-level-exp", map[string]any{"e": 100}))
	passApply(t, s, ctx, claim...)
	passApply(t, s, ctx, claim...)
	p, _ = s.Profile(socketUniqID)
	if p.Coins != before.Coins+135 || ppInt(p.PixelPass.State, "e") != 1620 || ppInt(p.PixelPass.State, "p") != 1 {
		t.Fatal("canonical claim failed or duplicated")
	}
	if _, err = s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{counterCommand(10, "level18-echo", map[string]any{"c": "GemsCurrency", "v": 14, "ca": 103}), claimCell(18, "level18-claim")}, passNow); !errors.Is(err, ErrPixelPassStateConflict) {
		t.Fatal("level18 eligibility relaxed", err)
	}
}

func TestProgressResyncFailureAndForgedClaimPreserveState(t *testing.T) {
	s, dir, ctx := passSetup(t)
	before, _ := s.Profile(socketUniqID)
	forged := counterCommand(323, "forged", map[string]any{"l": 5, "r": []any{"26015:9999"}})
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{forged}, passNow); err == nil || errors.Is(err, ErrPixelPassStateConflict) {
		t.Fatal("forgery treated as recoverable", err)
	}
	path := s.path
	s.path = filepath.Join(dir, "missing", "state.json")
	if _, err := s.RequestProfileProgressResync(socketUniqID, ctx); err == nil {
		t.Fatal("persistence succeeded unexpectedly")
	}
	s.path = path
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed reload persisted in memory")
	}
	if _, err := s.RequestProfileProgressResync(socketUniqID, ProgressWriteContext{Device: ctx.Device, Revision: ctx.Revision + 1}); !errors.Is(err, ErrStaleProgressRevision) {
		t.Fatal("stale caller advanced revision", err)
	}
}
