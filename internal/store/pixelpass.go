// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/pixelpass"
)

// PixelPassLedger is deliberately separate from uploaded slot snapshots. Stock
// owns presentation/config references, but cannot overwrite acknowledged claims.
type PixelPassLedger struct {
	TutorialVersion int            `json:"tutorial_version,omitempty"`
	PremiumBonus    int64          `json:"premium_bonus,omitempty"`
	Closed          bool           `json:"closed,omitempty"`
	State           map[string]any `json:"state"`
	Tasks           map[string]any `json:"tasks"`
	Claimed         map[string]any `json:"claimed_tasks"`
	MatchDay        string         `json:"match_day"`
	MatchCrowns     int64          `json:"match_crowns"`
}

func (l *PixelPassLedger) clone() *PixelPassLedger {
	if l == nil {
		return nil
	}
	c := *l
	c.State = cloneAnyMap(l.State)
	c.Tasks = cloneAnyMap(l.Tasks)
	c.Claimed = cloneAnyMap(l.Claimed)
	return &c
}

// migratePassTutorial stages a one-time full download for legacy accounts only.
// New ledgers already carry this contract version and complete locally in battle.
func migratePassTutorial(p *Profile) bool {
	l := p.PixelPass
	if l == nil || l.TutorialVersion != 0 || l.MatchCrowns <= 0 || ppInt(l.State, "i") != pixelpass.SeasonID {
		return false
	}
	l.TutorialVersion = 1
	p.ProgressRevision = nextProgressRevision(p.ProgressRevision)
	return true
}

// reconcilePassTutorial repairs accounts that already earned match crowns while
// the native tutorial base was missing. Never infer a match from total crowns:
// premium packs, purchased levels and quests also add to that total. No money,
// crowns, premium ownership or ordinary task claims are changed by this repair.
func reconcilePassTutorial(l *PixelPassLedger) {
	if l == nil || ppInt(l.State, "i") != pixelpass.SeasonID || l.MatchCrowns <= 0 {
		return
	}
	if l.Tasks == nil {
		l.Tasks = map[string]any{}
	}
	l.Tasks["5_9999"] = map[string]any{"p": int64(1), "c": int64(1), "r": int64(1)}
}

func projectPixelPass(p *Profile) {
	if p.PixelPass == nil {
		return
	}
	reconcilePassTutorial(p.PixelPass)
	if p.ProgressSlots == nil {
		p.ProgressSlots = map[string]any{}
	}
	if p.ProgressDocument == nil {
		p.ProgressDocument = map[string]any{}
	}
	for _, doc := range []map[string]any{p.ProgressSlots, p.ProgressDocument} {
		doc["74"] = map[string]any{"pp": cloneAnyMap(p.PixelPass.State)}
		if p.PixelPass.Closed {
			doc["74"] = map[string]any{"pp": map[string]any{}}
		}
		tasks, _ := doc["33"].(map[string]any)
		if tasks == nil {
			tasks = map[string]any{}
			doc["33"] = tasks
		}
		for k := range tasks {
			if strings.HasPrefix(k, "5_") {
				delete(tasks, k)
			}
		}
		for k, v := range p.PixelPass.Tasks {
			tasks[k] = cloneAny(v)
		}
	}
}
func ppInt(m map[string]any, k string) int64 { v, _ := counterInt64(m[k]); return v }
func ppCommand(id int64) bool                { return id >= 322 && id <= 333 || id == 335 || id >= 337 && id <= 339 }

var errPass = errors.New("store: invalid or unavailable Pixel Pass operation")

// ErrPixelPassStateConflict identifies a catalog-valid claim rejected only by
// authoritative ownership/progress. The API must reload the ledger, not relax
// eligibility or acknowledge a reward that was never committed.
var ErrPixelPassStateConflict = errors.New("store: Pixel Pass client state requires authoritative reload")

// reducePixelPass validates semantic operations before the generic reducer.
// Stock sends generic reward/debit echoes in the SAME transaction; consume
// those echoes, grant from the frozen catalog once, and persist one candidate.
// Match/task reports are client-authored (this server does not host Photon).
func reducePixelPass(p *Profile, commands []any, now time.Time) (map[int]bool, error) {
	skip := map[int]bool{}
	present := false
	for _, raw := range commands {
		c, _ := raw.(map[string]any)
		id, _ := counterInt64(c["id"])
		if ppCommand(id) || id >= 150 && id <= 155 {
			present = true
		}
	}
	if !present {
		return skip, nil
	}
	hasPass := false
	for _, raw := range commands {
		c, _ := raw.(map[string]any)
		id, _ := counterInt64(c["id"])
		if ppCommand(id) {
			hasPass = true
		}
		if id >= 150 && id <= 155 {
			d, _ := c["p"].(map[string]any)
			for k := range d {
				if strings.HasPrefix(k, "5_") {
					hasPass = true
				}
			}
		}
	}
	if hasPass && !pixelpass.Active(now) {
		end, _ := time.Parse(time.RFC3339, pixelpass.End)
		if now.Before(end) {
			return nil, errPass
		}
		for i, raw := range commands {
			c, _ := raw.(map[string]any)
			id, _ := counterInt64(c["id"])
			if ppCommand(id) {
				if id != 331 {
					return nil, errPass
				}
				skip[i] = true
			}
			if id >= 150 && id <= 155 {
				d, _ := c["p"].(map[string]any)
				for k := range d {
					if strings.HasPrefix(k, "5_") {
						return nil, errPass
					}
				}
			}
		}
		if p.PixelPass != nil {
			p.PixelPass.Closed = true
			projectPixelPass(p)
		}
		return skip, nil
	}
	if !hasPass {
		for i, raw := range commands {
			c, _ := raw.(map[string]any)
			id, _ := counterInt64(c["id"])
			if id < 150 || id > 155 {
				continue
			}
			skip[i] = true
			if id > 152 {
				continue
			}
			d, ok := c["p"].(map[string]any)
			if !ok || len(d) > 512 {
				return nil, errPass
			}
			tasks, err := ensureObject(p.ProgressSlots, "33")
			if err != nil {
				return nil, err
			}
			for k, v := range d {
				delta, ok := v.(map[string]any)
				if !ok {
					return nil, errPass
				}
				old, _ := tasks[k].(map[string]any)
				if old == nil {
					old = map[string]any{}
				}
				for a, b := range delta {
					old[a] = cloneAny(b)
				}
				tasks[k] = old
			}
		}
		return skip, nil
	}
	if p.PixelPass == nil {
		p.PixelPass = &PixelPassLedger{TutorialVersion: 1, State: map[string]any{"i": pixelpass.SeasonID, "lr": []any{}, "ctr": map[string]any{}}, Tasks: map[string]any{}, Claimed: map[string]any{}}
		// A native boot snapshot may precede delivery/retry of standalone 322.
		// Recover only the opaque cache reference, never its ownership/crowns.
		old := objectAt(p.ProgressSlots, "74", "pp")
		if ref, ok := old["c"].(string); ok && ppInt(old, "i") == pixelpass.SeasonID && len(ref) > 0 && len(ref) <= 65536 {
			p.PixelPass.State["c"] = ref
		}
	}
	l := p.PixelPass
	reconcilePassTutorial(l)

	state := l.State
	if ppInt(state, "i") != pixelpass.SeasonID {
		return nil, errPass
	}
	pending, _ := state["ctr"].(map[string]any)
	if pending == nil {
		pending = map[string]any{}
		state["ctr"] = pending
	}
	currency := p.ProgressSlots["3"].(map[string]any)
	adjust := func(name string, delta int64) error {
		old := ppInt(currency, name)
		if old < 0 || delta < 0 && old < -delta || delta > 0 && old > math.MaxInt64-delta {
			return errPass
		}
		currency[name] = old + delta
		if name == "Coins" {
			p.Coins = old + delta
		}
		if name == "GemsCurrency" {
			p.Gems = old + delta
		}
		return nil
	}
	// A missing echo is legal: never rely on a client-specified reward to grant.
	consume := func(id int64, field string, want any, amount int64) bool {
		for i, raw := range commands {
			if skip[i] {
				continue
			}
			c, _ := raw.(map[string]any)
			n, _ := counterInt64(c["id"])
			d, _ := c["p"].(map[string]any)
			if n != id || fmt.Sprint(d[field]) != fmt.Sprint(want) {
				continue
			}
			if id == 10 || id == 66 {
				v, ok := counterInt64(d["v"])
				if !ok || v != amount {
					continue
				}
			}
			skip[i] = true
			return true
		}
		return false
	}
	grant := func(tokens []string) error {
		for _, token := range tokens {
			idx, n, err := pixelpass.Reward(token)
			if err != nil {
				return err
			}
			switch idx {
			case 1015, 2015, 26015:
				name := map[int]string{1015: "Coins", 2015: "GemsCurrency", 26015: pixelpass.Currency}[idx]
				consume(10, "c", name, n)
				if err = adjust(name, n); err != nil {
					return err
				}
			default:
				item, ok := catalog.Find(10, idx)
				if !ok || n != 1 {
					return errPass
				}
				consume(132, "i", idx, 1)
				// Already-owned reward weapons never lose upgrades or rarity.
				if _, owned := objectAt(p.ProgressSlots, "28", "0")[strconv.Itoa(idx)]; !owned {
					level := p.Level
					if level < 1 {
						level = 1
					}
					if level > 65 {
						level = 65
					}
					if err = editInventorySlots(p, item, InventoryEdit{Action: "grant", Level: int64(level), Rarity: 5}); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	// GameTask delta commands carry a dictionary keyed by "5_<task index>".
	// Preserve other task families, but enforce monotonic bounded pass progress.
	for i, raw := range commands {
		c, _ := raw.(map[string]any)
		id, _ := counterInt64(c["id"])
		if id < 150 || id > 155 {
			continue
		}
		skip[i] = true
		d, ok := c["p"].(map[string]any)
		if !ok || len(d) > 512 {
			return nil, errPass
		}
		tasks, err := ensureObject(p.ProgressSlots, "33")
		if err != nil {
			return nil, err
		}
		if id >= 154 { // Reset commands must not erase pass progress or claims.
			for k := range d {
				if strings.HasPrefix(k, "5_") {
					return nil, errPass
				}
			}
			continue
		}
		for k, rawDelta := range d {
			delta, ok := rawDelta.(map[string]any)
			if !ok {
				return nil, errPass
			}
			if !strings.HasPrefix(k, "5_") {
				old, _ := tasks[k].(map[string]any)
				if old == nil {
					old = map[string]any{}
				}
				for a, b := range delta {
					old[a] = cloneAny(b)
				}
				tasks[k] = old
				continue
			}
			idx, err := strconv.Atoi(strings.TrimPrefix(k, "5_"))
			task, found := pixelpass.FindTask(idx)
			if err != nil || !found {
				return nil, errPass
			}
			// Stock flushes its in-battle dictionary via 153 (151 on completion),
			// including cached
			// calendar tasks not open yet. Validate that channel but do not
			// unlock future tasks or let it roll back the whole match report.
			if id == 151 || id == 153 {
				for field, raw := range delta {
					v, valid := counterInt64(raw)
					if !valid || v < 0 || (field != "p" && field != "c" && field != "r") || field == "p" && v > 1000000 || field != "p" && v > 1 {
						return nil, errPass
					}
				}
				if !pixelpass.TaskOpen(task, now) {
					continue
				}
			} else if !pixelpass.TaskOpen(task, now) {
				return nil, errPass
			}
			old, _ := l.Tasks[k].(map[string]any)
			if old == nil {
				old = map[string]any{}
			}
			for a, b := range delta {
				v, ok := counterInt64(b)
				if !ok {
					return nil, errPass
				}
				switch a {
				case "p":
					if v < 0 || v > 1000000 {
						return nil, errPass
					}
					if v > task.Needed {
						v = task.Needed
					}
					if v < ppInt(old, "p") {
						v = ppInt(old, "p")
					}
				case "c", "r":
					if v < 0 || v > 1 {
						return nil, errPass
					}
					// An old device can echo the pre-repair tutorial flags.
					if (idx == pixelpass.TutorialTaskIndex || id == 151 || id == 153) && v < ppInt(old, a) {
						v = ppInt(old, a)
					}
					if v < ppInt(old, a) {
						return nil, errPass
					}
				default:
					return nil, errPass
				}
				old[a] = v
			}
			if ppInt(old, "c") == 1 && ppInt(old, "p") < task.Needed {
				return nil, errPass
			}
			if ppInt(old, "r") == 1 && ppInt(old, "p") < task.Needed {
				return nil, errPass
			}
			l.Tasks[k] = old
			tasks[k] = cloneAnyMap(old)
		}
	}
	tutorialClaim := false
	for _, raw := range commands {
		c, _ := raw.(map[string]any)
		d, _ := c["p"].(map[string]any)
		if ppInt(c, "id") == 338 && ppInt(d, "t") == pixelpass.TutorialTaskIndex {
			tutorialClaim = true
		}
	}
	var totalReported int64
	for _, raw := range commands {
		c, _ := raw.(map[string]any)
		id, _ := counterInt64(c["id"])
		if id == 326 {
			d, _ := c["p"].(map[string]any)
			v, ok := counterInt64(d["e"])
			if !ok || v < 0 || v == 0 && !tutorialClaim || v > pixelpass.MaxCrowns {
				return nil, errPass
			}
			totalReported += v
		}
	}
	var earned, reported, duplicateCrowns int64
	// Initialization, premium and task claims establish the permitted crown budget.
	for i, raw := range commands {
		c, _ := raw.(map[string]any)
		id, _ := counterInt64(c["id"])
		if !ppCommand(id) {
			continue
		}
		skip[i] = true
		d, ok := c["p"].(map[string]any)
		if !ok || d == nil {
			return nil, errPass
		}
		switch id {
		case 322:
			ref, ok := d["c"].(string)
			if ppInt(d, "i") != pixelpass.SeasonID || !ok || len(ref) == 0 || len(ref) > 65536 {
				return nil, errPass
			}
			state["c"] = ref
		case 324:
			price := pixelpass.PremiumPrice
			pack := false
			for j, r := range commands {
				if skip[j] {
					continue
				}
				cmd, _ := r.(map[string]any)
				n, _ := counterInt64(cmd["id"])
				v, _ := cmd["p"].(map[string]any)
				if n == 66 && v["c"] == pixelpass.Currency && ppInt(v, "v") == pixelpass.PackPrice {
					price = pixelpass.PackPrice
					pack = true
					break
				}
			}
			consume(66, "c", pixelpass.Currency, price)
			if ppInt(state, "p") == 0 {
				if err := adjust(pixelpass.Currency, -price); err != nil {
					return nil, err
				}
				state["p"] = int64(1)
				if pack {
					// Stock can buy whole cells or just the remaining distance to the
					// tenth cell when the current cell already has crown progress.
					bonus := int64(1000)
					gap := bonus - ppInt(state, "e")%pixelpass.CrownStep
					if totalReported == gap {
						bonus = gap
					}
					if ppInt(state, "e")+bonus > pixelpass.MaxCrowns {
						bonus = pixelpass.MaxCrowns - ppInt(state, "e")
					}
					if bonus <= 0 {
						return nil, errPass
					}
					earned += bonus
					l.PremiumBonus = bonus
				}
			} else if pack {
				earned += l.PremiumBonus
				duplicateCrowns += l.PremiumBonus
			}
		case 337, 338:
			idx := int(ppInt(d, "t"))
			task, ok := pixelpass.FindTask(idx)
			if !ok || !pixelpass.TaskOpen(task, now) {
				return nil, errPass
			}
			key := strconv.Itoa(idx)
			progress, _ := l.Tasks["5_"+key].(map[string]any)
			if ppInt(progress, "p") < task.Needed {
				return nil, errPass
			}
			if id == 337 {
				if !samePassRewards(d["r"], task.Rewards) {
					return nil, errPass
				}
				if l.Claimed[key] != true {
					pending[key] = map[string]any{"25015": ppRewardAmount(task.Rewards)}
				}
			} else if l.Claimed[key] != true {
				if pending[key] == nil {
					return nil, errPass
				}
				earned += ppRewardAmount(task.Rewards)
				l.Claimed[key] = true
				delete(pending, key)
			} else {
				earned += ppRewardAmount(task.Rewards)
				duplicateCrowns += ppRewardAmount(task.Rewards)
			}
		case 325, 328:
			for _, t := range pixelpass.Tasks() {
				progress, _ := l.Tasks[fmt.Sprintf("5_%d", t.Index)].(map[string]any)
				if t.Type == 3 && ppInt(progress, "p") < t.Needed {
					return nil, errPass
				}
			}
			state["ce"] = int64(1)
			if id == 325 {
				if ppInt(state, "er") == 0 {
					if err := grant([]string{"2015:100"}); err != nil {
						return nil, err
					}
					state["er"] = int64(1)
				} else {
					consume(10, "c", "GemsCurrency", 100)
				}
			}
		case 326:
			n, ok := counterInt64(d["e"])
			if !ok || n < 0 || n == 0 && !tutorialClaim || n > pixelpass.MaxCrowns {
				return nil, errPass
			}
			reported += n
		case 327, 329, 331, 332, 333:
			return nil, errPass // no rerolls/offers/early season resets in this frozen season
		case 330: // ads are disabled in config; accepting them would invent rewards
			return nil, errPass
		case 335:
			state["pv"] = int64(1)
		case 339:
			n, ok := counterInt64(d["t"])
			if !ok || n < 0 || n > now.Unix()+86400 {
				return nil, errPass
			}
			state["lvp"] = n
		}
	}
	// Native level purchases are only a Pixel bucks debit plus ObtainExp(326).
	var bought int64
	if reported > earned {
		for i, raw := range commands {
			if skip[i] {
				continue
			}
			c, _ := raw.(map[string]any)
			id, _ := counterInt64(c["id"])
			d, _ := c["p"].(map[string]any)
			n := ppInt(d, "v")
			if id == 66 && d["c"] == pixelpass.Currency && n > 0 && n%pixelpass.LevelPrice == 0 {
				if n > 49*pixelpass.LevelPrice {
					return nil, errPass
				}
				crowns := n / pixelpass.LevelPrice * pixelpass.CrownStep
				gap := crowns - (ppInt(state, "e")+earned+bought)%pixelpass.CrownStep
				if reported-earned-bought == gap {
					crowns = gap
				}
				if ppInt(state, "e")+earned+bought+crowns > pixelpass.MaxCrowns {
					return nil, errPass
				}
				if crowns > pixelpass.MaxCrowns || bought+crowns > reported-earned {
					return nil, errPass
				}
				skip[i] = true
				if err := adjust(pixelpass.Currency, -n); err != nil {
					return nil, err
				}
				bought += crowns
			}
		}
	}
	// A repeated semantic task/premium transaction with new command IDs must not
	// turn its ObtainExp echo into a fresh match reward.
	semantic := false
	for _, raw := range commands {
		c, _ := raw.(map[string]any)
		id, _ := counterInt64(c["id"])
		d, _ := c["p"].(map[string]any)
		// The generated tutorial pays zero crowns and can complete in the same
		// end-of-match batch as the ordinary match reward. It has no crown echo
		// that could be replayed as a new match.
		if id == 324 || id == 338 && ppInt(d, "t") != pixelpass.TutorialTaskIndex {
			semantic = true
		}
	}
	extra := reported - earned - bought
	if extra < 0 || semantic && extra != 0 {
		return nil, errPass
	}
	if extra > 0 {
		// Native match rewards are bounded by 50; batched reports remain supported.
		for _, raw := range commands {
			c, _ := raw.(map[string]any)
			id, _ := counterInt64(c["id"])
			d, _ := c["p"].(map[string]any)
			if id == 326 && ppInt(d, "e") > 50 {
				return nil, errPass
			}
		}
		day := now.UTC().Format("2006-01-02")
		if l.MatchDay != day {
			l.MatchDay = day
			l.MatchCrowns = 0
		}
		if extra > 1000-l.MatchCrowns {
			return nil, errPass
		}
		l.MatchCrowns += extra
	}
	if ppInt(state, "e") > 1000000-(reported-duplicateCrowns) {
		return nil, errPass
	}
	state["e"] = ppInt(state, "e") + reported - duplicateCrowns
	// Claimable cells are checked AFTER crowns/premium in the same atomic batch.
	for _, raw := range commands {
		c, _ := raw.(map[string]any)
		id, _ := counterInt64(c["id"])
		if id != 323 {
			continue
		}
		d, _ := c["p"].(map[string]any)
		n := int(ppInt(d, "l"))
		if n < 1 || n > 50 {
			return nil, errPass
		}
		cell := pixelpass.Levels()[n-1]
		if !samePassRewards(d["r"], cell.Rewards) {
			return nil, errPass
		}
		if ppInt(state, "e") < cell.Exp || cell.Free == 0 && ppInt(state, "p") == 0 {
			return nil, fmt.Errorf("%w: level=%d crowns=%d required=%d premium=%d", ErrPixelPassStateConflict, n, ppInt(state, "e"), cell.Exp, ppInt(state, "p"))
		}
		list, _ := state["lr"].([]any)
		found := false
		for _, r := range list {
			v, _ := counterInt64(r)
			if v == int64(n) {
				found = true
			}
		}
		if found {
			for _, token := range cell.Rewards {
				idx, a, _ := pixelpass.Reward(token)
				name := map[int]string{1015: "Coins", 2015: "GemsCurrency", 26015: pixelpass.Currency}[idx]
				if name != "" {
					consume(10, "c", name, a)
				} else {
					consume(132, "i", idx, 1)
				}
			}
			continue
		}
		if err := grant(cell.Rewards); err != nil {
			return nil, err
		}
		state["lr"] = append(list, int64(n))
	}
	projectPixelPass(p)
	return skip, nil
}
func ppRewardAmount(tokens []string) int64 {
	var n int64
	for _, s := range tokens {
		_, v, _ := pixelpass.Reward(s)
		n += v
	}
	return n
}
func samePassRewards(raw any, want []string) bool {
	a, ok := raw.([]any)
	if !ok || len(a) != len(want) {
		return false
	}
	for i, v := range a {
		if v != want[i] {
			return false
		}
	}
	return true
}
