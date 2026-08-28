// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads server configuration from the environment.
//
// Credentials (Photon AppIDs, the AppID encryption key, the admin token) are
// only ever read from the environment, never from a committed file, so the
// repository can be published under the GPL without redacting anything.
package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/secrets"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
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

	// SioReply answers events on the encrypted /sio channel instead of only
	// tracing them. On by default, because an unanswered auth_v2 is exactly what
	// holds this client's loading bar at 78%: silence is not a safe fallback. It
	// is a switch because a wrong answer can be worse than none - a malformed
	// packet may read to the client as a protocol error - so the trace-only
	// behaviour can be restored without a rebuild.
	SioReply bool

	// SioKey, SioIV and SioMode pin the /sio payload codec to key material
	// captured from a traced client launch, skipping the key derivation.
	//
	// SECURITY-SENSITIVE DIAGNOSTIC, off by default. The client derives its
	// cipher password per connection from an input that includes the socket.io
	// session id, and that derivation is not reproduced yet, so the key cannot
	// be computed here. A captured key can still be pinned by hand, which is
	// only useful together with StableSessions: the same sid has to be issued
	// again for the captured key to be the one the client actually uses.
	SioKey  []byte
	SioIV   []byte
	SioMode sio.Mode

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
		SioReply:       envBool("OPG3D_SIO_REPLY", true),
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

	if err := loadSioCodec(cfg); err != nil {
		return nil, err
	}

	if cfg.DiscoveryPort < 0 || cfg.DiscoveryPort > 65535 {
		return nil, fmt.Errorf("config: OPG3D_DISCOVERY_PORT out of range: %d", cfg.DiscoveryPort)
	}
	return cfg, nil
}

// loadSioCodec resolves the pinned /sio codec material.
//
// The key and the IV have to arrive together: half a codec cannot decrypt
// anything, so it is reported as a configuration error instead of being
// silently dropped. The mode defaults to the variant measured against real
// traffic and only has to be given when re-testing that conclusion.
func loadSioCodec(cfg *Config) error {
	keyHex, ivHex := env("OPG3D_SIO_KEY", ""), env("OPG3D_SIO_IV", "")
	if keyHex == "" && ivHex == "" {
		return nil
	}
	if keyHex == "" || ivHex == "" {
		return fmt.Errorf("config: OPG3D_SIO_KEY and OPG3D_SIO_IV must be set together")
	}

	key, err := hexBlob("OPG3D_SIO_KEY", keyHex, sio.KeyLen)
	if err != nil {
		return err
	}
	iv, err := hexBlob("OPG3D_SIO_IV", ivHex, sio.IVLen)
	if err != nil {
		return err
	}

	mode := sio.DefaultMode
	if name := env("OPG3D_SIO_MODE", ""); name != "" {
		if mode, err = sio.ParseMode(name); err != nil {
			return fmt.Errorf("config: OPG3D_SIO_MODE: %w", err)
		}
	}

	cfg.SioKey, cfg.SioIV, cfg.SioMode = key, iv, mode
	return nil
}

func hexBlob(name, value string, want int) ([]byte, error) {
	blob, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", name, err)
	}
	if len(blob) != want {
		return nil, fmt.Errorf("config: %s must be %d bytes of hex, got %d", name, want, len(blob))
	}
	return blob, nil
}

// SealsAppIDs reports whether the AppID endpoint can serve anything.
func (c *Config) SealsAppIDs() bool {
	return len(c.AppIDKey) > 0 && len(c.PhotonAppIDs) > 0
}

// DecodesSio reports whether a /sio payload codec is pinned.
func (c *Config) DecodesSio() bool {
	return len(c.SioKey) == sio.KeyLen && len(c.SioIV) == sio.IVLen
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
