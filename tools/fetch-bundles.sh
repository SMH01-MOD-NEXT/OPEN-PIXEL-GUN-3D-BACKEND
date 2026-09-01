#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Fill this server's content directory with the asset bundles a 23.1.3 client
# needs, so the game downloads them from the LAN instead of the vendor CDN.
#
# The bundles are the game's copyrighted content and are never committed to this
# repository. This script is the reproducible way to build the local set from a
# client that already has it, which is why only the recipe lives in git.
#
# How a bundle is addressed
#   Every bundle is named by its own md5, and those two fields are the first two
#   path components of the download URL:
#     <upstream>/<name>/<md5>/<name>
#   so each download verifies itself. Files are stored here as
#   <content-dir>/bundles/<name>/<md5>/<name>, the exact layout the server
#   serves from.
#
# Where the list comes from, in the order this script looks
#   1. --manifest FILE, the client's own AssetBundles document
#      ({"b":[{"n","h","s",...}]}). It carries sizes, so --dry-run is exact and
#      needs no network at all. This server hosts that document as
#      <content-dir>/sio-configs/AssetBundlesAndroid.json, which is also where
#      this script looks by default.
#   2. --names FILE, one "<name>/<md5>" line per bundle. Sizes are unknown, so a
#      dry run asks the upstream for them.
#   3. The attached device, over adb:
#      /storage/emulated/0/Android/data/<package>/files/Cache/Info/names
#      That file lists only what the client happened to cache, which is a subset
#      of the manifest.
#
# The run is idempotent and resumable: a bundle whose stored copy already hashes
# correctly is skipped, so re-running after an interruption only fetches what is
# still missing.
#
# Usage
#   tools/fetch-bundles.sh --dry-run            # how much is left to download
#   tools/fetch-bundles.sh                      # fill the content directory
#   tools/fetch-bundles.sh --manifest doc.json --jobs 16
#   tools/fetch-bundles.sh --dry-run --content-dir /tmp/empty   # size the full set
#
# Options
#   -n, --dry-run        report what would be fetched; write nothing
#       --manifest FILE  AssetBundles document to plan from
#       --names FILE     plain "<name>/<md5>" list to plan from
#       --content-dir D  content root to fill (default: content)
#       --upstream URL   where bundles are fetched from
#       --jobs N         parallel transfers (default: 8)
#       --package NAME   android package the device manifest is pulled from
#
# Environment overrides: OPG3D_CONTENT_DIR, OPG3D_BUNDLE_UPSTREAM,
# OPG3D_BUNDLE_JOBS, OPG3D_BUNDLE_TIMEOUT, OPG3D_PACKAGE.

set -euo pipefail

# Downloads run in parallel by re-entering this script once per bundle, so every
# setting a worker needs is read from the environment here and exported by the
# parent run after the flags are parsed. A worker never parses flags itself.
CONTENT_DIR="${OPG3D_CONTENT_DIR:-content}"
UPSTREAM="${OPG3D_BUNDLE_UPSTREAM:-https://pixelgun3d.akamaized.net}"
JOBS="${OPG3D_BUNDLE_JOBS:-8}"
PACKAGE="${OPG3D_PACKAGE:-com.pixel.gun3d}"
TIMEOUT="${OPG3D_BUNDLE_TIMEOUT:-300}"
DRY_RUN="${OPG3D_BUNDLE_DRY_RUN:-0}"
REPORT="${OPG3D_BUNDLE_REPORT:-}"
SIZES="${OPG3D_BUNDLE_SIZES:-}"
MANIFEST=""
NAMES=""

hosted_manifest="sio-configs/AssetBundlesAndroid.json"
device_manifest="/storage/emulated/0/Android/data/${PACKAGE}/files/Cache/Info/names"

die() {
	printf 'fetch-bundles: %s\n' "$1" >&2
	exit 1
}

usage() {
	awk 'NR > 2 && /^#/ { sub(/^# ?/, ""); print; next } NR > 2 { exit }' "$0"
	exit 0
}

mib() {
	awk -v bytes="$1" 'BEGIN { printf "%.1f", bytes / 1048576 }'
}

# A stored bundle counts as present only when it hashes to the name it is filed
# under, so a truncated or corrupted file is refetched rather than trusted.
stored_size() {
	local dest="$1" hash="$2"
	[[ -s "$dest" ]] || return 1
	[[ "$(md5sum "$dest" | cut -d' ' -f1)" == "$hash" ]] || return 1
	stat -c %s "$dest"
}

# The size the manifest states for one bundle, when a manifest was used.
manifest_size() {
	local entry="$1"
	[[ -n "$SIZES" && -f "$SIZES" ]] || return 1
	awk -F'\t' -v want="$entry" '$1 == want { print $2; found = 1; exit } END { exit !found }' "$SIZES"
}

# Size of a bundle without downloading it, for lists that carry no sizes. A one
# byte range request is the fallback for edges that refuse HEAD; its
# Content-Range still carries the total.
probe_size() {
	local url="$1" size=""
	size="$(curl -fsSI -m 60 --retry 2 "$url" 2>/dev/null | tr -d '\r' |
		awk -F': ' 'tolower($1) == "content-length" { print $2 }' | tail -1)" || true
	if [[ -z "$size" ]]; then
		size="$(curl -fsS -o /dev/null -r 0-0 -D - -m 60 "$url" 2>/dev/null | tr -d '\r' |
			awk -F'/' 'tolower($0) ~ /^content-range:/ { print $2 }' | tail -1)" || true
	fi
	printf '%s' "$size"
}

# One bundle per worker invocation. A failure is reported through the shared
# report file rather than a non-zero exit, so one dead object cannot abort the
# whole run.
handle_one() {
	local entry="$1"
	local name="${entry%%/*}"
	local hash="${entry##*/}"
	local dir="${CONTENT_DIR}/bundles/${name}/${hash}"
	local dest="${dir}/${name}"
	local url="${UPSTREAM}/${name}/${hash}/${name}"
	local present

	if present="$(stored_size "$dest" "$hash")"; then
		printf 'SKIP\t%s\t%s\t%s\n' "$name" "$hash" "$present" >>"$REPORT"
		return 0
	fi

	if [[ "$DRY_RUN" == "1" ]]; then
		local size
		if ! size="$(manifest_size "$entry")"; then
			size="$(probe_size "$url")"
		fi
		if [[ -z "$size" ]]; then
			printf 'FAIL_SIZE\t%s\t%s\t0\n' "$name" "$hash" >>"$REPORT"
		else
			printf 'PLAN\t%s\t%s\t%s\n' "$name" "$hash" "$size" >>"$REPORT"
		fi
		return 0
	fi

	mkdir -p "$dir"
	local temp
	temp="$(mktemp "${dir}/.partial-XXXXXX")"
	if ! curl -fsS -m "$TIMEOUT" --retry 3 --retry-delay 2 -o "$temp" "$url"; then
		rm -f "$temp"
		printf 'FAIL_DOWNLOAD\t%s\t%s\t0\n' "$name" "$hash" >>"$REPORT"
		return 0
	fi

	if [[ "$(md5sum "$temp" | cut -d' ' -f1)" != "$hash" ]]; then
		# Never publish bytes that are not the content that was addressed.
		rm -f "$temp"
		printf 'FAIL_HASH\t%s\t%s\t0\n' "$name" "$hash" >>"$REPORT"
		return 0
	fi

	mv -f "$temp" "$dest"
	printf 'OK\t%s\t%s\t%s\n' "$name" "$hash" "$(stat -c %s "$dest")" >>"$REPORT"
}

if [[ "${1:-}" == "--worker" ]]; then
	[[ -n "$REPORT" ]] || die "worker mode is driven by a parent run; call the script without --worker"
	handle_one "$2"
	exit 0
fi

while [[ $# -gt 0 ]]; do
	case "$1" in
	-n | --dry-run)
		DRY_RUN=1
		shift
		;;
	--manifest)
		MANIFEST="${2:-}"
		shift 2
		;;
	--names)
		NAMES="${2:-}"
		shift 2
		;;
	--content-dir)
		CONTENT_DIR="${2:-}"
		shift 2
		;;
	--upstream)
		UPSTREAM="${2:-}"
		shift 2
		;;
	--jobs)
		JOBS="${2:-}"
		shift 2
		;;
	--package)
		PACKAGE="${2:-}"
		device_manifest="/storage/emulated/0/Android/data/${PACKAGE}/files/Cache/Info/names"
		shift 2
		;;
	-h | --help) usage ;;
	*) die "unknown argument: $1 (try --help)" ;;
	esac
done

command -v curl >/dev/null || die "curl is required"
command -v md5sum >/dev/null || die "md5sum is required"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

entries="${workdir}/entries"
source_label=""

# The hosted document is the same list the client is served, so planning from it
# by default keeps this script and the server describing one set of bundles.
if [[ -z "$MANIFEST" && -z "$NAMES" && -f "${CONTENT_DIR}/${hosted_manifest}" ]]; then
	MANIFEST="${CONTENT_DIR}/${hosted_manifest}"
fi

if [[ -n "$MANIFEST" ]]; then
	[[ -s "$MANIFEST" ]] || die "manifest is empty: $MANIFEST"
	command -v python3 >/dev/null || die "python3 is required to read a manifest; pass --names instead"
	SIZES="${workdir}/sizes"
	python3 - "$MANIFEST" "$entries" "$SIZES" <<-'PY' || die "could not read the manifest: $MANIFEST"
		import json, re, sys

		document, entries_path, sizes_path = sys.argv[1], sys.argv[2], sys.argv[3]
		with open(document, encoding="utf-8") as handle:
		    bundles = json.load(handle).get("b") or []

		# Only well formed rows become URLs; anything else belongs to a
		# different build of the game.
		name_ok = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*$")
		hash_ok = re.compile(r"^[0-9a-f]{32}$")

		seen, rows = set(), []
		for bundle in bundles:
		    name, digest = str(bundle.get("n", "")), str(bundle.get("h", ""))
		    if not name_ok.match(name) or not hash_ok.match(digest):
		        continue
		    entry = "%s/%s" % (name, digest)
		    if entry in seen:
		        continue
		    seen.add(entry)
		    size = bundle.get("s")
		    rows.append((entry, int(size) if isinstance(size, int) else 0))

		with open(entries_path, "w", encoding="utf-8") as handle:
		    handle.write("".join("%s\n" % entry for entry, _ in rows))
		with open(sizes_path, "w", encoding="utf-8") as handle:
		    handle.write("".join("%s\t%d\n" % row for row in rows))
	PY
	source_label="manifest ${MANIFEST}"
else
	if [[ -z "$NAMES" ]]; then
		command -v adb >/dev/null ||
			die "no manifest or --names file given and adb is not installed; copy ${device_manifest} off a client and pass it with --names"
		NAMES="${workdir}/names"
		adb pull "$device_manifest" "$NAMES" >/dev/null 2>&1 ||
			die "could not pull ${device_manifest}; launch the game once so it writes its manifest, or pass --names"
		printf 'manifest pulled from the device: %s\n' "$device_manifest"
	fi
	[[ -s "$NAMES" ]] || die "list is empty: $NAMES"
	# Accept only well formed "<name>/<md5>" lines.
	tr -d '\r' <"$NAMES" | grep -E '^[A-Za-z0-9][A-Za-z0-9._-]*/[0-9a-f]{32}$' | sort -u >"$entries" || true
	source_label="list ${NAMES}"
fi

total="$(wc -l <"$entries")"
[[ "$total" -gt 0 ]] || die "no usable bundles in $source_label"

REPORT="${workdir}/report"
: >"$REPORT"

# Hand the resolved settings down to the workers through the documented
# environment names, so a flag and an export mean exactly the same thing.
export OPG3D_CONTENT_DIR="$CONTENT_DIR"
export OPG3D_BUNDLE_UPSTREAM="$UPSTREAM"
export OPG3D_BUNDLE_TIMEOUT="$TIMEOUT"
export OPG3D_BUNDLE_DRY_RUN="$DRY_RUN"
export OPG3D_BUNDLE_REPORT="$REPORT"
export OPG3D_BUNDLE_SIZES="$SIZES"

if [[ "$DRY_RUN" == "1" ]]; then
	printf 'planning %s/bundles against %s (nothing will be written)\n' "$CONTENT_DIR" "$UPSTREAM"
else
	printf 'filling %s/bundles from %s\n' "$CONTENT_DIR" "$UPSTREAM"
fi
printf 'bundles listed by %s: %s, parallel jobs: %s\n' "$source_label" "$total" "$JOBS"

started="$(date +%s)"
xargs -P "$JOBS" -I{} "$0" --worker {} <"$entries"
elapsed=$(($(date +%s) - started))

count_of() { grep -c "^$1" "$REPORT" || true; }
bytes_of() { awk -F'\t' -v want="$1" '$1 == want { sum += $4 } END { printf "%d", sum }' "$REPORT"; }

present="$(count_of SKIP)"
present_bytes="$(bytes_of SKIP)"
failed="$(count_of FAIL)"

if [[ "$DRY_RUN" == "1" ]]; then
	planned="$(count_of PLAN)"
	planned_bytes="$(bytes_of PLAN)"
	printf 'already present: %s bundles (%s MiB)\n' "$present" "$(mib "$present_bytes")"
	printf 'to download: %s bundles (%s MiB)\n' "$planned" "$(mib "$planned_bytes")"
	printf 'unknown size: %s bundles, checked in %ss\n' "$failed" "$elapsed"
	if [[ "$failed" -gt 0 ]]; then
		printf '\nupstream did not answer for:\n'
		grep '^FAIL' "$REPORT" | cut -f2,3
	fi
	exit 0
fi

downloaded="$(count_of OK)"
printf 'downloaded=%s (%s MiB) already-present=%s (%s MiB) failed=%s seconds=%s\n' \
	"$downloaded" "$(mib "$(bytes_of OK)")" "$present" "$(mib "$present_bytes")" "$failed" "$elapsed"

if [[ "$failed" -gt 0 ]]; then
	printf '\nfailures:\n'
	grep '^FAIL' "$REPORT" | cut -f1-3
	die "$failed bundle(s) could not be stored; re-run to retry only those"
fi

printf 'content directory is complete for this list\n'
