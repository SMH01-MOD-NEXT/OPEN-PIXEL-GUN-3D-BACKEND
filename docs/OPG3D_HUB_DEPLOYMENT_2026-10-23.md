# OPG3D HUB deployment — 2026-10-23

## Release

- Feature: `9a52f57`; code/UX/test fix: `9d1db88`; complete runtime configuration: `8bb5520`.
- Runtime version: `23.1.3-hub-8bb5520` (Linux amd64, CGO disabled).
- Binary SHA-256: `956a553eb6aaab48b45c83e43e9b660ecad7904bb94a48387169f2c3239a85ed`.
- Public URL: `https://opg3d.smh01.net.eu.org/`.
- Final binary built and tested from an isolated `git archive 8bb5520` source
  snapshot, not from uncommitted working-tree code.
- Local backend rebuilt/restarted; health and HUB endpoints checked.
- AWS `opg3d-backend.service` updated; health passed. Shared Caddy and legacy
  game gateway stayed running. Caddy root redirect replaced by the HUB proxy;
  admin blocking and unrelated vhosts preserved.
- Stable encryption keys generated locally and on AWS without printing values.
  Existing env values preserved; no key or real-player data committed.

## Preservation checks

Every swap compared the stopped-server authoritative state before/after startup:
byte-identical. Two accounts/profiles retained. Neither local state nor frontend
files were copied onto AWS. Native frontend manifest stayed:
`0f4238d665f654816693fd07fdddfebc05b237140f85e4d017c396e57bbac86f`.

The campaigns remained unchanged:

- 2026-10-26 12:00 UTC: 2626 Coins + 1010 Gems.
- 2026-10-31 12:00 UTC: 3131 Coins + 1010 Gems + weapon 143001.

Protected AWS backups:

- `/var/backups/opg3d/hub-20261023T144310Z-9a52f57`
- `/var/backups/opg3d/hub-20261023T144545Z-9d1db88`
- `/var/backups/opg3d/hub-20261023T144811Z-8bb5520` (final release)

They contain old binaries/env/state and sanitized receipts. First backup also
has `backend.env.after` with the generated stable key, mode 0600. Later backups
must always preserve the **current key together with hub-security.enc**, once
users enroll. The database is authoritative; never roll back live progress by
restoring an old data snapshot during a binary rollback.

**Security rollback constraint:** do not restore an older OTP-only binary with
web authentication enabled after anyone enrolls 2FA. That binary cannot enforce
the new factor store. If an old binary is unavoidable, disable the web-auth
origin first; restore compatible security code/key/state before reopening HUB
login. A compatible HUB release fails closed on missing/wrong encryption keys.

## Verification

- Full `go test ./...`, `go test -race ./...`, `go vet ./...`: passed.
- Six existing admin-console Node suites: passed.
- HUB JS syntax and TLS deployment Python syntax: passed.
- HUB integration includes genuine signed P-256 WebAuthn fixture ceremonies;
  wrong origin, missing UV and replay are rejected. OTP/MFA, encrypted storage,
  recovery replay/concurrency, CSRF/fresh login, exchange rollback/idempotency,
  owner/ban races and revision fences are covered.
- Isolated browser QA (actual UI files, explicit fake test-account API fixtures,
  original extracted images) inspected: desktop 1440px, phone 390px, light/dark,
  home, game-ID login, signed-in wallet/exchange, security and confirmation.
  No JS page errors or horizontal overflow in the exercised states. It does
  not substitute for signing in with a real account or registering a real phone
  passkey.
- Public HTTPS checks: `/`, `/account/login`, JS/CSS/images/stats returned 200;
  private account/shop 401 without login; settings refused unauthenticated
  access; public admin paths 404; cross-origin exchange 403.
- Passkey discoverable-login options returned 200 without creating an account
  or changing game state. No real user passkey was enrolled by the agent.
- Updated Certbot deploy hook activated with the actual certificate lineage:
  configuration unchanged, certificate reloaded. Existing timer stayed active.

## Owner follow-up

1. Open HUB in a regular up-to-date browser, sign in with the game's personal
   mailbox code, and inspect the wallet/shop.
2. Security: enroll authenticator, save the one-time recovery-code list, then
   test normal login (game code followed by 2FA). Use a new TOTP time step for
   another operation; accepted codes cannot be reused.
3. Add a passkey on an owned device and test direct login there. Hardware/native
   platform prompts cannot be validated by the remote agent's browser preview.
4. Close the game before an exchange and reopen afterwards to load the fenced
   external wallet change. No Android frontend edits are required.
5. Supply the OneDrive HTTPS build link when ready; download remains disabled
   until `OPG3D_HUB_DOWNLOAD_URL` is explicitly configured.

HUB 2FA/passkeys protect website access only, not the legacy game's device-ID
login. User account/device compromise remains a separate risk.
