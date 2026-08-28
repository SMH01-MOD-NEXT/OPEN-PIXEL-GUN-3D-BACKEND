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

      Read `socket.ws_closed ... err=EOF` as operator action, not as a protocol
      failure. The captured `alive_s=65` close is the game being shut down by
      hand: heartbeats (`socket.rx packet=2` / `packet=3`) were still flowing
      seconds earlier, and the client applies no timeout of its own to `/sio` --
      after its first binary emit it waits indefinitely. An unanswered event
      therefore never disconnects anyone, which is why the channel can be left
      open while the codec is worked out.
- [ ] Recover the attachment codec. This is now the single blocker for the rest
      of the channel. Parse the registration path for the constants instead of
      guessing them; keep the key in `.env` (`OPG3D_SIO_KEY`), put the codec in
      `internal/secrets` beside the existing envelope, and unit-test it against
      the captured 16-byte and 608-byte blobs.

      The encryption is measured, not assumed. Both captured sessions produced
      the same two lengths (16 B and 608 B, each an exact multiple of the AES
      block size), yet across sessions the blobs share no structure at all:
      common prefix 0 bytes, 1/16 and 2/608 byte positions equal, which is
      what chance alone predicts. The 608-byte bodies carry 7.64 bits of
      entropy per byte over 227-229 distinct values; the 16-byte bodies use 16
      distinct values, so every single byte differs. A MessagePack document
      would repeat structure and open with a stable type tag - these open with
      0x37, 0x2d, 0xd6 and 0xcc. Nothing is appended for an IV or a MAC, so the
      frame length is exactly the ciphertext length: attachment 0 hides at most
      15 bytes of plaintext under PKCS#7, which is the size of a short event
      name.

      The emitter is located, and it is not the socket library.
      `BestHTTP.SocketIO` is stock, and its `Emit(string, object[])`
      (`dump2313.cs:422755`, RVA 0x3ECE0C4) has exactly two callers, so the
      game seals the bytes before emitting them:
      `Rilisoft.WebSocket.WebSocketManager` and `MatchmakingSocketManager`.
      Only the former's wrapper carries bytes - `(string, object, byte[])` at
      RVA 0x35C31E8 - and it is reached from one place only, the payload
      builder at RVA 0x35C2D4C, so `/sio` is `WebSocketManager`. Neither
      direction contains a crypto call: inbound runs
      `Packet.ReconstructAttachmentAsIndex` plus `Convert.ToInt32`, meaning the
      client resolves attachments by index itself. The bytes therefore arrive
      already sealed from outside the socket layer.

      The seal sits one level below that wrapper, and the class is now pinned
      rather than inferred. Two classes in `Rilisoft.WebSocket` carry the same
      obfuscated member names, and arithmetic separates them: the address
      accessors the client logged at `0x77EABF6C50` and `0x77EABF6C48`, minus
      the logged `libil2cpp.so` base `0x77E7635000`, are `0x35C1C50` and
      `0x35C1C48` -- the accessors of `WebSocketManager`, TypeDefIndex 9374
      (`dump2313.cs:381634`). The other cluster is `MatchmakingSocketManager`.
      Inside the live class the codec is the pair `丌丗七与丘丄丌丞万`
      (RVA 0x35C4028) and `丞丛业上丗专丏丄三` (RVA 0x35C4170), both
      `(Dictionary<string,object>, Dictionary<object,object>, List<byte[]>)`:
      open dictionaries on one side, the wire's attachment vector on the other.
      The second name occurs exactly once in the whole dump.

      One consequence reorders the plan: the send API still carries the event
      name as a plaintext `string`, so the RPC names are recoverable by tracing
      alone, without breaking the codec first.

      The key material has an honest source, so nothing needs to be guessed.
      `PGCompany.丈丛业不东丅丐丝世` (`dump2313.cs:448770`) hands out byte
      arrays: one path runs `Convert.FromBase64String` over an embedded
      constant, the other calls `Rilisoft.万东丕丏丄万丝且不.世丌丟丟丁上不丗三()`
      (RVA 0x26C933C), which builds its result with `SHA1Managed` and
      BouncyCastle array helpers over a value read from an `AndroidJavaObject`
      - the APK signing certificate. Both constants can be recovered from the
      APK already on disk. Do not confuse any of this with
      `PGCompany.丅丝业七三丈丝丑丏`, whose `"BinaryConfigStorage.Key"` and
      Rijndael container (32-byte key, 16-byte block, 20-byte MAC,
      `RNGCryptoServiceProvider`, `dump2313.cs:452433`) seal the *local disk
      cache*; that framing cannot fit inside a 16-byte attachment.

      That signature path turned out to be anti-tamper rather than the wire
      codec, and the offline search for a static key is now closed as
      exhausted. A metadata sweep for base64-shaped constants returned only a
      2048-bit RSA modulus with exponent `AQAB`, sitting among Mono's
      `<RSAKeyValue>` XML literals: a public verification key, not a symmetric
      one. `analys2313/` ships no Il2CppDumper string-literal side files, so the
      literal handed to `Convert.FromBase64String` cannot be named from the dump
      alone, and the four app-owned GUID-shaped constants were all rejected by
      the decrypt oracle.

      The decisive evidence is structural. Attachment 0 was `374bc0db...` in
      session `329b4708...` and `9135b38a...` in session `bdf88121...`, 16 bytes
      each, for what is by position the same first event. A static key under ECB,
      or a static key under a fixed IV, would have produced identical blocks. So
      the codec is keyed or IV'd per connection -- and 16 bytes total leaves no
      room for an IV shipped beside the ciphertext, which would need at least 32.
      Zero-IV oracle sweeps are settled negative and should not be repeated.

      Device access is no longer the blocker. The device is attached with root;
      the installed `base.apk` is self-signed because the trace library was
      injected into it, and the untouched original sits on the device at
      `/data/local/tmp/orig_sign.apk`, certificate SHA-1
      `7b68ad8a71e44c888f27184bd7690ef92738a5ae` against the `6ecc4203...` of
      the resigned package. Any signature-derived constant must be computed from
      the original, never from what is installed.

      The remaining route is the logging-only client trace, and it has landed:
      `opg3d/src/main/cpp/sio_codec_trace_2313.h` (client repo, branch
      `23.1.3-thin-client`, commit `f556192`) hooks the emit wrapper, both codec
      overloads and the inbound dispatcher, printing the open event name, the
      `byte[]` blob and every attachment before and after the original call.
      That produces a known plaintext -- the event name -- against the exact
      16-byte block already captured on the wire, which is the pair needed to
      identify key and mode.
- [ ] Name the events the client sends, and answer them. This no longer waits
      on the codec: the trace above reads the names in the open. They still
      cannot be assumed -- `get_progress_slots`, `get_progress_hashes_v2`,
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

## Session 2026-08-28 - the 78% stall is an unanswered `auth_v2` on `/sio`

Run facts (front sid 67858a36960f1e274f042b61):

- The bar stops at `bar=0.750` (UI 78%) right after `AuthSceneController.Awake
  RETURN`. `InitializeSwitcher` finishes (state 45, one step skipped by the
  client stall guard), the main-menu scene load is requested, and the client
  then waits in `AuthorizationStates.WaitSocketAuthReponse (6)`.
- The client emits a socket.io binary event on `/sio`
  (`452-/sio,[{_placeholder,num:0},{_placeholder,num:1}]`): args[0] is a sealed
  16-byte blob, args[1] a sealed 608-byte blob. The backend buffers both
  attachments and never answers. Only engine.io ping/pong follows. The
  `connection reset by peer` is the user backgrounding the game, not a bug.

### Outbound payload codec - fully recovered from A64 (read-only analysis)

- Codec class `Rilisoft.丏东丑丄东丈丟且东` (TypeDefIndex 8304):
  `.ctor(byte[] password)` 0x3B680F4, decrypt `丂东且丅丏丑三专丛` 0x3B6835C,
  encrypt `业不丐丅丒丝丙专万` 0x3B6883C.
- Derivation exactly as compiled:
  - `salt = new byte[8]`, filled by `RuntimeHelpers.InitializeArray` from a
    metadata field default value (usage slot 0x6756008 -> FieldInfo, fieldRef 115).
  - `Rfc2898DeriveBytes(password, salt, iterations = 1).GetBytes(48)`.
  - `key = derived[0..32]` (`Array.Copy` 0,0,32), `iv = derived[32..48]`.
  - `Rijndael.Create()`, `KeySize = 256`, `BlockSize = 128`,
    `Mode = 4 (CipherMode.CFB)`, `Padding = 2 (PaddingMode.PKCS7)` - read from
    the SymmetricAlgorithm vtable slot order (BlockSize 0x1a8, IV 0x1e8,
    Key 0x208, KeySize 0x248, Mode 0x268, Padding 0x288). Mono's Rijndael keeps
    `FeedbackSize = 128`, so this is full-block CFB. The earlier "CBC" note in
    this plan was an assumption and is wrong.
- The password is not a literal: `BestHTTP.SocketIO.SocketManager.get_Extras`
  (0x3ECF1F4) builds 32 bytes by XOR-ing the first static `byte[]` field of two
  classes (`class + 0xb8`, then `[0]`), news the codec and caches it in the
  SocketManager field at +0xa0.
- Ctor call sites: 0x0249FDF8 (`PGCompany.东下丘上丗丞丅丆丌`), 0x03B692AC
  (`Rilisoft.丑丙丛专丘下不丙上..ctor`), 0x03ECF35C (`get_Extras`).

### Inbound direction needs no codec - the actionable finding

`WebSocketManager.丂丁与丛丕丑丅丁业(Socket, Packet, object[])` 0x35C396C:

- `if (packet.AttachmentCount >= 1) Packet.ReconstructAttachmentAsIndex(packet, 0)`,
  then `args = Packet.Decode(encoder)`. BestHTTP takes the event name from JSON
  element 0 as a plain string (`Packet.EventName`, field +0x38).
- Gate: `authController.<bool>() || EventNames.IsAllowed(packet.EventName)`;
  otherwise it logs "Incoming event is skipped because client is not authorized"
  (string literals 3586 / 3587 / 11658).
- Handling: `args[0] as Dictionary<string, object>` -> `三丟丄下丂丏丅丏不`
  (0x35C3E54) -> `Dictionary<object, object>`, then
  `丌丗七与丘丄丌丞万(header, payload, List<byte[]>)` 0x35C4028 and
  `丞丛业上丗专丏丄三(...)` 0x35C4170 with `packet.Attachments` (+0x40).

Server -> client is therefore plain socket.io JSON: `42/sio,["<event>", { ... }]`
plus optional binary attachments. The backend does not need the outbound codec
to answer; it needs the right event name and the right JSON body.

### Correlation

`丝七丛丅丅一丒不专(event, Dictionary<object,object>, timeout, callback, byte[])`
returns `ValueTuple<int, Task<丙下七丄业丁丆丅丐>>` and `WebSocketManager` keeps
`Dictionary<int, TaskCompletionSource<...>>`, so every request carries an integer
id that the reply must echo. That id sits inside sealed attachment 1, so a
correct reply still requires decoding attachment 1.

### Tooling (host side, /tmp, outside both repos)

- `/tmp/md2.py` reads IL2CPP metadata v29 (`sanity fab11baf`, `version 29`):
  `slots` resolves a code metadata-usage slot (slot -> pointer -> encoded index,
  `kind = enc >> 29`, `index = enc & 0x1FFFFFFF`; 1 TypeInfo, 2 Il2CppType,
  3 MethodDef, 4 FieldInfo, 5 StringLiteral, 6 MethodRef), `grep` searches all
  43998 string literals, `auto` walks a disassembled range.
- Verified header offsets: stringLiteral 256/351984, stringLiteralData
  352240/1475060, fieldDefaultValues 12149532/283020,
  fieldAndParameterDefaultValueData 12432552/251240, fields 14523620/1362288,
  typeDefinitions 17115492 (23762 types x 88 bytes), fieldRefs 19216012/7472.
- `analys2313/` has no `script.json`, so usages and literals must be resolved
  this way.

### Closed hypotheses (do not repeat)

- [x] attachment 0 is not `auth_v2` under any key derived from sid, device_id,
      player_id or 18 static string seeds - 85824 combinations (AES ECB/CBC/CFB/
      OFB/CTR over 11 IV candidates, RC4, repeating-key XOR), zero hits.
- [x] the hooked pair `丌丗七与丘丄丌丞万` / `丞丛业上丗专丏丄三` is not the crypto - they
      are the inbound dispatchers, so the frontend `sio_codec_trace_2313.h`
      traces the wrong methods.
- [x] attachment 0 is not a digest or MAC of attachment 1.
- [x] the codec is not CBC and the password is not a string literal.

### Next steps

- [ ] Resolve the three blobs with `/tmp/md2.py`: the 8-byte salt (fieldRef 115)
      and the two 32-byte static arrays XOR-ed in `get_Extras`; brute-forcing all
      8-byte and 32-byte `fieldDefaultValues` blobs is a cheap fallback because
      PBKDF2 runs a single iteration.
- [ ] Verify offline against the four captured attachment-0 blobs; expect
      `auth_v2` + 9 x 0x09 under AES-256-CFB-128/PKCS7.
- [ ] Open contradiction to settle: with a static password the 16-byte blob
      would repeat across sessions and it does not (9135b38a..., 04b165b4...,
      629093f2..., bca2fc32...), so one XOR half is written at runtime - find its
      writer.
- [ ] Then decode attachment 1, read the request id and fields, and answer on
      `/sio` with a plain JSON event next to `encodeBinaryEvent` in
      `internal/api/socketio_binary.go`.
- [ ] Frontend, proposal only and needs an explicit go-ahead: retrace
      `Rilisoft.丏东丑丄东丈丟且东..ctor` 0x3B680F4 (dump the password), 0x3B6883C and
      0x3B6835C (plaintext/ciphertext pairs), `SocketManager.get_Extras`
      0x3ECF1F4 (the two XOR halves).

## Session 2026-08-28, part 2: the codec in code, and what the sweeps ruled out

### Landed

- `7e123bf` `internal/sio`: the client's payload codec transcribed from
  libil2cpp.so (PBKDF2-HMAC-SHA1, one iteration, 8-byte static salt, 48 derived
  bytes split into an AES-256 key and a 128-bit IV, CFB chaining, PKCS#7), plus
  a known-plaintext search and the `opg3d-siokey` command that drives it.
  PBKDF2 is checked against RFC 6070; the full-block feedback path is
  cross-checked against `crypto/cipher`.
- `3cfa181` `OPG3D_STABLE_SESSIONS`: session tokens become SHA-256(device id)
  so two game runs differ in everything except the token. Off by default,
  warned about at boot, documented in `docs/RUN.md` as lab-only.

### The oracle

`Socket.Emit` puts `Encrypt(UTF8(eventName))` in attachment 0, so all 16 bytes
of the first block's plaintext are known: `auth_v2` plus nine bytes of PKCS#7.
A candidate key either reproduces those 16 bytes or it does not, which makes
any hit certain rather than probable. No scoring, no heuristics.

### Candidate constants

Every FieldRVA default value was exported from `global-metadata.dat`:
292 distinct 8-byte blobs (salt candidates) and 641 distinct 32-byte prefixes
(password-half candidates; blobs longer than 32 are truncated because the XOR
loop in `get_Extras` only reads `0x20` bytes).

### Ruled out

All three chaining variants (CFB-8, CFB-128, CBC) were swept for each:

| Hypothesis | Trials | Result |
| --- | --- | --- |
| password = one static constant | 561,516 | no hit |
| password = session token, 7 encodings, alone or XOR any constant | 7,866,356 | no hit |
| password = socket sid, 7 encodings, alone or XOR any constant | 7,866,356 | no hit |

Session encodings tried: raw 32 bytes, ASCII first half, ASCII second half,
SHA-256 of each, byte-reversed, and UTF-8 of the device id. Sid encodings:
SHA-256 of the ASCII and of the raw bytes, zero-padded, repeated, doubled MD5,
and SHA-256 of sid concatenated with the session token.

### Why the static pair sweep is not worth running

Encryption is deterministic, the plaintext is always `auth_v2`, and the salt is
a build-time constant read from a FieldRVA blob. If both password halves were
also constants, attachment 0 would be byte-identical in every capture. It is
not: four captures differ in all 16 bytes. At least one codec input therefore
varies per session, which closes the entire pure-constant branch by argument
rather than by brute force.

### Next

The stable-session experiment decides where to look. If attachment 0 stops
changing when the token is held still, the key is a function of a value the
server issues and the server can derive it. If it keeps changing, the varying
input is client-side and the remaining static route is to disassemble the
assignment site of the runtime-filled half (static field behind TypeInfo slot
`0x6842528`) rather than to search for it.

## Session 2026-08-28, part 3: the token is cleared, the socket id was not held

### The run

`OPG3D_STABLE_SESSIONS=true`, two launches of the same device against one
backend process, force-stopped in between. Everything ahead of `/sio` is
identical in both: `player_id=165330406`, `nick=Player0406`, `created=false`,
and the same three legacy content files.

| | run 1 | run 2 |
| --- | --- | --- |
| session token | `a6b26db4..a842bf18` | identical |
| socket.io sid | `c16ba8e1a6f4befa13247399` | `f0212cf3d2b03d7e4a72e98d` |
| attachment 0 | `eb42a6d9b1eb4bc29c79ee154af6b656` | `0d4fb8c24e4dbd0ffdffcb24d37707bf` |
| attachment 1 | 608 bytes | 608 bytes, unrelated |

Attachment 0 is `Encrypt(UTF8("auth_v2"))`: a fixed 16-byte plaintext through a
deterministic codec. It changed in all 16 bytes while the session token was held
still, so the codec key is **not** a function of the session token. The
hypothesis is now closed by observation as well as by the earlier sweep.

### Why the run was not decisive

`OPG3D_STABLE_SESSIONS` only reached `store.Store`, which mints session tokens.
The socket.io id was still `secrets.RandomToken(12)` inside `sioHub.create`, so
it rotated between the two launches. Two candidate inputs moved together, and a
ciphertext that differs eliminates neither one alone. The earlier reading, that
the key has to be lifted out of the client, was premature: it holds for the
token only, not for the socket id.

### get_Extras disassembled: no session input on the password path

`SocketManager` has no `set_Extras`. The field at `0xA0` is built lazily inside
the getter at RVA `0x3ECF1F4` and then cached, so the codec is constructed once
per manager, at the first emit.

```
3ecf260: ldr  x0, [x19, #0xa0]!  ; x0 = this->_extras
3ecf264: cbnz x0, 0x3ecf3a8      ; already built -> return the cache
3ecf2ac: mov  w1, #0x20
3ecf2b0: bl   0x1291f20          ; array_new(byte[], 32) -> password
3ecf318: ldrb w11, [x11, x10]    ; B[i]
3ecf31c: ldrb w12, [x13, x10]    ; A[i]
3ecf328: eor  w11, w12, w11      ; A[i] ^ B[i]
3ecf330: strb w11, [x20, x10]    ; password[i] = ...
3ecf338: b.lt 0x3ecf2d4          ; 32 iterations
3ecf348: bl   0x1291fc8          ; object_new(codec)
3ecf35c: bl   0x3b680f4          ; codec .ctor(password)
3ecf368: str  x21, [x19]         ; this->_extras = codec
```

The two source arrays are the first static field of the classes whose TypeInfo
slots are `0x680c000+0xee0` (B) and `0x6842000+0x528` (A); both are class-init'd
in the prologue. Nothing else feeds the password: no sid, no token, no handshake
field, no RNG call. Since the ciphertext still moves, at least one of those two
arrays has its **contents mutated at runtime**, somewhere outside this getter
and before the first emit. That is also why the FieldRVA sweep could never hit:
it only ever tried build-time defaults.

### Landed

- `internal/api`: `OPG3D_STABLE_SESSIONS` now pins the socket.io session id to
  `fixedSid` as well as deriving the session token, so a single run freezes
  every per-session value this server originates. Off by default, still warned
  about at boot, still lab-only. Covered by `internal/api/stable_sid_test.go`
  in both directions (pinned repeats and reuses the live entry; default stays
  random, unique, and never equal to the pinned value).

### Next

Re-run the same two-launch capture with the rebuilt binary. Both the token and
the socket id are now held still, so the result is decisive either way:

- attachment 0 **repeats** -> the key is a function of the socket id, the server
  can compute it, and `auth_v2` is readable without touching the client.
- attachment 0 **still differs** -> no value this server sends determines the
  key. The input is client-local (per-process randomness or state), the key has
  to be lifted from a live client once and pinned into backend config, and the
  static route is to disassemble the write into array A or B rather than to
  search for its value.

While that capture is being taken, the independent items are unchanged: confirm
what the new `prefixed=true` flag on `socket.attachment` denotes (attachment 0
is still exactly 16 bytes, so any prefix sits outside that count), and keep the
response path ready to encrypt with the same codec, since the client's inbound
`get_Extras` -> decrypt pair will reject plain JSON.

## Session 2026-08-28, part 4: the pin holds, the trace was on the wrong class

The third capture arrived with the client's full logcat for the first time, so
this round settles two questions and reopens one.

### The sid pin works, end to end

`socket.ws_open` reported `sid=0123456789abcdef01234567`, and the same value
appears in `socket.auto_connect`, `socket.rx`, `socket.connect` and both
`socket.attachment` lines. The client accepted it and went on to emit `auth_v2`,
so `fa13a6e` does what it claims. The pinned value is a compile-time constant,
so it also survives a backend restart.

### The A/B is still one sample short

| | run 1 | run 2 | run 3 (pinned) |
| --- | --- | --- | --- |
| session token | `a6b26db4...a842bf18` | same | same |
| socket.io sid | `c16ba8e1a6f4befa13247399` | `f0212cf3d2b03d7e4a72e98d` | `0123456789abcdef01234567` |
| attachment 0 | `eb42a6d9b1eb4bc29c79ee154af6b656` | `0d4fb8c24e4dbd0ffdffcb24d37707bf` | `74a1de5b1e2465e4529d219ac23400fe` |

Attachment 0 is a third distinct value, but this log contains only one socket.io
session, and all three runs used a different sid - two random, one pinned. So
"the key is a function of the sid" and "the key comes from client-local state"
predict the same three-way divergence, and one pinned-sid sample cannot separate
them. The comparison has to be pinned sid against pinned sid: two launches, same
fixed value.

### The codec trace was hooked on the wrong class

All four WebSocketManager hooks installed at exactly their intended addresses.
With base 0x77F0541000:

| logcat address | RVA | constant | fired |
| --- | --- | --- | --- |
| 0x77f3b041e8 | 0x35C31E8 | `kEmit` | yes |
| 0x77f3b05028 | 0x35C4028 | `kCodecShared` | no |
| 0x77f3b05170 | 0x35C4170 | `kCodecUnique` | no |
| 0x77f3b0496c | 0x35C396C | `kInbound` | no |

Across the whole session only the emit hook printed:

```
23.1.3-sio[emit] event='auth_v2' args=0x788d9102a0 blob_bytes=-1 blob_hex=<not-an-array>
23.1.3-sio[emit] event='auth_v2' accepted=1
```

There is no `[codec-shared]`, `[codec-sio]` or `[inbound]` line anywhere. That
silence is evidence rather than a dropped hook: the trace numbers its lines,
logcat carried #000001 through #000262 with no gap, and the 600-line budget had
spent two. The hypothesis written into the trace header - that the last pair of
WebSocketManager methods is the codec - is therefore false. Those methods are
not on the emit path.

This agrees with the earlier `get_Extras` disassembly: the cipher is the
standalone `Rilisoft` type cached in `SocketManager._extras` (0xA0), and the
three members that matter are

| member | RVA | argc | what it yields |
| --- | --- | --- | --- |
| `.ctor(byte[])` | 0x3B680F4 | 1 | the 32-byte password in cleartext |
| encrypt `(byte[], bool = False)` | 0x3B6883C | 2 | open/sealed pairs for every blob |
| decrypt `(byte[])` | 0x3B6835C | 1 | what the client expects in reply |

### How far the client now gets

`InitializeSwitcher` ran to state 45 with one skipped step, `LoadMainMenu`
finished, and `AuthSceneController.Awake` entered and returned. After that the
log contains nothing but `skipped retired ConnectionLostChecker.Update` for
forty seconds. The 78% stall is therefore a confirmed idle wait for the
`auth_v2` reply rather than a crash, and this time the socket was never closed
(no `socket.ws_closed` at all).

### Landed

Client repo, commit `8f3458e`, trace only and so permitted by the read-only
rule: the codec trace now hooks the cipher itself, resolved by metadata rather
than by address - the constructor for the password, encrypt for open/sealed
pairs, decrypt for the inbound direction. Every hook calls through to the
original and returns its result unchanged, and the existing 600-line budget
still caps the output. Once the original constructor has run, the algorithm's
`KeyValue`/`IVValue` are read by name too; both reads fail closed, so an
unexpected layout only costs two log lines. Argument counts separate all three
targets from each other and from the same-named ArraySegment overloads on the
sibling type. The `codec trace armed` line now carries seven flags. Compiled
with NDK r29 and checked twice: standalone, and on the real translation unit
with the project's own flags from `compile_commands.json`.

### Next

Rebuild the client and take a single launch. The password prints directly, which
short-circuits the whole key hunt: with it plus any captured open/sealed pair
the salt collapses to the 292 already-enumerated candidates, and if the derived
key/IV lines appear the salt stops mattering at all. Grep for
`23.1.3-sio[cipher]`.

The pinned-sid A/B stays available as an independent check but is no longer on
the critical path. The remaining items are unchanged: what `prefixed=true`
denotes on `socket.attachment` (attachment 0 is still exactly 16 bytes, so any
prefix sits outside that count), encrypting the reply with the same codec,
answering `auth_v2` so the client leaves `WaitSocketAuthReponse`, and serving a
slot config that contains 23.1.3.

## Session 2026-08-28, part 5: the A/B closes - the key material follows the sid

### The pinned-sid A/B is decisive, and it reverses part 3's reading

`backend-2026-08-28.log` now holds ten `socket.ws_open` lines over eight boots.
Grouping every `socket.attachment` line by the sid that produced it:

| log line | time (UTC) | sid | attachment 0 |
|---|---|---|---|
| 209 | 11:18:45 | `c16ba8e1a6f4befa13247399` (random) | `eb42a6d9b1eb4bc29c79ee154af6b656` |
| 246 | 11:19:40 | `f0212cf3d2b03d7e4a72e98d` (random) | `0d4fb8c24e4dbd0ffdffcb24d37707bf` |
| 289 | 11:47:54 | `0123456789abcdef01234567` (pinned) | `74a1de5b1e2465e4529d219ac23400fe` |
| 328 | 11:50:04 | `0123456789abcdef01234567` (pinned) | `74a1de5b1e2465e4529d219ac23400fe` |
| 372 | 12:37:35 | `0123456789abcdef01234567` (pinned) | `74a1de5b1e2465e4529d219ac23400fe` |
| 415 | 12:39:12 | `0123456789abcdef01234567` (pinned) | `74a1de5b1e2465e4529d219ac23400fe` |

Four connections that shared the pinned sid - spanning three separate server
restarts - produced a byte-identical attachment 0, and the two distinct random
sids produced two distinct values. Attachment 0 is the sealed form of the
constant 7-byte event name `auth_v2`, so its only inputs are the key and the IV.
The key material is therefore a deterministic function of the sid.

Part 3 eliminated the session token correctly - it stayed constant while
attachment 0 moved - but it did not identify what the movement tracked. The
variable was the sid, which `secrets.RandomToken(12)` regenerated on every
connection. That is exactly what the pin removed.

Attachment 1 corroborates and adds one fact. The two pinned-sid samples share a
long identical prefix and then diverge:

```
line 290  ...e435a5765e8d7e83f52bb605 | 61 47 d9 da 86 e8 95 18 ...
line 329  ...e435a5765e8d7e83f52bb605 | 64 f2 e9 85 4d 64 f5 e0 ...
```

About 110 bytes - roughly seven AES blocks - are identical before the split.
Under CBC or CFB an identical ciphertext prefix requires an identical key *and*
an identical IV, so neither carries per-connection randomness on top of the sid.
The divergence comes from the plaintext instead, at the payload's own
per-connection field around byte 108 - the incremental request id at
`SocketManager.<RequestCounter>` (offset 0x40), or a timestamp.

### Why the offline sweep never could have worked

`get_Extras` (RVA 0x3ECF1F4) XORs two static 32-byte arrays into the password
with no session value on that path, and that reading still holds for the
password itself. The sid has to enter elsewhere: as the PBKDF2 salt the
constructor picks up on its own, or through one of those two arrays being
written at runtime rather than being the constant it appears to be. Either way
the salt was never inside the 292-candidate static set, so the 561,516 +
7,866,356 + 7,866,356 checks were searching a space that does not contain the
answer. `SaltLen = 8` and `PasswordLen = 32` in `internal/sio/codec.go` both
need re-examining once the real values are in hand.

### The trace is on the right class; the class is shared

The rebuild armed all seven hooks with no warnings:

```
23.1.3-sio: codec trace armed (emit=1 codec-shared=1 codec-sio=1 inbound=1
cipher-ctor=1 cipher-open=1 cipher-seal=1); logging only, no packet is altered
```

and the cipher hooks then worked exactly as designed - passwords, derived keys,
derived IVs and both sides of every transform. None of it belonged to socket.io.
That cipher type is not the socket codec's private property: the app also uses
it for PlayerPrefs key obfuscation and for its on-disk config cache. In one boot
the 600-line budget was gone in 2.15 seconds, spent on

- 279 encrypts of PlayerPrefs keys, caller RVA 0x3B6C4D4 (`LoadoutsSlot`,
  `shop_slot`, `MapVotesKey`, `Attribution.ConversionData`, and so on);
- 312 decrypts of the local config cache, caller RVA 0x24A4318, plaintexts
  shaped `h_<16 hex>_c_<json>` (BalanceWeapons, BalanceEconomic, BalancePets,
  lootboxes, news, profanity list; one of them 215,132 bytes), plus
  `decrypt open bytes=6 text='23.1.3'`;
- three constructors, none of them from the `0x3ECFxxx` range - two with a
  40-byte password from caller RVA 0x3B692B0 and one with a 32-byte password
  from RVA 0x249FDFC.

The first cipher line landed at 15:39:07.277 and the last at 15:39:09.429. This
run's `ws_open` is at 12:39:12 UTC, which is 15:39:12 local - 2.6 seconds after
the budget was already spent. `[emit]` printed zero lines for the same reason.
This is not a hook fault: contiguous line numbering and the armed line rule that
out.

One detail from those constructors is worth keeping. The two 40-byte passwords
differ only in their first 20 bytes and share an identical 20-byte tail
`015263f1ac363c681d89256d1c7883845d1ea19b`. The socket codec's own password
should be 32 bytes, matching the XOR of the two arrays.

### Client fix (commit fa96a8a)

Identify the codec rather than widen the budget. `SocketManager.get_Extras`
(`BestHTTP.SocketIO`, argc 0, and the only `get_Extras` that class declares) is
the accessor both the emit and the inbound path call to reach the codec, so its
return value is the socket.io instance. The new hook publishes that pointer, and
encrypt and decrypt log only when `this` matches. The pointer starts out null,
so every pre-handshake caller is filtered out without having to be recognised,
and the PlayerPrefs and config-cache flood disappears on its own. The
constructor stays ungated: a handful of lines per boot, the one place the
password is visible, and the fallback that still records the socket password if
the accessor hook fails to install. `kMaxLines` goes to 2000 as headroom for
that fallback, not as a substitute for the filter - at roughly 280 lines/s a
bigger budget only reaches the point where logd starts dropping messages. The
armed line now carries eight flags, and a dedicated `LOGE` fires if `get_Extras`
cannot be hooked. Checked twice again: standalone, and on the real translation
unit with the project's own flags.

### Next

One launch, then grep `23.1.3-sio[cipher]`. The expected transcript is short: a
`socket codec identified` line carrying the derived key and IV, a
`.ctor password bytes=32` near socket-connect time with its own derived key/IV,
then `encrypt open bytes=7 flag=0 text='auth_v2'` whose `encrypt sealed` must
equal `74a1de5b1e2465e4529d219ac23400fe` under the pinned sid, followed by the
608-byte payload pair. If encrypt and decrypt stay silent while the constructor
prints, the accessor hook did not install and the `LOGE` says so.

With key and IV in hand the derivation question resolves immediately: compare a
pinned-sid capture against a random-sid one and see whether the password, the
key or only the IV moves. That pins down whether the salt is the raw sid, its
ASCII form or a transform of it. Then `internal/sio/codec.go` can open both
attachments, sweeping `ModeCFB8`, `ModeCFB128` and `ModeCBC`, since the client
never writes `FeedbackSize`.

The remaining items are unchanged: what `prefixed=true` denotes on
`socket.attachment` (attachment 0 is still exactly 16 bytes, so any prefix sits
outside that count), locating the per-connection field near byte 108 of the
payload, encrypting the reply with the same codec, answering `auth_v2` so the
client leaves `WaitSocketAuthReponse (6)` and the `bar=0.750` stall clears, and
serving a slot config that contains 23.1.3.
