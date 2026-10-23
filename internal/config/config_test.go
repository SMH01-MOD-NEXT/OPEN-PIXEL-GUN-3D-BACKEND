// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"encoding/hex"
	"testing"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
)

// The key and IV captured from a traced 23.1.3 launch, used here only as
// correctly sized input.
const (
	testSioKey = "d670c4e97d6aafcdc9940cfb1f411df8c08b8fc480e50b239fe852a1f6ddd6d2"
	testSioIV  = "c72cc19a7fb79ef6e6d7a6f3944514bd"
)

// TestSioCodecDefaultsToMeasuredMode covers the normal case: a pinned key and
// IV with no explicit mode must select the variant that was measured against
// real traffic, so a working configuration never has to name it.
func TestSioCodecDefaultsToMeasuredMode(t *testing.T) {
	t.Setenv("OPG3D_SIO_KEY", testSioKey)
	t.Setenv("OPG3D_SIO_IV", testSioIV)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.DecodesSio() {
		t.Fatal("DecodesSio is false with a full key and IV configured")
	}
	if cfg.SioMode != sio.DefaultMode {
		t.Fatalf("SioMode = %s, want the measured default %s", cfg.SioMode, sio.DefaultMode)
	}

	wantKey, _ := hex.DecodeString(testSioKey)
	if string(cfg.SioKey) != string(wantKey) {
		t.Fatal("SioKey does not round-trip through the environment")
	}

	// The material must actually build a codec, otherwise the validation here
	// and the constructor in the api package could disagree.
	if _, err := sio.NewFromKeyIV(cfg.SioKey, cfg.SioIV, cfg.SioMode); err != nil {
		t.Fatalf("NewFromKeyIV on loaded material: %v", err)
	}
}

// TestSioCodecOffByDefault guards the default. Decrypting traffic with a pinned
// key is a diagnostic, so an unconfigured server must not enable it.
func TestSioCodecOffByDefault(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DecodesSio() {
		t.Fatal("the payload codec is enabled without being configured")
	}
	if len(cfg.SioKey) != 0 || len(cfg.SioIV) != 0 {
		t.Fatal("codec material was invented from an empty environment")
	}
}

// TestSioCodecRejectsBadMaterial keeps a broken codec from loading quietly. A
// server that silently ran without decryption would look identical in the log
// to one whose captured key simply did not fit the session.
func TestSioCodecRejectsBadMaterial(t *testing.T) {
	cases := []struct {
		name string
		key  string
		iv   string
		mode string
	}{
		{"key without iv", testSioKey, "", ""},
		{"iv without key", "", testSioIV, ""},
		{"short key", "d670c4e9", testSioIV, ""},
		{"short iv", testSioKey, "c72cc19a", ""},
		{"key is not hex", "zz" + testSioKey[2:], testSioIV, ""},
		{"unknown mode", testSioKey, testSioIV, "ecb"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPG3D_SIO_KEY", tc.key)
			t.Setenv("OPG3D_SIO_IV", tc.iv)
			t.Setenv("OPG3D_SIO_MODE", tc.mode)

			if _, err := Load(); err == nil {
				t.Fatal("Load accepted the broken codec configuration")
			}
		})
	}
}

func TestWebAuthOriginFailsClosed(t *testing.T) {
	for _, origin := range []string{"http://game.example", "https://game.example/", "https://game.example/path", "https://user@game.example", "https://game.example?x=1", "https://game.example#f"} {
		t.Run(origin, func(t *testing.T) {
			t.Setenv("OPG3D_WEB_AUTH_ORIGIN", origin)
			if _, err := Load(); err == nil {
				t.Fatal("unsafe web origin accepted")
			}
		})
	}
	t.Setenv("OPG3D_WEB_AUTH_ORIGIN", "https://game.example")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPG3D_STABLE_SESSIONS", "true")
	if _, err := Load(); err == nil {
		t.Fatal("deterministic game credentials accepted for OTP")
	}
}
