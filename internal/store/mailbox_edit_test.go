// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestMailboxEditScheduledPersistenceAndIdentity(t *testing.T) {
	s, dir, _ := syncedGameplayStore(t)
	at := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	n := rewardNews(t, s, &at)
	before, _ := s.Profile(socketUniqID)
	d := n.MailboxNewsDraft
	d.Header.Russian = "Изменённый заголовок"
	d.Rewards = MailboxRewards{Coins: 2626, Gems: 1010, Weapon: 143001}
	nextAt := at.Add(time.Hour)
	d.PublishAt = &nextAt
	updated, err := s.UpdateMailboxNews(n.ID, n.Changed, d)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != n.ID || updated.Changed <= n.Changed || updated.Date != nextAt.Unix() || updated.RewardWireVersion != mailboxRewardWireVersion || updated.Rewards != d.Rewards {
		t.Fatal(updated)
	}
	if !s.MailboxNewsForAdmin()[0].DeliveryEditable {
		t.Fatal("future gift unexpectedly locked")
	}
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("edit changed progress")
	}
	// Returned dates must not allow mutations outside the store lock.
	*updated.PublishAt = at
	if !s.MailboxNews()[0].PublishAt.Equal(nextAt) {
		t.Fatal("return aliases stored date")
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.MailboxNews(), reopened.MailboxNews()) {
		t.Fatal("edit did not persist")
	}
	current := s.MailboxNews()[0]
	same, err := s.UpdateMailboxNews(current.ID, current.Changed, current.MailboxNewsDraft)
	if err != nil || !reflect.DeepEqual(current, same) {
		t.Fatal("no-op changes cache version", err)
	}
	if _, err = s.UpdateMailboxNews(n.ID, n.Changed, d); !errors.Is(err, ErrMailboxConflict) {
		t.Fatal("stale update accepted", err)
	}
}

func TestMailboxEditPublishedLocksDeliveryAndPreservesClaims(t *testing.T) {
	s, _, ctx := syncedGameplayStore(t)
	n := rewardNews(t, s, nil)
	if _, err := s.ApplyProfileCounterCommands(socketUniqID, ctx, mailClaim(n, "before-edit")); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Profile(socketUniqID)
	d := n.MailboxNewsDraft
	d.Description.Russian = "Исправленный текст"
	updated, err := s.UpdateMailboxNews(n.ID, n.Changed, d)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Date != n.Date || updated.ID != n.ID || updated.Changed <= n.Changed {
		t.Fatal("identity/date changed", updated)
	}
	if s.MailboxNewsForAdmin()[0].DeliveryEditable {
		t.Fatal("published gift editable")
	}
	after, _ := s.Profile(socketUniqID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("text edit changed claims/progress")
	}
	if _, err = s.ApplyProfileCounterCommands(socketUniqID, ctx, mailClaim(updated, "after-edit-new-ci")); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Profile(socketUniqID)
	if after.Coins != before.Coins || after.Gems != before.Gems || !reflect.DeepEqual(after.MailboxClaims, before.MailboxClaims) {
		t.Fatal("edit rearmed gift")
	}
	for _, change := range []func(*MailboxNewsDraft){
		func(d *MailboxNewsDraft) { d.Rewards.Coins++ },
		func(d *MailboxNewsDraft) { d.Rewards = MailboxRewards{} },
		func(d *MailboxNewsDraft) { at := time.Now().Add(time.Hour); d.PublishAt = &at },
	} {
		d = updated.MailboxNewsDraft
		change(&d)
		if _, err = s.UpdateMailboxNews(n.ID, updated.Changed, d); !errors.Is(err, ErrMailboxDeliveryLocked) {
			t.Fatal("published delivery changed", err)
		}
	}
	// Even an unclaimed published text-only letter cannot be rescheduled to
	// bypass the lock and later turn into a second gift entitlement.
	text, err := s.CreateMailboxNews(mailboxDraft())
	if err != nil {
		t.Fatal(err)
	}
	d = text.MailboxNewsDraft
	d.Type = "DeveloperMessage"
	d.Rewards.Coins = 1
	if _, err = s.UpdateMailboxNews(text.ID, text.Changed, d); !errors.Is(err, ErrMailboxDeliveryLocked) {
		t.Fatal(err)
	}
}

func TestMailboxEditClaimedFutureAndDeadlineLocks(t *testing.T) {
	s, _, _ := syncedGameplayStore(t)
	at := time.Now().Add(time.Hour)
	n := rewardNews(t, s, &at)
	at = *n.PublishAt // creation normalizes to whole UTC seconds
	// An existing receipt must lock delivery even after a wall-clock rollback.
	p := s.state.Profiles[socketUniqID]
	p.MailboxClaims = map[string]bool{"1": true}
	s.state.Profiles[socketUniqID] = p
	if s.MailboxNewsForAdmin()[0].DeliveryEditable {
		t.Fatal("claimed future editable")
	}
	d := n.MailboxNewsDraft
	d.Rewards.Gems++
	if _, err := s.UpdateMailboxNews(n.ID, n.Changed, d); !errors.Is(err, ErrMailboxDeliveryLocked) {
		t.Fatal(err)
	}
	if mailboxDeliveryEditable(n, at, false) {
		t.Fatal("exact publication boundary editable")
	}
	if !mailboxDeliveryEditable(n, at.Add(-time.Nanosecond), false) {
		t.Fatal("prepublication unexpectedly locked")
	}
}

func TestMailboxEditValidationRollbackAndArchive(t *testing.T) {
	s, dir, _ := syncedGameplayStore(t)
	at := time.Now().Add(time.Hour)
	n := rewardNews(t, s, &at)
	snapshot, _ := json.Marshal(s.state)
	for _, change := range []func(*MailboxNewsDraft){
		func(d *MailboxNewsDraft) { d.Header = MailboxText{} },
		func(d *MailboxNewsDraft) { d.Rewards.Coins = -1 },
		func(d *MailboxNewsDraft) { d.Rewards.Gems = 10000001 },
		func(d *MailboxNewsDraft) { d.Rewards.Weapon = 999999999 },
		func(d *MailboxNewsDraft) { d.Type = "None" },
		func(d *MailboxNewsDraft) { t := time.Unix(2147483648, 0); d.PublishAt = &t },
	} {
		d := n.MailboxNewsDraft
		change(&d)
		if _, err := s.UpdateMailboxNews(n.ID, n.Changed, d); !errors.Is(err, ErrInvalidMailboxNews) {
			t.Fatal(err)
		}
	}
	if _, err := s.UpdateMailboxNews(n.ID, 0, n.MailboxNewsDraft); !errors.Is(err, ErrInvalidMailboxNews) {
		t.Fatal(err)
	}
	if _, err := s.UpdateMailboxNews(999, n.Changed, n.MailboxNewsDraft); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	oldPath := s.path
	s.path = filepath.Join(t.TempDir(), "missing", "state.json")
	d := n.MailboxNewsDraft
	d.Description.English = "changed"
	if _, err := s.UpdateMailboxNews(n.ID, n.Changed, d); err == nil {
		t.Fatal("disk failure ignored")
	}
	s.path = oldPath
	after, _ := json.Marshal(s.state)
	if string(snapshot) != string(after) {
		t.Fatal("failed edit leaked changes")
	}
	reopened, err := Open(dir)
	if err != nil || !reflect.DeepEqual(s.MailboxNews(), reopened.MailboxNews()) {
		t.Fatal("disk rollback failed", err)
	}
	if err = s.ArchiveMailboxNews(n.ID); err != nil {
		t.Fatal(err)
	}
	current := s.MailboxNews()[0]
	if _, err = s.UpdateMailboxNews(current.ID, current.Changed, d); !errors.Is(err, ErrMailboxArchived) {
		t.Fatal(err)
	}
	if s.MailboxNewsForAdmin()[0].DeliveryEditable {
		t.Fatal("archived delivery editable")
	}
}

func TestMailboxEditConcurrentRevisionHasSingleWinner(t *testing.T) {
	s, _, _ := syncedGameplayStore(t)
	n, err := s.CreateMailboxNews(mailboxDraft())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := n.MailboxNewsDraft
			d.Header.English = "new"
			_, err := s.UpdateMailboxNews(n.ID, n.Changed, d)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrMailboxConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 || len(s.MailboxNews()) != 1 {
		t.Fatalf("wins=%d", wins)
	}
}
