# Stock 23.1.3 mailbox news

Backend-only implementation. The frontend repository is read-only for this task;
no APK/native module changes or official AWS deployment are required.

## Operator workflow

1. Open `/admin/` on this server and authenticate with the existing admin token.
2. Select **Новости / Mailbox** (English: **News / Mailbox**).
3. Choose a category and enter the title, summary and body in Russian and/or English.
   Publish immediately to all authenticated players of this backend.
4. Reconnect the game to obtain a new mailbox subscription and open the stock
   mailbox/news screen. Merely refreshing the admin page does not reconnect the game.

The server does **not** push new publications to already subscribed sockets in
this first implementation. News is delivered on the next stock subscription;
reconnecting is the reliable refresh path. The admin console says this explicitly.

One language is sufficient. Missing English is filled from Russian, and vice
versa, so the client's English fallback is always available. Limits are UTF-8
bytes per language: title 512, summary 2048, body 16384; request body 64 KiB.
There are at most 500 news records. Categories are `DeveloperMessage`,
`Entertainment`, `Update`, `Upcoming`, `Contest` (exact stock enum names).

News is stored in `mailbox_news` in the existing atomic `data/state.json` store.
IDs, Unix-second publication times and change times belong to the server.
Publication never edits player profiles, economy, progress revisions or accounts.
Failed writes are rolled back in memory and are not acknowledged as publications.
No automatic startup seeding occurs. `content/mailbox/test-news.json` is an
operator-selected example request, not a news item silently added to every server.

## Admin API

- `GET /api/v1/admin/mailbox/news` → `{ "news": [...], "limit": 500 }`
- `POST /api/v1/admin/mailbox/news` → HTTP 201, `{ "news": {...} }`

Both endpoints require the existing `Authorization: Bearer ...` admin credential.
Do not put credentials in URLs. POST accepts exactly `type`, `header`,
`short_description`, `description`; text objects accept `Russian` and `English`.
Unknown properties, empty required texts, trailing JSON, unsupported types and
oversized text are rejected. No edit/delete/scheduling/reward/personal targeting
API is provided in this scope.

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
  client's own cache. No server read/claim protocol is invented here.

The global subscription sends `{ "1": 1792750000, ... }` and the personal
subscription sends `{}`. `mailbox_get_messages` returns a correlated response
with `news: { "1": { "short_header": {...}, "header": {...}, "type": "Update",
"short_description": {...}, "description": {...}, "date": 1792750000,
"ctime_change": 1792750000 } }`. Empty news is `{}`, never `null` or an array.
Unauthenticated/banned socket sessions cannot subscribe or fetch news.
Unknown IDs are omitted, not fabricated. Message IDs are bounded positive Int32;
both stock JSON integers and decimal-string IDs are accepted, at most 500 per call.

Text news intentionally omits `items`, `id_reward`, `reward_type`, `reward_cnt`,
`r_message`, targeting and external picture/link fields. There are no fake gifts
or unverifiable claims. Read flags remain stock-client-local, not cross-device
server state. Text formatting follows the stock client; the admin list HTML-escapes
all submitted text and does not execute markup.

## Verification

- `go test ./...`, `go test -race ./...`, `go vet ./...`
- `go build -o bin/opg3d-backend ./cmd/opg3d-backend`
- `node tools/console_mailbox_test.cjs` and existing console regression scripts

Tests cover auth/bans, schema, malformed/oversized inputs, per-session encrypted
subscribe ACK + topic push + message fetch, persistence/restart, rollback,
account isolation, bounded concurrent publication, RU/EN, HTML injection and
double-submit prevention.

Local operation is separate from official AWS. No Android device was connected
when implemented, so actual in-game display/read-cache behavior is a manual
acceptance check, not a claimed completed device test.
