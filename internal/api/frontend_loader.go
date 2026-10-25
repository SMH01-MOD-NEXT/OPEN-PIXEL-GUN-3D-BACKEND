package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/frontendrelease"
)

func (s *Server) frontendDir() string {
	if p := os.Getenv("OPG3D_FRONTEND_DIR"); p != "" {
		return p
	}
	return filepath.Join(s.cfg.ContentDir, "frontend")
}
func (s *Server) handleLoaderInfo(w http.ResponseWriter, r *http.Request) {
	country := strings.ToUpper(os.Getenv("OPG3D_SERVER_COUNTRY"))
	if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
		country = "ZZ"
	}
	// The loader has no player identity. Keep native delivery available so the
	// game can authenticate and apply the per-player TechnicalWorks decision.
	snap := s.settings.snapshot()
	_, err := frontendrelease.Read(s.frontendDir())
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, 200, map[string]any{"protocol": 1, "game_version": frontendrelease.GameVersion, "abi": "arm64-v8a", "server_name": s.cfg.ServerName, "country": country, "maintenance": false, "game_maintenance": snap.Maintenance, "release_available": err == nil})
}
func (s *Server) handleFrontendManifest(w http.ResponseWriter, r *http.Request) {
	v, err := frontendrelease.Read(s.frontendDir())
	if err != nil {
		s.writeError(w, 503, "frontend_unavailable", "No valid signed native release published")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Write(v.Envelope)
}
func (s *Server) handleFrontendBinary(w http.ResponseWriter, r *http.Request) {
	bin, err := frontendrelease.ReadBinary(s.frontendDir(), r.PathValue("hash"))
	if err != nil {
		s.writeError(w, 404, "frontend_not_found", "Native release not found")
		return
	}
	if rec, ok := w.(*recorder); ok {
		rec.skipBody = true
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "public,max-age=31536000,immutable")
	w.Header().Set("ETag", "\""+r.PathValue("hash")+"\"")
	w.Header().Set("Content-Length", strconv.Itoa(len(bin)))
	w.Write(bin)
}
