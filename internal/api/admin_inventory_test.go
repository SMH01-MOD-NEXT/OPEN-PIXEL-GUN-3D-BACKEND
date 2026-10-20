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
func TestInventoryConsoleAssetsAreLocalizedAndEmbedded(t *testing.T) {
	html := inventoryConsoleHTML()
	if strings.Contains(html, "/* INVENTORY_PANEL */") || !strings.Contains(html, "mountInventory(id)") || !strings.Contains(html, "Пересинхронизац") && !strings.Contains(html, "пересинхронизац") {
		t.Fatal("missing assets")
	}
	if !strings.Contains(html, "if (selected !== id)") {
		t.Fatal("stale player request guard")
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
