# Backend-only Pixel Pass season 1 (23.1.3)

## Season and economy

An authored Cyberpunk-themed season using existing stock localization and items,
not a claim to reproduce an official historical season byte-for-byte.

- UTC lifecycle: 2026-10-21 00:00 through 2026-12-20 00:00 (exclusive).
- 50 cells on five pages; cumulative thresholds 0, 100, …, 4900 crowns.
  Cell 1 is immediately claimable, including on player rank 1.
- Free cells: 1, 5, 10, 15, 25, 35, 45. Other cells require premium.
- Premium: **600 Pixel bucks**, currency ID `PixelPassCurrency` / item 26015.
  Premium + ten levels: 1200; an individual level costs 20 Pixel bucks.
- Coins, gems and Pixel bucks; featured weapon indexes: free 832001 at cell 10,
  premium 1292001/1277001/1291001/1279001 at cells 20/30/40/50.
  New weapons receive the player's current rank (1–65), rarity 5; already-owned
  weapon upgrades are not downgraded. Each reward can be collected once.
- Three calendar objectives unlock daily: play 3 matches, defeat 25 enemies,
  win 1 match; each gives 60 crowns. Older unlocked calendar objectives remain
  available. There are 180 calendar objectives, with distinct IDs, not local
  midnight resets. Three elite objectives require 100 matches, 1000 kills,
  30 wins and grant 400 crowns each; completing all unlocks 100 gems.
- Stock end-match reward tables grant 20/15/10 crowns by placement, with score
  threshold 50. No ads, offers, paid rerolls or repeatable post-pass cells.
  No account receives premium or reward weapons simply by logging in.

## Stock contract and source of truth

`internal/pixelpass/season.go` is the shared frozen catalog used by both delivery
and the reducer; a content-directory PixelPass override cannot change prices or
rewards independently of validation. `ExpOpen.json` preserves the stock embedded
116 document, changing only `feature.pixelpass`'s experience threshold to zero
(and adding the conventional configId label).

The existing config poll and `config_info_versions` push host `PixelPass` (123)
and `ExpOpen` (116). Both use inline JSON text in `val`, not raw JSON disguised
as a binary/MessagePack attachment. These models have the `(id,1,true)` config
attribute; the season getter at 0x1A08038 reads 123 through shared accessor
0x2606038. No frontend source or client patch is part of this implementation.

Important reverse-engineered details from the supplied ARM64 23.1.3 build:

- Reward converter 0x24B4260 accepts `itemIndex:amount`, **not** type:index:amount.
- Tier/task `f` and state booleans are salted **integers**, not JSON booleans.
- `GameTaskActionSettings.gamemode` uses enum `None=100`, **not** `Any`.
  Objective actions use stock `endMatch` / `killPlayer` and `shouldwin=1`.
- Pass slot **74** is `{"pp":{...}}`; state fields include `i,p,e,lr,ctr,c`.
  `c` is the client's opaque saved-config reference, not a nested season model.
- GameTask slot **33** uses `5_<task index>` keys; command payloads 150–152
  contain a dictionary of partial task records (`p,c,r`), not a flat task ID.
- Pending `ctr` values encode taskIndex → itemIndex → quantity. Commands
  337/338 add/remove pending rewards; crowns are reported separately in 326.
- SaveConfig 322 (`i,c`), ClaimCell 323 (`l,r`), Premium 324, EliteClaim 325,
  ObtainExp 326 (`e`), EliteComplete 328, Presentation 335, PendingTask 337
  (`t,r`), ClaimTask 338 (`t`), PremiumPrompt 339 (`t`) are persisted.
- Active-season reset 331 is denied; closing an expired season is accepted and
  projects an empty pp while retaining the server audit ledger. Ads/rerolls/
  offers are disabled and their purchase commands are rejected.

The native reward manager emits generic money/inventory operations before the
claim marker in the same outer command transaction (regular claim 0x18F4ADC;
command factories 0x1B58C70–0x1B5A114). The reducer consumes matching generic
reward/debit echoes and applies catalog rewards once. A candidate profile and
its receipts are committed atomically before success is acknowledged. Failed
validation or fsync rolls the entire batch back. Existing command-ID/content
receipts survive restart; standalone native commands have only outer `i`
(no child `ci`), so that stable outer ID is used internally for receipts; cell, premium and task claims additionally have
semantic one-time guards when a whole claim is retried under new IDs.

`pixel_pass_ledger` is server-only persistence. Snapshot uploads cannot replace
its season, premium, crowns, claimed cells or objective progress; both download
channels are re-projected from it. Unknown/unacknowledged pass task rows are
removed from snapshots. Unrelated slot data is preserved.

## Trust boundary / deliberate limits

The service does **not** host the Photon battle simulation. Match results and
objective progress are bounded client reports, not independently verified kills
or wins. Match reports accept at most 50 crowns per command and 1000 per UTC day;
calendar/elite rewards and correctly priced level purchases have separate budgets.
Retry protection depends on stable native ci/content for ordinary match reports
and level purchases. It is not universal anti-cheat. Existing generic currency,
inventory and full economic snapshot trust outside pass state is unchanged.

Only this frozen season exists. At expiration, claiming/purchasing/progress stops;
there is no automatic season rollover. Releasing another season requires an
explicit catalog/ledger migration, not changing config files behind old receipts.
Expired claims are rejected, but an ended season can be closed normally.

## Verification and operation

Automated coverage includes all 50 cells from player rank 1, free and premium
restrictions, normal/pack purchases, partial-cell level purchase, all quest types,
elite completion before individual claims, pending reward serialization, match
caps, semantic and exact retries, reopen/persistence, concurrent claims, stale
snapshot uploads, invalid data and persistence-failure rollback. API tests cover
stock outer transaction acknowledgement/correlation, config hashing, inline push
and the rank-one feature gate. Lifecycle reducer tests use an injected timestamp;
the live-time API smoke is skipped outside this deliberately fixed season.

Run `go test ./...`, `go vet ./...`, `go test -race ./...`, existing console tests,
and build both `cmd/opg3d-backend` and `cmd/opg3d-siokey`. Deployment uses a private state,
binary and environment backup, graceful stop, atomic binary swap and health
check with binary rollback on startup failure. Never mutate a live account for
synthetic pass testing. Real phone UI/gameplay verification remains required:
`adb devices` exposed no connected device during implementation.

Deployment smoke: an unauthenticated encrypted Engine.IO/Socket.IO config poll
against the running binary successfully received the exact catalog and the
rank-one ExpOpen gate in `config_info_versions`. No account was authenticated
or modified by that smoke. An independently reconnecting real client supplied
a native standalone SaveConfig shape without ci; the API now normalizes its
outer i for durable receipts, with regression coverage. The client can also
upload the saved config reference in slot 74 before command delivery; only that
reference is recovered on first ledger creation, never unverified premium,
crowns or claimed rewards.

## Zero-match lobby lock correction

The exact client has two ExpOpen configs: **ExpOpen=116** and
**ExpOpenForB=176**. Both are polled by the actual reconnecting client; the
provider at 0x366900C–0x366902C selects one using a cohort flag. The embedded
116/176 documents are identical. Hosting only 116 does not unlock cohort B.

The Pixel Pass row lookup (0x1A0D51C → 0x3669480) can then return null.
Its UI threshold getter 0x18EE3D4 defaults to zero for a null row, while its
entry predicate 0x18EEF7C → 0x20DF334 returns false for null. This explains
“play 0 matches” combined with an entry that stays locked after a match;
it is not a zero-crown problem and not a missing tutorial quest.

The backend now hosts ExpOpenForB as a strict alias of the canonical ExpOpen
catalog, with inline JSON delivery for both IDs. Both cohorts have the actual
feature.pixelpass row with threshold zero; unrelated gates stay untouched.
There is no season-ID change, account migration, artificial match completion,
crown reset, premium reset or frontend edit. Regression tests cover both
cohorts' non-null row, zero threshold, equal catalogs/hashes, poll cache hits
and inline push bodies. Physical UI confirmation still requires restarting
the client so its config provider ingests the newly supplied 176 document.

## Claim conflict recovery (2026-10-22)

Real 23.1.3 claims for level17/135 coins and level18/14 gems were rejected by
server eligibility, not a process crash. Paid level purchases and the premium
pack were durable. Live client memory later held 2440 crowns against 2320 in the
server ledger: a persistent 120-crown disagreement. The historical origin of
that excess is not proven; do not compensate by inventing server crowns or
weakening level/premium checks. Normal reconnect alone retained the optimistic
local pass state, causing subsequent catalog-valid claims to fail again.

Catalog-valid claims blocked by authoritative crowns/premium now return typed
`ErrPixelPassStateConflict`. The API keeps a failure acknowledgement (including
outer i/optional ci) and durably requests the existing correlated `sync_full` +
Login download. It does not forcibly close a transport, issue unearned rewards,
or report success for a failed grant. Malformed rewards are ordinary validation
errors and do not schedule a reload. This recovery can cause one authorization
reload; it is not a promise to suppress every validation-error reconnect.

`RequestProfileProgressResync` preserves all gameplay, balances, premium,
claimed cells and **payment command receipts**. It does not call the operator
revision helper that clears receipts. Repeated pending requests reuse a revision,
a persistence failure restores the old profile, and existing authenticated
revision gates protect against stale uploads while the download is pending.
Once canonical progress is loaded, eligible rewards follow the normal atomic,
idempotent claim path without an extra reload.

Regression coverage includes the observed paid pack/four level purchases,
1520-crown level17 rejection, full reload/restart, exact paid retries without a
second debit, a subsequent valid single grant, level18 rejection at1620,
forged rewards, persistence rollback, and the API marker/download/Login boundary.

Device recovery verified revision109 through a real correlated full download
and durable Login acknowledgement, with both client and server at2320 crowns
and premium still active. Before/after recovery gameplay and receipt payloads
were identical. The next observed claim (level19,145 coins) committed two native
commands, received err_code=0, appeared in the reward UI and increased stored
coins by145 on the same authenticated socket session. No subsequent conflict or
authorization reload was observed during this verification. This is recovery
of a measured divergence; the origin of the earlier120-crown drift remains open.
