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
