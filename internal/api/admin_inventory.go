// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"errors"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/catalog"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
	"io"
	"net/http"
	"strconv"
)

func (s *Server) handleAdminCatalog(w http.ResponseWriter, r *http.Request) {
	t, err := strconv.Atoi(r.URL.Query().Get("type"))
	if err != nil && r.URL.Query().Get("type") != "" {
		http.Error(w, "invalid type", 400)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if offset < 0 {
		offset = 0
	}
	if limit < 1 || limit > 100 {
		limit = 100
	}
	q := r.URL.Query().Get("q")
	if len(q) > 200 {
		http.Error(w, "query too long", 400)
		return
	}
	items := catalog.Search(q, t, r.URL.Query().Get("editable") == "1")
	total := len(items)
	if offset > total {
		offset = total
	}
	last := offset + limit
	if last > total {
		last = total
	}
	s.writeJSON(w, 200, map[string]any{"version": "23.1.3", "items": items[offset:last], "total": total, "offset": offset})
}
func (s *Server) handleAdminInventory(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.Profile(r.PathValue("id"))
	if err != nil {
		s.writeAdminError(w, err)
		return
	}
	s.writeJSON(w, 200, map[string]any{"player_id": p.PlayerID, "revision": p.ProgressRevision, "updated_at": p.UpdatedAt, "items": store.Inventory(p)})
}
func (s *Server) handleAdminEditInventory(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var edit store.InventoryEdit
	if err := dec.Decode(&edit); err != nil {
		http.Error(w, "invalid inventory edit", 400)
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "expected one inventory edit", 400)
		return
	}
	p, changed, err := s.st.EditInventory(r.PathValue("id"), edit)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInventoryConflict), errors.Is(err, store.ErrEquippedItem):
			s.writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		case errors.Is(err, store.ErrInventoryPersistence):
			s.writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "inventory could not be saved; no change applied"})
		case errors.Is(err, store.ErrNotFound):
			s.writeAdminError(w, err)
		default:
			s.writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		}
		return
	}
	if changed {
		s.log.Event("admin.inventory_edited", map[string]any{"player_id": p.PlayerID, "type": edit.Type, "index": edit.Index, "action": edit.Action, "quantity": edit.Quantity, "level": edit.Level, "rarity": edit.Rarity, "revision": p.ProgressRevision})
		s.reloginOnlinePlayerAfterProgressEdit(p.PlayerID, p.ProgressRevision)
	}
	s.writeJSON(w, 200, map[string]any{"player_id": p.PlayerID, "changed": changed, "revision": p.ProgressRevision, "updated_at": p.UpdatedAt, "items": store.Inventory(p)})
}
