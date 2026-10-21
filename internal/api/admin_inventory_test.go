// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAdminInventoryAuthCatalogAndScopedEdits(t *testing.T) {
	s, _ := newTestServer(t)
	a, _, err := s.st.AdoptSocketIdentity("612672163", "inventory-device", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := s.st.AdoptSocketIdentity("612672164", "other-device", "")
	other, _ := s.st.Profile(b.PlayerID)
	for _, path := range []string{"/api/v1/admin/catalog", "/api/v1/admin/players/" + a.PlayerID + "/inventory"} {
		r := do(t, s, http.MethodGet, path, nil, nil)
		if r.Code != http.StatusUnauthorized {
			t.Fatal(r.Code)
		}
	}
	rec := do(t, s, http.MethodGet, "/api/v1/admin/catalog?q=Future+Police&type=10", nil, adminAuthHeader)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "727001") {
		t.Fatal(rec.Code, rec.Body)
	}
	rec = do(t, s, http.MethodGet, "/api/v1/admin/catalog?offset=999999", nil, adminAuthHeader)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	p, _ := s.st.Profile(a.PlayerID)
	edit := store.InventoryEdit{Type: 10, Index: 727001, Action: "grant", Quantity: 1, Level: 65, Rarity: 5, Revision: p.ProgressRevision, UpdatedAt: p.UpdatedAt}
	path := "/api/v1/admin/players/" + a.PlayerID + "/inventory"
	rec = do(t, s, http.MethodPost, path, edit, adminAuthHeader)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	var result struct {
		Changed  bool                  `json:"changed"`
		Revision uint64                `json:"revision"`
		Items    []store.InventoryItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Revision != p.ProgressRevision+1 || len(result.Items) != 1 {
		t.Fatal(result)
	}
	after, _ := s.st.Profile(b.PlayerID)
	if after.ProgressRevision != other.ProgressRevision || !after.UpdatedAt.Equal(other.UpdatedAt) {
		t.Fatal("unrelated account modified")
	}
	rec = do(t, s, http.MethodPost, path, edit, adminAuthHeader)
	if rec.Code != http.StatusConflict {
		t.Fatal("stale edit", rec.Code)
	}
	p, _ = s.st.Profile(a.PlayerID)
	edit.Revision = p.ProgressRevision
	edit.UpdatedAt = p.UpdatedAt
	edit.Action = "revoke"
	rec = do(t, s, http.MethodPost, path, edit, adminAuthHeader)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	for _, body := range []string{`{"type":10,"extra":1}`, `{} {}`, strings.Repeat("x", 17000)} {
		rec = doRawAdmin(t, s, http.MethodPost, path, body)
		if rec.Code != 400 {
			t.Fatal("malformed", rec.Code)
		}
	}
}
func TestAdminCatalogExposesAndGrantsPet(t *testing.T) {
	s, _ := newTestServer(t)
	identity, _, err := s.st.AdoptSocketIdentity("612672165", "pet-inventory-device", "")
	if err != nil {
		t.Fatal(err)
	}
	resp := do(t, s, http.MethodGet, "/api/v1/admin/catalog?q=pet_alien_cat&type=80&editable=1", nil, adminAuthHeader)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "pet_alien_cat") {
		t.Fatal(resp.Code, resp.Body)
	}
	p, _ := s.st.Profile(identity.PlayerID)
	edit := store.InventoryEdit{Type: 80, Index: 1003, Action: "grant", Quantity: 1, Level: 37, Revision: p.ProgressRevision, UpdatedAt: p.UpdatedAt}
	path := "/api/v1/admin/players/" + identity.PlayerID + "/inventory"
	resp = do(t, s, http.MethodPost, path, edit, adminAuthHeader)
	if resp.Code != http.StatusOK {
		t.Fatal(resp.Code, resp.Body)
	}
	var result struct {
		Changed bool                  `json:"changed"`
		Items   []store.InventoryItem `json:"items"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatal("pet grant did not change inventory", result)
	}
	for _, item := range result.Items {
		if item.Type == 80 && item.Index == 1003 && item.Quantity == 1 && item.Level == 37 {
			return
		}
	}
	t.Fatal("pet not returned by admin inventory", result.Items)
}

func TestAdminModuleCatalogAndGrant(t *testing.T) {
	s, _ := newTestServer(t)
	response := do(t, s, http.MethodGet, "/api/v1/admin/catalog?q=mod_regenerator_armor&type=1150", nil, adminAuthHeader)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body)
	}
	var result struct {
		Items []struct {
			Name     string `json:"name"`
			Editable bool   `json:"editable"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Items) != 1 || result.Items[0].Name != "Armor Regeneration" || !result.Items[0].Editable {
		t.Fatal("module should expose its readable label and editable storage adapter", response.Code, response.Body, err)
	}
	response = do(t, s, http.MethodGet, "/api/v1/admin/catalog?q=mod_regenerator_armor&type=1150&editable=1", nil, adminAuthHeader)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "mod_regenerator_armor") {
		t.Fatal("editable module missing from catalog", response.Code, response.Body)
	}
	identity, _, err := s.st.AdoptSocketIdentity("612672166", "module-inventory-device", "")
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := s.st.Profile(identity.PlayerID)
	for _, item := range store.Inventory(profile) {
		if item.Type == 1150 && item.Index == 326009 && item.Quantity > 0 {
			t.Fatal("module fixture must start unowned")
		}
	}
	edit := store.InventoryEdit{Type: 1150, Index: 326009, Action: "grant", Quantity: 1, Level: 7, Revision: profile.ProgressRevision, UpdatedAt: profile.UpdatedAt}
	path := "/api/v1/admin/players/" + identity.PlayerID + "/inventory"
	response = do(t, s, http.MethodPost, path, edit, adminAuthHeader)
	if response.Code != http.StatusOK {
		t.Fatal("module grant", response.Code, response.Body)
	}
	profile, _ = s.st.Profile(identity.PlayerID)
	storage := profile.ProgressSlots["27"].(map[string]any)
	regenerator := storage["5"].([]any)
	points := profile.ProgressSlots["26"].(map[string]any)["c"].(map[string]any)["1155"].(map[string]any)
	if len(regenerator) != 1 || regenerator[0] != int64(326009) || points["368009"] != int64(1) {
		t.Fatalf("module grant did not write linked storage and point ownership: storage=%#v points=%#v", storage, points)
	}
	for _, item := range store.Inventory(profile) {
		if item.Type == 1150 && item.Index == 326009 && item.Quantity == 1 && item.Level == 7 {
			return
		}
	}
	t.Fatal("module grant not returned by inventory", store.Inventory(profile))
}

func TestInventoryConsoleAssetsAreLocalizedAndEmbedded(t *testing.T) {
	html := inventoryConsoleHTML()
	for _, marker := range []string{`id="ownedSearch"`, `id="catalogSearch"`, `id="playerTabs"`, `data-route="support"`, `/inventory/bulk/preview`, `/inventory/bulk/apply`, `seq !== playerSeq`, `seq !== catalogSeq`, `grantAllWear`, `grantAllArmor`, `grantAllGadgets`, `grantAllPets`, `grantAllModules`, `data-route="settings"`, `const LOCALES`, `bulkGrantModulesHint`, `settingsDescription`, `Theme and language`, `derivedModuleName`, `пересинхронизац`} {
		if !strings.Contains(html, marker) {
			t.Fatalf("console missing %s", marker)
		}
	}
	if strings.Contains(html, "/* CONSOLE_") {
		t.Fatal("unexpanded asset")
	}
}

func TestInventoryPersistenceFailureRejectsAPIAndGameplayACK(t *testing.T) {
	s, _ := newTestServer(t)
	dir := t.TempDir()
	var err error
	s.st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.st.AdoptSocketIdentity(revisionTestPlayerID, "device", "")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.st.Profile(revisionTestPlayerID)
	path := filepath.Join(dir, "state.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "blocked"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	edit := store.InventoryEdit{Type: 10, Index: 727001, Action: "grant", Quantity: 1, Level: 65, Rarity: 5, Revision: before.ProgressRevision, UpdatedAt: before.UpdatedAt}
	rec := do(t, s, http.MethodPost, "/api/v1/admin/players/"+revisionTestPlayerID+"/inventory", edit, adminAuthHeader)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), dir) {
		t.Fatal("failed edit acknowledged or leaked path", rec.Code)
	}
	head := gameplayBatch("inventory-persist-fail", 132, map[string]any{"i": 727001, "u": map[string]any{"l": 65, "r": 5}})
	body := s.answerUpdateProgress(revisionTestSession("persist-fail"), head, 49)
	if body["err_code"] == sioErrNone || body["i"] != head["i"] {
		t.Fatal("failed ownership write acknowledged")
	}
	after, _ := s.st.Profile(revisionTestPlayerID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed writes changed profile")
	}
}

func TestInventoryPurchaseACKFollowsDurableOwnershipAndCharge(t *testing.T) {
	s, session, rev := newOperatorEditedProgress(t)
	completeRevisionDownload(t, s, session, rev)
	head := sioHead{"id": float64(63), "i": "purchase", "p": map[string]any{"c": []any{
		map[string]any{"id": float64(66), "ci": "pay-inventory", "p": map[string]any{"c": "Coins", "v": float64(100)}},
		map[string]any{"id": float64(132), "ci": "own-inventory", "p": map[string]any{"i": float64(727001), "u": map[string]any{"l": float64(65), "r": float64(5)}}},
	}}}
	for i := 0; i < 2; i++ {
		if body := s.answerUpdateProgress(session, head, 50+i); body["err_code"] != sioErrNone {
			t.Fatal(body)
		}
	}
	p, _ := s.st.Profile(revisionTestPlayerID)
	if p.Coins != 123123123-100 {
		t.Fatal("purchase double charged")
	}
	owned := false
	for _, v := range store.Inventory(p) {
		if v.Type == 10 && v.Index == 727001 {
			owned = true
		}
	}
	if !owned {
		t.Fatal("ownership ACK without stored ownership")
	}
}

func TestInventoryConsoleLocalesHaveMatchingKeys(t *testing.T) {
	locales := make([]map[string]any, 0, 2)
	for _, name := range []string{"console.ru.json", "console.en.json"} {
		raw, err := inventoryAssets.ReadFile("admin_assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var locale map[string]any
		if err := json.Unmarshal(raw, &locale); err != nil {
			t.Fatal(err)
		}
		locales = append(locales, locale)
	}
	if len(locales[0]) != len(locales[1]) {
		t.Fatalf("locale key count differs: ru=%d en=%d", len(locales[0]), len(locales[1]))
	}
	for key := range locales[0] {
		if _, ok := locales[1][key]; !ok {
			t.Fatalf("English locale missing %q", key)
		}
	}
}
