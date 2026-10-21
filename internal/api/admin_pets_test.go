// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

func TestAdminPetGrantRetrySyncAndRevoke(t *testing.T) {
	s, session, revision := newOperatorEditedProgress(t)
	completeRevisionDownload(t, s, session, revision)
	before, _ := s.st.Profile(revisionTestPlayerID)
	path := "/api/v1/admin/players/" + revisionTestPlayerID + "/inventory"
	for _, q := range []string{"Fox", "pet_fox", "20003"} {
		response := do(t, s, http.MethodGet, "/api/v1/admin/catalog?type=80&editable=1&q="+q, nil, adminAuthHeader)
		var result struct {
			Items []catalog.Item `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 || len(result.Items) != 1 || result.Items[0].Name != "Fox" || result.Items[0].ID != "pet_fox" || !result.Items[0].Editable {
			t.Fatal(response.Code, response.Body)
		}
	}
	edit := store.InventoryEdit{Type: 80, Index: 20003, Action: "grant", Quantity: 1, Level: 1, Revision: before.ProgressRevision, UpdatedAt: before.UpdatedAt}
	for _, invalid := range []store.InventoryEdit{
		{Type: 80, Index: 20003, Action: "grant", Quantity: 2, Level: 1, Revision: before.ProgressRevision, UpdatedAt: before.UpdatedAt},
		{Type: 80, Index: 20003, Action: "grant", Quantity: 1, Level: 65, Revision: before.ProgressRevision, UpdatedAt: before.UpdatedAt},
		{Type: 80, Index: 20003, Action: "grant", Quantity: 1, Level: 1, Rarity: 5, Revision: before.ProgressRevision, UpdatedAt: before.UpdatedAt},
	} {
		if r := do(t, s, http.MethodPost, path, invalid, adminAuthHeader); r.Code != 400 {
			t.Fatal(r.Code, r.Body)
		}
	}
	unchanged, _ := s.st.Profile(revisionTestPlayerID)
	if !reflect.DeepEqual(before, unchanged) {
		t.Fatal("invalid pet grant changed profile")
	}
	response := do(t, s, http.MethodPost, path, edit, adminAuthHeader)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	granted, _ := s.st.Profile(revisionTestPlayerID)
	if granted.ProgressRevision != revision+1 {
		t.Fatal("grant revision", granted.ProgressRevision)
	}
	if r := do(t, s, http.MethodPost, path, edit, adminAuthHeader); r.Code != 409 {
		t.Fatal("stale retry accepted", r.Code)
	}
	edit.Revision, edit.UpdatedAt = granted.ProgressRevision, granted.UpdatedAt
	response = do(t, s, http.MethodPost, path, edit, adminAuthHeader)
	var retry struct{ Changed bool }
	if err := json.Unmarshal(response.Body.Bytes(), &retry); err != nil || response.Code != 200 || retry.Changed {
		t.Fatal("grant must be idempotent", response.Code, response.Body)
	}
	// A pre-edit client snapshot must not erase the pending server grant.
	s.answerReplaceProgress(session, sioHead{sioSlotsKey: wrapRevisionEcho(before.ProgressSlots)}, 41)
	afterStale, _ := s.st.Profile(revisionTestPlayerID)
	if !reflect.DeepEqual(afterStale.ProgressSlots["53"], granted.ProgressSlots["53"]) {
		t.Fatal("stale client removed pet")
	}
	download := revisionTestSession("pet-full-download")
	download.setProgressWriteRevision(granted.ProgressRevision)
	s.answerLastCommand(download, sioHead{}, 8)
	full := s.answerSyncFull(download, sioHead{sioSlotsKey: map[string]any{}}, 17)
	wire := full[sioSlotsKey].(map[string]any)["53"].(map[string]any)
	if !reflect.DeepEqual(wire, granted.ProgressSlots["53"]) {
		t.Fatal("pet not in full-sync response", wire)
	}
	s.answerPlayerSuccessInit(download, sioHead{}, 31)
	applied, _ := s.st.Profile(revisionTestPlayerID)
	if _, pending := applied.PendingProgressRevision("device"); pending {
		t.Fatal("pet revision not acknowledged")
	}
	edit.Action, edit.Revision, edit.UpdatedAt = "revoke", applied.ProgressRevision, applied.UpdatedAt
	response = do(t, s, http.MethodPost, path, edit, adminAuthHeader)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	final, _ := s.st.Profile(revisionTestPlayerID)
	for _, item := range store.Inventory(final) {
		if item.Type == 80 && item.Index == 20003 {
			t.Fatal("revoked pet still owned")
		}
	}
	if final.Coins != before.Coins || final.Gems != before.Gems || final.Level != before.Level || final.Experience != before.Experience {
		t.Fatal("pet edit changed economy")
	}
}
