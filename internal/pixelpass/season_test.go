// SPDX-License-Identifier: GPL-3.0-or-later
package pixelpass

import (
	"encoding/json"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
	"testing"
	"time"
)

func TestFrozenStockContract(t *testing.T) {
	levels := Levels()
	if len(levels) != 50 {
		t.Fatal("tiers")
	}
	free, premium := 0, 0
	for i, l := range levels {
		if l.Level != i+1 || l.Exp != int64(i)*100 || l.Page != (i/10)+1 {
			t.Fatal(l)
		}
		if l.Free == 1 {
			free++
		} else {
			premium++
		}
		for _, r := range l.Rewards {
			idx, n, err := Reward(r)
			if err != nil || n <= 0 || len(catalog.ForIndex(idx)) == 0 {
				t.Fatal(r, err)
			}
		}
	}
	if free == 0 || premium == 0 {
		t.Fatal("tracks")
	}
	ids := map[int]bool{}
	for _, task := range Tasks() {
		if ids[task.Index] || task.Needed < 1 {
			t.Fatal(task)
		}
		ids[task.Index] = true
		for _, r := range task.Rewards {
			idx, n, err := Reward(r)
			if err != nil || idx != 25015 || n < 1 {
				t.Fatal(r)
			}
		}
	}
	if len(ids) != 183 {
		t.Fatal("calendar/elite count")
	}
	var doc map[string]any
	if err := json.Unmarshal(JSON(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, v := range doc["tb"].([]any) {
		a := v.(map[string]any)["as"].(map[string]any)
		if a["gamemode"] != "None" {
			t.Fatal("23.1.3 has None=100, NOT Any", a)
		}
	}
	start, _ := time.Parse(time.RFC3339, Start)
	end, _ := time.Parse(time.RFC3339, End)
	if !Active(start) || Active(start.Add(-time.Second)) || Active(end) || end.Sub(start) != 60*24*time.Hour {
		t.Fatal("season boundaries")
	}
}

func TestGeneratedTutorialHasNativeBase(t *testing.T) {
	tutorial, ok := FindTask(TutorialTaskIndex)
	if !ok || tutorial.TaskID != 9999 || tutorial.Type != 4 || tutorial.Needed != 1 || tutorial.Free != 1 || tutorial.Rewards[0] != "25015:0" {
		t.Fatal("native generated tutorial contract", tutorial)
	}
	for _, task := range Tasks() {
		if task.Index == TutorialTaskIndex {
			t.Fatal("stock generates tutorial; must not duplicate in calendar")
		}
	}
	found := 0
	for _, raw := range Document()["tb"].([]any) {
		base := raw.(map[string]any)
		if base["i"] != TutorialTaskIndex {
			continue
		}
		found++
		action := base["as"].(map[string]any)
		if action["type"] != "endMatch" || action["gamemode"] != "None" || action["neededcount"] != 1 || action["shouldwin"] != nil || base["d"] != "Key_6085" {
			t.Fatal("tutorial must require exactly one completed match, not a win", base)
		}
	}
	if found != 1 {
		t.Fatal("missing/duplicate tutorial base", found)
	}
	if _, _, err := Reward("25015:0"); err == nil {
		t.Fatal("tutorial zero reward must not relax generic economy validation")
	}
}

func TestTaskOpenMatchesNativeOneBasedCalendar(t *testing.T) {
	start, _ := time.Parse(time.RFC3339, Start)
	end, _ := time.Parse(time.RFC3339, End)
	for _, tc := range []struct {
		name string
		day  int
		at   time.Time
		want bool
	}{
		{"before-season", 0, start.Add(-time.Nanosecond), false},
		{"legacy-zero", 0, start, true}, {"first-day", 1, start, true},
		{"second-day-locked", 2, start.Add(24*time.Hour - time.Nanosecond), false},
		{"second-day-boundary", 2, start.Add(24 * time.Hour), true},
		{"third-day-locked", 3, start.Add(24 * time.Hour), false},
		{"last-day", 60, end.Add(-time.Nanosecond), true},
		{"after-season", 1, end, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TaskOpen(Task{StartDay: tc.day, Type: 2}, tc.at); got != tc.want {
				t.Fatalf("TaskOpen = %v, want %v", got, tc.want)
			}
		})
	}
}
