# Map popularity: stock 23.1.3 contract repair

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

## Important scope: not live population statistics

The backend does not currently own or query Photon room statistics. All counts
returned by this implementation are explicitly **zero**. This repairs the stock
parser/cache contract, not the end-to-end live-popularity feature. Zero means
there is no authoritative count available here, not a verified empty Photon room.

Do not populate it using bots, historical play totals, or `update_player_online`
without completing a separate verified presence integration. Observed presence
reports include partial updates, encoded game-mode values, client version rather
than matchmaking protocol, and offline/lobby activity. They are not interchangeable
with this endpoint's request tree. A future real statistics source must establish
mode/group/version mapping, online-only semantics, account deduplication,
disconnect/expiry handling, and isolation between server populations. Do not sum
incompatible protocol populations merely because the response lacks a version key.

## Input safety

Requests are bounded to 64 modes, 16 versions per mode, 256 groups per version,
4096 total group references and 4096 total map references. Text keys are limited
to 128 bytes. Mode/group keys are canonical nonnegative Int32 strings to prevent
client integer-key collisions. Malformed or oversized input receives the existing
correlated error envelope, never partial success. No request-controlled strings
are echoed outside the validated data tree, and the input is not mutated.

## Verification and rollout — 2026-10-22

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
