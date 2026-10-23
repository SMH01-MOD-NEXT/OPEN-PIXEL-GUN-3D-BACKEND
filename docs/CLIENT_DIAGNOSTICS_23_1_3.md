# Client diagnostics — 23.1.3

## Collection and transport

`POST /api/v1/client-errors` accepts 1–10 events (32 KiB request cap). The
server assigns canonical player, timestamp and source. Codes and severity are
allowlisted; clients cannot submit backend events or impersonate another player.
The native client captures Unity Error, Assert and Exception callbacks and sends
bounded reports from a background worker, never from the Unity callback thread.
It also reports progress-reload start, failure and return to the lobby.

Registration returns a separate `diagnostics_token`, signed using the random
32-byte `client-diagnostics.key` (0600, same data directory). The token is bound
to the registration player, valid for 24 hours and accepted ONLY by diagnostics.
It survives gameplay-session rotation/identity reconciliation and server restart.
Bans are still enforced. A client running continuously beyond 24 hours must
restart to renew it. Removing/rotating the key revokes existing tokens; include
it in protected backups, never publish it. Corrupt/unavailable key disables
minting/validation, not game startup. Existing clients can use regular session
authentication with `X-OPG3D-Player` and Bearer or `X-OPG3D-Session`.
The new client uses `X-OPG3D-Diagnostics` and never treats it as gameplay authority.

This follows the existing registration trust model (device-bound development
accounts). It is not device attestation. Existing HTTP transport is not encrypted;
production TLS remains a separate deployment requirement.

## Journal and limits

`client-errors.json` is separate from player state, atomically persisted with
0600 permissions. Retention: at most 2,000 accepted events, at most seven days.
An unreadable journal is not overwritten: ingestion returns 503 and the console
shows storage unavailable. Disk-write failure rolls back the in-memory append.

Limits: 60 events/minute per source/player, 600 globally; 429 with Retry-After.
Client queue: 32 in memory, repeated message/stack suppressed for 30 seconds,
three delivery attempts, 5-second worker interval (60-second pause after 429).
Client IDs make retries idempotent. Queue is not a durable offline outbox.

Credential-containing lines are removed; URLs, emails, paths and long opaque
identifiers are redacted; message/stack sizes are bounded. Diagnostic request
bodies are hidden from HTTP traces even with body tracing enabled. Sanitization
is defense in depth, not a guarantee against all possible secrets in free text.
Reports are escaped before rendering. Review access and retention accordingly.

## Progress reconciliation

Backend records `resync_required` on a real Pixel Pass conflict,
`resync_failed` if reload scheduling fails, `progress_update_failed` on other
counter-command failures, and `resync_completed` only when the real applied
revision acknowledgment arrives. Operator-triggered reloads use
`operator_resync_scheduled` (not an error). Client `resync_lobby_ready` only means
the lobby loaded; it is not an authoritative revision acknowledgment.

## Administration

`GET /api/v1/admin/client-errors`: existing admin Bearer authentication;
filters `q`, `player`, `level`, `source`, `offset`, `limit` (max 100).
Groups cover the last 24 hours, based on sanitized source/code/message/stack,
client version and module. A group shows counts, affected players, first/last
seen and the latest example, with revision and stack where present.

`GET /api/v1/admin/stats` includes `client_errors`: accepted reports, error
reports, unique groups, affected players, server-requested resyncs and actual
applied revisions. These are retained-event counts, not all crashes/occurrences;
rate-limit counts are batches rejected since process start. The RU/EN console
has a dedicated Client errors tab and overview cards. Existing AWS gateway
keeps admin routes private; access through the existing trusted admin path.

## Verification and limitations

Go unit/integration/race tests cover authentication, scoped credentials,
identity/session rotation, privacy, persistence/restart, write failure,
retention, grouping, throttling and actual Pixel Pass resync lifecycle.
Node VM tests exercise the actual UI renderer, filters, pagination, RU/EN,
HTML escaping and stale-route responses. Native tests cover queue, dedup,
privacy, JSON and resync notifications; release build passes.

This is Unity managed error reporting, NOT a native crash reporter. SIGSEGV,
SIGABRT, Android ANR and process kills require a separate tombstone/ANR pipeline.
Events before the module is initialized or still queued at process death can
be lost. Warnings and normal logs are intentionally not collected.

## Deployment verification

AWS backend: `23.1.3-client-diagnostics-v2`; binary SHA-256
`a5ce6bb8cbd26b11bb9b8e8103049c63c4f67f23b218ac6870c2a18e7f0516d2`.
Signed frontend module: build 49, `23.1.3-client-diagnostics-v2`, SHA-256
`4e9acbc2be3dceba7d7db9017b5cdae20340865c1f30ba2631ca66592d2015f0`.
Both public artifact signature/hash and backend health were verified; two player
accounts preserved. Gateway/admin exposure unchanged. Backend rollback backup:
`/var/backups/opg3d/client-diagnostics-20261023-093716`; module backup:
`/var/backups/opg3d/aws-release49-20261023-093739`. Reserved signed monotonic
rollback build 50 is NOT active.

A real server-side `resync_completed` event was observed in the journal and
both admin list/stats endpoints. Module 47 hooks ran on Nothing but ingestion
exposed gameplay-session replacement (401), leading to the scoped-credential
fix covered by integration tests. Nothing disconnected before final module 49
end-to-end retest. Do not claim device delivery verified until reconnecting and
seeing a 202 response plus its canonical-player event in the admin journal.
