// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func mailboxDraft() MailboxNewsDraft {
	return MailboxNewsDraft{Type: "Update", Header: MailboxText{Russian: "Заголовок"}, ShortDescription: MailboxText{English: "Summary"}, Description: MailboxText{Russian: "Текст", English: "Body"}}
}
func TestMailboxPublicationPersistenceAndAccountIsolation(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := s.RegisterOrLogin("mailbox-test", "Player", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(s.state)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.CreateMailboxNews(mailboxDraft())
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != 1 || n.Date <= 0 || int64(n.Changed) != n.Date || n.Header.English != "Заголовок" || n.ShortDescription.Russian != "Summary" {
		t.Fatal(n)
	}
	saved := s.MailboxNews()
	saved[0].Header.English = "mutated"
	if s.MailboxNews()[0].Header.English == "mutated" {
		t.Fatal("interior storage exposed")
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened.MailboxNews(), s.MailboxNews()) {
		t.Fatal("news lost across restart")
	}
	account, err := reopened.Account(a.PlayerID)
	if err != nil || !reflect.DeepEqual(account, a) {
		t.Fatal("account changed", err)
	}
	s.state.MailboxNews = nil
	after, _ := json.Marshal(s.state)
	if string(before) != string(after) {
		t.Fatal("publication changed existing player data")
	}
}
func TestMailboxValidationAndAtomicFailure(t *testing.T) {
	s, _ := Open(t.TempDir())
	for _, mutate := range []func(*MailboxNewsDraft){
		func(d *MailboxNewsDraft) { d.Type = "None" }, func(d *MailboxNewsDraft) { d.Type = "<script>" },
		func(d *MailboxNewsDraft) { d.Header = MailboxText{} }, func(d *MailboxNewsDraft) { d.ShortDescription = MailboxText{} }, func(d *MailboxNewsDraft) { d.Description = MailboxText{} },
		func(d *MailboxNewsDraft) { d.Header.English = strings.Repeat("a", 513) },
		func(d *MailboxNewsDraft) { d.ShortDescription.English = strings.Repeat("a", 2049) },
		func(d *MailboxNewsDraft) { d.Description.English = strings.Repeat("a", 16385) },
		func(d *MailboxNewsDraft) { d.Header.Russian = "bad\x00text" },
		func(d *MailboxNewsDraft) { d.Header.Russian = string([]byte{0xff}) },
	} {
		d := mailboxDraft()
		mutate(&d)
		if _, err := s.CreateMailboxNews(d); !errors.Is(err, ErrInvalidMailboxNews) {
			t.Fatal("invalid accepted", err)
		}
	}
	n, err := s.CreateMailboxNews(mailboxDraft())
	if err != nil {
		t.Fatal(err)
	}
	oldPath := s.path
	s.path = filepath.Join(t.TempDir(), "absent", "state.json")
	if _, err = s.CreateMailboxNews(mailboxDraft()); err == nil {
		t.Fatal("persistence failure ignored")
	}
	s.path = oldPath
	if news := s.MailboxNews(); len(news) != 1 || news[0].ID != n.ID {
		t.Fatal("failed publication escaped rollback")
	}
	reopened, err := Open(filepath.Dir(oldPath))
	if err != nil || len(reopened.MailboxNews()) != 1 {
		t.Fatal("disk changed", err)
	}
	if _, err = os.Stat(oldPath); err != nil {
		t.Fatal(err)
	}
}
func TestMailboxConcurrentBoundedCreation(t *testing.T) {
	s, _ := Open(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.CreateMailboxNews(mailboxDraft()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	seen := map[int]bool{}
	for _, n := range s.MailboxNews() {
		if seen[n.ID] {
			t.Fatal("duplicate ID")
		}
		seen[n.ID] = true
	}
	if len(seen) != 20 {
		t.Fatal(len(seen))
	}
	s.mu.Lock()
	s.state.MailboxNews = make([]MailboxNews, MailboxNewsLimit)
	s.mu.Unlock()
	if _, err := s.CreateMailboxNews(mailboxDraft()); !errors.Is(err, ErrMailboxFull) {
		t.Fatal(err)
	}
}

// Exact strings are grounded in the read-only 23.1.3 OfferItem parser:
// 0x29C53D4 numeric IDs -> catalog type; 0x29C5658 enum path -> type/id/count;
// 0x34104EC currency grid accepts only Gems=1000 and Coins=1010, not Currency=1200.
func TestMailboxStock2313CurrencyTokens(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rewards MailboxRewards
		want    []string
	}{
		{"empty", MailboxRewards{}, []string{}},
		{"coins", MailboxRewards{Coins: 1000}, []string{"Coins::1000"}},
		{"gems", MailboxRewards{Gems: 500}, []string{"Gems::500"}},
		{"weapon", MailboxRewards{Weapon: 203001}, []string{"203001:1"}},
		{"mixed", MailboxRewards{Coins: 3131, Gems: 1010, Weapon: 143001}, []string{"Coins::3131", "Gems::1010", "143001:1"}},
		{"maximum", MailboxRewards{Coins: 10000000, Gems: 10000000}, []string{"Coins::10000000", "Gems::10000000"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rewards.Tokens(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("stock tokens: got %q want %q", got, tc.want)
			}
		})
	}
}
