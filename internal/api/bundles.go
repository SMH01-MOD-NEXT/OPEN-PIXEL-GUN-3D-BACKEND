// SPDX-License-Identifier: GPL-3.0-or-later

package api

// The 23.1.3 client streams its asset bundles from a vendor CDN with content
// addressed URLs: https://<cdn>/<name>/<md5>/<name>. The md5 in the path is the
// object's own hash, so a bundle reference is self-verifying and immutable --
// a given name/hash pair can only ever mean one byte sequence.
//
// This server hosts the same layout locally so a LAN install never depends on
// the vendor CDN staying alive. The client-side URL rewriter sends the request
// here as /@<cdn host>/<name>/<hash>/<name>, which is mapped onto the internal
// /bundles/ namespace before routing.
//
// The bundle files are the game's copyrighted content and are deliberately not
// part of this repository; tools/fetch-bundles.sh fills the directory from a
// client's own manifest.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// bundlePrefix is the internal namespace bundle downloads are routed to.
	bundlePrefix = "/bundles/"

	// bundleHostSuffix identifies the retired CDN in a rewritten URL. Matching
	// on the host rather than the path shape keeps the bundle namespace from
	// colliding with the legacy config and analytics routes.
	bundleHostSuffix = "akamaized.net"

	// bundleDirDefault is used when no directory is configured, so a zero value
	// Config still resolves to the documented layout.
	bundleDirDefault = "bundles"

	// maxBundleBytes caps an on-miss upstream download. The largest bundle in
	// the shipped 23.1.3 set is about 7 MiB, so this leaves room for future
	// content without letting a hostile upstream fill the disk.
	maxBundleBytes = 64 << 20

	// bundleFetchTimeout bounds a single on-miss download.
	bundleFetchTimeout = 60 * time.Second
)

var (
	bundleNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	bundleHashPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// bundleRequestPath maps a rewritten CDN URL onto the internal namespace.
// Returns false for every other host, which leaves the existing routes alone.
func bundleRequestPath(raw string) (string, bool) {
	rest, ok := strings.CutPrefix(raw, "/@")
	if !ok {
		return "", false
	}
	host, tail, ok := strings.Cut(rest, "/")
	if !ok || tail == "" {
		return "", false
	}
	if bare, _, found := strings.Cut(host, ":"); found {
		host = bare
	}
	if !strings.HasSuffix(strings.ToLower(host), bundleHostSuffix) {
		return "", false
	}
	return bundlePrefix + tail, true
}

// bundleRef validates a /bundles/<name>/<hash>/<file> path. The file segment
// must repeat the name exactly, which is how the CDN itself is addressed, and
// the strict patterns make directory traversal unrepresentable rather than
// merely filtered.
func bundleRef(requested string) (name, hash string, ok bool) {
	rest, found := strings.CutPrefix(requested, bundlePrefix)
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return "", "", false
	}
	name, hash, file := parts[0], parts[1], parts[2]
	if !bundleNamePattern.MatchString(name) || !bundleHashPattern.MatchString(hash) || file != name {
		return "", "", false
	}
	return name, hash, true
}

// bundleFile is the on-disk location of one bundle version.
func (s *Server) bundleFile(name, hash string) string {
	dir := s.cfg.BundleDir
	if strings.TrimSpace(dir) == "" {
		dir = bundleDirDefault
	}
	return filepath.Join(s.cfg.ContentDir, dir, name, hash, name)
}

// handleBundle serves one asset bundle from disk, downloading it from the
// configured upstream first if it is missing and that is allowed.
func (s *Server) handleBundle(w http.ResponseWriter, r *http.Request) {
	name, hash, ok := bundleRef(r.URL.Path)
	if !ok {
		s.log.Event("bundle.rejected", map[string]any{"path": r.URL.Path, "ip": clientIP(r)})
		s.writeError(w, http.StatusBadRequest, "bad_bundle_path", r.URL.Path)
		return
	}

	file := s.bundleFile(name, hash)
	handle, err := os.Open(file)
	if errors.Is(err, fs.ErrNotExist) && s.bundleFetchAllowed() {
		if fetchErr := s.fetchBundle(r.Context(), name, hash, file); fetchErr != nil {
			s.log.Event("bundle.fetch_failed", map[string]any{
				"name": name, "hash": hash, "upstream": s.cfg.BundleUpstream, "err": fetchErr.Error(),
			})
		} else {
			s.log.Event("bundle.fetched", map[string]any{
				"name": name, "hash": hash, "file": file, "upstream": s.cfg.BundleUpstream,
			})
			handle, err = os.Open(file)
		}
	}
	if err != nil {
		s.log.Event("bundle.missing", map[string]any{
			"name": name,
			"hash": hash,
			"file": file,
			"hint": "run tools/fetch-bundles.sh to fill " + s.cfg.ContentDir,
		})
		s.writeError(w, http.StatusNotFound, "bundle_missing", name+"/"+hash)
		return
	}
	defer handle.Close()

	info, err := handle.Stat()
	if err != nil {
		s.log.Event("bundle.stat_failed", map[string]any{"file": file, "err": err.Error()})
		s.writeError(w, http.StatusInternalServerError, "bundle_unreadable", name+"/"+hash)
		return
	}

	// Bundle bodies are megabytes of binary with no diagnostic value, so they
	// are kept out of the request trace buffer.
	discardTraceBody(w)

	// Content addressed by construction: the hash is in the URL, so the answer
	// can be cached forever and revalidated by ETag alone.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", `"`+hash+`"`)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

	s.log.Event("bundle.served", map[string]any{
		"name": name, "hash": hash, "bytes": info.Size(), "ip": clientIP(r),
	})
	http.ServeContent(w, r, name, info.ModTime(), handle)
}

func (s *Server) bundleFetchAllowed() bool {
	return s.cfg.BundleFetchOnMiss && strings.TrimSpace(s.cfg.BundleUpstream) != ""
}

// fetchBundle downloads one bundle from the upstream CDN and stores it only if
// its md5 matches the hash the client asked for.
//
// SECURITY-SENSITIVE: the hash check is what makes an unauthenticated upstream
// safe to cache. md5 is used because the vendor's URL scheme is built on it --
// it is a content address, not a signature, and unverified bytes are never
// written into the served tree.
func (s *Server) fetchBundle(ctx context.Context, name, hash, dest string) error {
	upstream := strings.TrimSuffix(strings.TrimSpace(s.cfg.BundleUpstream), "/")
	if upstream == "" {
		return errors.New("no bundle upstream configured")
	}

	ctx, cancel := context.WithTimeout(ctx, bundleFetchTimeout)
	defer cancel()

	url := upstream + "/" + name + "/" + hash + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream answered %d", resp.StatusCode)
	}

	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".partial-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	digest := md5.New()
	written, err := io.Copy(io.MultiWriter(temp, digest), io.LimitReader(resp.Body, maxBundleBytes+1))
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if written > maxBundleBytes {
		return fmt.Errorf("upstream body exceeds %d bytes", maxBundleBytes)
	}
	if sum := hex.EncodeToString(digest.Sum(nil)); sum != hash {
		return fmt.Errorf("hash mismatch: upstream body is %s", sum)
	}
	return os.Rename(tempName, dest)
}
