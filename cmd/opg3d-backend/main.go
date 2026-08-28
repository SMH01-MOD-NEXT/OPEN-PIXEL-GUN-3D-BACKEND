// SPDX-License-Identifier: GPL-3.0-or-later

// Command opg3d-backend runs the Open Pixel Gun 3D backend: the account,
// configuration, credential and update service that replaces the retired
// vendor services for a self-hosted private server.
package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/api"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/config"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/discovery"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/logs"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/store"
)

// version is stamped by the build: -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		// Nothing else is available yet if configuration or logging failed.
		os.Stderr.WriteString("fatal: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger, err := logs.New(cfg.LogDir, cfg.TraceBodies, cfg.MaxBodyLog)
	if err != nil {
		return err
	}
	defer logger.Close()

	st, err := store.OpenWithOptions(cfg.DataDir, store.Options{
		StableSessions: cfg.StableSessions,
	})
	if err != nil {
		return err
	}

	logger.Event("boot", map[string]any{
		"version":         version,
		"addr":            cfg.Addr,
		"data_dir":        cfg.DataDir,
		"log_dir":         cfg.LogDir,
		"trace_bodies":    cfg.TraceBodies,
		"discovery_port":  cfg.DiscoveryPort,
		"accounts":        len(st.Accounts()),
		"appid_sealing":   cfg.SealsAppIDs(),
		"admin_enabled":   cfg.AdminToken != "",
		"stable_sessions": cfg.StableSessions,
	})
	if cfg.StableSessions {
		logger.Printf("warn: OPG3D_STABLE_SESSIONS is on: session tokens are derived from the device id and are not secret. Diagnostic use only.")
	}
	if !cfg.SealsAppIDs() {
		logger.Printf("warn: Photon AppID delivery is disabled; set OPG3D_APPID_KEY and OPG3D_PHOTON_APPID_PUN or _REALTIME")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		descriptor := discovery.Descriptor{
			Magic:    discovery.Probe,
			Name:     cfg.ServerName,
			HTTPPort: httpPort(cfg.Addr),
			APIPath:  "/api/v1",
			Version:  version,
		}
		if err := discovery.Serve(ctx, cfg.DiscoveryPort, descriptor, logger); err != nil {
			// LAN discovery is a convenience; a manual address still works.
			logger.Event("discovery.unavailable", map[string]any{"err": err.Error()})
		}
	}()

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(cfg, logger, st),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Printf("listening on %s", cfg.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		logger.Printf("shutdown requested")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	logger.Printf("stopped cleanly")
	return nil
}

// httpPort extracts the port from a listen address so LAN discovery can
// advertise it. An unparsable address yields 0, which the client treats as
// "ask the user".
func httpPort(addr string) int {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	value, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return value
}
