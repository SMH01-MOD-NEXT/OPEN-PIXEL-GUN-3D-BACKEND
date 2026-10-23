# OPG3D HUB

The HUB is embedded in the backend binary and served at the configured HTTPS
`OPG3D_WEB_AUTH_ORIGIN`. Root `/` is the public landing page; `/account/login`
serves the same application with the login dialog open. Android files are not
modified. See `WEB_AUTH.md` for existing game-mail OTP and transport constraints.

## Features and data sources

- Responsive RU/EN, system light/dark theme. Original game map/currency imagery
  was extracted read-only from the owner-provided 23.1.3 OBB; source paths and
  hashes are in `internal/api/hub/assets/provenance.json`. Artwork remains owned
  by its respective rights holders; it is not relicensed as original GPL art.
- Developer links: `https://t.me/SmaliPG`.
- `OPG3D_HUB_DOWNLOAD_URL`: optional **HTTPS** external link supplied by the
  owner. Empty means unavailable, not a fake download or an unrelated build.
- Online: unique authenticated, non-retired game socket accounts seen within
  60 seconds. Includes lobby clients, not only matches; not Photon actor counts
  and not website sessions. There can be a brief disconnect-detection delay.
- Today: canonical accounts with last activity since **00:00 UTC**, not rolling
  24 hours. No player/device identifiers are exposed by the public stats API.
- Signed-in wallet reads the real profile. Offers come directly from
  `internal/exchange.Offers()`, the same source as the native exchange shop.
  No separate website prices, real-money billing or invented weapon pricing.

## Exchange settlement

`GET /api/v1/hub/account` returns wallet, profile revision, timestamp and CSRF.
`GET /api/v1/hub/shop` returns offers, catalog hash and current season availability.
`POST /api/v1/hub/exchange` accepts the offer ID, catalog hash, expected revision,
expected `updated_at`, and a fresh lowercase 16-byte hex operation ID. Costs and
rewards are never supplied by the browser. Ownership and bans are rechecked
inside the same store lock as settlement.

The debit, credit, mirror documents, next progress revision and idempotency
receipt are persisted atomically. Failure rolls back the in-memory candidate.
A network retry keeps the **same** operation ID and cannot charge twice.
Another operation with an old balance returns 409; refresh and reconfirm.
Receipts are bounded (2048/account, aged out after 24h); an old replay still
carries a stale revision. External settlement deliberately preserves gameplay
command receipts so old in-game purchase commands cannot be replayed for credit.

**Close the game before exchange; reopen it afterwards.** The server fences
stale uploads using the progress revision; it cannot rewrite an already open
stock client UI. Existing server exchange season availability applies equally
in the game and HUB. Displayed wallet contains both pass currencies as well as
Coins and Gems.

## Authentication and security

1. Normal login: public game ID -> personal game-mail OTP -> if enabled,
   authenticator TOTP or an unused recovery code -> full web session.
2. Passkey login: discoverable WebAuthn assertion -> verified RP/origin/challenge,
   signature, account incarnation and **required user verification** -> full web
   session, without the in-game letter. This is an alternative login method, not
   an extra TOTP prompt. A stolen unlocked device/passkey is therefore sensitive.
3. Enrollment/removal requires a full session authenticated within 5 minutes,
   exact same-origin HTTPS, and session CSRF. Security changes revoke all earlier
   sessions for this account and rotate the current one without extending its
   fresh-authentication timestamp. Normal OTP finalization, factor mutations,
   passkey finalization and authenticated wallet calls share an auth gate to
   avoid MFA-enrollment/session-issuance races.
4. TOTP: 6 digits, SHA-1, 30s, +/-1 step; accepted steps are durably single-use.
   First setup gives 10 random 128-bit one-use recovery codes, shown once; only
   hashes persist. Disabling/regenerating requires a current factor/recovery
   code as well as fresh session/CSRF. Codes cannot be reused in the same step.
5. WebAuthn uses maintained `go-webauthn` v0.18.2, not handwritten verification.
   Up to 8 passkeys/account. Credentials have random opaque user handles.
   Challenges expire after 3 minutes, are cookie/session bound where applicable,
   and are one-use. Clone warnings/decreasing signature counters fail closed.
6. Mail/MFA attempts and API calls are bounded/rate-limited. Challenges and MFA
   flows are process-local. Browser sessions now persist for up to 90 days in
   encrypted `web-sessions.enc`; enrolled factors and consumed recovery codes
   also survive restarts. See [HUB support and sessions](HUB_SUPPORT_AND_SESSIONS.md)
   for the current lifecycle, support tickets, staff roles and ownership evidence.

`OPG3D_HUB_SECURITY_KEY` must contain 32 cryptographically random bytes encoded as
64 hex characters. Generate it **once**, store in the protected service env file,
and back it up. `data/hub-security.enc` is AES-256-GCM encrypted with per-write
random nonces, atomic replace and mode 0600. Missing/wrong key with existing
state disables web authentication rather than bypassing 2FA. Do not replace
this file or key on redeploy. Loss of either can lock enrolled users out. No
admin reset/bypass endpoint is provided. Account recreation/device rebinding
invalidates existing identity-bound sessions and credentials.

TOTP/registration flow secrets are only sent in protected, no-store responses;
credential and HUB auth request/response bodies are redacted in diagnostics.
There are no third-party analytics/scripts/fonts or storage of login tokens in
localStorage. Cookies are Secure, HttpOnly, SameSite=Strict and __Host-prefixed.
Security is limited by the underlying game's device-ID login: website 2FA does
not harden that legacy Android authentication mechanism or prevent a compromised
game account/device from receiving its in-game mail.

## Operations and tests

Build requires Go **1.26+** (dependency minimum). `go.sum` pins dependencies.

```sh
go test ./...
go test -race ./...
go vet ./...
for file in tools/console*_test.cjs; do node "$file"; done
node --check internal/api/hub/app.js
CGO_ENABLED=0 go build -trimpath -o bin/opg3d-backend ./cmd/opg3d-backend
```

Tests cover encrypted restart/tampering/key failure, recovery/TOTP reuse and
parallel attempts, persistence rollback, account incarnation, passkey uniqueness,
counters, full OTP -> MFA isolation, fresh-session/CSRF checks, real P-256 signed
WebAuthn registration and login, wrong origin/missing UV/replays, unique game
presence, all exchange rates/idempotency/restart/mirrors/receipt preservation,
concurrency/stale wallet and failed persistence. Synthetic authenticator test
keys exist only inside isolated tests, never as user credential substitutes.

Browser visual QA uses isolated non-production API fixtures, actual HTML/CSS/JS
and extracted imagery at 390px/1440px, light/dark, sign-in/shop/security/confirm
states. A real phone/platform passkey prompt still needs owner verification on
an actual device. Do not claim a human enrolled passkey or Android on-device
purchase was tested by the automated fixtures.

For AWS, preserve authoritative data and `.env` secrets, back up before swapping
only the backend binary, then activate the updated `tools/ec2-web-auth-tls.py`
route so `/` proxies to HUB instead of redirecting to the old login screen.
Do not stop or replace the unrelated shared Caddy vhost. Certbot deployment hook
uses that script, so also install the updated script under its existing path.
