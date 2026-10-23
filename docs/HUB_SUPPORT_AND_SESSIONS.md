# HUB sessions and support

## Browser sessions

- The Secure, HttpOnly, SameSite=Strict, host-only `__Host-opg3d_session` cookie has a 90-day maximum age and explicit expiration. The server enforces an absolute 90 days from authentication, not a sliding indefinite session.
- Session-token digests, account-incarnation fingerprints and authentication times are persisted in AES-256-GCM encrypted `data/web-sessions.enc`. Raw browser tokens and game OTPs are not persisted. CSRF derivation remains stable across restarts.
- Preserve the existing `OPG3D_HUB_SECURITY_KEY` and **all** encrypted files: `hub-security.enc`, `web-sessions.enc`, `support.enc`. Losing/rotating this key loses access to stored protection, sessions and tickets. Missing keys or corrupt security/session files fail closed; corrupt support storage disables support without bypassing game login.
- Explicit logout is durably revoked. Changes to 2FA, recovery codes or passkeys increment a persistent security epoch and revoke previous sessions. Account/device incarnation changes and bans are checked on every authenticated request. Security configuration still requires authentication within five minutes.
- Existing process-local sessions from the old release cannot be migrated: those browsers must sign in once after this deployment. New sessions survive navigation, reload and backend restarts. The brand navigation is also handled in-page, but full-page navigation must not invalidate a valid session.
- Tests cover navigation/restarts; immediate navigation-only loss was not independently reproduced before changing the old process-local implementation. The old implementation definitely lost all sessions on restart and imposed a one-hour idle limit.

## Player support

The HUB navigation now includes Support. Recovery is available without game login; bug reports and general questions require login.

Recovery asks two short yes/no/unknown questions (access to old device and whether it changed) and four optional fields (device model/OS, approximate last login, last shop purchases, recent currency spending). Additional information is optional for recovery, required for bug reports/questions. No payment receipts are requested. Never send game OTPs, 2FA codes, device identifiers, passwords or passkeys in tickets.

- A guest receives a random ticket number plus a secret access code. The browser remembers guest ticket access for up to 90 days using a separate Secure/HttpOnly/Strict cookie. Another browser can reopen the ticket using the saved number and code, sent only in a POST body.
- The secret is a high-entropy ticket-only capability, **not** account authentication or ownership proof. A claimed public game ID never grants game-account access or makes the claimant the authenticated owner. Recovery decisions and any account transfer remain manual operator actions.
- Authenticated tickets belong to the authenticated account incarnation. Logging out does not leave those tickets accessible through the guest cookie. Guest codes cannot unlock authenticated tickets.
- Players see their tickets, status and persistent conversation. `needs_info` becomes `open` after a player reply. Resolved/closed tickets are read-only to players; staff can reopen them.
- Forget access removes guest browser grants, not the tickets. Keep the ticket number/code to return. There is no email delivery or attachment upload in this release.

## Operator and support-admin access

1. Open the existing protected operator console through its normal local/SSH route. Public `/admin` and `/api/v1/admin` remain blocked by the gateway.
2. In **Players → player card**, the verification-evidence disclosure shows real available registration/last-seen/IP/device observations/currency movements. Technical device IDs are confidential reusable credentials; do not send them to the claimant.
3. The **HUB support administrator** checkbox and **Save support permissions** button grant/revoke the backend role for that account incarnation. No player is auto-selected or auto-promoted.
4. In **Support → Player tickets**, review tickets, answers and evidence, reply, filter/page through the queue and set Open / Additional information required / Resolved / Closed.
5. Alternatively, sign in to HUB as the flagged player and use **Support → Player tickets**. This role grants access to tickets and verification evidence only, not the server administration token or wallet-edit endpoints.

Role changes are operator-token-only and auditable; the server checks the role on every request. UI visibility is not authorization. Claiming a staff player's game ID in a recovery ticket cannot grant the role.

## Ownership evidence and provenance

The server retains at most 24 bounded device observations per account incarnation, deduplicated over 24 hours. Observations record server time, observed IP, supplied User-Agent and allowlisted device model/OS/platform/version fields when the existing client actually supplies them. HTTP registration/legacy login and proven socket login are sources. Existing 23.1.3 analysis exposes Unity's device-model accessor, but does not establish that every request transmits it. A missing model is shown as **not supplied**, never inferred from an ID or fabricated. Client fields and device identifiers can be spoofed and IPs shared; compare several independent signals.

Currency evidence retains at most 100 actual nonzero wallet-delta events. Authoritative HUB exchanges include the offer ID; accepted game-command batches record aggregate currency deltas with separate provenance. This is not a complete item-purchase ledger and does not retroactively recover old transactions. Replays, rejected writes and no-ops must not add events. Older purchases, snapshots and every possible administrative wallet change are not reconstructed or asserted as proof.

## Storage, limits and privacy

- `support.enc` is separately encrypted and atomically replaced, mode 0600, using purpose-specific authenticated encryption under the existing stable key. It includes tickets/messages, hashed browser grants, access-code verification material, support roles and audit trails. Plain ticket access codes are not stored or logged.
- Player responses omit private proof hashes, IPs, audit records and account evidence. Staff details are returned only through authenticated, role-checked routes. Untrusted text is rendered with `textContent`, never HTML.
- Origin/HTTPS/host checks, support-cookie CSRF, bounded JSON, per-IP read/write/create/unlock throttles, pagination, ownership/incarnation checks, optimistic revisions and idempotency keys are enforced server-side. Successful guest recovery does not expose whether the claimed game ID exists.
- Limits: 100 tickets per creator browser/account incarnation, 10,000 total, 200 messages and 1,000 audit events per ticket, and an 8 MiB serialized support-state ceiling to bound memory/write costs. Capacity failures refuse new writes; they do not overwrite old tickets. Support is intended for this small test server, not an unbounded enterprise help desk.
- Existing tickets/history are not automatically purged. Keep backups private, monitor capacity, and establish an operator retention policy before a longer public rollout. No email/Telegram ticket-content forwarding or other third-party storage was added.
- Back up encrypted files **with their existing environment key** after stopping the backend; never copy local profiles over EC2 authoritative data. Do not roll back to an OTP-only release that bypasses persisted 2FA.

## Verification

Go unit/API tests cover encrypted persistence, tampering, failed-write rollback, session restart/idle/90-day expiry/logout/freshness, missing keys, ticket capabilities/ownership/IDOR, role checks/revocation/incarnation, status transitions, retries/CAS/concurrency and evidence bounds. Run `go test -race ./...`, `go vet ./...` and `node tools/console_support_test.cjs` alongside the existing console tests.

Isolated Chromium fixture checks exercise the actual HUB/operator JS, separate recovery fields, guest receipts, tickets/admin replies, RU/EN, error states, text-only XSS handling, navigation and reload. Visual QA is performed at 390 px mobile and 1440 px desktop. Fixture tokens, codes and accounts are synthetic, not live player data. Server persistence/authorization is covered separately by real handler/store tests, not inferred from UI fixtures.

### Player-initiated closure
Owners (including guest recovery capability holders) may close their own open or needs-info ticket after confirmation. This status-only operation uses the same CSRF, ownership, revision, idempotency and durable-write checks as replies and records `closed_by_player` in the private audit trail. It preserves all messages; players cannot reopen or change other statuses, or close already resolved tickets. Support staff retain their existing status controls.
