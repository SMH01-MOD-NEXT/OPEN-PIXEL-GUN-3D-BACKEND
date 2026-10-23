// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"fmt"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
	"math"
	"sort"
	"strconv"
	"time"
)

var ErrMailboxClaim = errors.New("store: invalid or unavailable mailbox reward")

// Slot 42 is the stock HashSet<int> serialized as a bare list. Uploaded copies
// and command 188 may never delete the durable, server-owned claim ledger.
func projectMailbox(p *Profile) {
	if p.MailboxClaims != nil {
		if p.ProgressDocument == nil {
			p.ProgressDocument = map[string]any{}
		}
		if p.ProgressSlots == nil {
			p.ProgressSlots = map[string]any{}
		}
	}
	for _, doc := range []map[string]any{p.ProgressDocument, p.ProgressSlots} {
		if doc == nil {
			continue
		}
		if _, exists := doc["42"]; !exists && p.MailboxClaims == nil {
			continue
		}
		ids := []int{}
		for key, claimed := range p.MailboxClaims {
			if id, e := strconv.Atoi(key); e == nil && claimed {
				ids = append(ids, id)
			}
		}
		sort.Ints(ids)
		records := make([]any, len(ids))
		for i, id := range ids {
			records[i] = id
		}
		doc["42"] = records
	}
}

// Stock grant path 0x33B904C emits generic reward commands and then 187 with
// records:[messageID] in ONE transaction. Never trust client gift amounts.
func reduceMailbox(p *Profile, commands []any, news []MailboxNews, now time.Time, skip map[int]bool) (map[int]bool, error) {
	catalogNews := map[int]MailboxNews{}
	for _, n := range news {
		catalogNews[n.ID] = n
	}
	expectedCurrency := map[string][]int64{}
	expectedWeapon := map[int]int{}
	hasClaim := false
	for pos, raw := range commands {
		c, _ := raw.(map[string]any)
		id, _ := counterInt64(c["id"])
		if id != 187 && id != 188 {
			continue
		}
		if skip[pos] {
			return nil, ErrMailboxClaim
		}
		d, _ := c["p"].(map[string]any)
		records, ok := d["records"].([]any)
		if !ok || len(records) == 0 || len(records) > MailboxNewsLimit {
			return nil, ErrMailboxClaim
		}
		skip[pos] = true
		for _, record := range records {
			nID, ok := counterInt64(record)
			if !ok || nID < 1 || nID > math.MaxInt32 {
				return nil, ErrMailboxClaim
			}
			if id == 188 {
				continue
			} // local cache cleanup, NOT an entitlement reset
			hasClaim = true
			n, ok := catalogNews[int(nID)]
			key := strconv.FormatInt(nID, 10)
			claimed := p.MailboxClaims[key]
			if !ok || (!claimed && !n.VisibleAt(now)) || len(n.Rewards.Tokens()) == 0 {
				return nil, ErrMailboxClaim
			}
			r := n.Rewards
			for name, amount := range map[string]int64{"Coins": r.Coins, "GemsCurrency": r.Gems} {
				if amount == 0 {
					continue
				}
				expectedCurrency[name] = append(expectedCurrency[name], amount)
				if !claimed {
					current := p.Coins
					if name == "GemsCurrency" {
						current = p.Gems
					}
					if current > math.MaxInt64-amount {
						return nil, ErrMailboxClaim
					}
					if name == "Coins" {
						p.Coins += amount
					} else {
						p.Gems += amount
					}
				}
			}
			if r.Weapon != 0 {
				expectedWeapon[r.Weapon]++
				if !claimed {
					item, ok := catalog.Find(10, r.Weapon)
					if !ok {
						return nil, ErrMailboxClaim
					}
					if _, owned := objectAt(p.ProgressSlots, "28", "0")[strconv.Itoa(r.Weapon)]; !owned {
						level := p.Level
						if level < 1 {
							level = 1
						}
						if level > 65 {
							level = 65
						}
						if err := editInventorySlots(p, item, InventoryEdit{Action: "grant", Level: int64(level), Rarity: 3}); err != nil {
							return nil, err
						}
					}
				}
			}
			if p.MailboxClaims == nil {
				p.MailboxClaims = map[string]bool{}
			}
			p.MailboxClaims[key] = true
		}
	}
	// Consume only measured echoes. Extra/wrong source-62 grants are rejected,
	// including echoes without 187, retries with changed ci and multiplied gifts.
	for pos, raw := range commands {
		c, _ := raw.(map[string]any)
		id, _ := counterInt64(c["id"])
		d, _ := c["p"].(map[string]any)
		cause, _ := counterInt64(d["ca"])
		if id == 10 && cause == 62 {
			if skip[pos] {
				return nil, ErrMailboxClaim
			}
			name, _ := d["c"].(string)
			amount, ok := counterInt64(d["v"])
			found := -1
			if ok {
				for i, want := range expectedCurrency[name] {
					if want == amount {
						found = i
						break
					}
				}
			}
			if found < 0 {
				return nil, ErrMailboxClaim
			}
			a := expectedCurrency[name]
			expectedCurrency[name] = append(a[:found], a[found+1:]...)
			skip[pos] = true
		}
		if id == 132 && hasClaim && !skip[pos] {
			idx, ok := counterInt64(d["i"])
			if !ok || expectedWeapon[int(idx)] == 0 {
				return nil, ErrMailboxClaim
			}
			u, _ := d["u"].(map[string]any)
			level, lok := counterInt64(u["l"])
			rarity, rok := counterInt64(u["r"])
			if !lok || !rok || level < 1 || level > 65 || rarity < 0 || rarity > 5 {
				return nil, ErrMailboxClaim
			}
			expectedWeapon[int(idx)]--
			skip[pos] = true
		}
	}
	if hasClaim {
		if p.ProgressDocument == nil {
			p.ProgressDocument = map[string]any{}
		}
		currencies, _ := p.ProgressSlots["3"].(map[string]any)
		if currencies == nil {
			return nil, fmt.Errorf("%w: missing currency projection", ErrMailboxClaim)
		}
		currencies["Coins"], currencies["GemsCurrency"] = p.Coins, p.Gems
		projectMailbox(p)
	}
	return skip, nil
}
