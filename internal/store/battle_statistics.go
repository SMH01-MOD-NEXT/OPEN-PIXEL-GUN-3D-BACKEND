// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// BattleStatisticsLedger separates acknowledged native commands from stale
// boot uploads. It is NOT an anti-cheat authority: this retired client reports
// its own combat results. Never accept a payload identity as the write target.
type BattleStatisticsLedger struct {
	Slots         map[string]any `json:"slots"`
	Modes         map[string]any `json:"modes"`
	ReportedModes map[string]any `json:"reported_modes"`
	Matches       map[string]any `json:"matches"`
	Endings       map[string]any `json:"endings"`
}

func (l *BattleStatisticsLedger) clone() *BattleStatisticsLedger {
	if l == nil {
		return nil
	}
	return &BattleStatisticsLedger{Slots: cloneAnyMap(l.Slots), Modes: cloneAnyMap(l.Modes), ReportedModes: cloneAnyMap(l.ReportedModes), Matches: cloneAnyMap(l.Matches), Endings: cloneAnyMap(l.Endings)}
}

var battleSlotIDs = []string{"45", "57", "77", "80", "84"}

func battleSlots(p *Profile) map[string]any {
	if p.BattleStatistics != nil {
		return p.BattleStatistics.Slots
	}
	out := map[string]any{}
	for _, id := range battleSlotIDs {
		if v, ok := p.ProgressSlots[id]; ok {
			out[id] = cloneAny(v)
		} else if v, ok := p.ProgressDocument[id]; ok {
			out[id] = cloneAny(v)
		}
	}
	return out
}
func newBattleStatistics(p *Profile) *BattleStatisticsLedger {
	l := &BattleStatisticsLedger{Slots: battleSlots(p), Modes: map[string]any{}, ReportedModes: map[string]any{}, Matches: map[string]any{}, Endings: map[string]any{}}
	for _, raw := range battleArray(objectAt(l.Slots, "57")["h"]) {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		t, ok := counterInt64(m["t"])
		if !ok || t <= 0 {
			continue
		}
		key := strconv.FormatInt(t, 10)
		if _, ok := l.Matches[key]; ok {
			continue
		}
		l.Matches[key] = true
		if ppInt(m, "r") == 1 {
			mode := strconv.FormatInt(ppInt(m, "m"), 10)
			l.Modes[mode] = ppInt(l.Modes, mode) + 1
		}
	}
	for _, raw := range battleArray(objectAt(l.Slots, "77")["h"]) {
		if m, ok := raw.(map[string]any); ok {
			for k := range m {
				l.Endings[k] = true
			}
		}
	}
	mergeModeMax(l.ReportedModes, objectAt(p.ClientState, "mods"))
	return l
}
func projectBattleStatistics(p *Profile) {
	if p.BattleStatistics == nil {
		return
	}
	if p.ProgressSlots == nil {
		p.ProgressSlots = map[string]any{}
	}
	if p.ProgressDocument == nil {
		p.ProgressDocument = map[string]any{}
	}
	for _, id := range battleSlotIDs {
		if v, ok := p.BattleStatistics.Slots[id]; ok {
			p.ProgressSlots[id] = cloneAny(v)
			p.ProgressDocument[id] = cloneAny(v)
		}
	}
}
func battleArray(v any) []any { a, _ := v.([]any); return a }
func battleNumber(v any) float64 {
	if n, ok := counterInt64(v); ok {
		return float64(n)
	}
	n, _ := v.(float64)
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return 0
	}
	return n
}
func mergeModeMax(dst, src map[string]any) {
	for k, v := range src {
		// Legacy one-time mode-stat migration (0x2505AFC) uses these aliases.
		switch k {
		case "br":
			k = "20"
		case "re":
			k = "22e"
		case "rm":
			k = "22m"
		case "rh":
			k = "22h"
		}
		n, ok := counterInt64(v)
		if ok && n > ppInt(dst, k) {
			dst[k] = n
		}
	}
}
func statInt(d map[string]any, k string) (int64, error) {
	n, ok := counterInt64(d[k])
	if !ok || n < 0 || n > 10000000 {
		return 0, fmt.Errorf("store: invalid battle statistic %s", k)
	}
	return n, nil
}
func statBool(v any) (bool, bool) {
	if b, ok := v.(bool); ok {
		return b, true
	}
	if n, ok := counterInt64(v); ok && (n == 0 || n == 1) {
		return n == 1, true
	}
	return false, false
}
func statAdd(m map[string]any, k string, n int64) error {
	old := ppInt(m, k)
	if old < 0 || n < 0 || old > math.MaxInt32-n {
		return errors.New("store: battle statistic overflow")
	}
	m[k] = old + n
	return nil
}
func statMax(m map[string]any, k string, n int64) {
	if n > ppInt(m, k) {
		m[k] = n
	}
}
func statsCommand(id int64) bool {
	switch id {
	case 192, 193, 254, 336, 352, 370:
		return true
	}
	return false
}

func applyBattleStatistics(p *Profile, id int64, d map[string]any, now time.Time) error {
	if p.BattleStatistics == nil {
		p.BattleStatistics = newBattleStatistics(p)
	}
	l := p.BattleStatistics
	switch id {
	case 192, 193:
		k, err := statInt(d, "k")
		if err != nil {
			return err
		}
		deaths := battleNumber(d["d"])
		if _, ok := counterInt64(d["d"]); !ok {
			n, valid := d["d"].(float64)
			if !valid || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
				return errors.New("store: invalid deaths")
			}
		}
		if deaths > 10000000 || deaths < 0 {
			return errors.New("store: invalid deaths")
		}
		if _, ok := d["d"]; !ok {
			return errors.New("store: missing deaths")
		}
		w, ok := statBool(d["w"])
		if !ok {
			return errors.New("store: invalid match win")
		}
		m, err := ensureObject(l.Slots, "45")
		if err != nil {
			return err
		}
		section := "t"
		if id == 193 {
			section = "br"
		}
		total, err := ensureObject(m, section)
		if err != nil {
			return err
		}
		if err = statAdd(total, "k", k); err != nil {
			return err
		}
		total["d"] = battleNumber(total["d"]) + deaths
		if id == 192 {
			a := append(battleArray(m["l_m"]), map[string]any{"k": k, "d": deaths, "w": w})
			if len(a) > 30 {
				a = a[len(a)-30:]
			}
			m["l_m"] = a
		}
	case 254:
		match, ok := d["m"].(map[string]any)
		if !ok {
			return errors.New("store: missing native match")
		}
		t, ok := counterInt64(match["t"])
		if !ok || t <= 0 || t > now.Add(24*time.Hour).Unix() {
			return errors.New("store: invalid match timestamp")
		}
		mode, ok := counterInt64(match["m"])
		if !ok || mode < 0 || mode > 100 {
			return errors.New("store: invalid match mode")
		}
		r, ok := counterInt64(match["r"])
		if !ok || r < 0 || r > 10 {
			return errors.New("store: invalid match result")
		}
		players, ok := match["p"].([]any)
		if !ok || len(players) == 0 || len(players) > 100 {
			return errors.New("store: invalid match players")
		}
		owner := false
		for _, raw := range players {
			a, ok := raw.(map[string]any)
			if !ok {
				return errors.New("store: invalid match player")
			}
			who, _ := a["i"].(string)
			if who == p.PlayerID {
				owner = true
			}
			for _, k := range []string{"k", "s", "p", "t"} {
				if _, err := statInt(a, k); err != nil {
					return err
				}
			}
			nick, _ := a["n"].(string)
			if len(nick) > 512 || len(who) > 128 {
				return errors.New("store: oversized match identity")
			}
		}
		if !owner {
			return errors.New("store: match must contain authenticated player")
		}
		key := strconv.FormatInt(t, 10)
		if _, ok := l.Matches[key]; ok {
			return nil
		}
		if len(l.Matches) >= 100000 {
			return errors.New("store: match receipt limit")
		}
		l.Matches[key] = true
		m, err := ensureObject(l.Slots, "57")
		if err != nil {
			return err
		}
		a := append(battleArray(m["h"]), cloneAnyMap(match))
		sort.SliceStable(a, func(i, j int) bool { return ppInt(a[i].(map[string]any), "t") < ppInt(a[j].(map[string]any), "t") })
		if len(a) > 10 {
			a = a[len(a)-10:]
		}
		m["h"] = a
		if r == 1 {
			if err = statAdd(l.Modes, strconv.FormatInt(mode, 10), 1); err != nil {
				return err
			}
		}
	case 336:
		u, ok := counterInt64(d["u"])
		if !ok || u <= 0 || u > now.Add(24*time.Hour).Unix() {
			return errors.New("store: invalid battle ending timestamp")
		}
		win, ok := statBool(d["w"])
		if !ok {
			return errors.New("store: invalid battle result")
		}
		key := strconv.FormatInt(u, 10)
		if _, ok := l.Endings[key]; ok {
			return nil
		}
		if len(l.Endings) >= 100000 {
			return errors.New("store: battle receipt limit")
		}
		l.Endings[key] = true
		m, err := ensureObject(l.Slots, "77")
		if err != nil {
			return err
		}
		field := "d"
		if win {
			field = "w"
		}
		total, err := ensureObject(m, "t")
		if err != nil {
			return err
		}
		if err = statAdd(total, field, 1); err != nil {
			return err
		}
		day := strconv.FormatInt(u/86400, 10)
		daily, err := ensureObject(m, "k", day)
		if err != nil {
			return err
		}
		if _, ok := daily["d"]; !ok {
			daily["d"] = int64(0)
		}
		if _, ok := daily["w"]; !ok {
			daily["w"] = int64(0)
		}
		if err = statAdd(daily, field, 1); err != nil {
			return err
		}
		result := int64(0)
		if win {
			result = 1
		}
		a := append(battleArray(m["h"]), map[string]any{key: result})
		if len(a) > 5 {
			a = a[len(a)-5:]
		}
		m["h"] = a
	case 352, 370:
		values := map[string]int64{}
		for _, k := range []string{"k", "d", "hds", "h", "s", "ks", "tw", "sw"} {
			n, err := statInt(d, k)
			if err != nil {
				return err
			}
			values[k] = n
		}
		w, ok := statBool(d["w"])
		if !ok {
			return errors.New("store: invalid stats win")
		}
		competition, ok := statBool(d["c"])
		if !ok {
			return errors.New("store: invalid stats competition")
		}
		dk, _ := d["dk"].(string)
		day, err := time.Parse("1-2-2006", dk)
		if err != nil || day.After(now.Add(48*time.Hour)) || day.Year() < 2010 {
			return errors.New("store: invalid stats day")
		}
		sid, _ := d["sid"].(string)
		if len(sid) > 128 {
			return errors.New("store: invalid stats season")
		}
		slot := "84"
		if id == 352 {
			slot = "80"
		}
		m, err := ensureObject(l.Slots, slot)
		if err != nil {
			return err
		}
		daily, err := ensureObject(m, "ds", dk)
		if err != nil {
			return err
		}
		if err = applyDailyStats(daily, values, w, values["tw"]); err != nil {
			return err
		}
		// Native V2 stores at most 30 daily entries (0x510D040).
		days := objectAt(m, "ds")
		if len(days) > 30 {
			keys := make([]string, 0, len(days))
			for k := range days {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool {
				a, _ := time.Parse("1-2-2006", keys[i])
				b, _ := time.Parse("1-2-2006", keys[j])
				return a.Before(b)
			})
			for _, k := range keys[:len(keys)-30] {
				delete(days, k)
			}
		}
		if competition && sid != "" {
			ss, err := ensureObject(m, "ss")
			if err != nil {
				return err
			}
			season, ok := ss[sid].(map[string]any)
			if !ok {
				season = map[string]any{}
				m["ss"] = map[string]any{sid: season}
			}
			if err = applyDailyStats(season, values, w, values["sw"]); err != nil {
				return err
			}
		}
	}
	projectBattleStatistics(p)
	return nil
}
func applyDailyStats(m map[string]any, v map[string]int64, win bool, streak int64) error {
	for _, k := range []string{"h", "s", "hds"} {
		if err := statAdd(m, k, v[k]); err != nil {
			return err
		}
	}
	if err := statAdd(m, "m", 1); err != nil {
		return err
	}
	w := int64(0)
	if win {
		w = 1
	}
	if err := statAdd(m, "w", w); err != nil {
		return err
	}
	statMax(m, "ks", v["ks"])
	statMax(m, "ws", streak)
	kd, err := ensureObject(m, "k", "t")
	if err != nil {
		return err
	}
	if err = statAdd(kd, "k", v["k"]); err != nil {
		return err
	}
	return statAdd(kd, "d", v["d"])
}

// BattleWins returns the native mode keys. "t" means tournament, NOT total;
// the 23.1.3 parser computes TotalWins by summing every key except "t".
func (p *Profile) BattleWins() map[string]any {
	l := p.BattleStatistics
	if l == nil {
		l = newBattleStatistics(p)
	}
	out := cloneAnyMap(l.Modes)
	mergeModeMax(out, l.ReportedModes)
	return out
}
func (s *Store) MergeBattleModes(playerID string, mods map[string]any, ctx ProgressWriteContext) error {
	if len(mods) > 100 {
		return errors.New("store: too many battle modes")
	}
	for k, v := range mods {
		n, ok := counterInt64(v)
		if len(k) == 0 || len(k) > 8 || strings.TrimSpace(k) != k || !ok || n < 0 || n > math.MaxInt32 {
			return errors.New("store: invalid mode wins")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	playerID = s.resolvePlayerIDLocked(playerID)
	p, ok := s.state.Profiles[playerID]
	if !ok {
		return ErrNotFound
	}
	allowed, err := allowProfileClientWrite(p, []ProgressWriteContext{ctx})
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	candidate := p.clone()
	if candidate.BattleStatistics == nil {
		candidate.BattleStatistics = newBattleStatistics(&candidate)
	}
	if candidate.BattleStatistics.ReportedModes == nil {
		candidate.BattleStatistics.ReportedModes = map[string]any{}
	}
	mergeModeMax(candidate.BattleStatistics.ReportedModes, mods)
	candidate.UpdatedAt = time.Now().UTC()
	*p, candidate = candidate, *p
	if err = s.persistLocked(); err != nil {
		*p = candidate
	}
	return err
}

// NativePlayerStatistics uses V2 when available, never sums V1 and V2 copies.
// General is rolling 30-calendar-day data; global wins come from BattleWins.
func (p *Profile) NativePlayerStatistics(now time.Time) map[string]any {
	slots := battleSlots(p)
	daily := objectAt(slots, "84", "ds")
	if daily == nil {
		daily = objectAt(slots, "80", "ds")
	}
	out := map[string]any{}
	v := map[string]int64{}
	kills, deaths := float64(0), float64(0)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for key, raw := range daily {
		day, err := time.Parse("1-2-2006", key)
		if err != nil || day.Before(today.AddDate(0, 0, -29)) || day.After(today.AddDate(0, 0, 1)) {
			continue
		}
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for _, k := range []string{"h", "s", "hds", "m", "w"} {
			v[k] += ppInt(m, k)
		}
		for _, k := range []string{"ks", "ws"} {
			if n := ppInt(m, k); n > v[k] {
				v[k] = n
			}
		}
		kd := objectAt(m, "k", "t")
		kills += battleNumber(kd["k"])
		deaths += battleNumber(kd["d"])
	}
	out["kl"], out["hds"], out["ms"], out["ws"], out["wns"], out["kls"] = int64(kills), v["hds"], v["m"], v["w"], v["ws"], v["ks"]
	accuracy := float64(0)
	if v["s"] > 0 {
		accuracy = 100 * float64(v["h"]) / float64(v["s"])
	}
	rate := kills
	if deaths > 0 {
		rate = kills / deaths
	}
	out["ac"], out["klr"] = accuracy, rate
	wins := p.BattleWins()
	out["w"] = wins
	total := int64(0)
	for k := range wins {
		if k != "t" {
			total += ppInt(wins, k)
		}
	}
	out["tws"] = total
	return out
}
