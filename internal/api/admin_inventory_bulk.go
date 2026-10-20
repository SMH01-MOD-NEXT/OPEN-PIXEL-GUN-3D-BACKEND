// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"errors"
	"net/http"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

func (s *Server) inventoryEditError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInventoryConflict), errors.Is(err, store.ErrEquippedItem):
		s.writeJSON(w, 409, map[string]any{"error": err.Error()})
	case errors.Is(err, store.ErrInventoryPersistence):
		s.writeJSON(w, 503, map[string]any{"error": "inventory could not be saved; no change applied"})
	case errors.Is(err, store.ErrNotFound):
		s.writeAdminError(w, err)
	default:
		s.writeJSON(w, 400, map[string]any{"error": err.Error()})
	}
}
func (s *Server) handleAdminInventoryBulk(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	var edit store.InventoryBulkEdit
	if err := decodeStrictAdminJSON(r.Body, &edit); err != nil {
		s.writeJSON(w, 400, map[string]any{"error": "invalid bulk edit"})
		return
	}
	id := r.PathValue("id")
	if r.PathValue("mode") != "preview" && r.PathValue("mode") != "apply" {
		s.writeJSON(w, 404, map[string]any{"error": "unknown bulk operation"})
		return
	}
	if r.PathValue("mode") == "preview" {
		plan, err := s.st.PreviewInventoryBulk(id, edit)
		if err != nil {
			s.inventoryEditError(w, err)
			return
		}
		s.writeJSON(w, 200, plan)
		return
	}
	p, plan, changed, err := s.st.EditInventoryBulk(id, edit)
	if err != nil {
		s.inventoryEditError(w, err)
		return
	}
	if changed {
		s.log.Event("admin.inventory_bulk_edited", map[string]any{"player_id": p.PlayerID, "action": edit.Action, "changes": plan.Changes, "skipped_equipped": plan.SkippedEquipped, "revision": p.ProgressRevision})
	}
	s.writeJSON(w, 200, map[string]any{"player_id": p.PlayerID, "changed": changed, "revision": p.ProgressRevision, "updated_at": p.UpdatedAt, "plan": plan})
	if changed {
		s.reloginOnlinePlayerAfterProgressEdit(p.PlayerID, p.ProgressRevision)
	}
}
