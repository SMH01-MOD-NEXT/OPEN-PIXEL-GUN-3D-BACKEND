# Map popularity: stock contract and conservative live presence

## Verified client contract

The owned stock arm64 client dump identifies `MapPopularityController` callback
at RVA `0x33C433C`. It checks `err_code` first; when absent it reads `data`,
enumerates two integer-keyed dictionaries, then string map keys and integer
values. The resulting cache type is
`Dictionary<int, Dictionary<int, Dictionary<string, int>>>`.

For example, a request:

```json
{"params":{"2":{"16.6.0mm_v2":{"0":["10038","1043"]}}}}
```

must receive a correlated response with **three**, not four, dictionary levels:

```json
{"req_id":103,"result":"ok","error":0,"data":{"2":{"0":{"10038":0,"1043":0}}}}
```

The version/protocol level belongs only to the request. Multiple requested
versions merge their map sets under the corresponding mode/group. Empty catalogs
produce `data: {}`; empty groups are objects, not null/arrays. Success MUST NOT
include `err_code`, even zero: its presence selects the client's error branch.

The previous handler echoed `params` and returned `popularity: {}`, but omitted
`data`, causing the observed `KeyNotFoundException`. No APK patch, exception
suppression, account reset, or economy mutation is involved in this repair.

## Live counts: measured stock path, 2026-10-22

Two distinct authenticated phones (Nothing and Realme) were observed in the
**same real Photon room**, map `1001`, mode `110002`, protocol
`16.6.0mm_v2`. Passive native tracing confirmed offlineMode=false and Joined.
The stock online-room producer sends join/map/leave changes without an APK
change. Realme's earlier tutorial sent room=`offline`; that is excluded.

`update_player_online` is now merged as a bounded **session-local delta**.
`get_map_popularity` counts unique canonical authenticated accounts, not bots,
room actorCount, messages, or lifetime matches. Explicit lobby/empty-room/map=-1
updates remove the account; an owner WebSocket close/operator abort clears its
room fields immediately, dropped sockets disappear, and sockets with no inbound
traffic for 60 seconds are excluded even before the existing session reaper runs.
A newer authenticated socket permanently retires older sockets of that account,
so closing the newer lobby socket cannot resurrect an old in-match observation.
Banned and unauthenticated accounts never contribute; identity changes clear
telemetry. No profile/progress/economy persistence is involved.

### Deliberately conservative coverage

Only the measured **23.1.3, platform component 1, variant 0, ordinary group 0**
path is enabled. The account must have supplied its own validated stock map
catalog on the same socket; the mode/protocol/map must occur in exactly one
catalog group, that group must be `0`, and stock mmrank must explicitly be `0`.
Nonzero tier_mm, ambiguous catalogs (e.g. ranked groups 1/101), other platforms,
variants, versions and unknown mappings return zero. Missing catalog after a
reconnect is not guessed from another session. Empty pre-catalog polls preserve
a previously known catalog; a nonempty validated catalog replaces it.

This avoids equating platform `group=1` or raw experience `tier=6` to popularity
group. It supports ordinary modes using the same verified stock path, but only
team-fight mode 2 has been exercised on these two phones. A multi-protocol query
for the same mode/group/map is left zero rather than combining incompatible pools
into a reply which has no protocol dimension.

### Trust and freshness limits — not authoritative Photon statistics

This is real **connected-backend-client self-reported presence**, not an
anti-cheat or authoritative Photon room-statistics feed. A modified client can
lie about its own room/catalog. API heartbeat freshness cannot detect a silent
Photon-only disconnect while the API socket remains alive; a missed Photon leave
without API loss may remain counted until a later stock update or API disconnect.
Stock reports are transitions, not 15-second full presence heartbeats. No claim
of exact all-region/all-client population is made: this instance sees only its
own connected accounts, not official Sweden or clients on other backends.

For exact partition/region mapping and deterministic Photon-only disconnect
expiry, implement the full snapshot/leave/heartbeat frontend contract in
`MAP_POPULARITY_FRONTEND_HANDOFF_23_1_3.md`, or use a trusted Photon statistics
feed. Unsupported/unknown zeroes mean **not measured**, not verified empty.

## Input safety

Requests are bounded to 64 modes, 16 versions per mode, 256 groups per version,
4096 total group references and 4096 total map references. Text keys are limited
to 128 bytes. Mode/group keys are canonical nonnegative Int32 strings to prevent
client integer-key collisions. Malformed or oversized input receives the existing
correlated error envelope, never partial success. No request-controlled strings
are echoed outside the validated data tree, and the input is not mutated.

## Original contract-repair verification and rollout — 2026-10-22 (historical)

- Dedicated routing and stock-shape regression tests, including encrypted reply
  round-trip, multi-version union, repeated calls/input isolation, empty catalogs,
  malformed nested types, numeric aliases/overflow, and aggregate size limits.
- `go test ./...`, `go test -race ./...`, `go vet ./...`, and executable build passed.
- Local backend deployed with graceful restart, private binary/state backups and
  health-check rollback support. Health passed; persisted state SHA-256 was
  identical before/after restart. No profile resync requested.
- Deployed binary SHA-256:
  `33d39b4ec7f05fd94f04be648f59cc95a75e2d15e1bb3a257cbffd8cbb26c40a`.
- Official Sweden service was NOT modified by this change.
- ADB device unavailable during verification: a fresh stock UI request and absence
  of its exception still require device reconnection. No claim of live UI proof
  or working nonzero population counters is made.

## Следующий шаг

См. `MAP_POPULARITY_FRONTEND_HANDOFF_23_1_3.md`: проверенные поля штатного
присутствия, ограничения исследования, эксперимент без новой APK и точное ТЗ
минимального frontend/backend расширения. Ненулевые счётчики ещё не включены.

## Conservative live-presence verification and local rollout — 2026-10-22

- Unit tests cover 0→1→2, repeated/delta updates, map movement, lobby leave,
  explicit drop, 60-second expiry, unauthenticated/banned users, account identity
  replacement, superseded-socket resurrection, protocol/group/catalog ambiguity,
  malformed/oversized fields, owner-vs-old WebSocket closure and operator abort.
- Full tests, race tests, vet and executable build passed on the final sources.
- Two actual phones supplied their own catalogs and online mode-2 room reports.
  Read-only encrypted Engine.IO/Socket.IO probes (no fake player auth or presence)
  observed `map 1001: 2 → 1 → 0` on the close-hook binary. The first reduction followed
  force-stopping Realme; the second followed force-stopping Nothing. Both were
  reflected on the next probe about 2 seconds later, without waiting for TTL.
  Both applications were relaunched after the test; their saved data was kept.
  A final malformed-optional-rank hardening change then passed the full suite
  and was deployed with an empty-live-state encrypted probe returning 0.
- Ordinary explicit stock leave was separately observed clearing room/map and
  reducing the count before the final close-hook refinement. The first abrupt
  close test exposed the old transport's delayed cleanup (up to 60 seconds);
  the owner-transport clearing hook fixes this, with regression coverage.
- Initial map-22 two-account test also returned 2; no offline tutorial actors
  were inserted into the counters. Final test is on map 1001, not a fabricated
  0→2 simulation. Non-team-fight modes and advanced partitions are NOT live-tested.
- Cold API restart/reconnect can temporarily undercount until the client sends
  a new map catalog; stock caches may delay that poll. During this run a cached
  catalog delayed one account's measurement until the next actual stock request.
- Final local binary SHA-256:
  `7d12bf9d74262d05ff0a527be72657e627053e1457c9f64b5fc63624ccb080de`.
  Graceful restart, private binary/state rollback backups, health check and
  byte-identical persisted state before/after deployment all passed. No profile
  resync was requested by this deployment. Official Sweden remains unchanged.
