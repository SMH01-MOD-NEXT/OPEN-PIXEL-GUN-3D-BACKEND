# Stock 23.1.3 mailbox news

Backend-only implementation. The frontend repository is read-only for this task;
no APK/native module changes or official AWS deployment are required.

## Operator workflow

1. Open `/admin/` on this server and authenticate with the existing admin token.
2. Select **Новости / Mailbox** (English: **News / Mailbox**).
3. Enter the title, summary and body in Russian and/or English. Set the optional
   **UTC** publication date (not browser-local time), currency amounts and a catalog
   weapon index (0 = no weapon). Gift letters use `DeveloperMessage` automatically.
4. Open the stock mailbox/news screen. New publications and archives refresh on
   the next stock heartbeat (usually within one keepalive interval), or reconnect.
5. **Remove letter** archives it for everyone after confirmation. It does not revoke
   granted rewards, delete receipts or reuse its ID. Unclaimed gifts are disabled.

One language is sufficient. Missing English is filled from Russian, and vice
versa, so the client's English fallback is always available. Limits are UTF-8
bytes per language: title 512, summary 2048, body 16384; request body 64 KiB.
There are at most 500 news records. Categories are `DeveloperMessage`,
`Entertainment`, `Update`, `Upcoming`, `Contest` (exact stock enum names).

News is stored in `mailbox_news` in the existing atomic `data/state.json` store.
IDs and change times belong to the server. `date` is the requested publication
time (or creation time for immediate mail); `publish_at` is canonical UTC.
Publication never edits player profiles, economy, progress revisions or accounts.
Failed writes are rolled back in memory and are not acknowledged as publications.
No automatic startup seeding occurs. `content/mailbox/test-news.json` is an
operator-selected example request, not a news item silently added to every server.

## Admin API

- `GET /api/v1/admin/mailbox/news` → `{ "news": [...], "limit": 500 }`
- `POST /api/v1/admin/mailbox/news` → HTTP 201, `{ "news": {...} }`

- `DELETE /api/v1/admin/mailbox/news/{id}` → archives the letter, idempotently.

All endpoints require the existing `Authorization: Bearer ...` admin credential.
Do not put credentials in URLs. POST accepts exactly `type`, `header`,
`short_description`, `description`, optional RFC3339 `publish_at` and
`rewards: {"coins":1000,"gems":500,"weapon":203001}`. Text objects accept
`Russian` and `English`. Currency amounts are integer 0..10,000,000, and weapon
indices must exist as type 10 in the frozen stock catalog.
Unknown properties, empty required texts, trailing JSON, unsupported types and
oversized text are rejected. Gifts require `DeveloperMessage`. Archived records
count toward the 500-record cap; there is no destructive deletion, reward editing
or personal targeting API. Publication or archival never changes balances.

## Recovered stock contract (read-only analysis)

Source: `OLD-PIXEL-GUN-3D-2313-FRONTEND/analys2313/dump2313.cs`,
`libil2cpp.so` and `global-metadata.dat` from the 23.1.3 build.

- Socket event constants: TypeDefIndex 9367, static constructor RVA `0x35BAC74`.
  Fields `+0x448`, `+0x458`, `+0x460` are respectively
  `mailbox_updated_messages`, **`mailbox_udpated_me_messages`** (stock typo),
  `mailbox_get_messages`.
- `MailBoxMessagesController` subscription setup RVA `0x33B6100`.
- Subscription callbacks `0x33B641C` / `0x33B64F0` delegate to `0x33B6424`.
  `0x33B8090` enumerates the **entire argument** as decimal ID → signed
  integer change time. The push must not contain `req_id`, `result`, `type`,
  or a wrapping `news` key. The subscribe request still receives its usual
  correlated ACK; this map is a separate encrypted topic event.
- `0x33B8A4C` sends `mailbox_get_messages` with `ids: [integer,...]`.
  The callback at `0x33B64F8` reads `news` as a dictionary. `0x33B65E8`
  parses each decimal ID key and constructs each news model at `0x33B699C`.
- The constructor reads `short_header`, `header`, `short_description`,
  `description` as language dictionaries, `type` as a string enum name,
  `ctime_change` as an integer and `date` as Int64 seconds.
- `NewsLobbyController` RVAs `0x340E21C` and `0x340F640` use the current
  language name and fall back to `English`.
- TypeDefIndex 1184 supplies category values 1..5.
- `0x33B9344` marks a model's `isReaded` locally and persists through the
  client's own cache. Read flags remain local; gift claims use the separately recovered progress protocol.

The global subscription sends `{ "1": 1792750000, ... }` and the personal
subscription sends `{}`. `mailbox_get_messages` returns a correlated response
with `news: { "1": { "short_header": {...}, "header": {...}, "type": "Update",
"short_description": {...}, "description": {...}, "date": 1792750000,
"ctime_change": 1792750000 } }`. Empty news is `{}`, never `null` or an array.
Unauthenticated/banned socket sessions cannot subscribe or fetch news.
Unknown IDs are omitted, not fabricated. Message IDs are bounded positive Int32;
both stock JSON integers and decimal-string IDs are accepted, at most 500 per call.

Text-only news omits reward fields. Gift letters add `items` as **strings**, e.g.
`["1015:1000","2015:500","203001:1"]`, plus **`id_reward: <message ID>`**.
The reward ID is stable and positive; it deliberately equals the message ID so
command 187 and slot 42 use the same server ledger key. No custom claim event or HTTP endpoint is
invented. Read flags remain stock-client-local. The admin list HTML-escapes text.

## Stock reward contract and safety

- **Visibility gate:** `NewsLobbyController` at `0x340F8DC` calls the model helper
  `0x33BA980`, which requires `_items.Count > 0` **and** `idReward >= 0`.
  Constructor `0x33B6C30..0x33B6C3C` defaults `idReward` to -1 and only fills it
  from `id_reward` at `0x33B7284..0x33B7340`. Sending `items` alone hides rewards.
  The DeveloperMessage helper `0x33BAB9C` also validates that reward ID. `r_message`
  is a separate big-reward-banner mode and is not needed for normal multi gifts.
- Existing gift records with an older `reward_wire_version` are migrated once on
  store open: only the wire version and monotonically increasing `ctime_change`
  change, forcing stock to re-fetch its cached body. IDs, schedules, gifts, account
  balances and durable claim receipts are untouched. Newly created gifts already
  carry the current wire version; later restarts do not invalidate them again.
- `NewsLobbyController.GetRewardByMessgae` RVA `0x340E9C8` invokes the mailbox
  grant path `0x33B904C`. It checks claim membership (`0x1B52ED4`), starts one
  progress transaction, grants each `OfferItem` via `0x335E104` with cause **62**
  (RewardDeveloperMessageMailbox), source 16 (MailBox), then records the message ID.
- Builder `0x1B52AFC` emits command **187**, `p: {"records":[messageID]}`.
  Command **188** removes local cache entries; the backend deliberately never
  interprets removal as permission to claim again.
- Slot 42 model TypeDefIndex 6761 serializes its integer HashSet as a bare list
  (`0x50FEEC0`) and tests membership at `0x50FF040`. The server projects its own
  `mailbox_claims` ledger into slot 42 in both download documents. Uploaded slot
  42 cannot create or clear claims. The ledger survives admin revisions/restarts.
- Message constructor `0x33B699C` reads `items` as a list of strings. Each token
  is parsed by `OfferItem(string)` (`0x29BD270` → `0x29BD724` → `0x29C5E50` →
  `0x29C53D4`, which splits at ':'), not as arbitrary reward JSON objects.
- The locked candidate reducer validates the published server letter, grants
  server-configured currencies/weapon, consumes generic reward echoes and records
  the receipt in one atomic state write **before ACK**. Source-62 currency echoes
  with wrong amounts, extra echoes or no matching 187 fail the entire batch.
  New command IDs, parallel requests and restarts never regrant the gift.
- New weapon gifts use the player's level (bounded 1..65), rarity 3 (Epic).
  Already-owned weapons retain all upgrades; they are not duplicated or downgraded.
  This does not add a general anticheat policy to pre-existing generic gameplay
  commands or client-authored balance snapshots.
- Progress transfer retains source claims and unions target claims to prevent
  rearming either account. Account-ID changes carry the ledger with the profile.
- Future/archived letters are filtered from both subscriptions and message fetch;
  an unclaimed gift cannot be obtained before publication or after archival.
  Encrypted event attachment groups are serialized against concurrent pushes.

## Requested local campaign

These are explicit admin imports, **not automatic startup seeds**; no expiry.

| Request file | Publication (UTC) | Gift |
| --- | --- | --- |
| `content/mailbox/reward-test.json` | Immediately | 1000 coins, 500 gems, Exterminator (203001) |
| `content/mailbox/test-opening-2026-10-26.json` | 2026-10-26 12:00:00Z | 2626 coins, 1010 gems |
| `content/mailbox/halloween-2026-10-31.json` | 2026-10-31 12:00:00Z | Pumpkin Thrower (143001), 3131 coins, 1010 gems |

The opening letter lists account/save checks, currency purchases and equipment,
Pixel Pass quests/rewards, player/bot battles, Escort, public/private rooms and
mailbox gifts. Text describes player-facing features only. Every existing/new
account can claim each published gift once. The previous text-only sample can be
archived when importing this campaign; keep the temporary gift until the operator
chooses to remove it.

## Verification

- `go test ./...`, `go test -race ./...`, `go vet ./...`
- `go build -o bin/opg3d-backend ./cmd/opg3d-backend`
- `node tools/console_mailbox_test.cjs` and existing console regression scripts

Tests cover auth/bans, schema, malformed/oversized inputs, per-session encrypted
subscribe ACK + topic push + message fetch, persistence/restart, rollback,
account isolation, bounded concurrent publication, RU/EN, HTML injection and
double-submit prevention, UTC scheduling, stock command-187 dispatch, forged
amounts, replay with new IDs, ownership preservation, concurrency, restart, disk
rollback, new accounts, transfer/rename and authoritative slot-42 projection.

Local operation is separate from official AWS. No Android device was connected
when implemented, so actual in-game display/read-cache behavior is a manual
acceptance check, not a claimed completed device test.
