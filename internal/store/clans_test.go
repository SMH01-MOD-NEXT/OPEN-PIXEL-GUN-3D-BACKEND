// SPDX-License-Identifier: GPL-3.0-or-later
package store

import (
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"
)

var clanTestLogo = base64.StdEncoding.EncodeToString(append(append([]byte{}, pngSignature...), 0, 0, 0, 13))

func clanTestStore(t *testing.T, n int) (*Store, []string) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i := 0; i < n; i++ {
		name := string(rune('A'+i)) + "clanner"
		a, _, err := s.RegisterOrLogin(name, name, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.EnsureProfile(a.PlayerID, name); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.PlayerID)
	}
	return s, ids
}

func clanMust(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatal(what, err)
	}
}

func clanExpect(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: got %v want %v", what, err, want)
	}
}

func clanSetLevel(s *Store, player string, level int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Profiles[player].Level = level
}

func TestClanMembershipLifecycle(t *testing.T) {
	s, ids := clanTestStore(t, 5)
	a, b, c, d, e := ids[0], ids[1], ids[2], ids[3], ids[4]

	id, err := s.CreateClan(a, ClanCreate{Name: "Alpha", Logo: clanTestLogo, Slogan: "go", Type: ClanTypePublic, Continent: "eu"})
	clanMust(t, err, "create")
	if !ValidClanID(id) || len(id) != 8 {
		t.Fatal("minted id", id)
	}
	_, err = s.CreateClan(b, ClanCreate{Name: " alpha "})
	clanExpect(t, err, ErrClanNameTaken, "name taken")
	_, err = s.CreateClan(a, ClanCreate{Name: "Second"})
	clanExpect(t, err, ErrClanMember, "second clan")
	_, err = s.CreateClan(b, ClanCreate{Name: "Bad", Logo: "bm90IGEgcG5n"})
	clanExpect(t, err, ErrClanInvalid, "logo must be png")

	clanMust(t, s.JoinClan(b, id), "public join")
	clanExpect(t, s.JoinClan(b, id), ErrClanMember, "double join")
	if s.PlayerClanID(b) != id {
		t.Fatal("membership")
	}

	private, minRank := ClanTypePrivate, 5
	clanExpect(t, s.ChangeClanParams(b, id, ClanParams{Type: &private}), ErrClanForbidden, "soldier edits")
	clanMust(t, s.ChangeClanParams(a, id, ClanParams{Type: &private, MinRank: &minRank}), "leader edits")
	clanExpect(t, s.JoinClan(c, id), ErrClanClosed, "private join")
	clanExpect(t, s.AskToJoinClan(c, id, "hi"), ErrClanRank, "rank gate")
	clanSetLevel(s, c, 10)
	clanMust(t, s.AskToJoinClan(c, id, "hi"), "request")
	clanExpect(t, s.AnswerClanRequest(b, id, c, true), ErrClanForbidden, "soldier answers")
	clanMust(t, s.AnswerClanRequest(a, id, c, true), "accept request")
	if s.PlayerClanID(c) != id {
		t.Fatal("accepted member")
	}

	clanMust(t, s.InviteToClan(a, id, d), "invite")
	if got := s.ClanInvitesFor(d); len(got) != 1 || got[0] != id {
		t.Fatal("invites", got)
	}
	clanMust(t, s.AnswerClanInvite(d, id, true), "accept invite waives private/min rank")
	if len(s.ClanInvitesFor(d)) != 0 {
		t.Fatal("invite consumed")
	}

	clanMust(t, s.ChangeClanRole(a, id, b, ClanRoleOfficer), "promote")
	clanExpect(t, s.ChangeClanRole(b, id, c, ClanRoleOfficer), ErrClanForbidden, "officer promotes")
	clanMust(t, s.KickFromClan(b, id, c), "officer kicks soldier")
	clanExpect(t, s.KickFromClan(b, id, a), ErrClanForbidden, "officer kicks leader")
	clanMust(t, s.AskToJoinClan(c, id, "again"), "kicked player may ask again")

	clanMust(t, s.ChangeClanRole(a, id, b, ClanRoleLeader), "transfer")
	v, _ := s.ClanByID(id, a)
	if v.Creator != b || v.Role(b) != ClanRoleLeader || v.Role(a) != ClanRoleOfficer {
		t.Fatal("transfer roles", v.Creator, v.Role(a), v.Role(b))
	}
	clanMust(t, s.LeaveClan(b, id), "leader leaves")
	var cooldown *ClanCooldownError
	if err := s.JoinClan(b, id); !errors.As(err, &cooldown) || cooldown.Seconds() <= 0 {
		t.Fatal("leave cooldown", err)
	}
	v, _ = s.ClanByID(id, a)
	if v.Creator != a || v.Role(a) != ClanRoleLeader {
		t.Fatal("successor", v.Creator)
	}

	msg, clanID, err := s.SendClanMessage(d, "hello clan", "100.5")
	clanMust(t, err, "message")
	again, _, err := s.SendClanMessage(d, "hello clan", "100.5")
	clanMust(t, err, "retried message")
	if clanID != id || again.Stamp != msg.Stamp {
		t.Fatal("message dedupe", msg, again)
	}
	if _, _, err := s.SendClanMessage(e, "outsider", "1"); err == nil {
		t.Fatal("outsider chat")
	}

	clanExpect(t, s.DeleteClan(d, id), ErrClanForbidden, "soldier deletes")
	clanMust(t, s.DeleteClan(a, id), "leader deletes")
	if s.PlayerClanID(a) != "" || s.PlayerClanID(d) != "" {
		t.Fatal("deleted clan keeps members")
	}
	if _, ok := s.ClanByID(id, a); ok {
		t.Fatal("deleted clan visible")
	}
}

func TestClanSearchPersistenceAndAccountRemoval(t *testing.T) {
	s, ids := clanTestStore(t, 3)
	a, b, c := ids[0], ids[1], ids[2]
	id, err := s.CreateClan(a, ClanCreate{Name: "Night Owls", Type: ClanTypePublic})
	clanMust(t, err, "create")
	clanMust(t, s.JoinClan(b, id), "join")
	for _, q := range []string{"owl", id} {
		found, err := s.SearchClans(c, q)
		if err != nil || len(found) != 1 || found[0].ID != id {
			t.Fatal("search", q, found, err)
		}
	}
	if _, ok := s.LeadingClan(); ok {
		t.Fatal("leader banner without war wins")
	}

	reopened, err := Open(filepath.Dir(s.path))
	clanMust(t, err, "reopen")
	v, ok := reopened.ClanByID(id, a)
	if !ok || len(v.Members) != 2 || v.Name != "Night Owls" {
		t.Fatal("durability", v)
	}

	clanMust(t, s.DeleteAccount(a), "delete leader account")
	v, ok = s.ClanByID(id, b)
	if !ok || v.Creator != b || len(v.Members) != 1 {
		t.Fatal("leader account removal", v)
	}
}

func TestClanFortAndProgress(t *testing.T) {
	s, ids := clanTestStore(t, 3)
	a, b, c := ids[0], ids[1], ids[2]
	id, err := s.CreateClan(a, ClanCreate{Name: "Builders", Type: ClanTypePublic})
	clanMust(t, err, "create")
	clanMust(t, s.JoinClan(b, id), "join")

	cells := []ClanFieldCell{{X: 1, Y: 0, Z: 2, Item: 1017, Rotation: 90, Size: 1}}
	clanMust(t, s.UpdateClanField(a, id, cells, 50), "field")
	clanExpect(t, s.UpdateClanField(b, id, cells, 50), ErrClanForbidden, "soldier edits field")
	clanExpect(t, s.UpdateClanField(a, id, []ClanFieldCell{{Item: 999999999, Size: 1}}, 0), ErrClanInvalid, "unknown placeable")

	cmd := ClanProgressCommand{Receipt: "182:x1", Items: []ClanItemGrant{{UniqueID: "u1", Clan: id, Index: 1011, Count: 3, Level: 1}}}
	applied, err := s.ApplyClanProgress(b, []ClanProgressCommand{cmd})
	if err != nil || applied != 1 {
		t.Fatal("progress", applied, err)
	}
	applied, err = s.ApplyClanProgress(b, []ClanProgressCommand{cmd})
	if err != nil || applied != 0 {
		t.Fatal("retried progress", applied, err)
	}
	v, _ := s.ClanByID(id, a)
	if len(v.Items) != 1 || v.Items[0].Index != 1011 || v.Items[0].Count != 3 {
		t.Fatal("storage", v.Items)
	}
	if len(v.Field) != 1 || v.FieldPower != 50 {
		t.Fatal("field state", v.Field, v.FieldPower)
	}
	foreign := ClanProgressCommand{Receipt: "182:x2", Items: []ClanItemGrant{{UniqueID: "u2", Clan: "12345678", Index: 1011, Count: 1}}}
	if _, err := s.ApplyClanProgress(b, []ClanProgressCommand{foreign}); err == nil {
		t.Fatal("foreign clan progress accepted")
	}
	if _, err := s.ApplyClanProgress(c, []ClanProgressCommand{cmd}); err == nil {
		t.Fatal("outsider progress accepted")
	}

	until, err := s.ActivateClanPromo(a, id, 3600*1e9)
	if err != nil || until <= 0 {
		t.Fatal("promo", until, err)
	}
	if _, err := s.ActivateClanPromo(a, id, 3600*1e9); !errors.Is(err, ErrClanPending) {
		t.Fatal("promo twice", err)
	}
}
