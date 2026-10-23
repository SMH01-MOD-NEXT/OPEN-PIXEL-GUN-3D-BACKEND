# Website login through the stock 23.1.3 mailbox

Backend only. The Android/game frontend and existing campaigns are not modified.
This is the login foundation and a small test page, **not a completed bug-report website**.

## Deployment and URLs

Set `OPG3D_WEB_AUTH_ORIGIN=https://opg3d.smh01.net.eu.org` (exact HTTPS origin,
no trailing slash). Empty disables all web-login routes. Stable/deterministic
sessions are incompatible and rejected. Login page: `/account/login`.
Existing HTTP gameplay stays compatible; HTTPS on the website does **not**
automatically upgrade the current game client's HTTP/WebSocket transport.

The EC2 server already runs Caddy for another domain. Do not stop it, bind
Certbot standalone to 80/443, edit its unused Caddyfile, or replace its JSON.

1. Cloudflare: A `opg3d` -> `13.51.187.37`, DNS only. Remove a conflicting AAAA.
2. Install Certbot. Install `tools/ec2-web-auth-tls.py` as root-owned 0755
   `/usr/local/sbin/opg3d-web-tls`. Run it with `prepare` to add only the ACME
   webroot on port 80, preserving the other site's routes.
3. Once public DNS resolves, issue (ACME terms accepted by the operator):

```sh
sudo certbot certonly --non-interactive --agree-tos \
  --register-unsafely-without-email --webroot -w /var/www/opg3d-acme \
  -d opg3d.smh01.net.eu.org --cert-name opg3d.smh01.net.eu.org
sudo /usr/local/sbin/opg3d-web-tls activate
```

4. Deploy hook `/etc/letsencrypt/renewal-hooks/deploy/opg3d-caddy` must call
   `/usr/local/sbin/opg3d-web-tls activate` only for this certificate lineage.
   The script copies this certificate/key into `/etc/caddy/opg3d-tls` with
   root:caddy ownership, directory0750/files0640; never make the key public.
5. Enable `certbot.timer`; run `certbot renew --dry-run` after successful issuance.
   This setup does not enroll an invented contact email: configure a real
   operator email separately if expiry notices are wanted.
6. Check public HTTPS, HTTP->HTTPS, `/api/v1/ping`, `/account/login`, and 404s
   on `/admin/` and `/api/v1/admin/...`. Verify the existing forward domain
   remains healthy. JSON backups are stored under `/var/backups/opg3d/tls-*`.

Without DNS/certificate issuance there is **no working public HTTPS login**.
The backend routes can be deployed safely beforehand: plain HTTP, wrong Host,
wrong Origin, cross-site requests, query credentials and untrusted forwarded
HTTPS assertions are rejected.

## Browser protocol (same origin only)

All mutation requests require `Origin` equal to the configured origin.
JSON bodies are limited to1024 bytes; unknown/trailing fields are rejected.
Send cookies with `credentials: 'same-origin'`; never put codes/tokens in URLs.

- `POST /api/v1/web-auth/challenge`, JSON `{"player_id":"123456789"}`
  ->202 `{"reference":"abcdef","expires_in":300}`. Sets a Secure, HttpOnly,
  SameSite=Strict, host-only `__Host-opg3d_challenge` cookie. Unknown/banned
  accounts have the same accepted response, but receive no letter.
- An authenticated game socket gets a personal DeveloperMessage, with the
  matching reference and an eight-digit code. **Only confirm requests you
  started**; a player-ID-only request from a stranger is not evidence of login.
- `POST /api/v1/web-auth/verify`, JSON `{"code":"12345678"}` with that browser's
  challenge cookie. Success consumes the challenge atomically and returns only
  playerID, `csrf_token`, expiry and the `bug_reports` scope. A separate
  `__Host-opg3d_session` cookie is set. No game/admin credentials are returned.
- `GET /api/v1/web-auth/me` -> public player ID/nick, CSRF token, scope, expiry.
- `POST /api/v1/web-auth/cancel` -> cancels only this browser's challenge.
- `POST /api/v1/web-auth/logout` requires `X-CSRF-Token` from verify/me.

The future bug-report endpoints must validate the same web session and its
CSRF token for every mutation. A scope label alone is **not** authorization
middleware, and no bug-report write endpoints are included in this change.

## Lifecycle, limits and client cache

- Challenge/code lifetime5 minutes, single use, maximum5 incorrect attempts.
- Creation limits:10/IP/10min and3/account/10min (canonical account for aliases).
  Verification60/IP/10min. Bounded2048 challenge records,2048 web sessions and
 8192 rate buckets; expired state is pruned. Closed records stay at most15min
  from creation to provide code-free updates to an already subscribed socket.
- Browser challenges are independent. A new request does not invalidate another
  browser's code; a new request from the same browser cancels its old one.
- Web session:24h absolute,1h idle. Identity recreation/rebinding/rename and
  active account/device/IP bans invalidate it. Restart invalidates every
  process-local challenge/session. No OTPs or session cookies go into state.json.
- OTP letters never contain gifts or reward IDs and never enter admin news.
- The stock typo `mailbox_udpated_me_messages` is preserved. Active letters
  use reserved positive Int32 IDs, Russian/English dictionaries and explicit
  owner `toId`; fetching a guessed ID does not bypass recipient checks.
- The stock initial personal cleanup classifies DeveloperMessage as personal.
  Shared campaigns of that type are included in the personal manifest too,
  preventing OTP cleanup from accidentally erasing scheduled global gifts.
- Client removal RVA0x33B6424/0x33B8B9C runs on the **initial** subscription,
  not every update. Live sockets receive a higher-ctime code-free "completed"
  replacement after use/cancel/expiry, on their next heartbeat. Reconnection
  receives active-only letters, removing expired cached personal messages.
  Immediate visual disappearance is not claimed; the code is invalid immediately.

## Security boundaries

Player ID alone does not authorize mail. Stock sockets must prove the stored
socket-device binding or a validated native session. Legacy recent-IP
correlation alone does not grant personal mail on that socket; reconnect after
initial binding. Existing-account socket auth now rejects empty/different
unproved devices instead of silently overwriting the binding. An authenticated
native session remains the intentional route for a legitimate binding change.
Account-incarnation fingerprints prevent a stale socket/session from following
an account replacement.

Device identifiers are still not attested credentials. Their cloning/leakage,
legacy initial account binding, a compromised device, and game HTTP/network
interception remain risks. This login proves access to the mailbox under the
existing game's credential model; it is not strong identity verification.
Do not use this session for purchases, admin, account recovery or currency writes.
Moving game traffic to TLS needs a separately approved frontend/launcher change.

Only loopback proxy forwarded headers are trusted by web auth. EC2 must keep
backend18080 bound to loopback, and Caddy must not trust arbitrary incoming
forwarded headers. Web-auth request/response bodies/queries and decoded mailbox
response bodies are redacted from diagnostics. Login UI does not store codes
or session credentials in localStorage and uses no third-party scripts.

## Verification

`go test ./...`, `go test -race ./...`, `go vet ./...`, console Node tests,
static Linux/amd64 build. Automated tests cover owner isolation, guessed IDs,
ID-only impersonation/binding preservation, stock language/schema, single use,
concurrent verification, cancellation/expiry, dummy accounts, rate/state caps,
restart, idle/absolute session lifetime, rebinding, CSRF, origin/transport and
untrusted proxy rejection, and diagnostic plaintext-secret redaction.
A real client test of letter layout/cache timing is still required.
