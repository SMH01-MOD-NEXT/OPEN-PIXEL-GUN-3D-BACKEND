// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// hostBundle writes one bundle into the content tree the way the fill script
// does, and returns its content address.
func hostBundle(t *testing.T, contentDir, name string, payload []byte) string {
	t.Helper()
	sum := md5.Sum(payload)
	hash := hex.EncodeToString(sum[:])
	dir := filepath.Join(contentDir, "bundles", name, hash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), payload, 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	return hash
}

// The client asks for bundles with its own CDN host still in the path, so the
// rewritten URL must reach the local copy byte for byte.
func TestBundleServedFromContentDirectory(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = t.TempDir()

	payload := []byte("UnityFS\x00fake bundle body")
	hash := hostBundle(t, cfg.ContentDir, "altars", payload)

	target := "/@pixelgun3d.akamaized.net/altars/" + hash + "/altars?opg3d_player=350604106"
	rec := do(t, s, http.MethodGet, target, nil, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != string(payload) {
		t.Fatalf("body = %q, want %q", got, payload)
	}
	if want := `"` + hash + `"`; rec.Header().Get("ETag") != want {
		t.Fatalf("ETag = %q, want %q", rec.Header().Get("ETag"), want)
	}
}

// A malformed reference must never reach the filesystem, and a bundle this
// server does not have must say so instead of answering with something else.
func TestBundleRejectsBadReferencesAndReportsMisses(t *testing.T) {
	s, cfg := newTestServer(t)
	cfg.ContentDir = t.TempDir()
	cfg.BundleFetchOnMiss = false

	cases := []struct {
		name   string
		target string
		want   int
	}{
		{"short hash", "/@pixelgun3d.akamaized.net/altars/deadbeef/altars", http.StatusBadRequest},
		{"name mismatch", "/@pixelgun3d.akamaized.net/altars/" + hex32 + "/capes", http.StatusBadRequest},
		{"extra segment", "/@pixelgun3d.akamaized.net/altars/" + hex32 + "/nested/altars", http.StatusBadRequest},
		{"absent bundle", "/@pixelgun3d.akamaized.net/altars/" + hex32 + "/altars", http.StatusNotFound},
	}
	for _, testCase := range cases {
		rec := do(t, s, http.MethodGet, testCase.target, nil, nil)
		if rec.Code != testCase.want {
			t.Fatalf("%s: status = %d, want %d, body = %s", testCase.name, rec.Code, testCase.want, rec.Body)
		}
	}

	// An encoded traversal is cleaned and redirected by the router before any
	// handler runs, so the only thing worth asserting here is that it never
	// turns into a served file.
	if rec := do(t, s, http.MethodGet, "/@pixelgun3d.akamaized.net/..%2f..%2fetc/passwd/x", nil, nil); rec.Code == http.StatusOK {
		t.Fatalf("traversal was served: body = %s", rec.Body)
	}

	// The reference parser is the actual guard: a path with a traversal or a
	// stray segment cannot name a bundle at all.
	for _, bad := range []string{
		"/bundles/../../etc/passwd",
		"/bundles/..%2fetc/" + hex32 + "/..%2fetc",
		"/bundles/altars/" + hex32 + "/altars/extra",
		"/bundles/altars/" + hex32,
	} {
		if _, _, ok := bundleRef(bad); ok {
			t.Fatalf("bundleRef accepted %q", bad)
		}
	}
}

const hex32 = "e2eeeada6892843d8229aae0a1040514"

// A LAN install should heal itself when it is asked for a bundle nobody has
// fetched yet, but only if the upstream bytes really are the bundle that was
// requested.
func TestBundleFetchesOnMissAndRefusesWrongBytes(t *testing.T) {
	payload := []byte("UnityFS\x00fetched on miss")
	sum := md5.Sum(payload)
	hash := hex.EncodeToString(sum[:])

	var serve []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rust_scene/"+hash+"/rust_scene" {
			http.NotFound(w, r)
			return
		}
		w.Write(serve)
	}))
	t.Cleanup(upstream.Close)

	s, cfg := newTestServer(t)
	cfg.ContentDir = t.TempDir()
	cfg.BundleUpstream = upstream.URL
	cfg.BundleFetchOnMiss = true

	target := "/@pixelgun3d.akamaized.net/rust_scene/" + hash + "/rust_scene"
	stored := filepath.Join(cfg.ContentDir, "bundles", "rust_scene", hash, "rust_scene")

	// Upstream hands back something that is not the requested content: the
	// answer must be a miss and nothing may be written to disk.
	serve = []byte("a different bundle entirely")
	if rec := do(t, s, http.MethodGet, target, nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("tampered upstream: status = %d, body = %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(stored); !os.IsNotExist(err) {
		t.Fatalf("tampered upstream body was stored: %v", err)
	}

	// The genuine bytes are served and cached for the next request.
	serve = payload
	rec := do(t, s, http.MethodGet, target, nil, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != string(payload) {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	saved, err := os.ReadFile(stored)
	if err != nil {
		t.Fatalf("bundle was not cached: %v", err)
	}
	if string(saved) != string(payload) {
		t.Fatalf("cached body = %q, want %q", saved, payload)
	}

	// With the upstream gone the local copy still answers.
	upstream.Close()
	if rec := do(t, s, http.MethodGet, target, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("offline replay: status = %d, body = %s", rec.Code, rec.Body)
	}
}

// Only the CDN host is mapped into the bundle namespace; the retired config
// host must keep its own handler.
func TestBundleRequestPathMapsOnlyTheCdnHost(t *testing.T) {
	if mapped, ok := bundleRequestPath("/@pixelgun3d.akamaized.net/altars/" + hex32 + "/altars"); !ok ||
		mapped != "/bundles/altars/"+hex32+"/altars" {
		t.Fatalf("cdn path mapped to %q (ok=%v)", mapped, ok)
	}
	if _, ok := bundleRequestPath("/@secure.pixelgun3dserver.com/pixelgun3d-config/x.json"); ok {
		t.Fatal("config host must not be mapped into the bundle namespace")
	}
	if _, ok := bundleRequestPath("/api/v1/ping"); ok {
		t.Fatal("plain paths must not be mapped into the bundle namespace")
	}
}
