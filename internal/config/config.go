// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads server configuration from the environment.
//
// Credentials (Photon AppIDs, the AppID encryption key, the admin token) are
// only ever read from the environment, never from a committed file, so the
// repository can be published under the GPL without redacting anything.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/secrets"
)

// Update describes the client build the server currently hands out.
type Update struct {
	Version   string `json:"version"`
	APKURL    string `json:"apk_url"`
	SHA256    string `json:"sha256"`
	NotesURL  string `json:"notes_url"`
	Mandatory bool   `json:"mandatory"`
}

// Config is the fully resolved runtime configuration.
type Config struct {
	ServerName    string
	Addr          string
	DataDir       string
	LogDir        string
	ContentDir    string
	TraceBodies   bool
	MaxBodyLog    int
	DiscoveryPort int
	AdminToken    string
	Maintenance   bool

	// StableSessions derives each account's session token from its device id
	// instead of minting random bytes, and pins the socket.io session id to a
	// fixed value.
	//
	// SECURITY-SENSITIVE DIAGNOSTIC, off by default. A deterministic session
	// token and a predictable socket id are both guessable credentials, so this
	// belongs on a lab machine only. It exists to answer one question about the
	// /sio payload codec: the client's encryption is deterministic, yet the same
	// auth_v2 event is different in every run, so some key input varies per
	// session. The first attempt pinned only the token and left the socket id
	// random, which could not separate the two candidates. Pinning both freezes
	// every per-session value this server originates, so a ciphertext that still
	// changes proves the varying input is client-local.
	StableSessions bool

	// PhotonAppIDs is keyed by mode: "pun", "realtime", "chat", "voice".
	PhotonAppIDs map[string]string
	PhotonRegion string

	// PhotonPUNFromRealtime records that "pun" is answered with the Realtime
	// AppID because no dedicated PUN AppID was configured.
	PhotonPUNFromRealtime bool

	// AppIDKey is the AES-256 key used to seal AppIDs. Empty disables the
	// endpoint instead of falling back to clear text.
	AppIDKey []byte

	Update Update
}

// Load resolves configuration from the environment.
func Load() (*Config, error) {
	cfg := &Config{
		ServerName:     env("OPG3D_SERVER_NAME", "open-pixel-gun-3d-backend"),
		Addr:           env("OPG3D_ADDR", ":8080"),
		DataDir:        env("OPG3D_DATA_DIR", "data"),
		LogDir:         env("OPG3D_LOG_DIR", "logs"),
		ContentDir:     env("OPG3D_CONTENT_DIR", "content"),
		TraceBodies:    envBool("OPG3D_TRACE_BODIES", true),
		MaxBodyLog:     envInt("OPG3D_MAX_BODY_LOG", 8192),
		DiscoveryPort:  envInt("OPG3D_DISCOVERY_PORT", 47318),
		AdminToken:     env("OPG3D_ADMIN_TOKEN", ""),
		Maintenance:    envBool("OPG3D_MAINTENANCE", false),
		StableSessions: envBool("OPG3D_STABLE_SESSIONS", false),
		PhotonAppIDs:   map[string]string{},
		PhotonRegion:   env("OPG3D_PHOTON_REGION", "eu"),
		Update: Update{
			Version:   env("OPG3D_UPDATE_VERSION", ""),
			APKURL:    env("OPG3D_UPDATE_APK_URL", ""),
			SHA256:    env("OPG3D_UPDATE_SHA256", ""),
			NotesURL:  env("OPG3D_UPDATE_NOTES_URL", ""),
			Mandatory: envBool("OPG3D_UPDATE_MANDATORY", false),
		},
	}

	for mode, key := range map[string]string{
		"pun":      "OPG3D_PHOTON_APPID_PUN",
		"realtime": "OPG3D_PHOTON_APPID_REALTIME",
		"chat":     "OPG3D_PHOTON_APPID_CHAT",
		"voice":    "OPG3D_PHOTON_APPID_VOICE",
	} {
		if value := env(key, ""); value != "" {
			cfg.PhotonAppIDs[mode] = value
		}
	}

	// PUN is a client library, not a Photon product: a PUN client connects with
	// the AppID of a Realtime application. Serving the Realtime AppID for "pun"
	// keeps the client from blocking on its AppID wait when only one Photon
	// application exists, which is the normal case for a private server.
	if cfg.PhotonAppIDs["pun"] == "" && cfg.PhotonAppIDs["realtime"] != "" {
		cfg.PhotonAppIDs["pun"] = cfg.PhotonAppIDs["realtime"]
		cfg.PhotonPUNFromRealtime = true
	}

	if material := env("OPG3D_APPID_KEY", ""); material != "" {
		key, err := secrets.DeriveKey(material)
		if err != nil {
			return nil, fmt.Errorf("config: OPG3D_APPID_KEY: %w", err)
		}
		cfg.AppIDKey = key
	}

	if cfg.DiscoveryPort < 0 || cfg.DiscoveryPort > 65535 {
		return nil, fmt.Errorf("config: OPG3D_DISCOVERY_PORT out of range: %d", cfg.DiscoveryPort)
	}
	return cfg, nil
}

// SealsAppIDs reports whether the AppID endpoint can serve anything.
func (c *Config) SealsAppIDs() bool {
	return len(c.AppIDKey) > 0 && len(c.PhotonAppIDs) > 0
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := env(key, "")
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	value := env(key, "")
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
