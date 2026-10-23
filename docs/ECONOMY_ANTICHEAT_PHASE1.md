# Economy anti-cheat: gradual rollout

User-approved policy (2026-10-23): observe unverified stock economy paths while
retaining proven server-backed validation. No automatic bans. No game frontend
or cheat project changes. This is NOT full authoritative economy or combat.

## Decisions and coverage

- Mailbox entitlements/amounts/availability come from server news and persistent
  claims, not uploaded slot 42 or reward amounts. Changed child IDs cannot grant
  the same mailbox reward again.
- Native/HUB currency exchanges retain server catalog prices, balance checks and
  atomic updates. HUB exchange IDs are durable; repeated valid unlimited native
  exchanges are legitimate separate purchases, not globally suppressed.
- Pass reducers retain frozen season/cell/claim rules. Their validation does NOT
  prove client-reported task completion, kills or match outcomes.
- Existing measured command shape, integer range, underflow, owned-module and
  transaction rollback checks remain enforced. Child ID + content receipts stop
  exact retries across restarts, but are NOT proof of grant legitimacy.
- New coverage telemetry observes measured commands after semantic reducers,
  including unconsumed currency/inventory/progression/combat/loadout commands.
  Unknown commands outside the measured reducer still rely on opaque snapshots;
  they are not interpreted or treated as verified.
- All six authenticated upload methods are observed: prefs, flat progress,
  boot document, partial slots, complete replacement slots and client state.
  Changed currency/progression/inventory/loadout and opaque fields are counted;
  unchanged JSON-equivalent values (integer vs JSON number) are ignored.
- Observations are created under the gameplay lock and emitted outside it.
  Trusted operator/server writes without client context are excluded. Diagnostic
  persistence failure never rolls back or falsely rejects gameplay.

## Confirmed rejection recovery

Invalid mailbox/exchange transactions now arm the same per-device authoritative
full-download handshake as a pass state conflict. Nonzero stock `err_code`, outer
`i`, optional single-child `ci` and transport request correlation remain intact.
No false success ACK, forced socket abort or speculative reward is introduced.
Pending revisions do not increase repeatedly; the existing download/login
boundary retires the marker. Other malformed pass requests retain old behavior.

## Operator use

In the protected operator console, open **Ошибки клиента** and search:

- `economy_observed`: accepted measured commands or changed snapshot attempts;
- `economy_rejected`: operation rejected by existing validation/storage rules;
- `economy_phase1`: module for both classes;
- filter by player and backend source to review one account.

Messages contain fixed server-generated coverage counts, channel, outcome and
rule only, never uploaded bodies, device identifiers, credentials or ticket/chat
text. `verified` means consumed by a semantic reducer, not combat legitimacy.
Snapshot `observed` denotes an attempted upload, not a purchase approval.
`validation_or_storage` includes storage failures: it is not a cheating verdict.

The existing independent `client-errors.json` journal retains at most 2000 events
for 7 days, rate limits backend events to 60/player/minute and 600/global/minute,
and coalesces identical events within 30 seconds. Limits are shared with other
backend diagnostics, so this is sampled operational evidence, not a complete
security audit ledger. Check journal availability/rate-limited counters. Normal
compatible operations can appear; never ban solely on these events.

## Explicit remaining gaps

Ordinary weapon/module price/resource tables and trusted combat reward authority
have not been recovered. Generic credits, absolute XP/level changes, inventory
changes and uploaded wallets/inventory remain compatible under observation;
changing child IDs can still produce unverified grants. Full snapshots can still
introduce unverified ownership/currency. Slot ownership/loadout consistency is
NOT comprehensively enforced in this phase. No guessed prices, hidden limits or
silent gameplay disabling.

PUN/Photon realtime combat is not simulated by this Go service. This release does
not stop local aim, recoil, damage, movement or room manipulation. Future price
recovery and combat/Photon authority require separate scoped implementation;
frontend changes require explicit permission.

## Verification

Store/API tests cover all snapshot channels, numeric-equivalent uploads,
unchanged/trusted exclusions, accepted unverified grants, durable exact retries,
mailbox verified vs forged claims, transaction rollback on invalid claims and
storage failures, callback re-entry/concurrency, journal privacy/persistence/
rate limits/unavailability, correlated mailbox/exchange rejection and completion
of the authoritative reload. Existing mailbox/pass/exchange and support tests
remain required.
