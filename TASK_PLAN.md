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

## Session 2026-08-28, part 6

### The codec is open

The instance filter from `fa96a8a` did what it was built for. One launch printed
four constructor triples and a single encrypt pair instead of six hundred lines
of unrelated cipher traffic, and the socket codec published its own material at
caller RVA `0x3ECF360`, inside `get_Extras`:

    password  32 B  a6aa0aee01fc80f44dc4252079475465e2b07448974597a25a2473a342b3588b
    key       32 B  d670c4e97d6aafcdc9940cfb1f411df8c08b8fc480e50b239fe852a1f6ddd6d2
    iv        16 B  c72cc19a7fb79ef6e6d7a6f3944514bd

### The chaining mode had to be measured, not read

The client never writes `FeedbackSize`, so CFB-8, CFB-128 and CBC were all
admissible and the code cannot say which one runs. With the key in hand the
question is decided offline against the captured ciphertexts:

| Mode | attachment 0 | attachment 1 |
| --- | --- | --- |
| `cfb8` | PKCS#7 error | PKCS#7 error |
| `cfb128` | `auth_v2` | 603 bytes of JSON |
| `cbc` | PKCS#7 error | PKCS#7 error |

Full-block CFB opens both attachments, and re-encrypting the recovered
plaintext reproduces both ciphertexts byte for byte. The padding arithmetic
agrees: 7 to 16 bytes, 603 to 608. `sio.DefaultMode` is now `ModeCFB128` and
`TestCapturedVectorPinsFullBlockCFB` keeps the whole vector in the repository,
so the measurement cannot be lost with the log.

### What auth_v2 actually says

    {"uniq_id":"350713954", "hash":"afd8f926-...", "device":"918ad658...",
     "token":"2690ea3d... (128 bytes)", "return":"151017...", "platform":"1",
     "device_model":"Nothing A059P", "type_device":2, "abuse_method":1,
     "block_param":1, "req_id":1, "sid":"0123456789abcdef01234567"}

That also closes the older question about attachment 1 diverging near byte 108.
The prediction was `SocketManager.<RequestCounter>` or a timestamp. It is
neither: the differing field is `token`, 128 bytes starting at plaintext offset
125, which lands inside AES block 7. The client generates it per connection.

### The salt is static, so the password is what follows the sid

All four constructors in the process go through one class, and that class reads
its salt from a static FieldRVA blob. Key and IV are therefore functions of the
password alone, which makes the password the sid-dependent value - not the salt,
which the previous revision of this plan still allowed for. The two 40-byte
passwords belong to the PlayerPrefs and config-cache ciphers and are unrelated
to the socket.

### The derivation is still hidden, and that is a real negative result

547 candidate salts were tried: every 8-byte window, plus `md5`, `md5[:8]`,
`md5[8:]`, `sha1`, `sha1[:8]`, `sha256`, `sha256[:8]` and the full digest of 19
seeds drawn from the sid and from every `auth_v2` field. None reproduces the key
or the IV from the known password. The password itself is not `sha256`, `md5`,
`sha1`, `md5(a)||md5(b)` or `sha256(a||b)` of any of those seeds either. Only a
second capture under a different sid can settle it.

### Backend changes

`75351d7` pins the captured vector as a test and records the measured mode.

`0616db6` wires the codec into the live channel. `OPG3D_SIO_KEY`, `OPG3D_SIO_IV`
and `OPG3D_SIO_MODE` carry a captured key, which is only meaningful together
with `OPG3D_STABLE_SESSIONS`, because the same sid has to be issued again for
the captured key to be the one the client uses. Key and IV must arrive together
and are length-checked at load time: a half configured codec would look in the
log exactly like a key that did not fit the session. On a completed binary event
every attachment is decrypted next to the verbatim trace and logged as
`socket.event_decoded`. A decryption failure is an expected outcome rather than
a server error, so it is recorded as `socket.attachment_undecodable` and the
session is never dropped over it.

    OPG3D_STABLE_SESSIONS=true \
    OPG3D_SIO_KEY=d670c4e9...d6d2 \
    OPG3D_SIO_IV=c72cc19a...14bd \
    ./bin/opg3d-backend

### Next

The paired capture: one launch with `OPG3D_STABLE_SESSIONS=true` and one
without, so the sid differs. Diff the two `.ctor password bytes=32` values
against the two sids - byte offsets first, then `passwordA ^ passwordB` against
`sidA ^ sidB`. If that says nothing, disassemble the runtime write into array A
(`0x6842000+0x528`) or array B (`0x680c000+0xee0`) with `tools/find_callers.py`.

Then the reply. `auth_v2` has to be answered with the same codec, which still
needs the inbound handler read out of `dump2313.cs` (`0x3ED8DC4`, `0x3ED8E58`)
to learn the reply event name and its shape. Unchanged: what `prefixed=true`
denotes, the new `state=40` stall (configure `OPG3D_APPID_KEY` with a Photon
AppID, or add 40 to the skip list), and a slot config containing 23.1.3.

## 7. The salt, and where the sid actually enters

### The paired capture

Both launches arrived: one with `OPG3D_STABLE_SESSIONS=true` (sid pinned to
`0123456789abcdef01234567`) and one plain (sid `b42e23b1aba53a14658a62ec`).
Only the backend side was captured, though - `/tmp/opg3d-cipher.log` still held
the earlier run - so there is no `.ctor password bytes=32` line for the random
sid, and the password comparison the previous part asked for could not be run.

What the capture did settle:

- The pinned sid produced `74a1de5b1e2465e4529d219ac23400fe` for attachment 0
  for the fifth time, across four separate boots. Identical output across boots
  rules out any client-side nonce, clock or RNG: the password is a
  deterministic function of the sid and of nothing else.
- The random sid produced `0251d6070768aae5bf29dd32b7f8a974`. Attachment 0 is a
  single AES block, so `pkcs7("auth_v2") XOR ciphertext` is `AES-ENC(Key, IV)`
  for that unknown password. That is a known-plaintext oracle: a guess at the
  derivation can now be tested with one AES call instead of another capture
  from the device. It is pinned as `randomSidKeystream`.

### The salt

`get_Extras` passes the constructor a password and nothing else, so the salt
had to be static data owned by the cipher class. With `Iterations == 1` the
first PBKDF2 block is exactly `HMAC-SHA1(password, salt || uint32be(1))`, and
`Key` is the leading 32 bytes of that stream, so a single HMAC decides an
8-byte candidate. Sliding an 8-byte window over both shipped files and testing
each window against one captured `(password, Key)` pair is therefore linear:

    salt      4c82a11824641596
    location  global-metadata.dat offset 0x00BF92B8
    searched  113 595 785 windows in libil2cpp.so, 20 702 065 in the metadata
    hits      exactly 1

It is not a lucky fit on one sample. The same 8 bytes reproduce both `Key` and
`IV` for all four cipher instances the traced process constructed, each from a
different password - the socket codec, two PlayerPrefs codecs with 40-byte
passwords, and the config-cache codec. A single false match has probability
2^-160; four independent passwords agreeing settles it.

The scheme documented in `codec.go` is now confirmed end to end except for one
step: how the sid becomes the password.

### What get_Extras actually does

Disassembled in full (`0x3ECF1F4`-`0x3ECF3C8`). Stripped of the six
`class_init` calls and the cached-field early return, the entire body is:

    arrayB = staticFields[0] of class slot 0x680c000+0xee0
    arrayA = staticFields[0] of class slot 0x6842000+0x528
    new    = array_new(byte[], 32)
    for i = 0 .. 31:
        new[i] = arrayA[i] ^ arrayB[i]
    this->_extras = codec.ctor(new, null)

The loop counter starts at `0x20` and every element access is `[base, x10]`
with `x12 = x10 - 0x20` used for the bounds checks. That is just the IL2CPP
array data offset folded into the index; the real index runs 0 to 31.

The important part is what is absent: there is no sid, no string, no hash and
no argument anywhere in the function. It XORs whatever the two static arrays
hold at the moment it runs. Since the password provably varies with the sid and
is stable across boots for a fixed sid, one of those two static arrays must be
overwritten with sid-derived bytes by other code, running after the handshake
and before the first emit.

### Ruled out

- 55 encodings of the `passwordB = passwordA XOR (g(sidA) XOR g(sidB))` model:
  ASCII, uppercase and hex-decoded sid at every offset, cyclic, reversed, and
  MD5/SHA-1/SHA-256 of each, all checked against the oracle. No hits. So `g` is
  not a plain substring or digest of the sid string.
- Hunting the implied static bases as literal byte runs in `libil2cpp.so` and
  `global-metadata.dat`. Present in neither file, which is consistent with the
  array being built at runtime rather than shipped.

### Landed

`Salt`, `NewFromPassword` and `ScanSalt` in `internal/sio`, so the constant
stays reproducible from the shipped file instead of being magic. Tests pin the
salt against all four captured instances, the later pinned-sid boot (it opens
with the pinned key and diverges from the first capture only from offset 157,
so the first 32 token characters are per-device and the remaining 96 are
per-connection), and the random-sid oracle.

### Next

Find the writer of the sid-dependent static array. Note that
`tools/find_callers.py` cannot do this: it decodes `BL`/`B` immediates and so
finds callers of a function, whereas what is needed is every `adrp`/`ldr` pair
referencing the data slots `0x680c000+0xee0` and `0x6842000+0x528`. That is a
different scan and has to be written.

The cheap alternative is one more launch with the tracer armed and the sid left
random, so that `23.1.3-sio[cipher] .ctor password bytes=32` is captured for a
second sid. Two `(sid, password)` samples plus the oracle would very likely
settle the derivation with no further disassembly.

Then the reply. `auth_v2` still has to be answered with the same codec, which
needs the inbound handler read out of `dump2313.cs` (`0x3ED8DC4`, `0x3ED8E58`)
for the reply event name and its shape. For the pinned sid nothing blocks that
any more: the key, the IV and now the salt are all known. Unchanged: what
`prefixed=true` denotes, the `state=40` stall, and a slot config containing
23.1.3.

## 8. Every session opens: the password follows the sid

The last unknown is closed. The per-connection password is a plain function of
the socket.io session id, and settling it took one capture rather than more
disassembly: a launch with a **random** sid and the tracer armed, paired against
the pinned-sid capture already on file.

### The rule

```
password[i] = STATIC32[i] XOR (0x30 + hexnibble(sid[i]))   i = 0..23
password[i] = STATIC32[i]                                  i = 24..31

STATIC32 = 969b38dd35c9b6c375fd1f1b457a6a5ad281467ba370a1955a2473a342b3588b
```

`0x30 + hexnibble(c)` is `c` itself for the digits `'0'`..`'9'` and `c - 0x27`
for `'a'`..`'f'`. That is exactly what the two arrays of `get_Extras` hold: A is
the sid-derived mask, B is the static base, and the body of the getter really is
nothing but `password[i] = A[i] XOR B[i]`. It also explains why no offline sweep
could have found the password: A is written at runtime, after the handshake, and
is present in neither shipped file.

The sid is 24 hex characters while the password is 32 bytes, so the trailing
eight bytes are never masked. They are why both traced passwords share the tail
`5a2473a342b3588b`.

### Verification

Recovering STATIC32 from either traced pair yields the same 32 bytes:

| sid | password |
| --- | --- |
| `0123456789abcdef01234567` pinned | `a6aa0aee01fc80f44dc4252079475465e2b07448974597a25a2473a342b3588b` |
| `7ba611c7d478bc451f8f865c` random | `a1a002eb04f88af448c928237e465e6fe3be7e449b4694a95a2473a342b3588b` |

32 of 32 bytes agree. Through the recovered salt both passwords reproduce the
captured key, IV and `auth_v2` attachment, so the chain sid -> password ->
key/IV -> ciphertext is closed end to end and pinned by tests.

### Proven on the wire

A third launch, on a sid neither the code nor the tests had ever seen, with
nothing pinned at all:

```
sid                   48957fc7ab292f6c15e313f1
client key (logcat)   6701d06b5095ea53bdf859cd354971cd466ad0fafc390ef3066005223e45f08c
client iv  (logcat)   4ec1c3b3f64969b319711b22ec8236fb
backend boot          sio_codec=false
result                socket.event_decoded event=auth_v2 key=derived-from-sid mode=cfb128
```

The backend read the event name and all 605 bytes of arguments, including the
`"sid":"48957fc7ab292f6c15e313f1"` the payload carries about itself. The key and
IV were also recomputed independently from the rule above and agree with the
client trace byte for byte. `OPG3D_SIO_KEY` is no longer needed to read this
channel and stays only as an override for replaying an old capture.

### Landed

| Commit | What it does |
| --- | --- |
| `5e37268` | `sio.PasswordForSid` and `sio.NewForSid`. Tests reproduce password, key, IV and the `auth_v2` attachment for both traced sids, and reject a non-hex or over-long sid. |
| `5fff99d` | `decodeBinaryEvent` derives the codec from each session's own sid, so every session is readable. The pinned key stays as an override, a non-hex sid is reported as `socket.codec_underived`, and the key origin is recorded on `socket.event_decoded`. |

### Next

Answer `auth_v2`. No key question blocks it for any session now, so what is
left is the reply's event name and shape, read out of the inbound handler
(`0x3ED8DC4`, `0x3ED8E58`) in `dump2313.cs`. `encodeBinaryEvent` already writes
the documented socket.io framing; whether a reply must instead hide its name
inside attachment 0, the way the client's own emits do, is the open question.

Unchanged and independent: what `prefixed=true` denotes, the `state=40` stall
(set `OPG3D_APPID_KEY` with `OPG3D_PHOTON_APPID_PUN` or `_REALTIME`), and a slot
config containing 23.1.3.

## 9. auth_v2 is answered: the 78% stall is gone

### The open question, answered in both directions

Yes: a reply must hide the event name inside attachment 0, exactly as the
client hides it outbound. And the plaintext must be JSON-quoted -- `"auth_v2"`,
9 bytes padded to 16 -- where the client sends it bare, 7 bytes padded to 16.
The directions are asymmetric, and that follows from code:

- Outbound, the server knows attachment 0 *is* the name and reads it as such.
- Inbound, the client substitutes the plaintext into its payload text
  literally, so the plaintext has to be a valid JSON fragment on its own.

### The four constraints

From `WebSocketTransport.OnBinary` (0x3ED8B80). Breaking any one of them makes
the client drop the packet without a word:

| Constraint | Evidence |
| --- | --- |
| Exactly two attachments | The indices are computed as n-2 and n-1, so one attachment asks the list for element -1 and a third stays in the payload as an unsubstituted placeholder |
| One leading byte per attachment, which the client discards | `AddAttachmentFromServer(data, copyFull: false)` at 0x3ECCC6C copies from offset 1; the byte is the engine.io v3 marker |
| Each plaintext must be a valid JSON fragment | The lambda at 0x3ECD170 is `Payload = Payload.Replace(json, plaintext)` and adds nothing around it |
| The name lives in attachment 0 | The inbound dispatcher at 0x35C396C takes the name from `Packet.EventName` and forwards only `args[0]`, so the payload must resolve to `["auth_v2", {...}]` |

### Correlation

`WebSocketManager` (0x35C3D08) completes a pending request by
`Convert.ToInt32(head[req_id])`, where the key is the string literal at slot
0x67C33C0 -- the same literal the outbound builder uses. A reply without
`req_id` is decrypted, parsed, dispatched and then matched against nothing,
which is exactly what an infinite wait looks like from the client side.

The second dictionary a subscriber receives is not sent by the server at all:
0x35C3E54 resolves it from a client-local cache keyed by
`Format(req_id, Handshake.Sid)` and removes it.

### On the wire

```
text     : '4' '5' "2-" "/sio," [{"_placeholder":true,"num":0},{"_placeholder":true,"num":1}]
binary #1: 0x04 || Encrypt('"auth_v2"')
binary #2: 0x04 || Encrypt('{"req_id":1, ...}')
```

Both attachments are sealed with the codec derived from the session's sid.

### Result

Autonomous run on 2026-08-28: build, commit, start the backend, launch the game
over adb, collect logs.

```
20:21:58  socket.event_decoded event=auth_v2 key=derived-from-sid req_id=1
20:21:58  socket.auth_answered req_id=1 attachments=2 mode=cfb128
20:21:58  client emits subscribe(friends_list) with req_id=7
20:22:31  bar=0.730 -> 0.740 -> 0.750, LoadMainMenu tail completed,
          the main-menu scene load is requested
20:22:31  update_player_online version=23.1.3, mmc_get_room, get_binded_ids
20:24:06  process alive, the socket never closed
```

The bar is past 78%. The client left `WaitSocketAuthReponse`, requested the
main-menu scene and moved on to its ordinary request stream. No
`socket.auth_undelivered`, no decryption failure, no authorization error.

The reply body is deliberately minimal and well-typed:

```json
{"req_id":1, "result":"ok", "error":0, "uniq_id":"350713954",
 "sid":"0a23f3504d958ebde65ef31c", "time":1787937718}
```

A shotgun of guessed aliases would be worse than a missing field, because
`Convert.ToInt32` on a string throws inside the client's handler and aborts
processing. The client's own identity is echoed rather than replaced: its
`uniq_id` is not the `player_id` from the HTTP registration, so minting a
different one here would only make the session disagree with itself.

### Landed

| Commit | What it does |
| --- | --- |
| `0ac15b1` | `encodeEncryptedEvent` writes the shape above and the live decode path answers `auth_v2` with it. `encodeBinaryEvent` stays as the reference encoder for the stock protocol. `OPG3D_SIO_REPLY` (default true) restores trace-only behaviour without a rebuild. Tests replay the client's inbound path over a reply this server produced -- strip the marker, decrypt, substitute literally -- and assert the client would hold `["auth_v2", {req_id}]`. |

### Closed

`prefixed=true` in `socket.attachment` is explained: it is the engine.io v3
binary marker, and `AddAttachmentFromServer(copyFull: false)` is why attachment
0 is still exactly 16 bytes with the prefix outside that count. A reply has to
carry the same byte or the client loses a byte of ciphertext.

### Next

Answer what the client now asks for. All of it is already in the log as
`socket.event_unanswered`:

| Event | Arguments |
| --- | --- |
| `subscribe` | `friends_list`, `lobby_likes_v2`, `mm_status` |
| `get_last_command` | - |
| `player_prefs`, `progress_status` | - |
| `mmc_get_room` | - |
| `update_player_online` | `game_mode=100006`, `version=23.1.3`, `map=-1`, `tier`, `rank` |
| `get_binded_ids` | `with_info=1` |

Unchanged and independent: the `state=40` Photon step (set `OPG3D_APPID_KEY`
with `OPG3D_PHOTON_APPID_PUN` or `_REALTIME`) and a slot config containing
23.1.3.

## 10. The autonomous loop, and the slot-config contract

### The loop is available end to end

Device `001691527000747` (A059P, arm64-v8a, SDK 36) is attached over adb and
`adb shell` is already uid=0 (KernelSU, context `u:r:ksu:s0`). The package sits
at `/data/app/~~Maqu9HHAC-hQIPCl51rQkw==/com.pixel.gun3d-hcxeFRGlR1J3EuHexLs3HA==/base.apk`.
So build -> push the trace library -> launch -> collect logcat needs no operator
step, and a change can be verified against the real client in one pass.

Baseline at the start of this session: `go build ./...`, `go vet ./...` and
`go test ./...` are all clean.

### The reply path is confirmed from the client side

`/tmp/opg3d-sync.log` carries the client's own decryption of a reply this
server produced:

    decrypt open bytes=16 text='"get_binded_ids"'
    decrypt open bytes=46 text='{"error":0,"ids":[],"req_id":14,"result":"ok"}'

The client decrypts both attachments, recovers the quoted event name and reads
the head with its `req_id`. The framing settled in part 9 is therefore proven
from both ends rather than from this server's log alone.

### The slot-config contract, read from metadata rather than guessed

`ParseFullSlotConfig` and `versionDict` appear nowhere in the 44 MB
`dump2313.cs`, so the method is obfuscated and only its messages survive. Those
messages are in `global-metadata.dat`, and together they name the whole
contract:

    ParseFullSlotConfig error: commandsDict == null
    ParseFullSlotConfig error: dataDict == null
    ParseFullSlotConfig error: slotList == null
    ParseFullSlotConfig error: versionDict == null
    ParseFullSlotConfig: versionDict not contains version

Four required top-level parts, plus a fifth check on the contents of one of
them. Two facts follow:

1. The document must carry `commandsDict`, `dataDict`, `slotList` and
   `versionDict`. A document missing any one of them fails earlier, with a
   different message, so those four names are not a guess about shape.
2. The failure observed is the *last* of the five. The client's `versionDict`
   parsed fine and simply has no `23.1.3` key, which means the client is not
   rejecting a document this server sent - it never received one.
   `answerSubscribe` replies with an empty change set, so the client keeps the
   stale cached copy it already had on disk.

The client polls 40+ configs in a single `subscribe`, each as
`{"h":"<16 hex>","b":0}`. `CommandsWithFullSlots` arrives at hash
`23fe54a91b31bc6c` and is `PGCompany.ConfigId = 7`.

One correction to inherited notes: the client repo's
`docs/PORT_23_1_3_AUTH_COMPLETION.md` calls this message "expected offline
noise". That was true of the in-process local backend, which served no configs
at all. It is not true here - a real server is meant to answer this poll - so
the message is a defect now, not noise.

### The runtime-settings layer is written but inert

`internal/api/settings.go` exists and is complete in itself: environment seed,
optional-field JSON patch, validation before any field is applied, atomic
persist to `data/settings.json`, masked AppIDs for the console, and the admin
token and AppID sealing key deliberately excluded so a leaked admin token
cannot rewrite the secrets guarding everything else.

It is untracked in git and nothing references it. A grep for `newSettings`,
`settingsPatch`, `settingsSnapshot`, `.snapshot()` and `masked(` finds no hit
outside the file itself, and `Server` in `api.go` has no settings field. The
layer is unreachable code, which is exactly why it behaves as unfinished.

### Next

- [ ] Determine how a config document is delivered on the channel: whether the
      `subscribe` reply carries bodies next to the changed hashes or a separate
      push event carries one document each, and whether the body is JSON or
      MessagePack. The client's own on-disk cache stores plaintext shaped
      `h_<16 hex>_c_<json>`, which argues for JSON, while the
      `BalanceController` consumer takes a `Byte[]`. Settle it from the
      consumer, not from the cache.
- [ ] Author `CommandsWithFullSlots` as editable JSON on disk with all four
      parts and a `versionDict` entry for `23.1.3`, serve it, and verify on the
      device that the message is gone and the boot continues past the main menu.
- [ ] Wire `settings.go` into the server, cover it with tests in both
      directions (a saved change survives a restart; a rejected patch applies
      nothing), and commit it.
- [ ] The `state=40` Photon step still wants `OPG3D_APPID_KEY` together with
      `OPG3D_PHOTON_APPID_PUN` or `_REALTIME`.

### 10.4 The gate is state 35 of InitializeSwitcher, not the config hash echo

Measured 2026-08-28 against the live backend, device 001691527000747.

Two hypotheses were tested and both are now closed.

1. **Acknowledging the cached config hashes is not enough.** `answerSubscribe`
   was changed to echo every polled hash back as "your copy is current" (149 of
   153 polled entries readable, 4 unreadable). The client did not advance: the
   identical stall at `InitSwitcher state=45 bar=0.750`, and
   `ParseFullSlotConfig: versionDict not contains version 23.1.3` still logged
   exactly once. The change is kept because it is the more truthful answer, but
   it is not the gate.

2. **The parser error messages do not name JSON keys.** `commandsDict`,
   `dataDict`, `slotList` and `versionDict` are local variable names, not
   document fields. In `global-metadata.dat` every occurrence belongs to an
   error string (literal indices 19148-19152, plus `dataDict == null armor slot`
   and `save slot dataDict == null`). A document reconstructed from those names
   cannot parse, so that route was abandoned before it was implemented.

What the evidence shows instead:

- Every HTTP request the client makes is answered 200. It asks for exactly three
  config documents and `content/` answers all three with 3-byte stubs (`{}`,
  `[]`, `[]`). No slot-config URL is ever requested.
- Every socket event is answered. After `get_binded_ids` (req_id 13 and 14) the
  connection carries nothing but engine.io heartbeats, so the client is not
  waiting on a reply from this server.
- `InitializeSwitcher` state 35 never returns, and `ranchoOk`
  (`<ranchoComplete>5__2`, iterator offset 0x28) is 0 in every heartbeat.
  `loading_stall_guard_2313.h` skips that step, so everything state 35 was
  meant to publish stays unpublished.
- `ParseFullSlotConfig` fails 47 ms after the skip: a consequence of the skip,
  not an independent defect.
- `AuthSceneController.Awake` runs, but the hooked auth state setter
  (`23.1.3-auth: direct state setter`) never logs a single transition, and
  neither `MAIN MENU REACHED` nor `TUTORIAL REACHED` appears. The bar stays at
  79 % with a live UI: the retired `ConnectionLostChecker` keeps ticking every
  20 s, so the process is healthy and idle, not frozen.

The client-side documentation names the same root cause from the other side.
`PORT_23_1_3_LIVE_CONTENT.md` records that configuration endpoints answering
`{}` leave the ExpOpenSystem registry empty, so every feature reports closed.
`PORT_23_1_3_BACKEND_EMULATION.md` lists it as a known limit: "a genuine
override cannot be expressed yet". `PORT_23_1_3_THIN_CLIENT.md` states the
intended end state for the boot watchdogs: they "come off one at a time against
a real server log".

**Next step, and the only one that does not require guessing.** Read the
document the client already parses. The consumer is
`世丄丅丏丌专上世丄(string, byte[], BalanceController.丐丛丏丒丘东三专一, ConfigId)`
at RVA `0x471CB40`. A trace hook there dumps the plaintext of the APK-embedded
document that currently satisfies four of the five ParseFullSlotConfig checks,
which yields both the real field names and the exact structure to extend with a
`23.1.3` entry. Adding a trace is the one client-side change this work is
allowed to make.

Slot traffic also has its own socket events, `create_slots` and
`get_progress_slots` (metadata literals 31358 and 33990). This server answers
neither, and the client reaches neither today, which is consistent with the
slot layout never being established.

### 10.5 The document is hosted, and the subscribe ack is not its delivery channel

The document's shape is no longer inferred. A hook on the client's own parser
(`Progress.东丝丂丄业丕且丙丑.七丈丆丈三丆丄丛丞`, RVA `0x1B3F618`, one string
argument) saved every document that parser receives. It captured 596 characters:

```json
{"16.2.2":{"slots":[2,7,14,21],"commands":{}}, ... "15.99.2":{"slots":[2],"commands":{}}}
```

Fourteen versions, `15.99.2` through `16.2.2`, every entry carrying exactly
`slots` and `commands`, and `commands` empty in all of them. That root object is
the `versionDict` named in `ParseFullSlotConfig: versionDict not contains
version 23.1.3`: the APK-embedded copy stops several releases before the served
build, so the lookup misses. The client is not at fault and cannot fix itself -
a stale embedded document can only be replaced by a server.

The backend now hosts it (`internal/api/slot_config.go`): the fourteen measured
versions kept verbatim, a `23.1.3` entry inheriting the newest measured shape
`[2 7 14 21]`, `commands` rendered as `{}` rather than `null`, and the poll hash
derived from the body so a copy the client already holds is confirmed instead of
resent. `socket.config_polled` now reports `served=1`.

Three spellings of the answer were put on the device. All three were refuted by
the same measurement: the reply lands, and about 3.5 seconds later the client's
parser still receives its own 596-character embedded copy.

| answer entry | reply sent | parser received |
| --- | --- | --- |
| `{h, c: "<json text>"}` | 09:19:59.369 | 596 chars, embedded |
| `{h, c: {...}}` | 09:24:35.790 | 596 chars, embedded |
| `{h, c: {...}, v: 1}` | 09:29:48.388 | 596 chars, embedded |

Nothing else moved either. No HTTP fetch for this config, the client asking only
for the three content stubs it always asks for; no write into the sealed cache
under `files/binary/18.1.0/<guid>/`, where only ConfigId `102` sits, dated
08-27; and no second parse. The entry is read and discarded.

So the ack for `subscribe` carries hashes, and the document arrives by some
other route. The registry read is `TryLoadConfigOrFallback` =
`PGCompany.丅丝业七三丈丝丑丏.与丌下丑丝丁丄丏丛` (`0x249E064`) and the store write is
`与万丝丗丁不丗一丗` (`0x249CD64`, `TrySaveConfig`); a server copy that never reaches
that store can only ever lose to the embedded fallback. Tracing those two
answers both open questions at once: whether any config is ever stored from the
socket path, and if one is, what an accepted entry looks like.

#### Next

- [ ] Trace `TrySaveConfig` and `TryLoadConfigOrFallback` by managed name and
      learn which channel actually feeds the config store.
- [ ] Once the channel is known, serve the document through it and confirm the
      bar passes 0.750.
- [ ] Answer `create_slots` and `get_progress_slots`, which this server still
      does not implement and the client has never yet reached.

### 10.6 The slot document is not the blocker; the auth scene never starts its state machine

Measured on device in run 10 (2026-08-29 09:47, library 450336 B):

- The config store trace armed on 3/3 entry points (save, load-or-fallback, load-core). The
  entire boot produced exactly one store call: `core-read id=102 ok=1 bytes=446`. There was no
  save at all, and not a single read for ConfigId 7. The config store is therefore not the
  delivery channel for `CommandsWithFullSlots`, and nothing we answer on the socket is written
  into it.
- Player prefs on the device hold 845 keys, none of which mentions a slot, command, config or
  version document, so the document is not kept there either. The parser class const
  `NotSlotCommandsStorageKey` is never written.
- The APK install directory changed underneath the tooling, so the hardcoded push target
  pointed at a directory that no longer exists. `/tmp/opg3d_deploy.sh` now resolves the target
  through `adb shell pm path com.pixel.gun3d`, and every push is verified by size and date.
- On the fresh install directory the sealed cache `files/binary/18.1.0/` is gone and the parser
  receives an empty document: `slottrace: #1 parse(chars=0) head=''`. Zero
  `ParseFullSlotConfig: versionDict not contains version 23.1.3` errors were logged, and the
  loading bar still stops at `state=45 bar=0.750`.

Conclusion: the full-slot document never was the blocker. Its 596-byte copy was per-install
state, and its absence changes nothing about the stall. The hosted copy stays in place, but it
is no longer on the critical path.

Where the boot actually stops, same run:

- `lifecycle diagnostics armed (auth-awake=OK optional=5/5)`, so the auth state setter and the
  `AppsMenu`, `TrainingController` and `MainMenuController` hooks are all live.
- `AppsMenu.Awake` then `AppsMenu.Start iterator requested`, the Switcher reaches state 45,
  `LoadMainMenu` completes (state 1 -> 2, result=1), then `AuthSceneController.Awake`
  ENTER/RETURN with `anomaly: OfflineModController verdict changed -1 -> 0` in between.
- After that no `direct state setter -> ...` line ever fires and `MainMenuController.Awake`
  never fires. The auth state machine (Initial, Authorizing, Authorized, CheckBindedId,
  ChooseId, ChooseProgress, SendCachedCommands, SynchronizeProgress, CheckAppVersion,
  CheckConnection, SendProgress, WaitAsync, TechnicalWorks, Login) does not advance a single
  step, and the last client log line of the run is at 09:50:12.

So the stall lives inside the auth scene startup, after `Awake` returns. `AuthSceneController`
owns five nested iterator classes (TypeDefIndex 2026, 2028, 2029, 2030, 2031) plus a closure
holder (2027) whose members are `internal bool ...()` predicates, which is the shape of
`yield return new WaitUntil(...)`. One of those waits never becomes true.

#### Next

- [ ] Trace `AuthSceneController.Start`, the five nested `MoveNext` bodies and the closure
      predicates, logging entry, iterator state and return value, to name the wait that never
      ends.
- [ ] Only then decide what the backend still owes the auth scene: a push, a reply field, or a
      state the client expects to observe.
- [x] The config store, the player prefs and the subscribe acknowledgement are all eliminated
      as the slot-document delivery channel.

### 10.7 The auth scene rests in Authorized, and its state field is never re-written

Two instrumented runs answer the question 10.6 left open. `auth_scene_trace_2313.h` hooks
`AuthSceneController.Start` (0x3DC1BCC), both startup steps (0x3DC1E18, 0x3DC2270), the state
dispatcher (0x3DC1F38), the scene coroutine (0x3DC2C3C) and the static state reader, 6/6 armed.
Run 11 logged exactly five lines and then went quiet for the rest of the capture:

    Start ENTER -> first startup step ENTER/RETURN -> state dispatch ENTER 2 (Authorized)
    -> state dispatch RETURN 2 (Authorized) -> Start RETURN

`auth_state_trace_2313.h` then followed the dispatcher's own call targets, read out of its
disassembly (`llvm-objdump -d --start-address=0x3DC1F38 --stop-address=0x3DC2270`) and resolved
with `tools/resolve_rva.py`: a static bool gate (0x2B71728) whose true branch tail-exits the
dispatcher, the iterator the Authorized branch was expected to start (0x3DC6DB0, `IEnumerator`,
plus its nested `MoveNext`), a scene collaborator singleton (0x3C12438) and the auth interface
entry point (0x4101F0C, `void(int)`). All 5/5 armed in run 12, and every answer is negative in a
useful way:

- the gate returns `false`, so the early tail-exit at the top of the dispatcher is not taken;
- the Authorized coroutine is never requested, and its `MoveNext` never runs;
- the auth interface is never asked for a screen;
- the collaborator step runs once during boot, nine seconds before the scene exists.

So dispatching state 2 is a no-op branch. `Authorized` is a resting state, and something outside
the dispatcher is supposed to write the next one. That writer is not the property setter:
`tools/find_callers.py 0x3DC0968` reports **0 call sites** in the whole image, which is exactly
why the hook on the setter never fired in any run -- the field is written directly. The only
caller of the second startup step (0x3DC2270) is a tail branch at 0x3DC20E0 inside the
dispatcher itself, on a path state 2 never reaches.

The wire side of run 11 ends just as quietly. The last real request is `get_binded_ids` with
`with_info=1`, answered `{"error":0,"ids":[],"req_id":13,"result":"ok"}`; after that the socket
carries only `engine.io` heartbeats every ~25 s. The client is not waiting on the network, it is
waiting on an internal callback -- and `CheckBindedId` and `ChooseId` are states 5 and 6 of this
very machine, which makes that empty answer the first thing to suspect.

#### Next

- [ ] Hook the only reply-shaped method on the scene (0x3DC418C, takes
      `Dictionary<string, object>`) to see whether the `get_binded_ids` answer reaches the scene
      at all.
- [ ] Depending on that: either enrich the answer the scene refuses to act on, or find the
      direct writer of the state field and the event that is supposed to trigger it.
- [x] The state property setter is dead code across the whole image; stop instrumenting it.

### 10.8 Correction: the Authorized branch is not empty, it opens the server message window

Section 10.7 concluded that dispatching state 2 does nothing. That was wrong, and the reason is
instructive: `auth_state_trace_2313.h` only watched the four call targets that could be read out
of the dispatcher's disassembly, and the range it was disassembled over -- 0x3DC1F38 to 0x3DC2270
-- turned out to span four separate methods, not one. The dispatcher proper ends at 0x3DC20E8;
0x3DC20E8, 0x3DC2110 and 0x3DC21B4 are methods of their own.

So instead of guessing again, every instance method of the class was instrumented at once.
`tools/find_callers.py` and a small generator (`/tmp/emit_sweep.py`) read the class body straight
out of `analys2313/dump2313.cs` and emitted `auth_scene_sweep_2313.h`: 51 hooks covering every
non-static instance method, each reporting only its first four calls so frame-polled predicates
cannot flood the log. Run 13 armed 51/51 and printed the live path:

    0x3DC18D4 -> Start -> 0x3DC1DB8 -> first startup step -> 0x3DC2458(state 2)
    -> state dispatch 2 -> [InfoWindowController instantiated] -> 0x3DC2410
    -> iterator 0x3DC4758 requested -> iterator 0x3DC6CD0 requested -> dispatch returns

Two things fall out of this. First, a second state-taking method (0x3DC2458) is called with state
2 from inside the first startup step, so the machine really does believe it is `Authorized`.
Second, a Unity warning carries the missing piece of the stack for free:

    DontDestroyOnLoad only works for root GameObjects or components on root GameObjects.
    UnityEngine.Object:Instantiate(Object, Vector3, Quaternion)
    InfoWindowController:<Instance>()
    AuthSceneController:<state dispatch>(AuthSceneState)

The dispatcher instantiates `InfoWindowController` -- the server message window -- and then starts
two coroutines and returns normally. Nothing is idle: the scene is waiting on those coroutines,
and possibly on a modal window that never closes. No `Coroutine couldn't be started` complaint
appears anywhere in the run, so both coroutines were accepted by Unity.

`info_window_trace_2313.h` (generated by `/tmp/emit_info.py`, 15 hooks) now follows exactly that:
the `MoveNext` bodies of both iterator state machines, reading `<>1__state` before and after each
step, plus thirteen `InfoWindowController` entry points including its visibility query, both show
paths, the dialog with accept/cancel callbacks and `OnClickOkDialog`.

#### Next

- [ ] Run 14: read where each coroutine parks, and whether the message window is up and waiting
      for a click that no player can give while the loading bar is still on screen.
- [ ] Then decide what the backend owes: a message payload the window can close on, a reply the
      coroutine waits for, or a push that resumes it.
- [x] Falsified: "the Authorized branch does nothing" (10.7). It opens the message window and
      starts two coroutines.
- [x] Falsified: instrumenting only the callees found in a disassembled address range. Ranges in
      this image can cover several methods; sweep the class instead.

### 10.9 The Authorized branch waits for an account object the server never describes

Run 15 (tag `23.1.3-await`, 9/9 hooks armed) reduced the stall to one condition.

- Coroutine `0x3DC4758` yielded an object whose managed class is `Coroutine`: its
  first segment starts a nested coroutine and then waits for it.
- The nested coroutine `0x3DC6CD0` yielded `WaitUntil`. Its predicate is
  `0x3DC7688`, the cached lambda `<>9__126_0` of the auth scene closure class,
  and it was polled more than 960 times in 32 seconds, every time false.
- Static disassembly of the predicate (llvm-objdump, section `il2cpp`) shows it
  returns `static field 0x50 != null` on the static class that owns `0x3DCAEAC`
  (TypeDefIndex 2059, dump line 93301). Nothing else is read.
- That field and its sibling at `0x58` hold TypeDefIndex 5176: an account model
  carrying two ints, two bools, an int, two strings, a nested object, a
  `HashSet`, and compiler-generated members named `userId` and `id`.
- The same static class owns six `(Dictionary<string, object>,
  Dictionary<object, object>)` socket answer handlers, five integer extractors
  over the answer dictionary, and two getters that return the model. The field is
  therefore filled from a socket reply, not from disk and not from the config.
- After the wait, the tail of the nested coroutine emits one more event through
  the client web socket manager at `0x35C3628`. That is why the session stops
  after `get_binded_ids`: the client sends nothing further until the object
  exists.
- Two earlier theories are dead: the server message window is only instantiated
  and never shown (run 14), and no event went unanswered in run 15.

What this server answers in that area today: `get_binded_ids` returns
`{"error":0,"ids":[],"req_id":N,"result":"ok"}` and `progress_status` returns
level, experience and a `progress` map. The client asked for `with_info: 1`, and
neither answer carries per-id information, so nothing can be constructed.

#### Next

- [ ] Run 16 with the 19-hook gate trace on TypeDefIndex 2059 to name the handler
      that should fill the field and to show whether it runs at all.
- [ ] Extend the `get_binded_ids` answer with the entries that handler consumes,
      keeping every value well typed.
- [ ] Confirm the predicate flips to true, the bar leaves `0.750`, and the main
      menu is reached.

### 10.10 The awaited object is built from a `user_characters` dictionary

Runs 17-21 closed the chain that 10.9 left open. The answer the login scene is
waiting for is now named end to end, and the first half of it is already being
sent.

**Why six hooks had failed.** All six answer handlers of the gate class take
three arguments, not two: `(Dictionary<string, object>, Dictionary<object,
object>, List<byte[]>)`. Printing declarations truncated at 104 columns had
hidden the third parameter, so those hooks were installed with the wrong arity
and silently refused. Arity is now computed programmatically by counting
top-level commas in the full declaration; run 17 armed 19 of 19 entry points.

**The handler's shape** (0x3DCFF30, disassembled):

    if (extract(answer) < 1) return;   // returns without touching anything
    tail-call 0x3DD0150(payload)        // the failure path, given the request

**The extractor's shape** (0x3DCFFC0, disassembled):

    if (answer == null) return 1;
    if (answer.ContainsKey(KEY1)) return Convert.ToInt32(answer[KEY1]);
    if (answer.TryGetValue(KEY2, out v) && v is Dictionary`2) apply(v);
    return 0;

So a returned 0 is the success path, not a failure: the handler's `>= 1` branch
is error handling, and everything that matters happens inside `apply`.

**The keys, read out of the live process.** The two keys are string literals
held in metadata-usage slots, so they were resolved from the running game rather
than guessed: KEY1 is `err_code` and KEY2 is `user_characters`, whose value must
be a `Dictionary`. The same probe also resolved the event whose answer this
handler parses - `get_binded_ids` - which confirms the correlation runs 15-16
had only inferred from timestamps.

Two traps are worth recording. A metadata-usage slot holds the address of the
cell the runtime fills in, so the object needs two dereferences, not one. And
those cells are filled in on the parser's first execution: reading them at hook
install time converted a pointer that was not a managed object yet and crashed
inside libil2cpp (run 19, SIGSEGV at fault addr 0xa000fd35). The probe now runs
inside the parser hook, where initialisation has already happened.

**What `apply` does** (0x3DD0268, disassembled):

    model = 0x4C98F44(dict, null);       // static factory of the model type
    gate_class.static_fields[0x50] = model;   // the awaited field itself
    write barrier; then invoke the Action in static_fields[0x28]

That is the whole blocker in three lines: the field the `WaitUntil` predicate
reads is written in exactly one place in the image, and that place is fed only
by `user_characters`.

**Measured result of the backend change (run 21).** `answerBindedIDs` now sends
`user_characters` as well as the empty `ids` list. The dictionary passes the
type check and `apply` is entered on the device for the first time - but the
factory returns nothing for an empty dictionary, so the field is still null and
the predicate still returns false. The remaining unknown is therefore narrow:
which keys the factory reads.

#### Next

- [ ] Resolve the factory's string-literal slots (0x4C98F44) from the live
      process with the same two-dereference probe, and name the keys of one
      `user_characters` entry.
- [ ] Fill `user_characters` in `answerBindedIDs` with those keys, typed the way
      the factory converts them.
- [ ] Confirm on the device: the awaited field is filled, the predicate returns
      true, the bar leaves 0.750 and the main menu scene loads.
- [ ] Keep the failure path honest: `err_code` must stay absent on success,
      because any value of one or more sends the client down 0x3DD0150.

### 10.11 Run 23: the dictionary reaches `apply`, and the factory still builds nothing

Backend restarted on the current binary (`boot accounts=10 addr=:8080
admin_enabled=true appid_sealing=true sio_codec=false stable_sessions=false`),
build, `go vet` and every test green beforehand. The client library was
redeployed unchanged - 497,592 B, the run-22 trace - so the only variable in
this run is the server's answer. One launch, 135 s of capture, install path
resolved through `pm path` as usual.

**The wire** (sid `1ee2bfed2a7006f3e85eb234`). Every event decoded with
`key=derived-from-sid mode=cfb128` and not one `socket.attachment_undecodable`.
`get_binded_ids` arrived with `with_info=1`, `req_id=13`, and was answered

    {"error":0,"ids":[],"req_id":13,"result":"ok",
     "user_characters":{"id":"918930694",
                        "ids":[{"id":"918930694","user_id":"918930694"}]}}

After that answer the socket carries engine.io heartbeats and nothing else: the
client emits no further event for the rest of the run, exactly as in run 21.

**The device** (19/19 gate hooks and 9/9 await hooks armed):

- handler `0x3DCFF30` ENTER call#1;
- extractor `0x3DCFFC0` read **0**, so the success path was taken and `err_code`
  correctly stayed absent;
- `apply` `0x3DD0268` ENTER call#1 - the dictionary passed the `Dictionary`2`
  type check;
- immediately after it: `after 0x3DD0268 the awaited field is still null (0x0)
  and its sibling is still null (0x0)`;
- predicate `0x3DC7688` polled 3,840 times in 135 s, false every time;
  `state=45 bar=0.750`, and `MainMenuController.Awake` never fires.

Two links that earlier runs had only inferred are now read out of the live
process: the extractor's two keys are `err_code` then `user_characters` with the
second value required to be a `Dictionary`2`, and the event emitted after the
wait is `get_binded_ids`, owned by the gate class `丞七丈不业丙丒丂丛`.

So the envelope is right and the blocker moved one level down. `apply` runs,
writes nothing, and the only step between it and the awaited field is the static
factory `0x4C98F44`. The entry keyed `id` / `user_id` was taken from the model's
compiler-generated member names; this run refutes that reading.

#### Next

- [ ] Hook the factory `0x4C98F44`. It is **not** among the 19 gate entry
      points, which is why this run can show that it fails but not what it
      wants. Probe its metadata-usage slots with the same two-dereference probe,
      and do it inside the hook rather than at install time - the install-time
      crash is recorded in 10.10.
- [ ] Fill `user_characters` with the keys the factory actually reads, typed the
      way it converts them, then confirm the awaited field is written, the
      predicate flips and the bar leaves 0.750.
- [x] Refuted: `user_characters` carrying `{"id", "ids":[{"id","user_id"}]}`
      satisfies the factory. It is accepted as a dictionary and produces no
      model.
- [x] Confirmed: the success path requires `err_code` to be absent, and this
      answer keeps it absent.

### 10.12 Runs 24-26: the account object is built, and the config poll answer is not the document channel

Run 24 closed the guessing about the factory. With `0x4C98F44` hooked, the field
names were read out of the live process instead of inferred from the model's
compiler-generated members: every entry of the `user_characters` list is built
from a pair keyed `id` and `hash`, the pair is added to a set, and the model is
returned only if that set ends up non-empty. The seven remaining keys the
factory accepts (`info`, `mobile_pay_shown`, `target_ad`, `inhouse_log_level`,
`update_banner_version`, `hub_version`, `change_log_data`) each sit behind their
own presence check, so omitting them is safe.

Run 25 confirmed the fix on the device. The session now remembers the hash the
client presented at auth and the answer carries `{id, hash}`, the factory
returned a model for the first time, the awaited field was written, the
predicate flipped, the loading bar left 0.790 and the loading screen finished
its remaining steps. The stop that held this project longest is gone.

The same run named the next stop in the client's own words: `AuthSceneController`
came up, the info window was created, and then the client logged
`Start wait config response` and did nothing for ten minutes except poll
`get_inapps` every 30 s and keep the socket alive. Eight seconds earlier it had
already complained `config 'ClanCommon' is null or empty`.

#### The config poll, measured rather than assumed

The client polls configs exactly once per boot, in one `subscribe` (`req_id 2`,
6208 B of plaintext) shaped
`{"list":{"<Name>":{"h":"<16 hex|empty>","b":0}, ...}}` and carrying **153**
names. **67** of them present a 16-hex fingerprint of a copy the client already
holds; the remaining **86** present `"h":""`, meaning the client holds no copy
and is asking for content. `ClanCommon` is one of the 86, and so is
`CommandsWithFullSlots`.

The 86 empty-hash names, verbatim: ABTest, Actions, AdventCalendar, ArmRace,
AssetBundlesMisc, Attributes, AttributesChange, Boosters, Campaign, CardRoulette,
ChangeLog, ClanBuffs, ClanBuildings, ClanCommon, ClanForge, ClanTasks,
ClanWarV2Even, ClanWarV2Odd, ClanWar_0, ClanWar_1, ClassSystem,
CommandsWithFullSlots, CompetionGameModeEvents, CompetitionSystemSettings,
ConfrontationEvent, ConnectSceneChest, ConnectSceneCommon,
ConstructableAdsOffers, ConstructableOffers, DefendTheTower, EventHub,
FortDefenceGlobal, FreePlayTask, FreeStuff, GameEvents, GameModeNotifications,
HideFromMonster, HoldingPositions, ImposterMode, LackBuy, Leaderboard2023Rewards,
LobbyNews, LocalNotification, LootBoxes, LootBoxesProviderClan,
LootBoxesProviderUniversal, ManualOffers, Marathon, MatchRewards, MatchmakingV2,
MessagePackTest, MiniBanners, Mutation, NoviceDoubleOffers, OffersAdaptive,
OffersDailyTier, OffersES, OffersLobby, OffersNovice, OffersNoviceForB,
OffersStarterPack, OffersTierBundle, PiggyOffers, PixelBookTest, PixelLegends,
PixelPass, PixelPassOffers, PlantTheBomb, PluginPhoton, RedLight, Refund,
RotationGameModeEvents, RouletteAds, SettingsMailBox, SocialReward,
Subscription20, Subscriptions, TemplateEvents, TierMatchmaking, TopUp,
TrophyRoad, TryGuns, TutorialForB, UniversalPromotion, UniversalPromotionForB,
X10.

#### The bug this exposed, and the fix

`sioCachedConfigHash` returned `("", false)` for an entry whose `h` was empty,
and `answerSubscribe` treated `false` as "unreadable, skip this name". All 86
names were therefore dropped from the answer, so the client received a list
naming only the documents it already had. Run 25 records it exactly:

    2026-08-29T08:44:49.062Z socket.config_polled acknowledged=67 configs=153
      req_id=2 served=0 sid=107a7cab0d154fde1605c3a8 unreadable=86

The reader now rejects only entries with no `h` key at all; an empty `h` is read
as "the client holds no copy", which is answered rather than dropped, and the
accounting line gained an `uncached` counter. Run 26:

    2026-08-29T09:23:56.121Z socket.config_polled acknowledged=153 configs=153
      req_id=2 served=1 sid=0d9426e6584b3114ae242b87 uncached=86 unreadable=0

#### Run 26: the decisive negative result

With every polled name answered and `CommandsWithFullSlots` served as a full
body, the client did not move. It accepted the same 14 documents, every one of
them `source=Embedded` and not one `source=Server`; it logged
`config 'ClanCommon' is null or empty` at 12:23:52.915 and
`Start wait config response` at 12:24:01.561, exactly as before.

Conclusion: the subscribe answer is a fingerprint-reconciliation channel only.
The client compares hashes there and expects document bodies to arrive by some
other route. Serving documents in that answer, in any of the three entry shapes
tried (`h`, `c` as text, `c` as a nested object with `v`), is now refuted on the
device.

Side results of these runs: `ConfigId 162` and `163` are `BalanceWeaponsCore`
and `BalanceWeaponsEconomic` (dump2313.cs, enum tail), which closes the last
unnamed ids in the polled set.

Infrastructure note, since it was tested deliberately: the `arch` (serveo) and
`arch-zrok` (zrok) MCP connections are two transports to one server process on
one host, not two environments. They do not conflict and they are not isolated -
a build launched through one is inspectable through the other, and parallel calls
on both succeed. Only per-call reliability differs, so zrok is primary and serveo
is the fallback. Never put a long `sleep` inside a single tool call: launch
detached with `setsid nohup` and poll in separate short calls.

#### Next

- [ ] Run 27 answers where the reply dies. `config_ingest_trace_2313.h` now
      hooks three more points on `BalanceController`: the per-id request
      `0x471C074`, the static hosted-config gate `0x471CAC0`, and the socket
      answer handler `0x4724604`, whose `Dictionary<string, object>` is walked
      read-only so the keys the client reads are named rather than guessed.
- [ ] If the answer never reaches the controller, the delivery event is
      something the server has not been asked for yet; find it from the client's
      own request trace instead of extending the subscribe reply again.
- [ ] Once ingestion is proven, host the 86 uncached documents starting with
      `ClanCommon` (ConfigId 69), hashing each at load and serving `h`, `c`, `v`.
- [ ] Implement a real `get_inapps` handler: the client repeats it every 30 s
      because it only ever receives a generic ack.

### 10.13 Runs 27-28: the boot config load is embedded-only, and the waiter is somewhere else

Run 27 added three hooks to `config_ingest_trace_2313.h` and armed only one of
them. Two method names were spelled with wrong code points (`\u5310` for
`\u4e10`, `\u4ee1` for `\u4e1f`), so metadata lookup refused them. Rule for
every future hook: transcribe the CJK method name code point by code point from
the dump and check the armed/refused line before trusting a run.

#### The call graph settles what the dictionary method is

`tools/find_callers.py` on the three RVAs returns one call site each:

    0x0471C074 (request by ConfigId)      <- 0x0471C02C  b   (dispatcher 0x471BF94)
    0x0471EAF4 (string handler)           <- 0x0471CD58  bl  (accept 0x471CB40)
    0x04724604 (Dictionary<string,object>) <- 0x0471EDF0  bl  (string handler)

So the chain is `accept -> string handler -> dictionary handler`: the
`Dictionary<string, object>` method is where an already-accepted document is
applied, not where a socket answer arrives. Naming it "the socket answer
handler" was wrong, and run 28 shows why the mistake was harmless: the dict it
receives is the parsed document itself (`class=Dictionary`2`, capacity 1931,
`count@0x20=1197`). The framework count offset is therefore wrong for this
build; the CoreLib layout with `_fastModMultiplier` before `count` (count at
0x28) is the remaining candidate, and the walk is now driven by the entries
array length instead of either offset.

#### Run 28: the boot config load never consults the socket

All three hooks armed. Two results, both decisive.

The per-id request `0x471C074` is **never called** during boot. Its only call
site is a tail branch inside the two-argument dispatcher `0x471BF94`, so nothing
in the loading path asks the controller for an individual config.

The static gate `0x471CAC0` is called exactly 16 times, every call from
`BalanceController.丅丗七丈万一丝丑业.MoveNext()` at `0x486FC98` - the `Start()`
iterator - for the fixed id list 20, 162, 163, 22, 175, 177, 23, 24, 25, 26, 27,
28, 30, 31, 32, 48. The verdict is 1 for exactly two of them, 175
(`BalanceEconomicForB`) and 177 (`BalanceWeaponsEconomicForB`), and 0 for the
other fourteen - which are exactly the fourteen documents the run accepts. The
gate is the A/B "ForB variant" filter, and the boot config load is a fixed walk
over embedded Balance documents that never touches the socket.

Consequence: `Start wait config response` is not this pipeline's wait. Extending
the subscribe answer, hosting more documents, or feeding `BalanceController`
cannot clear it, because the component that logs it has not been identified yet.
That identification is the only thing worth doing next.

#### Next

- [ ] Run 29 names the waiter directly: hook `UnityEngine.Debug.Log(object)`,
      filter for the message, and unwind a native backtrace from inside the
      hook, then symbolize it with `tools/symbolize_log.py`. The managed string
      is the only handle on that component, and the metadata neighbourhood probe
      is already exhausted.
- [ ] Read the awaited member of whatever class logs it, and answer that from
      the server.
- [ ] Hold the 86 uncached documents until the real ingestion path is known.


### 10.14 Run 29: the client named its own waiter, and the config bodies are binary attachments

Run 29 stopped guessing which component prints `Start wait config response` by
hooking `UnityEngine.Debug.Log(object)` and letting `LOGI`'s own `pc=` field name
the managed caller (log.h passes the proxy's return address, so inside a hook
that field *is* the caller -- no unwinding, which log.h:25-26 forbids):

```
12:45:05.712 pc=libil2cpp.so+0x3dc7940  23.1.3-dbglog: #1 managed caller of Debug.Log text='Start wait config response'
12:45:05.712 I/Unity: Start wait config response
```

`0x3DC7940` resolves inside `0x3DC771C AuthSceneController.<iterator>.MoveNext()`
(dump2313.cs:91539). Disassembling that method gives the whole waiting shape:

```
3dc795c  bl 0x2497ba0        ; config system singleton getter
3dc7960  cbz x0, ...         ; no singleton -> bail
3dc7964  ldrb w8, [x0, #0x2a] ; the awaited flag
3dc7968  cbz w8, 0x3dc7a70   ; false -> yield null, poll again next frame
```

So the wait is a per-frame poll of one byte at offset 0x2A of the config system
instance, class `<obfuscated>` TypeDefIndex 11094 (dump2313.cs:449523). That
byte is `<ready>k__BackingField`.

Who sets it. The property getter (0x24A052C) and setter (0x24A0534) both have
**zero call sites**: the AOT compiler inlined them, so `tools/find_callers.py`
returns nothing. Scanning the class's instruction range for `strb wN, [xM, #0x2a]`
instead finds exactly two stores -- the setter body itself, and one at
`0x24A2F58`, which sits inside:

```
0x24A1E1C  private void <parse>(Dictionary<string, object> answer,
                               Dictionary<object, object> requested,
                               List<byte[]> attachments)
```

That third parameter is the answer to the question runs 23-28 kept missing: the
document bodies are expected as **socket.io binary attachments**, not as text
inside the JSON answer. The same class owns the key constants `"v"`, `"h"` and
`"c"` (dump2313.cs:449525-449527), so the subscribe answer *is* the delivery
channel after all -- but each entry has to reference an attachment rather than
carry a body as a string, which is exactly why every previous body shape was
ignored without a single complaint in the log.

What the parser does, from its resolved call targets:

- `0x24A12B4 <nameToConfigId>(string)` - maps each polled name to its ConfigId.
- `0x24A3620 <tryToInt>(object, out int, out Exception)` - converts a per-entry
  value to an int; failures go to `Debug.LogException`.
- `List<byte[]>.get_Item` - indexes the attachment list.
- `0x249CD64 <storage>.<save>(ConfigId, byte[], out string)` - the binary config
  storage (class dump2313.cs:448789, TypeDefIndex 11078, consts
  `"BinaryConfigStorage.Key"` and `"18.1.0"`), i.e. bytes in, accepted document
  out.
- `0x249E1A8 <configChangedArgs>..ctor(ConfigId, string)` - the very event-args
  type `BalanceController.<dispatcher>(object, <args>)` (0x471BF94) takes, which
  is how a served document reaches the controller with `source=Server`.
- `0x24A380C <applyMany>(Dictionary<int, object>)` - the batch apply at the end.

Run 30 therefore traces the live shapes instead of guessing a fifth body form:
`config_system_trace_2313.h` hooks the parser (answer/requested/attachment
counts plus the ready flag right after the call), the object->int conversion
(logging the class it was handed and the text when that is a string), the
document apply, and the binary storage save with its error string.

#### Don't repeat

- Do not look for property writes with a call-site scan on this build: inlined
  accessors have zero call sites. Scan the class's instruction range for the
  `strb`/`str` to the field offset instead.
- Do not send a config body as text in the subscribe answer. Three shapes have
  now been refuted on device; the parser signature says the body travels as a
  `byte[]` attachment.

#### Next

1. Run 30: read the per-entry shape (is `c` an attachment index? is `v` the
   version? what does the storage report on a body it rejects?).
2. Server: answer `subscribe` with binary attachments -- declare the count in
   the text frame, put each document's bytes in its own attachment, and have the
   per-name entry reference it. `internal/api/socketio_binary.go` already
   assembles inbound attachments; the outbound direction needs the same in
   reverse.
3. Keep the 86 uncached names in mind: `ClanCommon` (69) is the one the client
   complains about out loud, so it is the first document to host.


### 10.15 Runs 30-31: the namespace was wrong, and then the parser turned out never to run

Run 30 armed **0/4** and named its own reason:

```
hook: optional method not found: <config system>.<parse>/3
hook: optional method not found: <config system>.<toInt>/3
hook: optional method not found: <config system>.<apply>/2
hook: optional method not found: <storage>.<store>/3
```

Every other obfuscated class in this library hooks with an empty namespace, so
that is what the new header used. The dump says otherwise: `dump2313.cs` carries
`// Namespace: PGCompany` above both the config system (line 449523) and the
binary config storage (line 448789). `il2cpp_class_from_name()` cannot find a
type under the wrong namespace, and the class and method code points were right
character for character -- verified by extracting them from the dump by RVA
instead of retyping them.

Run 31, namespace fixed, armed **4/4** and produced the sharpest negative result
of the session:

- The client reached `Start wait config response` at 13:03:12 as always.
- Not one `23.1.3-cfgsys` event fired. The answer parser is never called.
- `tools/find_callers.py analys2313/libil2cpp.so 0x24A1E1C` reports
  **0 call sites** for the parser, so it is reached through a subscriber
  delegate, exactly the 3-argument subscriber shape
  `internal/api/socketio_reply.go` documents: head dictionary, a client-local
  dictionary, and the packet's attachments as `List<byte[]>`.
- Emitted events for the whole run: `subscribe` x11, `get_inapps` x6,
  `update_player_online` x2, `progress_status` x2, `player_prefs` x2,
  `mmc_get_room` x2, `get_last_command` x2, `get_binded_ids` x2, `auth_v2` x2.
  After the wait line the client emits **only** `get_inapps`.
- The backend answered normally at 10:03:07Z: `acknowledged=153 configs=153`.

So the config system never asks for anything, and the answer we do send is not
routed to it. Both readings point at the same next question: does the config
system's request sequence ever start?

#### Don't repeat

- Do not assume the global namespace for obfuscated classes. Read the
  `// Namespace:` comment above the declaration; two of these classes live in
  `PGCompany`.
- Do not retype CJK identifiers from tool output. Extract them by RVA and emit
  `\uXXXX` escapes (`/tmp/opg3d_truth2.py <rva>...`), which is how run 31's
  names came out right on the first try.
- Do not read "trace armed" as "trace working". Run 30 armed the summary line
  while every individual target had failed; only the per-target warnings said so.

#### Next

Run 32 traces the request side with names taken from the dump by RVA:
`<groups>()` 0x24A1740, `<requestCo>(List<List<ConfigId>>)` 0x24A1CC8 and its
nested state machine's `MoveNext`, `<nameMap>()` 0x249FEE0 and `<embedded>()`
0x249EFE0. If the coroutine never starts, the gate is upstream of the network
and no server change can help until it is found; if it starts and emits, the
event name it emits is the name the server has to answer with.


### 10.16 Run 32: the config poll is the config system's own request, and the answer never comes back to it

Run 32 armed **9/9 in namespace PGCompany** and settled the ownership question
that runs 26-31 could only guess at. Within four milliseconds:

```
13:10:13.036 name map=0x7935d2fe40 count@0x20=163
13:10:13.037 groups built list=0x79396f35d0 size=4
13:10:13.037 request coroutine created groups=0x79396f35d0 size=4 enumerator=0x7935d2fae0
13:10:13.040 request step #1 state 0 -> 2 more=1 current=0x0 class=null
13:10:13.040 sio[cipher] encrypt open bytes=6208 text='{"list":{"Ads":{"h":"1e2dacba5f31b099", "b":0}, "GameEvents":{"h":"", ...
```

The 6208-byte, 153-name poll leaves the process three milliseconds after the
config system creates its request coroutine, from the coroutine's own first
step. So `subscribe` **is** the config system's request: the channel we have been
answering all along is the right one, and the earlier reading that "the client
never sends a config request" was wrong -- run 31 could not see it because only
the delivery side was traced.

What happens next is the actual defect. The iterator yields `null` (wait one
frame) for 111 consecutive frames, then moves on:

```
request step #1   state 0 -> 2 more=1 current=0x0        class=null
request step #112 state 2 -> 1 more=1 current=0x7694900e80 class=WaitUntil
```

State 2 is the per-group wait for an answer; it never completes, and the
coroutine falls through to its `WaitUntil` timeout. Throughout the run:
`parse=0 toInt=0 apply=0 store=0`. The answer parser is still never called.

#### Two candidate causes, one already eliminated

**Request-id mismatch: ruled out.** The client's counter and ours agree -- the
backend answered `req_id=1` through `req_id=15` in order at 10:10:13-10:10:17Z,
the config poll among them (`socket.config_polled ... req_id=2`). Auth and
binded-ids complete through the same pending-request machinery in the same run,
so req_id matching demonstrably works.

**Subscriber routing: still open, and now the only candidate.** The parser has
0 direct call sites and a 3-argument shape (head, client-local dict,
`List<byte[]>`), which is the subscriber signature `socketio_reply.go`
documents. Disassembling the request iterator's `MoveNext` (0x24A59EC, 614
instructions) shows **no reference to the parser address 0x24A1E1C**, so the
delegate is not created there. The iterator calls out once into the socket
manager range (`bl 0x35c2c1c`) and four times into the BestHTTP range
(`bl 0x3e8bf74`).

#### Don't repeat

- Do not conclude "the client never asks" from a silent delivery-side trace.
  Trace the request side before drawing that conclusion; run 31 spent a whole
  cycle on a reading that run 32 reversed in one line.
- Do not suspect `req_id` again. It is echoed correctly for every event in the
  run, and other events complete through the same path.
- Do not cap an iterator trace so hard that state changes are lost. Logging
  every state transition regardless of the call cap is what made step #112
  visible.

#### Next

Run 33 traces the send call itself: resolve `0x35c2c1c` and the BestHTTP targets
to method names, hook the emit-with-callback entry, and log both the event name
string and the delegate's target method. That names the event the config system
listens on, and whether our answer is dispatched to any subscriber at all.

### 10.17 Run 33: the answer was arriving all along, and a crashing subscriber was eating the dispatch

Run 32 was read one layer deeper, and the reading overturned the working theory.
The client did receive the config answer: the cipher trace shows it decrypting
11 bytes of event name and 5569 bytes of body that begins with the full 153-name
list. Nothing was lost on the wire, and nothing was wrong with req_id. The loss
happens after delivery, inside the client.

In the same millisecond the log carries exactly one managed exception for the
whole run:

    KeyNotFoundException: The given key 'info' was not present in the dictionary.
      at UpdatesChecker.<read>(Dictionary`2)
      at UpdatesChecker.<subscriber>(Dictionary`2, Dictionary`2, ...)
      at Rilisoft.WebSocket.<dispatcher>.<call>(String, Dictionary`2, Dictionary`2, ...)

The throwing frame is a subscriber, called from the socket event dispatcher, and
the subscriber has the same three-argument shape as the config parser. Ordering
by log line number ties the throw to the answer that immediately precedes it:
full_check_version. The same exception appears exactly twice per run in runs 30,
31 and 32 - once logged by BestHTTP, once printed by Unity - so it has been
present the whole time and was mistaken for input-method noise.

What the client actually wants was read from machine code rather than guessed.
UpdatesChecker's reader at 0x432FD40 does this:

    if (head.ContainsKey("err_code"))   // literal slot 0x67DB910
        return Convert.ToInt32(head["err_code"]);
    var info = head["info"];            // literal slot 0x67660C8 - throws here
    MiniJson.Deserialize(info);         // 0x2D7BC08
    this.<apply>(parsed);               // 0x432FF10

So err_code is the short branch and info is only the fallback. The server does
not need to invent an update document at all; it needs to answer the version
gate with an explicit code. answerCheckVersion now sets err_code to zero
(internal/api/socketio_events.go:459), the backend was rebuilt and restarted.

Run 33 measured the change. The dispatch crash is gone: zero EventDescriptor
exceptions and zero 'info' KeyNotFoundException lines, against one of each in
every previous run. The request coroutine also behaves differently - 41 steps
instead of 213, with the state change at step 77 instead of 112 - so the boot
path is genuinely shorter now. The parser is still not called and the wait
marker still prints twice, so this was a real defect but not the last one.

Socket manager facts established for the next step, all from the dump:

- The config coroutine sends through WebSocketManager 0x35C2C1C, which returns
  Task<response>; the config system has a Task field at 0x50, so it awaits it.
- The response class is TypeDefIndex 9371 (dump line 381527) and exposes exactly
  Dictionary<string, object>, Dictionary<object, object> and List<byte[]> -
  the parser's three arguments.
- The dispatcher is a public static method 0x35BA5E0 on the static class at dump
  line 381487 (TypeDefIndex 9369), taking the event name as its first argument.
- WebSocketManager 0x35C3D08 takes object[] and reads Convert.ToInt32(head[req_id]);
  0x35C4170 and 0x35C4028 take a head dictionary and are the completion paths.

Do not repeat:

- Do not treat req_id as suspect. Backend answered 1..15 in order in run 32.
- Do not assume the config answer fails to arrive. It arrives and decrypts.
- Always grep the logcat capture with -a. It contains binary bytes, and grep
  silently reports 'binary file matches' instead of the lines.
- Never stop the backend with pkill -f on a pattern that also matches the shell
  that launches it - that kills the new process too. Launch from a script file
  and match the binary name with an anchored pattern.

Next: hook the static dispatcher 0x35BA5E0 to log every event name it dispatches
and how many subscribers it has, and hook 0x35C3D08 to log req_id completion, so
the subscribe answer can be followed from the socket into the config system.

Correction, read after run 33 finished:

The zero exception count is not yet proof that the fix works. The backend
census for run 33 answers 17 events - auth_v2, get_binded_ids, get_inapps x4,
get_last_command, mmc_get_room, player_prefs, progress_status, subscribe x6,
update_player_online - and full_check_version is not among them. The client
emitted the version gate at 10:35:37Z, five seconds after it had already torn
its own socket down: the server logged connection reset by peer at 10:35:32Z
with alive_s=109 and packets=58. The request went into a dead socket, so the
subscriber never ran and the err_code answer was never delivered.

What run 33 does establish is the shape of the failure that follows the config
wait, and it is worse than a stall:

- The wait line prints at 10:33:49Z, the request coroutine reaches state 1 at
  step 77, and the parser is never called - unchanged from run 32.
- Roughly 100 seconds later the client, not the server, closes the websocket.
- It then fetches exactly one config over plain HTTP,
  /pixelgun3d-config/BuffSettings1031/BuffSettings_android.json, and this
  server answers it with an empty object.
- The process is killed with signal 9 at 13:38:23 local, after two minutes of
  ptrace-denied lines from zygote, i.e. it was hung rather than crashing.

So the version gate fix still needs a run in which the client is alive when the
answer arrives, and the config wait has to be broken before the socket teardown
timer expires. The dispatcher signature needed for that is now known in full:
the static class at dump line 381487 holds the subscriber registry in its only
field, Dictionary<string, List<delegate>> at offset 0x0, and dispatches through
0x35BA5E0 with four arguments - string event name, Dictionary<string, object>,
Dictionary<object, object>, List<byte[]>. That is the exact shape the config
parser takes, minus the name, so the registry tells us directly whether the
config system is subscribed and under which name.

### 10.18 Run 34: the config system listens on config_info_versions, an event this server has never sent

Run 34 added socket_dispatch_trace_2313.h, which hooks the four entry points
of the socket layer's subscriber registry (namespace Rilisoft.WebSocket, dump
line 381487): the dispatcher at 0x35BA5E0 and the subscribe, unsubscribe and
add/remove methods at 0x35C1818, 0x35C188C and 0x35C18F4. It armed 4/4 and
answered the question that four runs could not:

    subscribe event='config_info_versions' handler=<delegate>
        target=<config system instance>   // PGCompany class, TypeDefIndex 11094

The config system registers exactly one handler, and it registers it under the
event name config_info_versions. It does not listen on subscribe. The name
appears nowhere in this repository, so this server has never emitted it, and
run 34 dispatched it zero times.

That closes the whole line of investigation opened in run 26. The config poll
is a request/answer pair on `subscribe`, and its answer is consumed by the
socket API facade, not by the config system. The config bodies are delivered by
a separate, server-initiated event that the client subscribes to before the
poll goes out. Answering the poll perfectly, as this server has done since run
26, can never move the boot forward on its own.

The registry census from the same run is worth keeping: 63 distinct handler
registrations, including full_check_version -> UpdatesChecker (the subscriber
that threw the KeyNotFoundException in runs 30-32), expired_session ->
ConnectionLostChecker, get_time -> PixelTime, update_score_v3 ->
TournamentController, and subscribe -> the socket API facade. Events with a
null target are static handlers.

Next: emit config_info_versions from the server. The handler's three arguments
are Dictionary<string, object>, Dictionary<object, object> and List<byte[]>, so
the document bodies belong in the attachment list, which is what the parser's
third parameter has said since run 29. The exact key shape of the first
dictionary is being read out of the parser at 0x24A1E1C now.

### 10.19 Run 35: the config gate is open

The server now emits config_info_versions itself, right after it answers the
poll on subscribe. New file internal/api/socketio_configpush.go:

- encodeEncryptedConfigPush writes len(documents)+2 attachments. The document
  bodies go first as raw bytes, the encrypted name and argument stay last,
  and the payload carries exactly two placeholders numbered n-2 and n-1 -
  the indices WebSocketTransport.OnBinary substitutes. A placeholder for a
  body would survive unsubstituted and break the payload JSON.
- Each pushed entry is {h: hash, v: version, c: <attachment index>}, so `c`
  is an int index into List<byte[]>, not a body. That is what the parser's
  third parameter has implied since run 29.
- Only polled names that this server hosts are pushed. An index into an
  attachment that was never sent throws inside the client, not here.

Measured on device at 2026-08-29T11:06 UTC, one document (CommandsWithFullSlots,
3 attachments total):

    backend  socket.config_pushed attachments=3 documents=1 names=1
    client   #1 parse answer=... count@0x20=3 attachments=... size=3 itemsLen=3
    client   #1 parse returned, ready flag now 1
    client   Start wait config response
    client   End wait config response      <- same millisecond
    client   LoadMainMenu tail completed (state 1 -> 2, result=1)

The parser ran for the first time in the project's history, the ready byte at
offset 0x2A went to 1, and the coroutine that has held every run since 25
released in the same millisecond. The boot then walked into the main menu
sequence and opened a burst of new traffic: mailbox subscriptions,
friends_list, lobby_likes_v2, mm_status, mmc_get_room, get_binded_ids,
check_binded_gc. Zero socket.event_unanswered for the whole run, and the
socket stayed open - the only repeating traffic afterwards is the 30 s
get_inapps poll.

Note that `requested` (the second parameter) arrived null and the parser did
not care, so a server push does not need to fabricate one.

#### The next blocker, same shape as the last one

    KeyNotFoundException: The given key 'social_id' was not present in the dictionary.
      at <class>.<handler> (Dictionary`2[TKey,TValue])
      BestHTTP.SocketIO.Events.EventDescriptor:Call -> EventTable:Call -> OnBinary

Another subscriber indexes a key this server does not send, and the throw
again happens inside the dispatch loop. The remedy is the one that worked for
err_code: read what the handler checks before it indexes, then answer that.

### 10.20 Runs 36 and 37: err_code is a local cure, not a global one

Run 35 left one throw: the Google Play bind check indexed `social_id` on an
answer that did not have it. The consumer's helper at 0x3DCFC04 reads it like
this:

    ContainsKey(answer, 'err_code') ? (int)answer['err_code']
                                    : answer['social_id']   <- throws

Run 36 put err_code on every reply, in sioOK. The throw went away and the boot
went quiet with it:

    run 35 emits  subscribe 12, get_inapps 8, check_binded_gc 2, get_binded_ids 2, ...
    run 36 emits  subscribe 12, get_binded_ids 2, ...  and nothing after
    run 35 auth predicate 0x3DC7688 polled  3 times
    run 36 auth predicate 0x3DC7688 polled 34 times and kept going

Some other consumer reads the same key and takes a different branch when it is
present, so the client stopped asking. A global err_code buys silence at the
cost of the conversation.

Run 37 therefore keeps sioOK as it was and gives check_binded_gc its own
answer carrying err_code = 0, which is the truth: there is no bound account
here and that is not an error. The flow came back exactly as in run 35 -
check_binded_gc emitted and answered, get_inapps polling again, zero
KeyNotFoundException, auth predicate polled 3 times - and the config gate
stayed open (parse fired, ready flag 1, wait started and ended in the same
millisecond).

The next wait is named in the client's own words:

    Start wait config response
    End wait config response
    Start wait info about Game Center     <- run 37 parks here

AuthSceneController.<>c.<predicate at 0x3DC739C> polls a static field: it loads
a class from literal slot 0x678A498, reads statics+0xB8, and compares the
object at +0x30 against null. So the wait ends when something stores an object
there, not when a flag flips. The single caller of the err_code/social_id
helper is 0x3DCFB98, inside the static handler at 0x3DCFB50 - the
check_binded_gc consumer itself, which is where that store has to come from.

### 10.21 Run 38: the bind check is a data question, not an error question

Reading the whole of 0x3DCFC04 instead of its first branch changed the answer.
It is not an error reader with a fallback; it is a store with an escape hatch:

    if answer has err_code -> return (int)answer[err_code], and nothing else
    else                   -> statics+0x30 = answer['social_id']
                              statics+0x38 = second key, looked up not indexed
                              return 0

The auth scene predicate at 0x3DC739C loads that same class, reads statics+0x30
and returns true when it is non-null. So err_code is answered and understood -
and the field it was waiting for is never written. Run 37's 2880 polls were the
client waiting for a store that our own answer had skipped past.

Run 38 answers check_binded_gc with social_id = empty string and no err_code:

    {"error":0,"req_id":14,"result":"ok","social_id":""}

    14:27:04.833  Start wait config response
    14:27:04.833  End wait config response
    14:27:04.833  Start wait info about Game Center
    14:27:05.071  End wait info about Game Center

238 ms, and the second gate of the boot is open. The predicate polled 4 times
instead of 2880. Zero KeyNotFoundException, zero unanswered events, the config
push and parse unchanged.

A note worth keeping: even a value of the wrong type would have worked, because
the store falls back to String.Empty when the type check fails. Only a missing
key throws. The client asks 'who is bound here', and an empty answer is still an
answer; no answer at all is what breaks it.

The wait after this one is predicate 0x3DC75EC, polling in the same scene.

### 10.22 Runs 39-39b: the third gate has a name and a shape

The bar stops at state=45 bar=0.750 and the visible loading now reads 80%.
Predicate 0x3DC75EC was polled 6720 times in run 39b and never returned true.

Disassembled, that predicate is three loads and a compare:

    x19 = metadata slot 0x6778288      ; a class cell, two dereferences
    x0  = *x19                          ; the class
    x8  = [x0 + 0xB8]                   ; its static fields
    w8  = byte [x8]                     ; offset 0x0
    return w8 != 0

Two scanners written for this (slotrefs.py, slotwriters.py) walk every PT_LOAD
segment, pair ADRP with its LDR and follow page -> cell -> class -> statics.
The first pass found 142 users of the slot and no writer at all for offset 0x0,
which was wrong twice over: the tracker did not follow register moves, and it
only looked for byte stores. With moves and 16/32/64-bit stores added, four
writers appear, all in one class, each pairing a 16-bit store at offset 0x0
with a byte store at offset 0x2.

That class is Progress.<holder>, TypeDefIndex 6640, an internal static class:

    Nullable<bool> <..>k__BackingField;   // 0x0   <- what the scene waits on
    bool           <..>k__BackingField;   // 0x2
    Nullable<int>  <..>k__BackingField;   // 0x4
    Action ..., Action ...;               // 0x10, 0x18

Offset 0x0 is the "has a value" half of a Nullable<bool>, so the scene is not
waiting for a positive answer. It is waiting for any answer at all - the same
lesson run 38 taught on the Game Center gate, one field further along.

The filler has the shape of a socket subscriber:

    <handler>(Dictionary<string, object>, Dictionary<object, object>,
              List<byte[]>)                                       0x17A9980

    if answer is null                  -> return
    if answer contains err_code        -> return, nothing written
    if answer lacks one integer field  -> return, nothing written
    else v = Convert.ToInt32(field)
         statics+0x0 = Nullable<bool>(v == 0)
         statics+0x2 = (v == 2)
         statics+0x4 = Nullable<int>(v)
         if the value changed -> raise the change Action

So one integer decides three properties, zero means "clear", two means a state
of its own, and a missing field is indistinguishable from silence. This is the
command type the client has been waiting for.

Which event carries it is not yet proven. The subscriber registry dumped at
boot lists nine events whose handler is a static method, the shape this one
has: change_clan_state, check_binded_gc, check_binded_id, get_binded_ids,
mark_id_unused, player_prefs, progress_status, reset_gc_id, update_bind_id.
The client emitted only get_inapps, subscribe, update_player_online,
progress_status, mmc_get_room, get_binded_ids and check_binded_gc during the
whole of run 39b, so either an answer we already send is missing the field, or
the event has to be pushed the way config_info_versions is.

A correction to the previous reading: the "emitted census" of run 39b counted
registry entries as emissions. The backend log shows zero get_clans_core and
zero expired_session for the day; the conversation did not widen.

Run 39 carries 12 crash markers and run 39b none, so behaviour is read from
39b.

Run 40 answers both open questions in one launch. The client gains one trace,
command_type_trace_2313.h, which hooks the handler and both setters, prints the
holder's three bytes before and after, and dumps metadata slot 0x676E840 from
inside the hook - the only place that cell is initialised - to name the field.
The registry trace now prints each subscriber's own address, so the event that
owns 0x17A9980 names itself.

### 10.23 Runs 40-43: the third gate opens on one integer named "enabled"

Run 40 is a lesson about deployment, not about the game. The push picked a
library by searching the build tree, found a stale intermediate and overwrote a
working library with an older one: the launch reported eleven components
instead of the full set and none of the traces the run was made for. Deployment
goes through the deploy script, which pushes lib/arm64-v8a/libopg3d.so and
nothing else.

Run 40b answered the first question outright. The subscriber registry now
prints each handler's own method address, so the nine static subscriptions name
their owners:

    change_clan_state 0x29562FC     mark_id_unused  0x3DCF4C8
    check_binded_gc   0x3DCFB50     player_prefs    0x2F58218
    check_binded_id   0x3DCF62C     progress_status 0x17A9980
    get_binded_ids    0x3DCFF30     reset_gc_id     0x3DCFE58
                                    update_bind_id  0x3DCF224

The handler that fills the awaited flag belongs to progress_status - an event
this server has been answering since the socket layer existed. The gate was
never a missing event; it was a missing field in an answer we already sent.

Run 40b also crashed twice, and the crash was mine: the probe read the metadata
cell for the field name directly, and an uninitialised cell holds whatever the
loader left behind (0xa000fca9). Reading process memory through
process_vm_readv is not available at this API level, so the probe now checks
the page with mincore before it touches an address, and checks that a candidate
looks like a managed string - class pointer first, sane length at 0x10 - before
printing it.

Run 41 falsified the guesses. The answer carried five plausible names at once
(type, status, state, progress_status, code); the handler entered with an
eleven-entry answer and left the flag untouched. It also showed why the name
was unreadable: the literal cell is filled in by the method itself, so a probe
that prints on entry always prints garbage.

Run 42 read the name from the live process on the return path:

    the answer field this handler reads is "enabled"
    control field (the silent exit) is "err_code"

Run 43 acted on it. progress_status now answers with enabled = 0:

    {"enabled":0,"error":0,"experience":0,"level":0,
     "progress":{...},"req_id":3,"result":"ok"}

    before awaited=0 value=0 second=0
    after  awaited=1 value=1 second=0

Predicate 0x3DC75EC was polled once instead of 6720 times. LoadMainMenu ran to
completion (state 1 -> 2, result 1), the main-menu scene was requested, and the
scene's own controller started and dispatched state 2, Authorized. Zero crash
markers, zero Unity errors, every emitted event answered.

The meaning of the integer is worth recording: zero is stored as "enabled", two
selects the client's second state, and any other value is kept as a raw number.
As with the two gates before it, the client is not waiting for a particular
value - it is waiting for the field to be present at all.


### 10.24 Runs 44-45: what the integer actually means

Run 44 repeated run 43 on a clean library with the one-shot probe removed:
25 components, predicate 0x3DC75EC polled once, LoadMainMenu completed, zero
crashes, and no leftovers of the probe in the log. The fix stands on its own.

Then the screen said something the log could not. With enabled = 0 the game
reached the main menu and showed its technical works screen. Our own
maintenance switch was off at the time (the liveness answer carried
maintenance:false), so the screen came from the new field, not from the
runtime-settings layer.

That is the correction to section 10.23: the field is named "enabled", but the
client stores the boolean as (value == 0), and that boolean is what raises the
technical works screen. Zero is how a live server announces downtime. The
waiting predicate only needs the field to be present, so the value is free -
and one is the value that says the server is open.

Run 45 answers enabled = 1 and confirms both halves at once:

    predicate 0x3DC75EC polled #1 returned true
    LoadMainMenu tail completed (state 1 -> 2, result=1)
    Fatal signals: 0

No technical works screen, no new wait: over 12,000 log lines the only repeating
traffic is the thirty-second purchase poll.

The same run produced the first sign of life past the menu. The client emitted
init_progress - an event that never appeared in runs 43 or 44 - carrying its own
snapshot: a hash, a hash version, and a slot dictionary. The server answered it
through the generic path:

    event=init_progress body={"error":0,"req_id":17,"result":"ok"}

The client accepted the answer and unsubscribed from the event, which is how it
signals that this step is done. The acknowledgement is blind, though: nothing
keeps the snapshot. Storing it, and answering the next boot from storage, is the
next piece of work - otherwise the client remains the only owner of progress.


### 10.25 Runs 46-47: the switch belongs to the server

The first version of the fix answered enabled = 1 as a constant. That was wrong
for the opposite reason the zero was: a constant makes the operator's
maintenance switch invisible to the client, which would have left the whole
runtime-settings layer announcing downtime to nobody.

The value is derived now:

    sioProgressOpen        = 1
    sioProgressMaintenance = 0

    func (s *Server) progressEnabled() int {
        if s.settings.snapshot().Maintenance {
            return sioProgressMaintenance
        }
        return sioProgressOpen
    }

It is the same switch the liveness endpoint already reports over HTTP, so one
operator action is now announced on both channels.
TestProgressEnabledFollowsMaintenance covers both directions and the snapshot
the liveness answer reads.

Run 46 was taken with maintenance patched on over the admin API. The answer
carried enabled = 0, the waiting predicate still opened on the first poll, the
main menu still loaded - and init_progress did not appear at all. The client
stops its progress branch by itself when the server says it is closed, which is
exactly what a maintenance mode is supposed to do.

Run 47 patched the switch back off: enabled = 1, init_progress returned, the
menu loaded, zero crashes. The same library binary served both runs; nothing in
the client was rebuilt, redeployed or changed between them. Maintenance now
travels from an HTTP PATCH to the player's screen without the game knowing
anything about it.

#### Client inventory: what is a trace and what is a crutch

The project rule is that the client may trace and log, and nothing else, with
the Photon AppID path as the single agreed exception. Measured against the 26
install sites in main.cpp:

  * 19 are trace-only: version, switcher, anomaly comparison, config ingestion,
    managed log caller, config system, socket dispatch, slot document reader,
    config store, auth scene, auth state, auth scene sweep, info window,
    coroutine wait, binded account gate, quiet authorization, Photon trace,
    socket.io codec, and the startup trace itself.
  * 7 change behaviour: signature compatibility (startup shim for this exact
    build), startup guards, the loading stall watchdog, the HTTP backend
    redirect, the config socket redirect, Photon online (AppID), and the Photon
    default-plugin workaround.

Of those seven, only the two redirects and the AppID path are meant to stay -
they are how a thin client finds a private server. The rest have to dissolve as
the server becomes complete, and the first one to retire is the loading stall
watchdog: it still skips InitializeSwitcher state 35 on every boot
(OPG3D_2313_INIT_SKIP_STATES defaults to 35). Nothing about that step has been
served yet - it was skipped when the server could not answer at all. The next
investigation is what state 35 waits for, so it can be answered instead of
skipped and the watchdog can be removed.

### Runs 48-49: the 87% stall and the config push envelope

Run 48 was the furthest boot yet. Every event the client sent was answered,
both auth-scene waits cleared instantly, and the loading bar reached 0.750
before the boot coroutine died on a NullReferenceException inside the progress
applier. The blind acknowledgement this server sent for init_progress was the
suspect: the consumer at 0x17AB704 enters the applier on ContainsKey alone and
passes the isinst result on unchecked, so a member that is not a dictionary
arrives as null and throws at the first instruction of the applier, 0x2701904.

The same run exposed a second, independent defect. The config system iterates
the top level of a push and reads every key as a config name, and the push
carried the ordinary reply envelope:

    [ConfigSystem.OnWebS_ocketCallback] Unknown configId: None, Key: error
    [ConfigSystem.OnWebS_ocketCallback] Unknown configId: None, Key: list
    [ConfigSystem.OnWebS_ocketCallback] Unknown configId: None, Key: result

The gate opened, so the push looked healthy in the log of this server while not
one document was ingested.

Three changes went in for run 49. The store keeps the boot document in its own
field, apart from the flat counters register_push writes, because the applier
reads every member of the document as a slot. init_progress has a real handler
that persists the snapshot and answers with the stored document. And the push
argument is now the bare name map with no envelope around it.

Run 49 settled one of the two. The Unknown configId lines are gone: the client
now finds the config by name and asks for the next thing it needs, naming it
out loud - Key 'val' not found: CommandsWithFullSlots. The entry shape is the
remaining work there, and it is a measured next step rather than a guess.

The 87% stall did not move, and the trace says it is not a delivery problem.
The reply was decrypted intact with all 25 slots in result, dispatch #18 for
init_progress ran and returned, and the NullReferenceException followed four
milliseconds later. The document reached the consumer and the cast still
produced null.

That narrows the third literal to something this reply already carries. The
answer holds exactly three members - error, req_id and result - and result is
now a dictionary, so either the literal is error or req_id, both integers, or
the parser of the client hands nested objects back as Dictionary<object,
object> while the caller casts to Dictionary<string, object>. Run 50 settles it
with a trace-only probe in the client that prints the runtime type of each
member and resolves the three literal cells to their managed strings.

Housekeeping: the game process is force-stopped and logcat is killed at the end
of every run, so nothing is left hanging on the device.

## Runs 50-55: the 87% stall is broken

Runs 50 to 53 kept guessing the name of the member the boot coroutine applies.
"slots" produced a null document, "result" produced a null document, and each
guess cost a full run to disprove. Two of those runs were spent on the wrong
method entirely, because tools/symbolize_log.py does not index generic
instantiations and resolved the applier's return address to a method 166 KB
away. The real caller is the boot coroutine of AuthSceneController: the call at
0x3E753B4 inside its generic MoveNext hands the applier the response member at
+0x18 without checking it.

Run 54 stopped guessing and read the answer out of the client. The response is
built by the socket callback at 0x17A88A4, and its document member is not taken
from the reply body at all - it is copied from a field the request fills in
exactly one place, the reply reader at 0x17A8B40. That reader looks the answer
up by a single key, casts it to Dictionary<string, object>, and keeps a null
when the cast does not hold. A probe on that reader printed the two literals it
loads:

    key holding the progress document = "progress"
    key holding the error code        = "err_code"

The second is the control, since run 51 had already resolved that cell to
err_code by other means. The same probe listed the reply as the client saw it,
error and req_id as doubles and the document member as a dictionary, which
retired the last competing theory. The shape was never wrong. The name was.

Run 55 answered init_progress with the document under progress. There was no
NullReferenceException. The applier was handed a 25-entry dictionary,
InitializeSwitcher finished at state 45, LoadMainMenu completed its tail
(state 0 -> 2, result=1), the loading watchdog disarmed itself because the
Switcher is destroyed with its scene, and the client produced traffic it had
never reached before: get_progress_slots, create_slots, 23 update_progress,
8 update_accessories, get_player_version and refund_get. The Unity activity is
top-resumed with no ANR, and two screen captures four seconds apart differ, so
the menu is drawing rather than frozen on a last frame.

The 90% stop that was expected next never appeared. The client went from the
87% point into the menu in one step.

That run exposed the next piece of work. Forty events were answered by the
generic acknowledgement, and among them are the ones carrying the state of the
player: update_progress arrived 23 times in under three seconds, and
create_slots brought slots 25 to 30 that the stored document does not have.
Nothing persists either of them, and get_progress_slots is answered with ok
instead of the stored slots.

- [x] Read the literal the reply reader loads instead of guessing it
- [x] Answer init_progress under the progress member the client casts
- [ ] Persist update_progress and create_slots into the stored document
- [ ] Answer get_progress_slots with the stored slots
- [ ] Answer update_accessories, get_player_version and refund_get for real
- [ ] Index generic instantiations in tools/symbolize_log.py

## Run 56: the 90% stop is five documents nobody hosts

The client clears the applier and stalls at 90% with five consumers complaining
at once:

    [TrophyRoad] ---- parse error, message below
    [TopUp] ---- _config == null
    [ManualOffers] ---- _config == null
    [ConfrontationEvent] ---- _config == null
    [Boosters] ---- _config == null

The poll names the cause without any client-side work. In
logs/backend-2026-08-29.log each of those five names arrives with an empty hash
in 84 polls of the day and with a fingerprint in 8, while CombatTokens, ExpOpen
and WearSkins carry a fingerprint in all 100. An empty hash is the client saying
it holds no copy. This server hosted exactly one document - CommandsWithFullSlots,
built into slot_config.go - so hostedConfigReply returned nil for every other
name and each was answered with its own empty hash echoed back. Every poll of
the day logged served=0 except the three that served the slot document, and all
three pushes carried documents=1.

The consumer messages are a family, not five special cases. global-metadata.dat
holds the same two literals for ExpOpen, WearSkins, RecipeSystem, GraffitiSystem,
CombatTokens, Piggy and Achievements, so any unhosted config will report itself
the same way once its screen is reached. TrophyRoad differs only in having a
second source: [TrophyRoad] ---- parse from prefs error, message below sits
beside it, so a missing document reaches that parser as empty text rather than
as a null reference.

The shape of a document was measured, not guessed. The client's own parsers dump
what they receive to /sdcard/Android/data/com.pixel.gun3d/files/opg3d-doc-*.json;
the 14 files written there at 18:34 local (BalanceRewards, BalanceWeaponSkins,
BalanceOthers, BalanceLeveling and the rest of the Balance family) are each a
JSON object whose first member is configId naming the document - the same object
the client's config cache stores as the plaintext h_<16 hex>_c_<object>.

### The fix

content/sio-configs/<Name>.json is now where hosted documents come from.
hostedConfigDocument reads that directory first and falls back to the built-in
slot document, so both delivery paths - the poll answer (h, c, v) and the push
(val, hash) - serve any named document without a rebuild. A polled name is
refused as a file name unless it is a plain identifier, because those names
arrive from the client. The hash stays the sha256 prefix of the re-encoded
document, so reindenting a file does not tell every client its copy went stale.
The push now batches: each push spends two attachments on the event name and
argument, so documents beyond the 14 that fit go out in further pushes instead
of being dropped, which matters because the client polls once per boot.

The five names are seeded with the minimum the measurement supports,
{"configId": "<Name>"} and nothing invented on top of it. If a consumer needs a
member, the next logcat names it and the answer is a file edit rather than a
build.

- [x] Serve the missing configs from files instead of from code
- [x] Batch the push so more than 14 documents can be delivered
- [ ] Confirm on the device that the five consumers stop reporting null
- [ ] Host ClanCommon the same way if the clan screen still reports it empty

## Run 57: the documents arrive, the bodies are still empty

Device 001691527000747, backend pid 120855 (boot 2026-08-29T16:31:49Z, the
file-backed store live), socket session e4897e19bf1678978519a620. Logs kept as
`log_front.txt` (22672 lines of logcat, captured live) and `log_back.txt` (the
server slice from 16:31Z on).

- The poll is answered for the first time: `socket.config_polled
  acknowledged=153 configs=153 served=5 uncached=85 unreadable=0`. Every earlier
  run in this log logged `served=0`.
- The push carries the whole set in a single batch: `socket.config_pushed
  attachments=8 batch=0 batches=1 documents=6 names=6`.
- The client stores all five: `[ConfigSystem.OnWebS_ocketCallback] save incoming
  config: Boosters | ConfrontationEvent | ManualOffers | TopUp | TrophyRoad`,
  each value the hex of `{"configId":"<Name>"}`. Hash, body and cache write work
  end to end, so the delivery gap of run 56 is closed.
- The consumers still refuse the payload, once each, at construction:
  `[Boosters] ---- _config == null` (twice), `[TopUp]`, `[ConfrontationEvent]`,
  `[ManualOffers]`, and `[TrophyRoad] ---- parse error, message below`. Nothing
  repeats after 19:33:20, so this is a one-shot complaint, not a retry loop. The
  envelope is accepted and the members each consumer reads are what is missing,
  exactly as the device dumps hinted: `BalanceRewards` carries `Rewards`,
  `BalanceLeveling` carries `LevellingV2`.
- The loading screen does not park on the switcher: `InitializeSwitcher finished
  on its own at state=45 (1 step(s) skipped)`, `LoadMainMenu tail completed
  (state 0 -> 2, result=1)`, then `AuthSceneController.Awake` and `state dispatch
  2 (Authorized)`. Menu traffic follows (`get_binded_ids`, `check_binded_gc`,
  `init_progress`, `get_progress_slots`, `create_slots`, `update_progress`,
  `refund_get`, `get_player_version`, `lobby_likes_v2`) and settles into the
  30-second `get_inapps` heartbeat, still ticking four minutes in at ~28 fps.
- Honest comparison with the 90% session 8371249689a298609ad42a35 (15:34Z): that
  one reached the same menu events with `served=0` and `documents=1`, so menu
  traffic alone does not prove the stop is gone. What changed is measurable on
  the config channel only; the visible bar needs an eye on the device.
- Still open: `config 'ClanCommon' is null or empty` fires at 19:32:33 from the
  embedded set of 75, before the push arrives. Two late polls each ask for one
  uncached name (`configs=4 served=0 uncached=1`, `configs=2 served=0
  uncached=1`).
- Transient and self-healing: `23.1.3-stall: blocked -- InitializeSwitcher
  .MoveNext entered at state=40 has not returned for 5849 ms`; the state machine
  resumed and finished at 45.

- [ ] Read the members the five consumers expect and fill the seed documents;
      the store reads the files per request, so no rebuild is needed
- [ ] Decide whether `ClanCommon` deserves a hosted document
- [ ] Watch the device screen during the next run to see where the bar stands

## Run 59: the documents are stored and still unread

Device 001691527000747, backend pid 140701 (boot 2026-08-29T18:11:52Z), socket
session 26be541025b22309f215f440, 234 s alive. Logs: `log_front.txt` (28054
lines of logcat, captured live) and the server slice from 18:11Z on.

Between run 57 and this one six undocumented device runs happened (17:24Z
through 17:55Z). Two of them carried an experiment that answered the poll with
the document bodies attached and suppressed the ordinary reply, so the 153-name
list never reached the client; the 17:51Z session never got past `subscribe`
and `get_inapps`. The experiment was reverted before this run and only the
filled seed documents were kept, so exactly one variable moved.

- The five documents now carry members instead of `{"configId":"<Name>"}`, and
  the client accepts every one of them:
  `cfgsys: #1 store id=167 bytes=156 ok=1 error=''`, then id 7 (641 B), 171
  (262 B), 111 (43 B), 169 (231 B), 100 (163 B). Six stores, no error, no
  `config.document_unparsable`.
- **The store and the read are two different places.** Three milliseconds after
  `store id=167 ok=1`, the consumer asks for the same id and gets nothing:
  `cfgsys: get-text id=167 null=0 chars=0 head=''`, and then
  `[Boosters] ---- _config == null`. The same pair repeats for 169 (TopUp), 171
  (ConfrontationEvent), 111 (ManualOffers) and 100 (TrophyRoad, which reports a
  parse error over the empty text). The write succeeds; the read never sees it.
- The numbers say why the read is empty: the embedded set holds 80 entries
  (`embedded set count@0x20=80`) while the name map holds 163. The five ids are
  in the name map, so the push resolves the name and the store writes, but they
  are not in the embedded set the getter serves. Filling the bodies therefore
  cannot move the boot: no body, however complete, is ever read.
- Consequence for run 56's conclusion: hosting the five documents was necessary
  but is not the 90 % stop. The stop is the getter, not the content.
- Everything else held. The poll is answered in full
  (`acknowledged=153 configs=153 served=0`, served drops to zero only because
  the client already caches these hashes), the push lands in one batch
  (`attachments=8 documents=6 names=6`), `init_progress` answers 25 members,
  `get_progress_slots` is acknowledged without a `slots` member and the client
  uploads its own: `progress_slots_stored received=63 stored=63`. The bar walks
  to `bar=0.750`, `InitializeSwitcher` finishes at state 45, `LoadMainMenu` tail
  completes and `AuthSceneController` dispatches state 2 (Authorized).
- `config 'ClanCommon' is null or empty` still fires at 21:13:32, before the
  push, from the embedded set.

- [x] Fill the seed documents with members; delivery and storage confirmed
- [x] Revert the reply-path experiment; the run-57 delivery path stands
- [ ] Find the getter behind `get-text` (pc `libil2cpp.so+0x249803c`) and the
      store behind `0x24a27cc`, and learn what makes a stored id readable:
      a version bump in the poll answer, a reload call, or an entry the
      embedded set of 80 is missing
- [ ] Only then revisit the member names; the bodies are untested until a
      consumer can read them
- [ ] Decide whether `ClanCommon` deserves a hosted document

## Runs 60-62: the push is the only channel the client reads

Three runs asked one question each, and the third answered it by taking the
working behaviour away.

**Run 60, no server change, cold start.** The documents run 59 wrote into the
client's own storage are not read back on the next boot: `get-text` answers
`chars=0` for 167, 100, 169, 171, 111 and 7, the same consumers log
`_config == null`, and the bar stops where it stopped before. Storage survives
the restart; whatever reads it does not consult it for these ids.

**Run 61, `OPG3D_CONFIG_ALWAYS_SERVE=true`.** The poll answer carried all six
bodies even though the client said it already held those hashes
(`config_polled configs=153 served=6`). Nothing about the client changed: six
`store id=... ok=1` from the push as before, `apply` still never called, the
same `_config == null`, the same stop.

Two readings came out of that log that are worth more than the experiment:

- `get-text` is not broken. It answers with real content for ids the client
  ships with - 153, 99, 75, 43, 44 - and answers empty only for the six this
  server hands over. The read path works; our documents are not in the place it
  reads.
- After the big poll the client polls again in small groups it did not ask for
  at boot: `BlackMarket_1, GameExtendedRewards, GameModeEvents, TaskBook`, then
  `ClanChests, Gatcha`, then `AdsGun, OffersModules, UpdateBanner`. Four of them
  are polled with an empty hash, so the client holds no copy of those at all.
  This server hosts none of them.

`apply` was also taken off the table by the image itself rather than by a run:
`tools/find_callers.py` puts its ten call sites (RVA `0x24A1D80`) inside four
consumer classes, all in the same method name, not in the delivery path. It is
a setter consumers call, not the door a document comes in through.

**Run 62, `OPG3D_CONFIG_PUSH=false`, the control that cost the run.** With the
push withheld and only the poll answer carrying bodies, the client fell back to
the old stop: the loading screen returned to 79%, no `parse`, no `store`, no
consumer complaints at all because the run never reaches the consumers, and the
session degenerates into `get_inapps` heartbeats. Restoring the push and
relaunching (run 63) brought back six `store id=... ok=1` and the 90% state.

So the two delivery paths are now separated by measurement:

- `config_info_versions` (the push, bodies as attachments) is the channel the
  client actually consumes. It is what carries the run from 79% to 90%.
- The poll answer's `c` body is inert. Serving it changes nothing, and its
  absence changes nothing.

That also settles a question run 59 left open: the pushed slot document is not
written and forgotten - it is read and acted on, because withholding it moves
the bar back. The five documents whose consumers still log `_config == null`
differ from it in one measurable way: `CommandsWithFullSlots` has an entry in
the embedded set of 80, and Boosters, TopUp, TrophyRoad, ManualOffers and
ConfrontationEvent do not.

The two switches used here are kept, both defaulting to today's behaviour:
`OPG3D_CONFIG_ALWAYS_SERVE` (off) and `OPG3D_CONFIG_PUSH` (on). A control that
can be run without a rebuild is worth the two fields.

- [x] Rule out the poll answer as a delivery path (run 61, run 62)
- [x] Rule out `apply` as the ingestion point (ten call sites, all consumers)
- [x] Establish that the push is read, not just stored (run 62 regression)
- [ ] Trace what a consumer calls when it reports `_config == null`: the
      accessor is not `get-text`, since the slot document is used while its
      `get-text` is empty too
- [ ] Test whether an id absent from the embedded set of 80 can ever become
      readable, or whether the client needs it named somewhere first
- [ ] Consider hosting the late-polled names the client has no copy of
      (`GameModeEvents`, `ClanChests`, `AdsGun`, `OffersModules`)

## Runs 63-64: the consumer calls get-text, and get-text reads the embedded set

Run 63 was the restoration check after run 62 took the push away: the push is
back, six `store id=... ok=1` are back, and the client is back where it was.
The log of that run answered two questions that no server change could.

**The client remembers what it stored.** Its boot poll now carries this
server's own hashes back: `Boosters c5078ace838a9a63`, `TopUp
5181351cf6887c00`, `TrophyRoad dfe85e46b697d7e3`, `ManualOffers
d408b9c50beae45a`, `ConfrontationEvent b2cce38eae46fa54`,
`CommandsWithFullSlots 574622668ac01548`. Storage is written, survives a
restart, and is read back into the version list the client polls with. The
documents are not lost; the client considers them current.

**The consumer calls `get-text`, and gets nothing.** Every
`[Boosters] ---- _config == null` is immediately preceded by
`get-text id=167 null=0 chars=0`, and every `get-text` is preceded by
`embedded set count@0x20=80`. So the accessor is not in doubt any more: the
consumer asks the config system for the text of its ConfigId, the config system
looks in the set of 80 documents baked into the build, and answers with an
empty string for the six ids this server hosts. Storage and that set are two
different places, and only the second one is read.

The request coroutine is alive and does finish: `request step #1 state 0 -> 2`
through `#274 state 2 -> -1 more=0`, waiting on `WaitUntil` between groups. It
is not stalled waiting for a document.

**Run 64 asked whether a missing document is the problem.** Of the 153 names
the client polls, 80 come with an empty hash - it holds no copy of them at all,
`ClanCommon` among them, and the small late polls add `GameModeEvents`,
`ClanChests`, `AdsGun` and `OffersModules`. Five of those were given minimal
documents and hosted. The delivery worked exactly as before - eleven
`store id=... ok=1` including `id=69 bytes=25` for `ClanCommon` - and nothing
else moved: the same four complaints, `get-text id=167` still empty, the same
stop. `config 'ClanCommon' is null or empty` still fires, because it fires
before the push arrives and reads the same embedded set.

So hosting more names is not the missing piece, and neither is delivery,
storage, the poll answer, nor `apply`. What is missing is the step that puts a
stored document into the dictionary `get-text` reads. Nothing on the wire can
be changed to find it: the next move is client-side, tracing the read at
`libil2cpp.so+0x2606608` (`0x24981B0`) to see which field it consults, and the
storage class at `dump2313.cs:448789` to see who ever loads from it.

- [x] Confirm the stored documents survive a restart (the boot poll carries
      this server's hashes)
- [x] Identify the accessor behind `_config == null` (`get-text`, per-consumer,
      immediately before each complaint)
- [x] Rule out missing documents as the cause (run 64, five more hosted)
- [ ] Trace `get-text` internals and the storage reader in a new libopg3d, and
      find the loader that fills the readable dictionary
- [ ] Keep the five stub documents unstaged until a device run shows a change

## Runs 65-68: the client ingests every body in full, and the reader still sees nothing

Four runs closed the delivery question. The client does not merely receive the
documents this server pushes: it parses them, re-encodes them with its keys in
alphabetical order, and saves the result. Run 67 printed the saved bytes for all
eleven names, and decoding them returns this server's own documents, byte count
intact:

	{"bs":{"bp":{},"id":"none",...},"configId":"Boosters","e":false,...}   156 bytes
	{"cg":0,"configId":"TopUp","cu":0,"d":"00:00:00",...}                  231 bytes
	{"configId":"TrophyRoad","crs":{"irs":{},"its":[],...},...}            163 bytes

So the wire is not the problem, the encoding is not the problem, and the bodies
are not the problem. Every read of those names still answers with an empty
string, and every consumer still says `_config == null`.

What each run ruled out:

* Run 65 salted the fingerprint (`OPG3D_CONFIG_HASH_NONCE`) so that no document
  could ever state a fingerprint the client already holds. The comparison the
  handler makes is real, but losing it changes nothing: the same eleven bodies
  were stored, no entry gained text, and the reader kept answering `chars=0`.
* Run 65 also settled what run 64's five insertions were worth. The reader's
  dictionary booted at 85 entries instead of 80, so names this server introduces
  do survive a restart - as entries without text. The five new names joined the
  six old ones in the same dead state rather than escaping it.
* Run 66 combined the salt with `OPG3D_CONFIG_ALWAYS_SERVE`, so the poll answer
  carried every body as well and the client could not consider its copy current.
  No change.
* Run 67 widened the vocabulary of each push record (`OPG3D_CONFIG_PUSH_ALIASES`)
  to state the body index and fingerprint under `h`, `c` and `v` beside `val`
  and `hash`, in case the reader takes the text from a key the handler never
  writes. No change, and the handler's own `save incoming config` line appeared
  exactly twelve times, as it has since run 64.
* Run 68 replaced the hard kill with a home press and a six second pause before
  the restart, in case the texts were lost to a flush that never ran. The
  dictionary booted at 85 again and every read stayed empty.

The boundary is now exact. The push handler owns two destinations: the storage
it writes with `store id=<n> ok=1`, and the reader's dictionary, where it only
ever leaves a fingerprint. The text a consumer reads is written by neither. The
eighty names that answer with a body were populated before any of this server's
traffic arrived, which is why they answer and ours never will, no matter what
this server sends or how it spells it.

That makes the next question a client-side measurement rather than another
guess on the wire: which key the reader looks up inside an entry, and which
writer fills it. The trace to add sits at the two places already located, the
parse-side insert at 0x24A2B44 and the reader's inner lookup in 0x24A0E10, and
it needs to print the keys of the inner dictionary rather than its count. The
fix stays here afterwards - the trace only names the field this server has to
make the handler write.

The two flags this batch added stay off by default. They cost nothing when
unset and they are the controls that proved the fingerprint and the record
vocabulary are not the barrier.
