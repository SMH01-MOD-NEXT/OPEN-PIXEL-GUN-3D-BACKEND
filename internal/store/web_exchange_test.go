// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"encoding/json"
	"fmt"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/exchange"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func webReq(p Profile, id int, n int) WebExchangeRequest {
	return WebExchangeRequest{OfferID: id, Operation: fmt.Sprintf("%032x", n), CatalogVersion: ExchangeCatalogVersion(), Revision: p.ProgressRevision, UpdatedAt: p.UpdatedAt}
}
func TestWebExchangeRatesAtomicRetryRestartAndReceiptFence(t *testing.T) {
	s, dir, ctx := passSetup(t)
	// A game command receipt must survive a subsequent external wallet write.
	batch := exchangeBatch(exchange.Offers()[0], "before-web")
	passApply(t, s, ctx, batch...)
	for n, o := range exchange.Offers() {
		before, _ := s.Profile(socketUniqID)
		request := webReq(before, o.ID, n+1)
		p, applied, e := s.webExchange(socketUniqID, request, nil, "", passNow)
		if e != nil || !applied {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(before.ProgressCommandReceipts, p.ProgressCommandReceipts) {
			t.Fatal("erased gameplay receipts")
		}
		for _, key := range []string{"Coins", "GemsCurrency", "PixelPassCurrency", "BattlePassCurrency"} {
			want := ppInt(objectAt(before.ProgressSlots, "3"), key)
			if key == o.Pay {
				want -= o.Cost
			}
			if key == o.Receive {
				want += o.Amount
			}
			if ppInt(objectAt(p.ProgressSlots, "3"), key) != want || ppInt(objectAt(p.ProgressDocument, "3"), key) != want {
				t.Fatal("wallet mirror", o.ID, key)
			}
		}
		if len(p.WalletHistory) == 0 || p.WalletHistory[len(p.WalletHistory)-1].Source != "web_exchange" || p.WalletHistory[len(p.WalletHistory)-1].OfferID != o.ID {
			t.Fatal("missing authoritative wallet evidence")
		}
		if p.ProgressRevision == before.ProgressRevision {
			t.Fatal("missing fence")
		}
		s, e = Open(dir)
		if e != nil {
			t.Fatal(e)
		}
		replay, applied, e := s.webExchange(socketUniqID, request, nil, "", passNow)
		if e != nil || applied || !equalWebJSON(p, replay) {
			t.Fatal("durable idempotency", e)
		}
		stale := request
		stale.Operation = fmt.Sprintf("%032x", n+100)
		if _, _, e := s.webExchange(socketUniqID, stale, nil, "", passNow); e != ErrInventoryConflict {
			t.Fatal("stale", e)
		}
	}
}
func TestWebExchangeParallelAndFailedPersistence(t *testing.T) {
	s, _, _ := passSetup(t)
	before, _ := s.Profile(socketUniqID)
	req := webReq(before, 231304, 1)
	var count atomic.Int32
	var wg sync.WaitGroup
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, applied, e := s.webExchange(socketUniqID, req, nil, "", passNow)
			if e != nil {
				t.Error(e)
			}
			if applied {
				count.Add(1)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatal(count.Load())
	}
	before, _ = s.Profile(socketUniqID)
	req = webReq(before, 231304, 2)
	s.state.Profiles[socketUniqID].ProgressSlots["3"].(map[string]any)["Coins"] = int64(0)
	before, _ = s.Profile(socketUniqID)
	req = webReq(before, 231304, 2)
	if _, _, e := s.webExchange(socketUniqID, req, nil, "", passNow); e != ErrWebFunds {
		t.Fatal(e)
	}
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("insufficient balance mutation")
	}
	s.state.Profiles[socketUniqID].ProgressSlots["3"].(map[string]any)["Coins"] = int64(10000)
	before, _ = s.Profile(socketUniqID)
	req = webReq(before, 231304, 2)
	// Force the same persistence failure mechanism as the existing store tests.
	s.path = filepath.Join(t.TempDir(), "missing", "state.json")
	if _, _, e := s.webExchange(socketUniqID, req, nil, "", passNow); e != ErrInventoryPersistence {
		t.Fatal("expected persistence failure", e)
	}
	after, _ = s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed write mutated wallet")
	}
}

func equalWebJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestWebExchangeOwnedRejectsReboundOrBannedAccount(t *testing.T) {
	s, _, _ := passSetup(t)
	owner, _ := s.Account(socketUniqID)
	before, _ := s.Profile(socketUniqID)
	request := webReq(before, 231304, 1)
	wrong := owner
	wrong.DeviceID += "changed"
	if _, _, e := s.webExchange(wrong.PlayerID, request, &wrong, "192.0.2.1", passNow); e == nil {
		t.Fatal("owner race")
	}
	if e := s.AddBan(Ban{Kind: BanPlayer, Subject: socketUniqID, Reason: "test"}); e != nil {
		t.Fatal(e)
	}
	if _, _, e := s.webExchange(owner.PlayerID, request, &owner, "192.0.2.1", passNow); e == nil {
		t.Fatal("banned wallet write")
	}
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected write mutated profile")
	}
}
