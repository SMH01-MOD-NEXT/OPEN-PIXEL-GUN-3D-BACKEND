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


## Persistent sessions and support rollout

- Runtime code: `e3c39a1`, version `23.1.3-hub-e3c39a1`.
- Binary SHA-256: `8def85d06baa39963d30cfa4d55743a87b9687a5cc41c97d1d86910dc202337b`.
- Built from `git archive HEAD`; installed locally and on EC2. Local tracked PID at verification: 87816.
- AWS stopped-backend backup: `/var/backups/opg3d/hub-20261023T163117Z-e3c39a1` (binary, protected env, complete data and frontend manifest).
- Initial authoritative state byte-identical: two accounts/two profiles. Both scheduled reward campaigns unchanged. Frontend manifest SHA-256 remains `0f4238d665f654816693fd07fdddfebc05b237140f85e4d017c396e57bbac86f`.
- Existing HUB encryption key preserved and explicitly compared with backup without disclosure. Support storage is encrypted mode 0600. Local restart backups now include all three encrypted HUB stores.
- Caddy/gateway process IDs unchanged; all services active. No DNS, certificate, gateway, Android frontend or unrelated-service changes.
- Verified HTTPS `/`, public HUB scripts/text, support initialization and health: 200. Anonymous account: 401; anonymous support-admin: 403; public operator endpoints: 404; support mutation without matching Origin: 403. Guest cookie has explicit 90-day Max-Age/expiration and Secure/HttpOnly/Strict flags. No live test tickets or staff-role grants were created during smoke tests.
- Real handler/store tests cover durable authenticated sessions and revocation across restarts. Live player authentication was not impersonated during deployment. Browsers using pre-release memory-only sessions need one fresh login.
- `go test -race ./...`, `go vet ./...`, all seven dependency-free console harnesses, and isolated Chromium HUB/operator interaction checks passed. Mobile 390px and desktop 1440px screenshots inspected, including dark theme. Fixture accounts/codes were synthetic.
- See `HUB_SUPPORT_AND_SESSIONS.md` for staff-role setup, guest ticket access, evidence limitations, bounds and backup/privacy requirements. No automatic ownership recovery, complete historical purchase reconstruction or client telemetry fabrication was added.

## Android download enabled — 2026-10-25

Configuration-only update using the existing `OPG3D_HUB_DOWNLOAD_URL` feature.
The owner supplied this OneDrive sharing URL, which is used unchanged:

https://1drv.ms/u/c/648bba575409b1ee/IQBCBch0tW5nSZ1b44Kt4jpiAR3zki31m2AyyAsvw2hel2o

- Public HUB home page now offers **Скачать для Android / Download for Android**
  without signing in. The button opens the external sharing page in a new tab
  with `noopener noreferrer`; it does not bypass OneDrive's sharing/access rules.
- Configured only `OPG3D_HUB_DOWNLOAD_URL` in `/etc/opg3d/backend.env`, preserving
  all other environment lines and file owner/mode. No application source patch,
  executable replacement or native frontend publication was necessary.
- Stopped-service backup:
  `/var/backups/opg3d/hub-download-20261025T155908Z` (protected environment,
  complete data, native active manifest, checksums and deployment report).
- Backend restarted successfully, final PID `31479`; binary remains
  `23.1.3-maintenance-fix-78fe484`. Gateway/Caddy PIDs `21993`/`10286` unchanged.
- Public stats returned the exact configured HTTPS URL; real-browser checks
  confirmed enabled link, external-link note, correct target/rel and RU/EN labels.
  No large APK download, installation or OneDrive-account login was performed.
- All data-file hashes, including maintenance settings and the current
  operator-managed whitelist, matched the pre-start backup. The operator had
  added one whitelist entry before this task; no whitelist changes were made.
  The maintenance deadline remains **2026-10-26 12:00 UTC**, manual latch off.
- The first validation still assumed the previously empty whitelist and rolled
  back only the environment change. Corrected validation to preserve/compare the
  current operator settings; subsequent rollout completed without data changes.

To change the download later, update only the HTTPS environment setting and
restart the backend. Rollback restores the previous environment file and
restarts the same maintenance-capable executable; never restore old player data.
