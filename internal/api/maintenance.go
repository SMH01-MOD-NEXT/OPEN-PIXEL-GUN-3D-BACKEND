// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"errors"
	"net/http"
	"time"
)

// Empty endpoints disable the window. Windows are [start, end), evaluated on
// requests, so restarts, idle servers and clock jumps cannot lose a timer.
type maintenanceSchedule struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func (m maintenanceSchedule) validate() error {
	if m.Start == "" && m.End == "" {
		return nil
	}
	start, e1 := time.Parse(time.RFC3339, m.Start)
	end, e2 := time.Parse(time.RFC3339, m.End)
	if e1 != nil || e2 != nil || !end.After(start) {
		return errors.New("maintenance window needs RFC3339 start and a later end")
	}
	return nil
}
func (m maintenanceSchedule) utc() maintenanceSchedule {
	if m.Start == "" && m.End == "" {
		return m
	}
	start, _ := time.Parse(time.RFC3339, m.Start)
	end, _ := time.Parse(time.RFC3339, m.End)
	return maintenanceSchedule{start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)}
}
func (m maintenanceSchedule) active(now time.Time) bool {
	start, e1 := time.Parse(time.RFC3339, m.Start)
	end, e2 := time.Parse(time.RFC3339, m.End)
	return e1 == nil && e2 == nil && !now.Before(start) && now.Before(end)
}
func cloneAllowlist(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for id, allowed := range in {
		out[id] = allowed
	}
	return out
}
func (snap settingsSnapshot) patch() settingsPatch {
	return settingsPatch{Maintenance: &snap.MaintenanceManual, MaintenanceSchedule: &snap.MaintenanceSchedule, MaintenanceAllowlist: &snap.MaintenanceAllowlist, PhotonRegion: &snap.PhotonRegion, PhotonAppIDs: snap.PhotonAppIDs, Update: &snap.Update}
}
func (s *settings) maintenanceAllowed(playerID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return playerID != "" && s.maintenanceAllowlist[playerID]
}
func (s *settings) maintenanceFor(playerID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return (s.maintenance || s.maintenanceSchedule.active(time.Now())) && !(playerID != "" && s.maintenanceAllowlist[playerID])
}
func (s *settings) setMaintenanceAllowed(playerID string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := cloneAllowlist(s.maintenanceAllowlist)
	if enabled {
		ids[playerID] = true
	} else {
		delete(ids, playerID)
	}
	patch := settingsPatch{MaintenanceAllowlist: &ids}
	if err := patch.validate(); err != nil {
		return err
	}
	_, err := s.commitLocked(patch)
	return err
}
func (s *Server) handleAdminMaintenanceAccess(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !readSupportJSON(w, r, &body) || body.Enabled == nil {
		s.writeError(w, 400, "invalid_request", "")
		return
	}
	account, err := s.st.Account(r.PathValue("id"))
	if err != nil {
		s.writeError(w, 404, "not_found", "")
		return
	}
	if err = s.settings.setMaintenanceAllowed(account.PlayerID, *body.Enabled); err != nil {
		s.writeError(w, 500, "maintenance_save_failed", "")
		return
	}
	s.log.Event("maintenance.access_changed", map[string]any{"player_id": account.PlayerID, "enabled": *body.Enabled})
	s.writeJSON(w, 200, map[string]any{"player_id": account.PlayerID, "maintenance_allowed": *body.Enabled})
}
