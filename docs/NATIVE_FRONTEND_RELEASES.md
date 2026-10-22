# Signed native frontend releases

The permanent APK launcher is separate from the downloadable frontend module.
The backend never edits an APK or an already mapped .so in a running process.

## Initial key and APK build

Generate **once**, outside the repositories and the served content directory:

```sh
go run ./cmd/opg3d-release -generate-key -key "$HOME/.config/opg3d/frontend-signing.pem"
openssl pkey -in "$HOME/.config/opg3d/frontend-signing.pem" -pubout -outform DER -out public.der
```

Build the frontend with that public SPKI file through
`-POPG3D_RELEASE_PUBLIC_KEY_FILE=/absolute/path/public.der`. The private key must
never be copied to the frontend, committed, served or included in the APK. Back
it up securely; existing APKs trust that publisher key, not whichever key a newly
entered server happens to advertise. Public keys are intentionally not secret.

## Publish a build

```sh
go run ./cmd/opg3d-release \
  -key "$HOME/.config/opg3d/frontend-signing.pem" \
  -out content/frontend \
  -library /path/to/frontend/lib/arm64-v8a/libopg3d_client.so \
  -build 3 -version 23.1.3-native.3
```

Increase the numeric build for every release. The tool refuses to overwrite a
valid current release with an equal/older build. It validates AArch64 ELF, signs
manifest bytes using RSA3072/SHA256, publishes the hash-addressed binary/public
key first and atomically replaces `active.json` last. No backend restart is
required for publication. Native payload ABI1 must export the start, ABI and
functional-ready callback setter documented in frontend `docs/NATIVE_LOADER.md`.
Publish ONLY tested code; a signature authenticates a publisher, not correctness.

The server validates signature, schema1/loader1, game23.1.3, ARM64, build, size,
ELF and SHA256. Keys are not used for online signing. The release directory is
ignored by Git and should be deployed as content, not source.

## Configuration and HTTP contract

- `OPG3D_FRONTEND_DIR`: optional absolute release directory; defaults to
  `<OPG3D_CONTENT_DIR>/frontend`.
- `OPG3D_SERVER_COUNTRY`: explicit two-letter ISO3166 code, e.g. `RU` or `DE`.
  Missing/invalid codes yield `ZZ` (unspecified). It is not IP geolocation.
- `GET /api/v1/loader/info`: protocol, game/ABI, server name/country, maintenance,
  valid-release availability; no credentials and no gameplay mutation.
- `GET /api/v1/frontend/manifest`: signed Base64 payload + signature, no-store;
  503 when no valid release is configured.
- `GET /api/v1/frontend/<sha256>/library.so`: bounded verified content-addressed
  artifact, immutable cache header; previous hashes remain valid across active
  release changes. No paths or redirects are taken from untrusted client input.

Keep previous server artifacts during rolling publication. The client preserves
its old private cache during download failures, uses a known-good compatible
cached release for interrupted unconfirmed starts, and never hot-swaps code.
Automatic recovery is bounded by the functional-ready confirmation, not every
possible later gameplay failure. A loader defect itself requires a new APK.

The existing gameplay transport is HTTP. Signed updates prevent unsigned code
execution but do not encrypt account/session traffic; this is not TLS support.

## Tests

Run `go test ./...`, `go test -race ./...`, `go vet ./...`, then build
`./cmd/opg3d-backend` and `./cmd/opg3d-release`. Tests cover signature/key/binary
corruption, incompatible versions/ABI, oversized/path-traversal inputs, absent
release service and immutable artifact independence from active.json.
