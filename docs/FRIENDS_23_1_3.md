# Friends, private chat and public profiles (23.1.3)

## Scope

Backend implementation for the existing Android 23.1.3 friends UI: search by
nickname or exact player ID, outgoing/incoming invitations, explicit acceptance,
rejection, mutual friendship, removal, private chat, and public player profiles.
No game frontend files or APK assets are changed. Clan chat and friend
recommendations are not part of this change.

## Measured native contracts

Read-only evidence: `analys2313/libil2cpp.so` and `dump2313.cs` in the frontend
checkout. Addresses are RVAs in the stock 23.1.3 library, not proposed hooks.

| Event | Request / response | Native evidence |
| --- | --- | --- |
| `get_users_info_by_param_v3` | `param`; response `players` is an array of PlayerData dictionaries including `id`, not an array of IDs | `0x1D99924`, `0x1D99FA4` |
| `friend_request` | `whom` | `0x235F438` |
| `accept_friend` | `acceptee_id` | `0x235D7B4`, `0x235DAB8` |
| `reject_invite_friendship` | `rejectee_id` | `0x2360AAC` |
| `reject_friendship` | `rejectee_id` | `0x235C60C` |
| `delete_friend_invites` | no target; clear own incoming invitations | `0x2360CD0` |
| `friend_invites` subscription | `incoming`, `outcoming` arrays of IDs; `delta:0` replaces lists | `0x235E608`, `0x235F94C` |
| `friends_list` subscription | `friends` array of IDs | `0x3884544`, `0x3884654` |
| `friend_added`, `friend_removed` | push containing `id` | `0x3884ABC`, `0x3884D54` |
| `send_private_message` | `to`, `text`, `timeStamp`, `last_timeStamp`, `clan:false`; acknowledgement contains server `timeStamp` | `0x40134F8`, `0x40153EC` |
| `get_private_message` subscription | `list` of peer IDs, `last_timeStamp`; partial unsubscribe also supplies `list` | `0x4013F94`, `0x4015CC8` |
| private message push | `messages:[{"<numeric timestamp>":{"from":"...","text":"...","clan":false}}, ...]` | `0x401431C` |
| `get_all_short_info_by_id_v3` | `ids_v2:[{id,s_h,cl_h}]`; response `info` maps ID to PlayerData | `0x24FFA88`, mapper `0x2507314`, callback `0x2500D28` |
| `all_short_info_by_id_v3` subscription | separate key `list_v2:[{id,s_h,cl_h}]`; same `info` map | `0x2500544` |

All responses use the existing encrypted two-attachment Socket.IO framing.
Correlated acknowledgements echo `req_id`; subscription pushes do not. Successful
answers must omit `err_code` (the native client treats its presence as failure).
Outgoing frame groups are queued atomically: a full queue must not leave an
orphaned header or partial attachments. A failed subscription push is retried on
a later refresh without advancing its signature/cursor.

## Ownership and persistence

- Actor comes exclusively from the authenticated socket session. Client-supplied
  sender/owner fields cannot impersonate another player. Existing session ban
  checks remain active. This does not replace the existing game login protocol.
- Only the intended recipient can accept or reject an incoming invitation.
  Crossed invitations still require explicit acceptance. Repeated requests and
  acceptance do not create duplicates.
- Private chat requires a current mutual friendship. A third party cannot read
  or send into another pair's chat. Removing a friend revokes access and deletes
  that pair's stored message history; it cannot erase messages already cached
  on a recipient's device.
- Server-generated, monotonic microsecond timestamps order messages. Exact
  actor/peer/text/client-timestamp retries are deduplicated for five minutes.
- Social state is saved with the existing atomic state writer. Failed writes
  restore in-memory social state. Player ID changes, account reconciliation and
  account deletion respect canonical identities; delete/rename failure rolls back.
- Chat text is excluded from incoming and outgoing diagnostic body traces,
  including verbose tracing. Messages remain in the protected backend state
  file and backups; this is not end-to-end encrypted messaging.
- Public profiles use the native allowlist of gameplay fields. They do not
  expose wallet balances, IPs, device IDs, login material, or support information.

## Limits and compatibility boundaries

- 100 friends and 100 pending invitations per direction per account.
- 30 social actions and 30 new messages per minute per account; search is capped
  at 120 requests/minute, 20 results and a 128-byte search term. These volatile
  limits reset on server restart; they are not a complete anti-spam system.
- Message text: at most 512 Unicode code points and 2048 UTF-8 bytes; invalid
  UTF-8, empty text and control characters other than newline/tab are rejected.
- Invitations expire after 14 days. Chat retention is up to 30 days, 100 messages
  per pair and 20,000 messages globally. A response returns at most 100 incoming
  messages; this is bounded restoration, not an unlimited history archive.
- Stock 23.1.3 caches the optimistic outbox locally. The backend restores incoming
  messages in the native format, not a complete cross-device sent-message history.
- Clan-only `list:["-1"]` subscriptions/unsubscriptions do not erase private-chat
  subscriptions. Clan messages themselves are rejected, not treated as DMs.
- Subscription changes publish immediately after successful mutations and refresh
  on existing socket heartbeats. Offline recipients receive retained incoming
  messages when they subscribe again.

## Verification

Automated coverage includes actual encrypted request decoding/dispatch/replies,
two-player invitation/accept/remove lifecycle, spoofed actor rejection, third-party
chat denial, duplicate delivery protection, offline restoration, partial unsubscribe,
clan isolation, public profile privacy, full-queue retries, persistence/reopen,
identity reconciliation, injected write failures, concurrent accepts/sends and limits.

Run `go test ./...`, `go test -race ./internal/store ./internal/api
./internal/clienterrors`, `go vet ./...`, and all `tools/*test.cjs` scripts.

Device acceptance remains necessary: on two real accounts search by ID/nickname,
send/accept/reject requests, open the friend's statistics, exchange messages in
both directions, reconnect the recipient and remove the friend. No Android device
was connected through adb during implementation; automated tests are not a claim
of an on-device UI pass.

## Public profile repair and ordinary squads (2026-10-24)

The full-profile subscription is `info_by_id_v3` with `list_v2`, followed by
separate `info_by_id_v3` pushes shaped `{id,info:PlayerData}` (callback
`0x2501514`, parser `0x2502B50`). PlayerData `s` contains custom base64 skin
image data, while `a` contains a built-in avatar resource. Standard resource
names previously placed in `s` could throw in `Convert.FromBase64String` at
`0x4024CD4`, interrupting profile rendering. The backend now separates these
fields, strips the resource `.txt` suffix, and supplies a deterministic `sh`
cache key. Invalid/oversized custom data falls back to the default avatar.
PlayerData `v` uses a valid reported version or `23.1.3` for legacy profiles on
this version-specific backend. Online presence remains volatile and does not
write progress just to record a version. Nickname, level and available battle
statistics still come from the actual profile; missing history is not fabricated.

Ordinary squad signalling is implemented using the existing native contracts:

- `send_squad_invite` request (`0x456ACA4` / `0x456B2A8`) contains `receiver_id`,
  `squad_name`, `protocol`, `invite_number` and integer `*_mode` flags.
- `squad_invites` subscription (`0x456AC18`) receives full replacements shaped
  `{squad_invites:[{id,name,squad,protocol,invite_number,siege_mode,clanwar_mode,test_mode}]}`.
  The consumer at `0x4569F68` requires a list, not a dictionary keyed by ID.
- Sender identity and displayed nickname are taken from the authenticated
  account, never client-supplied `name` or sender IDs. Only current friends can
  send/receive. Removed friendships stop exposing pending invitations.
- Invites expire after two minutes, replace older invitations for the same pair,
  and are deduplicated without extending their lifetime. Limit: 10 new invites
  per sender/minute, 20 pending per recipient and 2,000 globally. Room/protocol
  lengths and integer ranges are validated. Invitations are volatile and reset
  on backend restart; reconnecting/subscribing restores only unexpired invites.
- Immediate delivery, heartbeat refresh, expiry replacement, unsubscribe and
  retry-after-full-queue reuse the encrypted social subscription transport.

Photon still owns room creation/joining and connectivity. Relaying an invitation
is not verification of Photon room ownership or a guarantee that the room still
exists. Clan-war, siege and test squad modes are explicitly rejected until their
separate permission/membership requirements are implemented. No frontend edits
were made. Two-client device acceptance must still verify the profile UI and
invite popup, then acceptance and joining the inviter's actual Photon room.
