// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/config"
)

// settings holds the parts of the configuration an operator may change while
// the server is running.
//
// These values used to be readable only from the environment, so announcing a
// build, rotating a Photon AppID or opening a maintenance window cost a restart
// and dropped every connected player. They are seeded from the environment,
// overridden by whatever the operator saved in an earlier run, and served from
// memory afterwards.
//
// SECURITY: anything that protects the rest of the deployment is deliberately
// absent here and stays environment-only - the admin token and the AppID
// sealing key in particular. A leaked admin token must not be able to rewrite
// the secrets that guard everything else, so this type exposes no way to do it.
type settings struct {
	mu                   sync.RWMutex
	maintenance          bool
	maintenanceSchedule  maintenanceSchedule
	maintenanceAllowlist map[string]bool
	photonRegion         string
	photonAppIDs         map[string]string
	update               config.Update

	// path is where changes are persisted. Empty disables persistence, which
	// keeps the type usable in tests that have no data directory.
	path string
}

// settingsSnapshot is an immutable copy handed to request handlers so they never
// read the live fields without holding the lock.
type settingsSnapshot struct {
	Maintenance          bool                `json:"maintenance"`
	MaintenanceManual    bool                `json:"maintenance_manual"`
	MaintenanceSchedule  maintenanceSchedule `json:"maintenance_schedule"`
	MaintenanceAllowlist map[string]bool     `json:"maintenance_allowlist"`
	PhotonRegion         string              `json:"photon_region"`
	PhotonAppIDs         map[string]string   `json:"photon_app_ids"`
	Update               config.Update       `json:"update"`
}

// settingsPatch is the operator's request. Every field is optional: a nil field
// means "leave this alone" rather than "clear it", so the console can save one
// section without having to resend the others.
type settingsPatch struct {
	Maintenance          *bool                `json:"maintenance"`
	MaintenanceSchedule  *maintenanceSchedule `json:"maintenance_schedule,omitempty"`
	MaintenanceAllowlist *map[string]bool     `json:"maintenance_allowlist,omitempty"`
	PhotonRegion         *string              `json:"photon_region"`
	PhotonAppIDs         map[string]string    `json:"photon_app_ids"`
	Update               *config.Update       `json:"update"`
}

func newSettings(cfg *config.Config) *settings {
	s := &settings{
		maintenance:  cfg.Maintenance,
		photonRegion: cfg.PhotonRegion,
		photonAppIDs: map[string]string{},
		update:       cfg.Update,
	}
	for mode, appID := range cfg.PhotonAppIDs {
		s.photonAppIDs[mode] = appID
	}
	if cfg.DataDir != "" {
		s.path = filepath.Join(cfg.DataDir, "settings.json")
	}
	return s
}

// load applies whatever the operator saved in an earlier run. A missing file is
// the normal case on a fresh deployment and leaves the environment seed in
// place.
func (s *settings) load() error {
	if s.path == "" {
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var saved settingsPatch
	if err := json.Unmarshal(raw, &saved); err != nil {
		return err
	}
	if err := saved.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyLocked(saved)
	return nil
}

// validate checks a patch before any of it is applied, so a rejected request
// cannot leave half of the settings changed.
func (p settingsPatch) validate() error {
	if p.MaintenanceSchedule != nil {
		if err := p.MaintenanceSchedule.validate(); err != nil {
			return err
		}
	}
	if p.MaintenanceAllowlist != nil {
		if len(*p.MaintenanceAllowlist) > 10000 {
			return errors.New("maintenance allowlist is too large")
		}
		for id := range *p.MaintenanceAllowlist {
			if strings.TrimSpace(id) == "" || len(id) > 128 {
				return errors.New("invalid maintenance player id")
			}
		}
	}
	if p.PhotonRegion != nil && strings.TrimSpace(*p.PhotonRegion) == "" {
		return errors.New("the Photon region cannot be empty")
	}
	if p.Update != nil {
		version := strings.TrimSpace(p.Update.Version)
		apkURL := strings.TrimSpace(p.Update.APKURL)
		// An announced version with nowhere to download it from would put every
		// client into an update prompt it cannot satisfy.
		if version != "" && apkURL == "" {
			return errors.New("an announced update needs a download url")
		}
		if sum := strings.TrimSpace(p.Update.SHA256); sum != "" && len(sum) != 64 {
			return errors.New("the update checksum must be a 64 character sha256")
		}
	}
	return nil
}

// apply validates, applies and persists a patch, returning the resulting state.
func (s *settings) apply(patch settingsPatch) (settingsSnapshot, error) {
	if err := patch.validate(); err != nil {
		return settingsSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commitLocked(patch)
}

// Serialize disk writes with mutations and roll back failed saves. A successful
// response must mean both runtime and restart state agree.
func (s *settings) commitLocked(patch settingsPatch) (settingsSnapshot, error) {
	previous := s.snapshotLocked()
	s.applyLocked(patch)
	snap := s.snapshotLocked()
	if err := s.persist(snap); err != nil {
		s.applyLocked(previous.patch())
		return previous, err
	}
	return snap, nil
}

func (s *settings) applyLocked(patch settingsPatch) {
	if patch.MaintenanceSchedule != nil {
		s.maintenanceSchedule = patch.MaintenanceSchedule.utc()
	}
	if patch.MaintenanceAllowlist != nil {
		s.maintenanceAllowlist = make(map[string]bool)
		for id, allowed := range *patch.MaintenanceAllowlist {
			if allowed {
				s.maintenanceAllowlist[id] = true
			}
		}
	}

	if patch.Maintenance != nil {
		s.maintenance = *patch.Maintenance
	}
	if patch.PhotonRegion != nil {
		if region := strings.TrimSpace(*patch.PhotonRegion); region != "" {
			s.photonRegion = region
		}
	}
	if patch.PhotonAppIDs != nil {
		// A mode sent with an empty id is a removal, which is how the console
		// retires a mode without a separate endpoint.
		next := map[string]string{}
		for mode, appID := range patch.PhotonAppIDs {
			mode = strings.ToLower(strings.TrimSpace(mode))
			appID = strings.TrimSpace(appID)
			if mode == "" || appID == "" {
				continue
			}
			next[mode] = appID
		}
		s.photonAppIDs = next
	}
	if patch.Update != nil {
		s.update = config.Update{
			Version:   strings.TrimSpace(patch.Update.Version),
			APKURL:    strings.TrimSpace(patch.Update.APKURL),
			SHA256:    strings.TrimSpace(patch.Update.SHA256),
			NotesURL:  strings.TrimSpace(patch.Update.NotesURL),
			Mandatory: patch.Update.Mandatory,
		}
	}
}

func (s *settings) snapshot() settingsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *settings) snapshotLocked() settingsSnapshot {
	appIDs := make(map[string]string, len(s.photonAppIDs))
	for mode, appID := range s.photonAppIDs {
		appIDs[mode] = appID
	}
	return settingsSnapshot{
		Maintenance:          s.maintenance || s.maintenanceSchedule.active(time.Now()),
		MaintenanceManual:    s.maintenance,
		MaintenanceSchedule:  s.maintenanceSchedule,
		MaintenanceAllowlist: cloneAllowlist(s.maintenanceAllowlist),
		PhotonRegion:         s.photonRegion,
		PhotonAppIDs:         appIDs,
		Update:               s.update,
	}
}

// appID resolves one Photon mode.
func (s *settings) appID(mode string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	appID, ok := s.photonAppIDs[mode]
	return appID, ok
}

// persist writes the settings out through a temporary file and a rename, so an
// interrupted write cannot leave a half-written file that fails to parse on the
// next start.
func (s *settings) persist(snap settingsSnapshot) error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(snap.patch(), "", "  ")
	if err != nil {
		return err
	}
	temp := s.path + ".tmp"
	if err := os.WriteFile(temp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, s.path)
}

// masked hides all but the tail of an AppID. The console needs to show which
// modes are configured without putting the live ids on screen.
func masked(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 6 {
		return strings.Repeat("*", len(value))
	}
	return strings.Repeat("*", len(value)-6) + value[len(value)-6:]
}

// settingsView is the console-facing shape of the runtime settings. The Photon
// ids are reduced to masked tails: the operator needs to see which modes are
// configured, and a console screen is not a reason to put live credentials on
// it.
func (s *Server) settingsView() map[string]any {
	snap := s.settings.snapshot()
	appIDs := make(map[string]string, len(snap.PhotonAppIDs))
	for mode, appID := range snap.PhotonAppIDs {
		appIDs[mode] = masked(appID)
	}
	return map[string]any{
		"maintenance":          snap.Maintenance,
		"maintenance_manual":   snap.MaintenanceManual,
		"maintenance_schedule": snap.MaintenanceSchedule,
		"photon_region":        snap.PhotonRegion,
		"photon_app_ids":       appIDs,
		"update":               snap.Update,
		"persisted":            s.settings.path != "",
	}
}

// handleAdminSettings serves the values an operator may change at runtime.
func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.settingsView())
}

// handleAdminSaveSettings applies an operator patch and saves it to disk, so
// announcing a build, rotating a Photon AppID or opening a maintenance window
// no longer costs a restart and every connected player with it.
func (s *Server) handleAdminSaveSettings(w http.ResponseWriter, r *http.Request) {
	raw, err := readAll(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "read_failed", err.Error())
		return
	}
	var patch settingsPatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	snap, err := s.settings.apply(patch)
	if err != nil {
		// validate() runs before anything is applied, so a rejected patch left
		// the settings untouched.
		s.writeError(w, http.StatusBadRequest, "settings_rejected", err.Error())
		return
	}
	modes := make([]string, 0, len(snap.PhotonAppIDs))
	for mode := range snap.PhotonAppIDs {
		modes = append(modes, mode)
	}
	sort.Strings(modes)
	s.log.Event("settings.updated", map[string]any{
		"maintenance":          snap.Maintenance,
		"maintenance_manual":   snap.MaintenanceManual,
		"maintenance_schedule": snap.MaintenanceSchedule,
		"photon_region":        snap.PhotonRegion,
		"photon_modes":         modes,
		"update_version":       snap.Update.Version,
	})
	s.writeJSON(w, http.StatusOK, s.settingsView())
}
