package store

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func directTaskClaim(idx int, amount int64, suffix string) []any {
	return []any{counterCommand(326, "crowns-"+suffix, map[string]any{"e": amount}), counterCommand(152, "claimed-"+suffix, map[string]any{fmt.Sprintf("5_%d", idx): map[string]any{"r": 1}})}
}
func completeDirectTask(t *testing.T, s *Store, ctx ProgressWriteContext, idx int, needed int64, now time.Time) {
	t.Helper()
	_, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{counterCommand(150, fmt.Sprintf("progress-%d", idx), map[string]any{fmt.Sprintf("5_%d", idx): map[string]any{"p": needed, "c": 1}})}, now)
	if err != nil {
		t.Fatal(err)
	}
}
func TestPixelPassNativeDirectTaskClaimsPersistWithoutMatchBudget(t *testing.T) {
	s, dir, ctx := passSetup(t)
	now := passNow.Add(24 * time.Hour)
	completeDirectTask(t, s, ctx, 1000, 3, now)
	completeDirectTask(t, s, ctx, 1003, 3, now)
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{counterCommand(326, "real-match", map[string]any{"e": 20})}, now); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Profile(socketUniqID)
	batches := [][]any{directTaskClaim(1003, 60, "4588e24e"), directTaskClaim(1000, 60, "f7ace037")}
	for _, b := range batches {
		if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, b, now); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range batches {
		if n, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, b, now); err != nil || n != 0 {
			t.Fatal("durable retry", n, err)
		}
	}
	for _, idx := range []int{1000, 1003} {
		if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, directTaskClaim(idx, 60, fmt.Sprintf("new-%d", idx)), now); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := s.Profile(socketUniqID)
	if ppInt(p.PixelPass.State, "e") != 140 || p.PixelPass.MatchCrowns != 20 || len(p.PixelPass.Claimed) != 2 || p.Coins != before.Coins || p.Gems != before.Gems || ppInt(p.PixelPass.State, "p") != ppInt(before.PixelPass.State, "p") {
		t.Fatal("task rewards changed match budget/economy", p.PixelPass)
	}
	for _, idx := range []int{1000, 1003} {
		if ppInt(objectAt(p.PixelPass.Tasks, fmt.Sprintf("5_%d", idx)), "r") != 1 {
			t.Fatal("reward flag missing")
		}
	}
}
func TestPixelPassDirectTaskClaimValidationAtomic(t *testing.T) {
	for _, tc := range []struct {
		name     string
		idx      int
		amount   int64
		complete bool
	}{
		{"incomplete", 1000, 60, false}, {"future", 1003, 60, false}, {"unknown", 9876, 60, false}, {"understated", 1000, 59, true}, {"inflated", 1000, 61, true}, {"missing-echo", 1000, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, ctx := passSetup(t)
			if tc.complete {
				completeDirectTask(t, s, ctx, 1000, 3, passNow)
			}
			before, _ := s.Profile(socketUniqID)
			if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, directTaskClaim(tc.idx, tc.amount, "invalid"), passNow); err == nil {
				t.Fatal("accepted invalid claim")
			}
			after, _ := s.Profile(socketUniqID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("partial mutation")
			}
		})
	}
	s, _, ctx := passSetup(t)
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, []any{counterCommand(326, "not-a-task", map[string]any{"e": 60})}, passNow); err == nil {
		t.Fatal("match limit bypass")
	}
}
func TestPixelPassDirectTaskConcurrentAndMultipleClaims(t *testing.T) {
	s, _, ctx := passSetup(t)
	completeDirectTask(t, s, ctx, 1000, 3, passNow)
	completeDirectTask(t, s, ctx, 1001, 25, passNow)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, directTaskClaim(1000, 60, fmt.Sprint(i)), passNow); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	batch := append(directTaskClaim(1000, 60, "multi0"), directTaskClaim(1001, 60, "multi1")...)
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, batch, passNow); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Profile(socketUniqID)
	if ppInt(p.PixelPass.State, "e") != 120 || p.PixelPass.MatchCrowns != 0 {
		t.Fatal("duplicate reward", p.PixelPass)
	}
}

func TestPixelPassDirectTaskPersistenceFailureAndElite(t *testing.T) {
	s, _, ctx := passSetup(t)
	completeDirectTask(t, s, ctx, 2000, 100, passNow)
	before, _ := s.Profile(socketUniqID)
	path := s.path
	s.path = path + "/missing/state.json"
	batch := directTaskClaim(2000, 400, "elite")
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, batch, passNow); err == nil {
		t.Fatal("persist should fail")
	}
	s.path = path
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed persistence changed profile")
	}
	if _, err := s.applyProfileCounterCommandsAt(socketUniqID, ctx, batch, passNow); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Profile(socketUniqID)
	if ppInt(after.PixelPass.State, "e") != 400 || after.PixelPass.MatchCrowns != 0 {
		t.Fatal("elite reward")
	}
}
