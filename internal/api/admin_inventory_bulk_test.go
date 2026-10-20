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

func TestAdminBulkAuthPreviewConfirmationAndIsolation(t *testing.T) {
	s, _ := newTestServer(t)
	a, _, _ := s.st.AdoptSocketIdentity("612672163", "bulk-a", "")
	b, _, _ := s.st.AdoptSocketIdentity("612672164", "bulk-b", "")
	targetTransport := &testReloginTransport{}
	foreignTransport := &testReloginTransport{}
	addReloginTestSession(t, s, a.PlayerID, "bulk-a", targetTransport)
	addReloginTestSession(t, s, b.PlayerID, "bulk-b", foreignTransport)
	p, _ := s.st.Profile(a.PlayerID)
	other, _ := s.st.Profile(b.PlayerID)
	path := "/api/v1/admin/players/" + a.PlayerID + "/inventory/bulk/"
	e := store.InventoryBulkEdit{Action: "grant_weapons", Level: 65, Rarity: 5, Revision: p.ProgressRevision, UpdatedAt: p.UpdatedAt}
	for _, mode := range []string{"preview", "apply"} {
		if r := do(t, s, http.MethodPost, path+mode, e, nil); r.Code != 401 {
			t.Fatal(r.Code)
		}
	}
	r := do(t, s, http.MethodPost, path+"preview", e, adminAuthHeader)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var plan store.InventoryBulkPlan
	if err := json.Unmarshal(r.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if targetTransport.aborts != 0 || foreignTransport.aborts != 0 {
		t.Fatal("preview reconnected player")
	}
	if plan.Changes < 100 || len(plan.PlanToken) != 64 {
		t.Fatal("bad plan")
	}
	after, _ := s.st.Profile(a.PlayerID)
	if !reflect.DeepEqual(p, after) {
		t.Fatal("preview changed profile")
	}
	if r := do(t, s, http.MethodPost, path+"apply", e, adminAuthHeader); r.Code != 400 {
		t.Fatal("missing confirmation", r.Code)
	}
	e.PlanToken = plan.PlanToken
	e.ConfirmPlayerID = a.PlayerID
	r = do(t, s, http.MethodPost, path+"apply", e, adminAuthHeader)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	if targetTransport.aborts != 1 || foreignTransport.aborts != 0 {
		t.Fatal("unscoped/repeated bulk reconnect")
	}
	got, _ := s.st.Profile(a.PlayerID)
	if got.ProgressRevision != p.ProgressRevision+1 {
		t.Fatal("one revision required")
	}
	otherAfter, _ := s.st.Profile(b.PlayerID)
	if !reflect.DeepEqual(other, otherAfter) {
		t.Fatal("unrelated profile changed")
	}
	if r := do(t, s, http.MethodPost, path+"apply", e, adminAuthHeader); r.Code != 409 {
		t.Fatal("replay accepted", r.Code)
	}
	if r := do(t, s, http.MethodPost, path+"other", e, adminAuthHeader); r.Code != 404 {
		t.Fatal(r.Code)
	}
	if r := do(t, s, http.MethodPost, path+"preview", map[string]any{"action": "grant_weapons", "extra": true}, adminAuthHeader); r.Code != 400 {
		t.Fatal("unknown field accepted", r.Code)
	}
}

func TestAdminBulkPersistenceFailureDoesNotRelogin(t *testing.T) {
	s, _ := newTestServer(t)
	dir := t.TempDir()
	var err error
	s.st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := s.st.AdoptSocketIdentity("612672163", "failure-device", "")
	if err != nil {
		t.Fatal(err)
	}
	transport := &testReloginTransport{}
	addReloginTestSession(t, s, a.PlayerID, "failure-device", transport)
	p, _ := s.st.Profile(a.PlayerID)
	e := store.InventoryBulkEdit{Action: "grant_weapons", Level: 65, Rarity: 5, Revision: p.ProgressRevision, UpdatedAt: p.UpdatedAt}
	plan, err := s.st.PreviewInventoryBulk(p.PlayerID, e)
	if err != nil {
		t.Fatal(err)
	}
	e.PlanToken = plan.PlanToken
	e.ConfirmPlayerID = p.PlayerID
	path := filepath.Join(dir, "state.json")
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(path, "block"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	r := do(t, s, http.MethodPost, "/api/v1/admin/players/"+p.PlayerID+"/inventory/bulk/apply", e, adminAuthHeader)
	if r.Code != 503 || strings.Contains(r.Body.String(), dir) || transport.aborts != 0 {
		t.Fatal(r.Code, r.Body, transport.aborts)
	}
	after, _ := s.st.Profile(p.PlayerID)
	if !reflect.DeepEqual(p, after) {
		t.Fatal("failed bulk partially committed")
	}
}
