// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

func TestAdminProfilePassCurrencyBalances(t *testing.T) {
	s, _ := newTestServer(t)
	account, _, err := s.st.AdoptSocketIdentity("612672163", "socket-device", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.SetProfile(account.PlayerID, "Before", 7, 100, 20, 3); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/players/" + account.PlayerID + "/profile"
	body := func() map[string]any {
		return map[string]any{"nick": "Before", "level": 7, "experience": 100, "coins": 20, "gems": 3}
	}
	edit := body()
	edit["pixel_pass_currency"] = 600
	edit["battle_pass_currency"] = 345
	if rec := do(t, s, http.MethodPut, path, edit, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unprotected currency edit: %d", rec.Code)
	}
	before, _ := s.st.Profile(account.PlayerID)
	rec := do(t, s, http.MethodPut, path, edit, adminAuthHeader)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	p, _ := s.st.Profile(account.PlayerID)
	if p.ProgressRevision != before.ProgressRevision+1 {
		t.Fatal("currency-only edit did not schedule one resync")
	}
	for _, doc := range []map[string]any{p.ProgressSlots, p.ProgressDocument} {
		c := doc["3"].(map[string]any)
		if fmt.Sprint(c["PixelPassCurrency"]) != "600" || fmt.Sprint(c["BattlePassCurrency"]) != "345" {
			t.Fatal("incorrect stock currency mapping", c)
		}
	}
	// Omission (old console/API clients) preserves balances; explicit zero clears.
	for _, edit := range []map[string]any{body(), body()} {
		edit["pixel_pass_currency"] = 0
		if rec := do(t, s, http.MethodPut, path, edit, adminAuthHeader); rec.Code != http.StatusOK {
			t.Fatal(rec.Body)
		}
	}
	p, _ = s.st.Profile(account.PlayerID)
	if fmt.Sprint(p.ProgressSlots["3"].(map[string]any)["PixelPassCurrency"]) != "0" || fmt.Sprint(p.ProgressSlots["3"].(map[string]any)["BattlePassCurrency"]) != "345" {
		t.Fatal("zero/omitted handling")
	}
	revision := p.ProgressRevision
	if rec := do(t, s, http.MethodPut, path, body(), adminAuthHeader); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	p, _ = s.st.Profile(account.PlayerID)
	if p.ProgressRevision != revision {
		t.Fatal("unchanged edit repeatedly schedules resync")
	}
	for _, key := range []string{"pixel_pass_currency", "battle_pass_currency"} {
		for _, value := range []any{-1, 2147483648, int64(9223372036854775807), 1.5, "100"} {
			edit := body()
			edit[key] = value
			edit["nick"] = "Must not save"
			before, _ := s.st.Profile(account.PlayerID)
			if rec := do(t, s, http.MethodPut, path, edit, adminAuthHeader); rec.Code != http.StatusBadRequest {
				t.Fatalf("accepted %s=%v: %d", key, value, rec.Code)
			}
			after, _ := s.st.Profile(account.PlayerID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid edit partially modified profile")
			}
		}
	}
	edit = body()
	edit["battle_pass_currency"] = 2147483647
	if rec := do(t, s, http.MethodPut, path, edit, adminAuthHeader); rec.Code != http.StatusOK {
		t.Fatal("valid int32 upper bound rejected", rec.Body)
	}
}
