# TASK_PLAN — Open Pixel Gun 3D Backend

Status keys: `[ ]` pending · `[/]` in progress · `[x]` completed

## Phase 0 — Analysis (done)

- [x] Confirm the client's main-server transport is **HTTP**, not WebSocket
      (`BestHTTP`/`UnityWebRequest`; the WebSocket code in the build belongs to
      the Photon peer and PGCompany analytics).
- [x] Inventory the retired hosts the 23.1.3 client talks to:
      `server-v2.pixelgun3dserver.com/auth_v2/`, `secure.pixelgun3dserver.com`
      (configs, ABTests, BuffSettings, rating/map configs),
      `pixelgun-clientlogs-{eu,us}.lightmap.com/problem`,
      `player-prefs.lightmap.com`, `geoip.lightmap.com`,
      `events-stat-{1,2}.lightmap.com`, `acct.pixelgun3dserver.com/events`,
      `secure.pixelgunserver.com/invite_service`, `cfg.pixelgun3d.com/config.json`.
- [x] Decide the split: server owns state and policy; the APK keeps only
      "where is my server" plus the transport shim.

## Phase 1 — Server core (done)

- [x] Go module, GPLv3, `.gitignore` excluding player data and secrets.
- [x] `internal/config`: environment-only configuration, no committed secrets.
- [x] `internal/logs`: one-line key=value events; verbatim body capture with
      hex fallback; dated log file plus stdout.
- [x] `internal/store`: accounts, rotating sessions, bans (player/device/IP,
      with expiry), atomic persistence.
- [x] `internal/secrets`: AES-256-GCM envelope, player-bound AAD, key derivation.
- [x] `internal/api`: v1 routes, legacy `/auth_v2/` `/time` `/problem` bridge,
      `/@host/...` prefix stripping, admin endpoints behind a bearer token,
      `http.unhandled` logging for uncovered routes.
- [x] `internal/discovery`: UDP LAN responder (`OPG3D-DISCOVER-V1`).
- [x] `cmd/opg3d-backend`: graceful start/stop, boot diagnostics.
- [x] Tests: registration/session rotation, sealed AppID + AAD binding,
      ban enforcement, admin gate, rewritten legacy path, update availability,
      store persistence and ban expiry.

## Phase 2 — Endpoint coverage driven by real logs

- [ ] Capture one full boot-to-battle session of the client pointed at this
      server; collect every `http.unhandled` line.
- [x] Live config channel transport: `internal/api/websocket.go` (minimal
      RFC 6455 server, standard library only) and `internal/api/socketio.go`
      (engine.io v3 **and** v4, chosen per session from the `EIO` parameter:
      polling handshake in both framings, long-poll GET/POST, WebSocket
      upgrade, `2probe`→`3probe`, heartbeat in whichever direction the
      version requires, namespace connect, session registry with idle
      reaping). `recorder` gained `Hijack()`, without which the upgrade cannot
      happen.
- [x] Correct the protocol assumption. The v3-only reading was wrong: in
      `global-metadata.dat` the templates are `{0}?EIO={1}&transport=...`, so
      `EIO` is a substituted parameter rather than a literal, and the WebSocket
      template carries no `sid`. The client requests
      `/socket.io/?EIO=4&transport=websocket&client_ver=v2&c=1`: straight to
      WebSocket, no polling. Both versions are served now, keyed off `EIO`.
- [x] Find the actual cause of the 77 % stall: a handshake deadlock, not the
      heartbeat. `dump2313.cs` shows the client is `BestHTTP.SocketIO`, with no
      `SocketIO3` namespace anywhere in the build, and its `HandshakeData`
      carries only Sid/Upgrades/PingInterval/PingTimeout - the socket.io **v2**
      client, whatever its query says. A v2 server connects the default
      namespace on its own and the client waits for that packet to open its
      `Socket`; it never sends `40` first. This server waited to be asked, so
      both sides waited: `packets=1`, not one `socket.rx` line, then a clean
      `close_code=1000 close_reason=Bye!` at 6.6 s, repeating on a fresh sid.
      The root namespace is now announced immediately after the open packet.
      The reversed v4 heartbeat was a real bug but not this one - the client
      leaves long before the 25 s ping timer fires, so the ping loop now yields
      to the client as soon as it sees a ping of its own.
- [x] Instrumentation for the next boot: `socket.rx` traces every inbound
      packet verbatim on both transports, `socket.ws_closed` carries the close
      code, close reason and packet count, and `socket.ws_open` records the raw
      query.
- [x] Bind binary attachments to the event that declared them
      (`internal/api/socketio_binary.go`). This is what the capture was waiting
      on, and it reshapes everything below it.

      The 2026-08-27 boot (`log_back.txt`) shows the client's first real
      request on the channel:

          40/sio
          452-/sio,[{"_placeholder":true, "num":0}, {"_placeholder":true, "num":1}]
          <binary 17 bytes>   04 37 4b c0 db ...
          <binary 609 bytes>  04 2d 1c c1 dd ...

      Three readings follow, and each closes a question that was open:

      1. **The event name is not on the wire.** `arg[0]` is a placeholder, so
         the name travels inside attachment 0. `event=<unparsed>` was never a
         parser bug — there was nothing to parse. No further log reading will
         produce a name.
      2. **The payload is encrypted.** With the marker removed the attachments
         are 16 and 608 bytes, both exact multiples of the AES block size
         (608 = 38 × 16). They are not plain MessagePack.
      3. **The leading `0x04` is the engine.io v3 binary marker.** BestHTTP
         sends it on the WebSocket transport despite asking for `EIO=4`, which
         is the same v2-client behaviour that made the namespace auto-connect
         necessary above.

      The server now buffers the frames per session, binds them to the
      preceding `BINARY_EVENT`, strips the marker inbound and restores it
      outbound, and traces every attachment with its length, marker state and
      block alignment (`socket.binary_header`, `socket.binary_event`,
      `socket.attachment`). An abandoned or expired assembly is reported rather
      than leaked, a frame arriving with no event waiting is reported instead
      of dropped, and `socket.ws_closed` now carries how much of a pending
      event was still missing. The XHR transport's base64 `b` framing works in
      both directions too, so a pushed document can no longer be silently
      discarded before the upgrade.
- [ ] Recover the attachment codec. This is now the single blocker for the rest
      of the channel. Parse the registration path for the constants instead of
      guessing them; keep the key in `.env` (`OPG3D_SIO_KEY`), put the codec in
      `internal/secrets` beside the existing envelope, and unit-test it against
      the captured 16-byte and 608-byte blobs.
- [ ] Only then: name the events the client sends, and answer them. The names
      cannot be assumed. `get_progress_slots`, `get_progress_hashes_v2`,
      `get_binded_ids`, `get_reg_ver` and `full_check_version` appear nowhere —
      not in the capture, not in `dump2313.cs`, not in the client repo's docs —
      and `CommandsWithFullSlots` is not an RPC at all: it is
      `PGCompany.ConfigId = 7` (`dump2313.cs:449064`), a config document id on
      this same channel. Whatever attachment 0 decrypts to is the real list.
- [ ] Serve the config documents over the channel from server-side JSON. The
      consumer is `BalanceController.<obfuscated>(String, Byte[], <type>,
      ConfigId)`, so a document arrives as a name plus a MessagePack byte array
      identified by a `ConfigId`.
- [ ] Deliver the slot config with a `versionDict` entry for `23.1.3`. The
      client-side failure is `ParseFullSlotConfig: versionDict not contains
      version 23.1.3` (`log_front.txt:654`, raised from a `Progress.*`
      coroutine), so this is a `ConfigId` document on this channel, not an HTTP
      file.
- [ ] Player profile / progression persistence server-side (replaces the
      client-side `Storager` trust model).
- [ ] Clans, mailbox, leaderboards, friends/invites.
- [ ] Decide per endpoint: implement, stub with a logged 200, or refuse.

### The auth state machine needs no further disassembly

The remaining boot question — what moves the auth scene out of
`Initial`/`Authorizing` — is already answered in the client repo, and the
answer is negative in a useful way:

- `AuthSceneState` (`dump2313.cs:92853`): `Initial 0`, `Authorizing 1`,
  `Authorized 2`, `FullySynchronized 3`, `Empty 4`, `CheckBindedId 5` …
  `TechnicalWorks 15`.
- `AuthSceneController.Start` (`0x3DC1BCC`) tail-calls the dispatcher
  (`0x3DC1F38`), which handles only `0`, `1` and `2`.
- A caller scan over 161,400 parsed methods finds **zero** call sites for the
  state getter (`0x3DC0920`), the state setter (`0x3DC0968`) or the
  session-ready setter (`0x3DC0A04`): IL2CPP inlined those
  `[CompilerGenerated]` accessors and stock code writes the static field at
  `+0x10` directly (`docs/PORT_23_1_3_AUTH_COMPLETION.md`, client repo).

So there is no single call to find: nothing calls a setter. The state is moved
by whichever response handler writes that static field, and the second state
machine names the route that matters to a server — `AuthorizationStates`
(`dump2313.cs:230714`) has `AuthorizingWithHttp 3`,
`AuthorizingWithSessionKey 4` and **`WaitSocketAuthReponse 6`**. Authorization
completes on a socket response, on the channel above. This item is therefore
blocked on the codec as well, and needs no further static analysis first.

## Phase 3 — Client-side surgery (repo: OLD-PIXEL-GUN-3D-ONLINE-PROJECT, branch 23.1.3)

- [ ] Remove the in-process C++ backend: `backend_emu_http.h`,
      `backend_emu_routes.h`, `backend_emu_store.h`, `backend_emu_2313.h`,
      `backend_local_2313.h`, `backend_local_1610.h`, `identity_2313.h`.
- [ ] Remove the retired-service compensation layers that the real server makes
      unnecessary (`online_state_2313.h`, `net_stall_guard_2313.h`,
      `cloud_guard.h`, `cheat_guard.h`, progression/pixel-pass/hidden-items and
      the rest of the local content grants), keeping the APK signature patch.
- [ ] Add the server selection screen: LAN scan or manual host/domain entry,
      persisted; all user-facing text through the localization files.
- [ ] Add the thin transport shim: rewrite retired hosts to the selected server,
      attach player id and session, decrypt the AppID envelope.
- [ ] Take the update check and download URL away from the client and read them
      from `/api/v1/update`.
- [ ] Client-side traffic log: request, response, and the value the client
      expected when a parse or state transition fails.

## Phase 4 — Operations

- [ ] `deploy/`: systemd unit and container file.
- [ ] Optional TLS / reverse-proxy notes for the VPS case.
- [ ] Admin CLI or minimal web panel for accounts and bans.
- [ ] CI: `go vet`, `go test`, cross-compiled release binaries.
- [ ] Decide whether live events need a WebSocket channel; add it only if a
      real use case appears.
