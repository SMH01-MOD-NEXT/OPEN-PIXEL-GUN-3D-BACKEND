// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"fmt"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/exchange"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// Captured from stock 23.1.3: unlimited offers omit 332 entirely.
func exchangeBatch(o exchange.Offer, suffix string) []any {
	return []any{
		counterCommand(66, "bonus-"+suffix, map[string]any{"c": o.Pay + "_1", "v": 0, "ca": 145}),
		counterCommand(66, "pay-"+suffix, map[string]any{"c": o.Pay, "v": o.Cost, "ca": 145}),
		counterCommand(10, "reward-"+suffix, map[string]any{"c": o.Receive, "v": o.Amount, "ca": 107}),
	}
}
func TestCurrencyExchangeNativeRatesRestartAndRetry(t *testing.T) {
	s, dir, ctx := passSetup(t)
	for n, o := range exchange.Offers() {
		before, _ := s.Profile(socketUniqID)
		batch := exchangeBatch(o, fmt.Sprint(n))
		passApply(t, s, ctx, batch...)
		after, _ := s.Profile(socketUniqID)
		for _, name := range []string{"Coins", "GemsCurrency", "PixelPassCurrency", "BattlePassCurrency"} {
			want := ppInt(objectAt(before.ProgressSlots, "3"), name)
			if name == o.Pay {
				want -= o.Cost
			}
			if name == o.Receive {
				want += o.Amount
			}
			for _, doc := range []map[string]any{after.ProgressSlots, after.ProgressDocument} {
				if got := ppInt(objectAt(doc, "3"), name); got != want {
					t.Fatal(o.ID, name, got, want)
				}
			}
		}
		var err error
		s, err = Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, batch, passNow); n != 0 || err != nil {
			t.Fatal("durable retry", n, err)
		}
		replay, _ := s.Profile(socketUniqID)
		for _, name := range []string{"Coins", "GemsCurrency", "PixelPassCurrency", "BattlePassCurrency"} {
			if ppInt(objectAt(after.ProgressSlots, "3"), name) != ppInt(objectAt(replay.ProgressSlots, "3"), name) {
				t.Fatal("replay changed wallet", name)
			}
		}
	}
}
func TestCurrencyExchangeRepeatIsNotFiniteOffer(t *testing.T) {
	s, _, ctx := passSetup(t)
	o := exchange.Offers()[0]
	before, _ := s.Profile(socketUniqID)
	for n := 0; n < 3; n++ {
		passApply(t, s, ctx, exchangeBatch(o, fmt.Sprint(n))...)
	}
	after, _ := s.Profile(socketUniqID)
	if after.Gems != before.Gems-3*o.Cost || ppInt(objectAt(after.ProgressSlots, "3"), o.Receive) != ppInt(objectAt(before.ProgressSlots, "3"), o.Receive)+3*o.Amount {
		t.Fatal("repeat")
	}
	if !reflect.DeepEqual(before.PixelPass, after.PixelPass) {
		t.Fatal("shop changed pass progress")
	}
}
func TestCurrencyExchangeInvalidIsAtomic(t *testing.T) {
	for _, kind := range []string{"funds", "wrong-price", "wrong-reward", "missing-debit", "missing-reward", "negative", "overflow", "missing-ci"} {
		t.Run(kind, func(t *testing.T) {
			s, _, ctx := passSetup(t)
			o := exchange.Offers()[0]
			batch := exchangeBatch(o, kind)
			switch kind {
			case "funds":
				s.state.Profiles[socketUniqID].ProgressSlots["3"].(map[string]any)[o.Pay] = int64(0)
			case "wrong-price":
				batch[1].(map[string]any)["p"].(map[string]any)["v"] = 1
			case "wrong-reward":
				batch[2].(map[string]any)["p"].(map[string]any)["v"] = 99999
			case "missing-debit":
				batch = append(batch[:1], batch[2])
			case "missing-reward":
				batch = batch[:2]
			case "negative":
				batch[1].(map[string]any)["p"].(map[string]any)["v"] = -100
			case "overflow":
				s.state.Profiles[socketUniqID].ProgressSlots["3"].(map[string]any)[o.Receive] = int64(2147483647)
			case "missing-ci":
				delete(batch[1].(map[string]any), "ci")
			}
			before, _ := s.Profile(socketUniqID)
			if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, batch, passNow); err == nil {
				t.Fatal("accepted")
			}
			after, _ := s.Profile(socketUniqID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("partial mutation")
			}
		})
	}
}
func TestCurrencyExchangeConcurrentReplayAndPersistenceFailure(t *testing.T) {
	s, _, ctx := passSetup(t)
	o := exchange.Offers()[0]
	batch := exchangeBatch(o, "same")
	before, _ := s.Profile(socketUniqID)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, batch, passNow); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	after, _ := s.Profile(socketUniqID)
	if after.Gems != before.Gems-o.Cost {
		t.Fatal("double debit")
	}
	s.path = filepath.Join(t.TempDir(), "missing", "state.json")
	before, _ = s.Profile(socketUniqID)
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, exchangeBatch(o, "write-fail"), passNow); err == nil {
		t.Fatal("expected write failure")
	}
	after, _ = s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed write mutated profile")
	}
}

func TestCurrencyExchangeCoexistsWithPassReward(t *testing.T) {
	s, _, ctx := passSetup(t)
	before, _ := s.Profile(socketUniqID)
	batch := []any{counterCommand(10, "pass-echo", map[string]any{"c": "Coins", "v": 55, "ca": 107}), claimCell(1, "free-cell")}
	batch = append(batch, exchangeBatch(exchange.Offers()[0], "mixed")...)
	passApply(t, s, ctx, batch...)
	after, _ := s.Profile(socketUniqID)
	if after.Coins != before.Coins+55 || after.Gems != before.Gems-100 {
		t.Fatal("mixed pass/exchange settlement")
	}
}
func TestCurrencyExchangeTwoPurchasesSameBatch(t *testing.T) {
	s, _, ctx := passSetup(t)
	before, _ := s.Profile(socketUniqID)
	batch := append(exchangeBatch(exchange.Offers()[0], "a"), exchangeBatch(exchange.Offers()[4], "b")...)
	passApply(t, s, ctx, batch...)
	after, _ := s.Profile(socketUniqID)
	if after.Coins != before.Coins+1000 || after.Gems != before.Gems-200 {
		t.Fatal("multi-purchase settlement")
	}
}
