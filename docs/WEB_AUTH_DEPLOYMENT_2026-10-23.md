# Personal mailbox OTP deployment — 2026-10-23

## Delivered

- Backend commit `5b85499`, runtime version `23.1.3-mailbox-otp-5b85499`.
- Static Linux/amd64 binary SHA256:
  `527658b536659496b0c639c680bc8384de99b85c405df255596655be2071d3b5`.
- AWS backend restarted successfully, service PID26325 at verification.
- Local backend rebuilt/restarted, PID60021 at verification, same binary hash.
  Local launch exports `OPG3D_WEB_AUTH_ORIGIN=https://opg3d.smh01.net.eu.org`;
  the existing local `.env` was not replaced.
- Test page: https://opg3d.smh01.net.eu.org/account/login
- This includes the earlier admin mailbox editing commit `867b9a7`.

## Preserved state and rollback

Protected AWS backup:
`/var/backups/opg3d/web-auth-20261023T133040Z-5b85499`.
Includes stopped-service binary/environment/data snapshot, frontend manifest
and a sanitized deployment receipt. Root-only directory; never publish its data.

Startup comparison: account2->2, profile2->2; state.json byte-for-byte unchanged.
Local state.json also byte-for-byte unchanged. No local accounts/test campaigns
were copied to AWS, no OTP/session state was persisted to gameplay storage.

Client manifest SHA256 unchanged:
`0f4238d665f654816693fd07fdddfebc05b237140f85e4d017c396e57bbac86f`.
Game frontend was read only; its preexisting dirty files remain untouched.

Scheduled campaigns unchanged:

| AWS ID | UTC publication | Gift |
| --- | --- | --- |
|1|2026-10-26 12:00|2626 coins,1010 gems|
|2|2026-10-31 12:00|3131 coins,1010 gems,PumpkinThrower143001|

For code rollback, stop only opg3d-backend, restore its old binary/environment
from this backup, then start/health-check. Do **not** restore an old data snapshot
on top of later player progress. TLS rollback uses the timestamped root-only
`/var/backups/opg3d/tls-*` JSON snapshots; keep the unrelated forward site's routes.

## HTTPS

- DNS A record confirmed at authoritative Cloudflare and public1.1.1.1/8.8.8.8:
  `opg3d.smh01.net.eu.org -> 13.51.187.37`; no AAAA.
- Certbot webroot issuance succeeded. Certificate CN matches domain, validity
  2026-10-23 through2027-01-21.
- Existing main Caddy JSON extended with an isolated host; the separate
  port8080 game gateway and existing forward site remain healthy.
- Caddy skips its own automatic issuance for this host because the Certbot
  certificate is loaded explicitly. Key root:caddy0640, directory0750.
- certbot.timer enabled/active; `certbot renew --cert-name
  opg3d.smh01.net.eu.org --dry-run` completed successfully (exit0).
- Dedicated deploy hook installed, invoked manually and verified; certificate
  copies refreshed and Caddy force-reloaded without stopping the other site.
- Public HTTP redirects308 to HTTPS. Public HTTPS admin routes return404,
  including `/@original.host/...` normalized-path aliases.

## Checks

- `go test ./...`, `go test -race ./...`, `go vet ./...`: passed.
- Six existing console Node suites: passed; login JS syntax: passed.
- Public TLS certificate/hostname verification enabled for probes.
- Public login HTML/JS/localizations and health200; unauthenticated me401.
- Unknown-account challenge202 with TTL300 and secure cookie, cancel200,
  cross-origin mutation403. No real player's unsolicited code was requested.
- Forged X-Forwarded-Proto on the legacy public HTTP gateway403.
- Public admin routes404, including rewritten host-prefix variants.
- Existing forward site HTTPS200, legacy public game health200, local health200.
- Public DNS resolvers resolved the new name, but the development workstation's
  system resolver still held an earlier negative cache entry during checks.
  External TLS probes pinned **only DNS** to the authoritative A record, retaining
  real SNI/hostname/certificate validation. Do not confuse this with disabling TLS
  verification. A workstation/browser may need its negative DNS cache to expire.

## Still requiring a real client check

No Android device was connected. Verify mailbox layout and update timing on the
actual23.1.3 client. Stock cache removal is not instantaneous: a code-free completed
letter replaces the live cached letter on heartbeat; it disappears on reconnect.
Code invalidation itself is immediate on use/cancel/expiry. Initial legacy IP-only
binding is not enough for personal mail on that socket: reconnect with the stored
matching device. Game HTTP/WebSocket traffic was intentionally not changed to TLS.
The full bug-report website remains a separate future feature.
