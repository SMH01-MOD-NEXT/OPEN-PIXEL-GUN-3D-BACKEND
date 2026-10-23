package clienterrors

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPrivacyPersistenceDedupAndGroups(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e := Event{ID: "one", Code: "unity_exception", Level: "error", Message: "NullReferenceException\nBearer secret-123\nhttps://host/auth?q=sensitive", Stack: "at A.B() in /data/user/game/file.cs\nuser@example.com"}
	if n, e2 := s.Add("a", "client", []Event{e}, now); e2 != nil || n != 1 {
		t.Fatal(n, e2)
	}
	if n, _ := s.Add("a", "client", []Event{e}, now); n != 0 {
		t.Fatal("retry duplicated")
	}
	if n, _ := s.Add("b", "client", []Event{e}, now); n != 1 {
		t.Fatal("cross-player dedup")
	}
	q := s.Query(Query{}, now)
	if q.Total != 1 || q.Groups[0].Count != 2 || q.Groups[0].Players != 2 || q.Summary.Errors != 2 {
		t.Fatal(q)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "client-errors.json"))
	for _, secret := range []string{"secret-123", "host/auth", "user@example.com", "/data/user"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("secret retained", secret)
		}
	}
	s, err = Open(dir)
	if err != nil || s.Query(Query{}, now).Summary.Events != 2 {
		t.Fatal("restart lost events", err)
	}
	if q = s.Query(Query{Player: "a", Source: "client", Level: "error", Text: "nullreference"}, now); q.Total != 1 || q.Groups[0].Count != 1 {
		t.Fatal(q)
	}
	if q = s.Query(Query{Player: "missing"}, now); q.Total != 0 || q.Summary.Events != 2 {
		t.Fatal("filter altered summary", q)
	}
}
func TestRetentionRatesConcurrencyAndBrokenStorage(t *testing.T) {
	s, _ := Open("")
	now := time.Now()
	e := Event{Code: "resync_required", Level: "warning", Message: "conflict"}
	for i := 0; i < MaxEvents+10; i++ {
		s.events = append(s.events, Event{At: now})
	}
	s.trim(now)
	if s.Query(Query{}, now).Summary.Retained != MaxEvents {
		t.Fatal("unbounded retention")
	}
	if s.Query(Query{}, now.Add(25*time.Hour)).Summary.Events != 0 {
		t.Fatal("bad 24h window")
	}
	if s.Query(Query{}, now.Add(Retention+time.Second)).Summary.Retained != 0 {
		t.Fatal("expired events visible")
	}
	s, _ = Open("")
	for i := 0; i < 60; i++ {
		e.ID = fmt.Sprint(i)
		if _, err := s.Add("a", "client", []Event{e}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Add("a", "client", []Event{e}, now); err != ErrRate {
		t.Fatal(err)
	}
	if _, err := s.Add("a", "client", []Event{e}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Add("b", "backend", []Event{e}, now); s.Query(Query{}, now) }()
	}
	wg.Wait()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "client-errors.json"), []byte("bad json"), 0600)
	broken, err := Open(dir)
	if err == nil || !broken.Query(Query{}, now).Summary.Unavailable {
		t.Fatal("corrupt storage hidden")
	}
	if _, err := broken.Add("a", "client", []Event{e}, now); err != ErrUnavailable {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "client-errors.json"))
	if string(raw) != "bad json" {
		t.Fatal("corrupt journal overwritten")
	}
}
func TestAtomicSaveFailure(t *testing.T) {
	s, _ := Open("")
	s.path = filepath.Join(t.TempDir(), "not-a-file")
	os.Mkdir(s.path, 0700)
	if _, err := s.Add("a", "backend", []Event{{Message: "fail"}}, time.Now()); err == nil {
		t.Fatal("disk failure hidden")
	}
	if !s.Query(Query{}, time.Now()).Summary.Unavailable {
		t.Fatal("disk failure hidden in dashboard")
	}
	if len(s.events) != 0 {
		t.Fatal("failed write published")
	}
}
