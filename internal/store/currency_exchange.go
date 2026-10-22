// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/exchange"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/pixelpass"
	"math"
	"time"
)

var errExchange = errors.New("store: invalid currency exchange transaction")

// The stock UNLIMITED PixelPass offer path (bl=0) emits the virtual payment
// 66/ca=145 and reward 10/ca=107, but deliberately does not emit finite-offer
// buy-count command 332. A native capture also has zero *_1 debits and analytics.
// Resolve both sides against our catalog, not a client-supplied conversion rate.
// The caller filters durable ci/content receipts before this reducer, then
// persists both balances and all receipts in one locked candidate transaction.
// This validates these offers, not the other client-authored currency paths.
func reduceCurrencyExchange(p *Profile, commands []any, now time.Time, skip map[int]bool) (map[int]bool, error) {
	if skip == nil {
		skip = map[int]bool{}
	}
	for i, raw := range commands {
		if skip[i] {
			continue
		}
		c, _ := raw.(map[string]any)
		d, _ := c["p"].(map[string]any)
		if ppInt(c, "id") != 66 || ppInt(d, "ca") != 145 {
			continue
		}
		cost, valid := counterInt64(d["v"])
		if valid && cost == 0 {
			continue
		} // stock bonus-wallet echo
		if !valid || cost < 0 || !pixelpass.Active(now) {
			return nil, errExchange
		}
		found := -1
		var selected exchange.Offer
		for j, rewardRaw := range commands {
			if skip[j] {
				continue
			}
			rewardCommand, _ := rewardRaw.(map[string]any)
			reward, _ := rewardCommand["p"].(map[string]any)
			if ppInt(rewardCommand, "id") != 10 || ppInt(reward, "ca") != 107 {
				continue
			}
			for _, offer := range exchange.Offers() {
				amount, valid := counterInt64(reward["v"])
				if d["c"] == offer.Pay && cost == offer.Cost && reward["c"] == offer.Receive && valid && amount == offer.Amount {
					found, selected = j, offer
					break
				}
			}
			if found >= 0 {
				break
			}
		}
		if found < 0 {
			return nil, errExchange
		}
		wallet, ok := p.ProgressSlots["3"].(map[string]any)
		if !ok {
			return nil, errExchange
		}
		balance, valid := counterInt64(wallet[selected.Pay])
		if !valid || balance < selected.Cost {
			return nil, errExchange
		}
		reward := int64(0)
		if value, present := wallet[selected.Receive]; present {
			var valid bool
			reward, valid = counterInt64(value)
			if !valid || reward < 0 {
				return nil, errExchange
			}
		}
		if reward > math.MaxInt32-selected.Amount {
			return nil, errExchange
		}
		wallet[selected.Pay] = balance - selected.Cost
		wallet[selected.Receive] = reward + selected.Amount
		p.Coins = ppInt(wallet, "Coins")
		p.Gems = ppInt(wallet, "GemsCurrency")
		skip[i], skip[found] = true, true
	}
	// Refuse an orphan native reward when its matching payment was removed or
	// malformed. Pass task/level echoes have already been consumed by reducePixelPass.
	for i, raw := range commands {
		if skip[i] {
			continue
		}
		c, _ := raw.(map[string]any)
		d, _ := c["p"].(map[string]any)
		if ppInt(c, "id") == 10 && ppInt(d, "ca") == 107 {
			return nil, errExchange
		}
	}
	return skip, nil
}
