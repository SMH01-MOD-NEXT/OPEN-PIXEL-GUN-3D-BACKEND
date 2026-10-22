// SPDX-License-Identifier: GPL-3.0-or-later
// Package pixelpass owns the frozen 23.1.3 season contract shared by delivery
// and persistence. User-visible copy uses existing client localization keys.
package pixelpass

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/exchange"
)

// Stock creates the Tutorial task itself, outside the calendar task list.
const TutorialTaskIndex = 9999
const SeasonID = 1
const PremiumPrice int64 = 600
const PackPrice int64 = 1200
const LevelPrice int64 = 20
const CrownStep int64 = 100
const MaxCrowns int64 = 4900
const Currency = "PixelPassCurrency"
const ConfigName = "PixelPass"
const Start = "2026-10-21T00:00:00Z"
const End = "2026-12-20T00:00:00Z"

type Level struct {
	Level   int      `json:"l"`
	Type    int      `json:"t"`
	Page    int      `json:"p"`
	Exp     int64    `json:"e"`
	Rewards []string `json:"r"`
	Free    int      `json:"f"`
	Cool    bool     `json:"c"`
}
type Task struct {
	Index    int      `json:"i"`
	StartDay int      `json:"sd"`
	TaskID   int      `json:"ti"`
	Type     int      `json:"t"`
	Needed   int64    `json:"p"`
	Free     int      `json:"f"`
	Ads      int      `json:"a"`
	Rewards  []string `json:"r"`
}

func Active(now time.Time) bool {
	a, _ := time.Parse(time.RFC3339, Start)
	b, _ := time.Parse(time.RFC3339, End)
	return !now.Before(a) && now.Before(b)
}
func Levels() []Level {
	levels := make([]Level, 50)
	weapons := map[int]int{10: 832001, 20: 1292001, 30: 1277001, 40: 1291001, 50: 1279001}
	for i := 1; i <= 50; i++ {
		free := 0
		if i == 1 || i%5 == 0 {
			free = 1
		}
		if i == 20 || i == 30 || i == 40 || i == 50 {
			free = 0
		}
		reward := fmt.Sprintf("1015:%d", 50+i*5)
		if i%2 == 0 {
			reward = fmt.Sprintf("2015:%d", 5+i/2)
		}
		if i%5 == 0 {
			reward = "26015:25"
		}
		cool := false
		if w, ok := weapons[i]; ok {
			reward = fmt.Sprintf("%d:1", w)
			cool = true
		}
		typ := 2
		if i == 1 {
			typ = 1
		}
		if i == 50 {
			typ = 3
		}
		levels[i-1] = Level{i, typ, (i-1)/10 + 1, int64(i-1) * CrownStep, []string{reward}, free, cool}
	}
	return levels
}

// Calendar tasks have unique indices for every day, avoiding a local-clock
// reset or reconnect granting yesterday's task again. Three daily objectives
// remain available on the stock calendar once unlocked, plus elite milestones.
func Tasks() []Task {
	tasks := []Task{}
	for day := 0; day < 60; day++ {
		for j, target := range []int64{3, 25, 1} {
			tasks = append(tasks, Task{1000 + day*3 + j, day, j + 1, 2, target, 1, 0, []string{"25015:60"}})
		}
	}
	for j, target := range []int64{100, 1000, 30} {
		tasks = append(tasks, Task{2000 + j, 0, j + 1, 3, target, 1, 0, []string{"25015:400"}})
	}
	return tasks
}
func FindTask(index int) (Task, bool) {
	if index == TutorialTaskIndex {
		// Native CreateTutorial uses the first cell threshold as its crown reward.
		return Task{TutorialTaskIndex, 0, TutorialTaskIndex, 4, 1, 1, 0, []string{fmt.Sprintf("25015:%d", Levels()[0].Exp)}}, true
	}
	for _, t := range Tasks() {
		if t.Index == index {
			return t, true
		}
	}
	return Task{}, false
}
func TaskOpen(t Task, now time.Time) bool {
	a, _ := time.Parse(time.RFC3339, Start)
	// Stock 23.1.3 task availability (0x18F29D4) compares StartDay to
	// floor((now-start)/86400)+1, not to the zero-based elapsed day. Keep
	// existing catalog indices/StartDay values to preserve earned receipts.
	return Active(now) && int(now.Sub(a)/(24*time.Hour))+1 >= t.StartDay
}
func Reward(token string) (index int, amount int64, err error) {
	p := strings.Split(token, ":")
	if len(p) != 2 {
		return 0, 0, fmt.Errorf("invalid reward")
	}
	index, err = strconv.Atoi(p[0])
	if err != nil {
		return
	}
	amount, err = strconv.ParseInt(p[1], 10, 64)
	if err == nil && (index <= 0 || amount <= 0) {
		err = fmt.Errorf("invalid reward bounds")
	}
	return
}
func Document() map[string]any {
	pages := []any{}
	levels := Levels()
	for p := 1; p <= 5; p++ {
		pages = append(pages, map[string]any{"p": p, "pi": levels[p*10-1].Rewards})
	}
	bases := []any{}
	for j, spec := range []struct{ key, action string }{{"Key_6085", "endMatch"}, {"Key_7765", "killPlayer"}, {"Key_6150", "endMatch"}} {
		action := map[string]any{"type": spec.action, "version": 1, "neededcount": 1, "gamemode": "None"}
		if j == 2 {
			action["shouldwin"] = 1
		}
		bases = append(bases, map[string]any{"i": j + 1, "tx": "", "d": spec.key, "sp": true, "as": action, "ts": map[string]any{}, "r": []any{}})
	}
	// Without this base, stock leaves its generated tutorial GameTask null:
	// the lobby renders "Play 0 matches" and can never unlock the pass.
	bases = append(bases, map[string]any{"i": TutorialTaskIndex, "tx": "", "d": "Key_6085", "sp": true, "as": map[string]any{"type": "endMatch", "version": 1, "neededcount": 1, "gamemode": "None"}, "ts": map[string]any{}, "r": []any{}})
	matches := []any{}
	for _, mode := range []int{0, 1, 2, 3, 4, 5, 8, 10, 12, 13, 14, 16, 17, 18, 19, 20, 24, 25, 26, 27, 28, 32, 33, 35, 36, 37, 40, 41} {
		matches = append(matches, map[string]any{"g": mode, "p": []int{20, 15, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10}, "s": 50})
	}
	return map[string]any{"configId": ConfigName, "c": map[string]any{"i": SeasonID, "sn": "bp_title_cyberpunk_bp", "s": Start, "e": End, "vc": 0, "etr": []string{"2015:100"}, "etp": []string{"2015:100"}, "tp": []string{"25015:60"}, "etf": 1, "lp": "26015:20", "pp": "26015:600", "po": "26015:600", "ps": 0, "ppb": "26015:1200", "ppo": "26015:1200", "dpb": 0, "pbl": 10, "sbpc": 86400, "hc": 60}, "p": pages, "l": levels, "t": Tasks(), "prt": []int{}, "tb": bases, "at": []any{}, "of": exchange.NativeOffers(), "r": map[string]any{"r": 25015, "m": matches}}
}
func JSON() []byte {
	b, err := json.Marshal(Document())
	if err != nil {
		panic(err)
	}
	return b
}
