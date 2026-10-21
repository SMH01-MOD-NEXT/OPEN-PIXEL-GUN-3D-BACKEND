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
