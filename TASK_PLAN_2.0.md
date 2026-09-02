# TASK_PLAN 2.0 - Open Pixel Gun 3D Backend

Status keys: `[ ]` pending / `[/]` in progress / `[x]` completed

`TASK_PLAN.md` is the historical record of every run up to the first complete
boot and stays untouched. This file continues from that point: the client now
reaches the lobby, so the work is no longer "make it load" but "make what it
loads correct".

## Session 2026-09-01 - the boot completes, the progress edit does not

### Baseline

- Client 23.1.3, `app_version` `1:23.1.3`, namespace `/sio`, engine.io v4,
  websocket transport, codec `cfb128` keyed from the sid.
- Device 001691527000747 (A059P), package `com.pixel.gun3d`, adb root available.
- Log of record: `logs/backend-2026-09-01.log`, 15:22:34Z to 15:28:04Z.
- Two sessions: `106e3dd7d7f5d6523977ae17` died at 15:22:48 on a write failure,
  `9c6ca94c479e3259c1b47393` ran to steady state.
- Player 541028684.

### What this run proves is already correct

- [x] Every event the client sends has a dedicated handler.
      `socket.event_generic_ack` appears **0** times across 82 answered events,
      so nothing fell through to the fallback acknowledgement. There are no
      unhandled-event placeholders left.
- [x] 24 distinct events answered: subscribe (27), get_inapps (11),
      update_progress (7), unsubscribe (5), update_player_online (3),
      update_player (2), progress_status (2), player_prefs (2), mmc_get_room (2),
      get_progress_slots (2), get_last_command (2), full_check_version (2),
      check_binded_gc (2), auth_v2 (2), and one each of replace_progress,
      refund_get, player_success_init, get_time, get_map_popularity,
      get_leader_clan, get_leaderboard_v3, get_info_by_id, get_binded_ids,
      check_binded_id.
- [x] Config delivery is healthy. Every poll reports `unreadable=0` and no
      `config.document_unreadable` was emitted: 153 names acknowledged, 82
      uncached, 3 documents pushed.
- [x] Stale-evidence hazard removed. The config faults previously chased
      (`config ClanCommon is null or empty`, `[Boosters] ---- _config == null`,
      `[TopUp]`, `[ManualOffers]`, `[ConfrontationEvent]`,
      `[TrophyRoad] ---- parse error`) all came from `log_front.txt`, whose
      16137 lines are **all** stamped `08-29 22:37:11` to `22:39:40` - three
      days stale and predating the config rework. The file was deleted so it
      cannot be mistaken for current evidence again.

### The defect: an operator progress edit never reaches the game

Measured on player 541028684. `data/state.json` held two contradictory truths:

| Source | level | experience | coins | gems |
|---|---|---|---|---|
| flat scalars | 1 | 0 | 0 | 0 |
| `progress` map | 32 | 20000 | 123123123 | 123123123 |
| `progress_document["4"]` | 32 (`l`) | 20000 (`e`) | - | - |
| `progress_document["3"]` | - | - | **absent** | **absent** |
| `progress_slots["4"]` | 1 (`l`) | 0 (`e`) | - | - |
| `progress_slots["3"]` | - | - | **`{}`** | **`{}`** |

The chain, in order:

1. `SetProfile` wrote the edit to the scalars, to `Progress`, and to slot 4 of
   `ProgressDocument` (`l`, `e`) - but **never to slot 3**, where `Coins` and
   `GemsCurrency` live. The client applies the document slot by slot, so level
   32 and experience 20000 arrived and the two currencies had no channel at all.
2. `progress_status` answered with the raw document *and* four flat counters
   read from the scalars, so one reply carried both level 32 and level 1, both
   123123123 coins and 0 coins.
3. The client adopted the flat zeros and uploaded them back: `replace_progress`
   arrived carrying slot 3 `{}` and slot 4 `{"l":1,"e":0}`.
4. `projectReplacementSlotState` treats an empty slot 3 as the client zero
   encoding, so it zeroed `Coins` and `Gems` and set `Level` to 1 - producing
   exactly the scalars found on disk. The edit was erased instead of applied.

- [x] Fix 1 - `SetProfile` now routes the edit through `syncClientFacingSlots`,
      which writes slot 4 (`l`, `e`) **and slot 3** (`Coins`, `GemsCurrency`)
      into both `ProgressDocument` and `ProgressSlots`, creating absent slots
      and preserving unknown members. Both documents are written because they
      travel on different events - `init_progress` carries the document and
      `get_progress_slots` carries the slots - and a disagreement between them
      lets whichever the client trusts last undo the edit.
- [x] Fix 2 - `progress_status` can no longer contradict itself. New
      `store.Profile.Counters()` reconciles the four flat counters against the
      raw `progress` document travelling in the same reply: the document wins
      wherever it carries a key, the projections remain the fallback.
- [x] Verified on device, 15:51-15:55. Fix 2 confirmed on the wire; Fix 1
      confirmed in storage but **not** confirmed end to end, because its
      delivery channel is not exercised on this boot path. See the run below.

### `update_progress` is acknowledged but never recorded

`answerUpdateProgress` echoes `err_code`, `i` and `ci` so the stock progress
manager can retire its pending command, and that is all it does: there is no
`MergeProfile*` call anywhere in `socketio_update_progress.go`. Seven batches
arrived in this session and none left a trace in the store.

The payload is a command stream, not a state upload:

    {"id":63,"i":"69ed2032","p":{"c":[{"id":74,"ci":"1eea10a4",
     "p":{"1":"Coins","2":"Gems"},"h":{"21":"0a04...685d"},
     "v":"23.1.3","u":[274]}]}}

`id` names a command, `p` carries its parameter slots, `h` the md5 of the
resulting client slot, `u` an unresolved list. Applying it server-side means
reimplementing the game economy, so it is deliberately **not** guessed at here.

- [ ] Decide the ownership model: either the client stays authoritative and
      `replace_progress` remains the only write path, in which case record the
      batches for audit only, or command ids 63/74/106 get measured and applied.
- [ ] Until that is decided, log the batch contents so a lost delta is visible
      in the log instead of silent.

### Open, lower priority

- [ ] `socket.ws_write_failed err="websocket closed by peer"` on
      `106e3dd7d7f5d6523977ae17` at 15:22:48, immediately after a `subscribe`
      answer. The client reconnected 6 s later and finished normally, so this
      looks like a benign reconnect race - confirm no reply was lost.
- [ ] `get_inapps` is polled every 30 s (12 times, req_id 50-60, 15:23:27
      through 15:27:58). The handler note claims the three-key answer stopped
      the re-ask; it did not. Establish whether a 30 s poll is simply what the
      client does.
- [ ] Thin-but-dedicated handlers, to be filled only where the reference or a
      device run supplies a real shape - never invented:
  - [ ] `answerRefundGet` ignores the request head and returns six empty lists.
  - [ ] `answerLeaderClan` hardcodes `leader_id "0"` and an empty `leader`.
  - [ ] `answerMapPopularity` echoes the params and returns `popularity {}`.
  - [ ] `answerCheckBindedID` hardcodes `binded 0`, `is_binded 0`,
        `allow_bind 1`.
  - [ ] `answerLeaderboardV3` returns a single self-only entry.
  - [ ] `subscribe` topics are acknowledged but never pushed: `friends_list`,
        `get_gifts`, `squad_invites`, `friend_invites`,
        `get_invites_from_clans`, `mailbox_updated_messages`,
        `mailbox_udpated_me_messages` (the client typo, preserved verbatim),
        `lobby_likes_v2`, `mm_status`, `change_clan_state`,
        `player_like_listener`, `get_player_info_subscription`, `player_online`,
        `all_short_info_by_id_v3`.
- [ ] Cross-read PGWSD `TestScript55.py` (2144 lines, v23.3.2) for the authentic
      shapes of the above before writing any of them.

### Method notes

- `PGWSD` is a read-only reference (v23.3.2, four months from 23.1.3) and is
  never executed. `PG-1200` is off limits.
- `OLD-PIXEL-GUN-3D-ONLINE-PROJECT` supplies logs and traces only.
- A frontend claim needs a logcat stamped the same day as the backend log. The
  08-29 capture already produced one wrong diagnosis in this session; check
  `stat` on any log before drawing a conclusion from it.

## Device run 2026-09-01 15:51-15:55 - what the fixes proved and what they did not

Rebuilt binary, backend restarted 15:51:40, operator edit re-applied through
`PUT /api/v1/admin/players/541028684/profile`, game force-stopped and relaunched,
fresh logcat captured to `log_front.txt` (8966 lines, all stamped 09-01 18:52-18:54).

### Confirmed fixed

- [x] **Fix 2 verified on the wire.** `progress_status` req_id 3 answered:

        {"coins":123123123,"enabled":2,"error":0,"experience":20000,
         "gems":123123123,"level":32,
         "progress":{"coins":123123123,"experience":20000,
                     "gems":123123123,"level":32},
         "req_id":3,"result":"ok"}

      The flat counters and the document now agree. Before the fix the same
      reply carried level 1 with 0 coins beside level 32 with 123123123.
- [x] **Fix 1 verified in storage.** The edit produced
      `progress_document["3"] = {"Coins":123123123,"GemsCurrency":123123123}`
      and the same in `progress_slots["3"]`, where slot 3 had previously been
      `{}`. `progress_document["3"]` still holds those values after the run.

### Not fixed - the client never receives the values

`123123123` appears **0 times** in the 8966-line client log. The edit did not
reach the game.

Event order of the boot, all inside two seconds:

| time | event | req_id | outcome |
|---|---|---|---|
| 15:53:03.486 | `progress_status` | 3 | server sent the correct values |
| 15:53:04.792 | `get_progress_slots` | 18 | server answered a **list of slot ids**, not slot data |
| 15:53:04.854 | `replace_progress` | 19 | client uploaded 87 slots, `coins=0 experience=0 gems=0 level=1` |

`socket.progress_replaced` then reset the scalars to 1/0/0/0,
`progress_slots["3"]` back to `{}` and `progress_slots["4"]` back to
`{"l":1,"e":0}`. `progress_document["3"]` survived only because
`replace_progress` does not write it.

Two findings make this decisive:

1. **`init_progress` never happens.** It appears **0** times across both of
      today's sessions, and `socket.progress_initialised` likewise. The channel
      that carries `ProgressDocument` - and therefore all of Fix 1 - is not part
      of this boot path at all. Fix 1 is correct but currently undelivered.
2. **The client is authoritative on boot.** 62 ms after being handed the slot
      list it uploaded its own blank local snapshot, and the server accepted it.
      No server-side write can survive that, so no operator edit can ever stick.

The client did run its applier during this window - `>>> hash`,
`slot GameModeHub(75) strHash: cd20697`, and
`Progress.<applier>:MoveNext()` frames at 18:53:03.947 - so it applied
*something*, just not currencies it was never sent.

### The decision this now forces

`projectReplacementSlotState` zeroes the counters whenever the uploaded core
slots are blank, and `TestReplaceProfileProgressSlotsTreatsEmptyCoreSlotsAsZeroState`
pins that behaviour deliberately: an empty slot 3 is documented as the client's
zero encoding. The device run shows the same rule destroying an operator edit.
Both readings are defensible, so this is an ownership decision and is **not**
changed unilaterally:

- [ ] **Option A - guard the blank upload.** Refuse to zero the counters when
      the uploaded core slot is keyless (`{}`) while the server holds non-zero
      values; honour any explicit member, including an explicit `{"Coins":0}`.
      Protects currencies only: slot 4 arrives as `{"l":1,"e":0}`, which is an
      explicit blank rather than an absent one, so level and experience need a
      separate rule. Breaks the test above, which would have to be rewritten.
- [ ] **Option B - make the server authoritative on boot.** Push the stored
      document on a channel the client actually reads and have it adopt the
      values before it uploads. Needs the `enabled` semantics measured first:
      this server answers `enabled: 2` (`sioProgressSynchronize`) whenever it
      holds any state, and the client responded by uploading rather than
      downloading. Worth testing whether `enabled: 1` (`sioProgressOpen`)
      changes that, and whether anything can still trigger `init_progress`.
- [ ] **Option C - accept client authority** and treat operator edits as
      advisory, applied only through the command stream the client itself
      drives. Would make the admin profile editor honest about its limits.

Until this is decided the admin profile editor cannot durably change progress,
and that limitation should be stated in the console rather than left implicit.

### Corrected from the earlier session

- [x] The config faults are **current after all**, not stale. The fresh
      09-01 logcat still shows `[Boosters] ---- _config == null` (x2),
      `[ConfrontationEvent]`, `[ManualOffers]`, `[TopUp]` and
      `config 'ClanCommon' is null or empty`. The earlier conclusion that these
      were purely an artefact of the 08-29 capture was wrong: the same five
      names are exactly the ones parked in `content/sio-configs-parked/`.
- [ ] Promote the parked configs (`Boosters`, `ConfrontationEvent`,
      `ManualOffers`, `TopUp`, `ClanCommon`) from
      `content/sio-configs-parked/` into `content/sio-configs/`, then re-run and
      confirm the five complaints disappear. Note `Boosters.json` exists in both
      directories, so the served copy is the one the client is rejecting.
- [ ] 86 `Exception` lines in the fresh client log, but the visible ones are all
      platform noise (Play Games, APN, launcher, Phenotype). Re-check after the
      configs land.

## Device measurement 2026-09-01 16:15-16:20 UTC - the `enabled` code, both arms

The previous section left the ownership question open as options A/B/C. Option B
was chosen, and it asked for a measurement rather than a decision, so the
escalation is now a flag (`OPG3D_PROGRESS_SYNCHRONIZE_MODE`, on by default) and
both arms were run on the device against player 541028684 with the same operator
edit re-applied before each boot.

One reading was wrong going in and is recorded so it is not repeated: `enabled`
is **not** an `AuthSceneState` value. `AuthSceneState.SynchronizeProgress` is 9
(`dump2313.cs:92853`) while the server sends 2, so the two are unrelated. The
handler at `libil2cpp.so+0x17A9980` splits the integer into three statics and the
outer auth iterator reads the byte `(value == 2)` at `0x3DC78D4`. The mapping
already in the code is correct and was left alone.

### The two arms

| | `enabled: 2` (default) | `enabled: 1` (flag off) |
|---|---|---|
| Boot | completes, 45 answered events, full menu traffic | stalls after `get_inapps`, 34 events, socket re-auths after 90 s |
| `get_progress_slots` | asked; 87 stored slot ids served | never asked |
| `replace_progress` | client uploads its blank snapshot | never sent |
| Operator edit | erased 62 ms after being answered | survives; `updated_at` still the admin write |
| `init_progress` | never emitted | never emitted |
| `123123123` in client log | 0 occurrences | 0 occurrences |

Arm 2 event tail: `auth_v2`, 8 subscribes, `progress_status`, `player_prefs`,
`get_last_command`, `get_time`, `full_check_version`, `mmc_get_room`,
`update_player_online`, `get_binded_ids`, `check_binded_gc`, then `get_inapps`
every 30 s and a second `auth_v2` at 16:17:54. Absent: the whole slot handshake,
`player_success_init`, `get_info_by_id`, `update_player`, `refund_get`,
`unsubscribe`, `check_binded_id` and all five `update_progress` batches.

### What this settles

The server cannot take ownership of boot progress by choosing a different
`enabled` code. Code 2 buys a completed boot at the price of the client
overwriting the server; code 1 protects the server's copy at the price of the
boot never finishing. Neither arm opens a download channel, because neither
arm emits `init_progress`.

The missing piece is now named: the 23.3.2 reference protocol has a **download**
event this backend does not implement at all -
`["get_progress", {"player_id": "...", "slots": [...]}]`
(`PGWSD/.../TestScript55.py:1237`, beside `get_progress_hashes_v2` at `:639`).
`sioAnswerFor` has no case for either name, so if the client ever asked, it would
receive the generic acknowledgement. That is the channel the stock
SynchronizeProgress continuation is documented to use, and it is the only route
by which a server-held value can reach the client without the client uploading
first.

- [x] Measured option B on the device, both arms. Recorded above.
- [ ] Implement `get_progress` (and `get_progress_hashes_v2`) so the server has a
      download channel at all, then re-run arm 1 and check whether the
      continuation asks for it once an answer exists.
- [ ] Only then revisit option A. Guarding the blank upload while there is no
      download channel would keep the edit in storage and still never show it.
- [ ] Leave `OPG3D_PROGRESS_SYNCHRONIZE_MODE` on. Off is a diagnostic arm, not a
      configuration: it does not finish the boot.

### Corrected again: the config fault is delivery-accepted, not missing

Arm 2 caught the config complaint in the act, which retires the "promote the
parked files" theory for at least one name. The push arrives and the client
saves it:

```
[ConfigSystem.OnWebS_ocketCallback] save incoming config: Boosters with value 7B-22-62-65-73-...
```

Hex-decoded that body is
`{"bes":{},"bs":[],"configId":"Boosters","e":false,"ed":"2027-01-01T00:00:00","gm":[],"l":1,"ms":{},"s":1,"sd":"2026-01-01T00:00:00"}`,
and two milliseconds later the same thread logs `[Boosters] ---- _config == null`
from `Boosters.<init>` inside the same callback stack. So the file is served, the
body is accepted and stored, and the feature's own initialiser still ends up with
a null config. `"e":false` is the first thing to test as the cause.

- [ ] Test `Boosters` with `"e":true` and a non-empty `bs`, and re-check whether
      `_config == null` disappears. Do this before moving any parked file.

## Run 49: the operator edit survives a full boot

The previous section ended with a measurement and no fix. On the working
`enabled: 2` arm the client erased the edit 62 ms after the slot handshake; on
the `enabled: 1` arm the edit survived only because the boot never finished.
Neither arm is a working product. Both halves are addressed now.

### What changed

**The blank-upload guard** (`internal/api/socketio_replace_progress.go`,
`4362f78`..`5ce41be`). `replace_progress` now compares the two core slots the
store projects - slot 3 for the currencies, slot 4 for level and experience -
against the stored profile before installing the snapshot. A slot 3 that
projects to no currency at all and a slot 4 that projects to level one with no
experience are refilled from what the server holds. Every other slot in the
upload is installed exactly as it arrived, and the repaired ids are named in the
`socket.progress_replaced` line so the log says when the guard acted.

The guard sits in the API layer on purpose. `projectReplacementSlotState` is
right to zero what it was handed; the bug is that the client should never have
been the authority on those four numbers at that moment. Moving the zeroing into
the store would have broken
`TestReplaceProfileProgressSlotsTreatsEmptyCoreSlotsAsZeroState`, which pins
behaviour worth keeping.

`OPG3D_PROGRESS_BLANK_UPLOAD_GUARD` (default on) turns it off. The trade-off is
narrow and deliberate: with the guard on, a genuine client-side reset to zero
from a non-blank profile loses to the stored value. The server wins the four
counters an operator can edit; the client keeps everything else.

**The download channel** (`internal/api/socketio_get_progress.go`). `get_progress`
and `get_progress_hashes_v2` both fell through to the generic acknowledgement.
The first now serves the stored slot document, honours the optional numeric slot
filter, checks the claimed `player_id` against the authenticated one, and always
answers a dictionary under `progress` because the client casts that member
unchecked. The second is logged under its own name and still answered bare: the
digest form is unmeasured, and an invented cast there is exactly what killed the
boot coroutine in run 48.

### Measured on device, run 49

| | run 48 (before) | run 49 (after) |
|---|---|---|
| `progress_status` | `enabled: 2`, counters correct | unchanged |
| `get_progress_slots` | 87 ids served | 87 ids served |
| `replace_progress` | blank slot 3 and 4 accepted | `repaired_slots="[3 4]"` |
| stored coins after boot | `0` | `123123123` |
| stored level after boot | `1` | `32` |
| stored slot 3 | `{}` | `{"Coins":123123123,"GemsCurrency":123123123}` |
| stored slot 4 | `{"l":1,"e":0}` | `{"cr":1,"cre":0,"e":20000,"l":32}` |
| boot | completes, 45 events | completes, 44 events |

```
16:34:55.336Z socket.progress_slots_asked held=87 served=87 player_id=541028684
16:34:55.457Z socket.progress_replaced coins=123123123 level=32 received=87
              repaired_slots="[3 4]" stored=87
```

The slot document carries the repaired values too, not just the flat counters,
so the next handshake serves the corrected snapshot rather than re-teaching the
client its own blank.

### What this does not fix

The client still does not display the edited balance. `123123123` appears zero
times in the 8015-line client log for run 49. The download handler was never
asked: no `socket.progress_download_served` line exists for this boot, because
the client still parks before the `SynchronizeProgress` continuation that emits
`get_progress`. The handler is necessary and, on today's evidence, not yet
sufficient.

So the honest split is: **the edit is now durable server-side** - it survives a
full boot instead of being erased by one - **and the client-facing display is
still open.** The remaining lever is the continuation itself, not the value.

- [ ] Catch the client emitting `get_progress` and measure what it does with the
      answer. Until then the download channel is inert.
- [ ] Trace why `SynchronizeProgress` (state 9) is never reached even though
      `statics+0x02` is set by `enabled: 2`. Frontend trace switches
      (`boot progress applier trace`, `slot document reader trace`) are the
      cheapest next probe; hot-pushing the lib is authorised.
- [ ] Decide the `update_progress` ownership model. The guard covers the boot
      snapshot only; an in-session batch can still overwrite the same counters.

## Runs 59-66 (2026-09-02) - backend-only authoritative progress delivery completed

This section supersedes the earlier conclusion that the stock 23.1.3 client has
no usable server-to-client progress route. The route exists, but it is gated by
a nullable local cursor and must be entered through the exact auth state
machine. No gameplay-affecting frontend change is part of the result. The
frontend library used for these runs contains passive logging only.

### Acceptance contract

The operator edit used for the final measurement stayed at revision 2:

```text
player       541028684
nick         Удачливый Снайпер
level        32
experience   470
coins        123123123
gems         123123124
```

Completion required all of the following, not merely the absence of the chooser:

- [x] stock 23.1.3 downloads server-owned slots;
- [x] the correlated callback hands a non-null 87-slot dictionary to the stock
      applier;
- [x] AuthScene reaches Login without `ChooseProgress` (state 7);
- [x] the lobby visibly renders 123123123 coins and 123123124 gems;
- [x] a later stock upload echoes raw slot 3 and slot 4 values exactly;
- [x] the server writes a per-device acknowledgement only after that raw echo
      and `player_success_init`;
- [x] an unchanged app restart does not re-arm or re-download the revision;
- [x] the same remains true after the backend itself reopens `data/state.json`.

### Corrected `enabled` routing

The final routing is deliberately revision-aware:

```text
maintenance
  -> enabled=0 (TechnicalWorks)

pending operator revision, first phase
  -> enabled=1
  -> get_last_command returns non-null index="" and device_id=""
  -> state 8 completes
  -> state 9 sends correlated sync_full
  -> authoritative slots are applied
  -> Login stages a raw-echo boot

same pending revision, delivery staged in this backend process
  -> enabled=2
  -> state 13 uploads replace_progress
  -> exact raw slot-3/slot-4 match becomes acknowledgement evidence
  -> player_success_init persists the per-device revision acknowledgement

no pending revision
  -> ordinary ProgressSynchronizeMode behavior
  -> default enabled=2 for a stored profile
```

A process restart before the raw-echo boot deliberately loses only the transient
stage and repeats the safe state-9 download. It cannot manufacture an
acknowledgement. The final acknowledgement remains durable in the profile store.

The broad `OPG3D_PROGRESS_SYNCHRONIZE_MODE` flag now controls only ordinary
stored profiles. It cannot strand an operator revision: phase one forces code 1
and a staged phase two forces code 2. Maintenance code 0 still has priority.

### Static client proof behind the safe cursor

The 23.1.3 `Progress` singleton (`TypeDef 6674`) contains:

```text
+0x18 string index
+0x20 string device_id
```

Relevant code:

```text
0x3DC7584  state-8 predicate: Progress.Singleton.index != null
0x1B43248  cached-command method called after the predicate succeeds
0x3DC5C44  state-8 completion callback
0x3DC59E8  state-9 iterator factory
0x17AB594  sync_full request builder
0x17AB704  correlated sync_full callback
0x2701830  slot-document applier
```

The old response omitted `index`, leaving a null managed reference and parking
state 8 at 83%. An empty string is a real, non-null managed string, satisfies the
predicate, and does not fabricate a foreign revision cursor. `device_id` also
stays empty. A non-empty mismatching device id is the external-profile branch
that opens state 7, so it is forbidden.

The accepted `get_last_command` body is:

```json
{
  "command": "",
  "command_id": 0,
  "device_id": "",
  "error": 0,
  "index": "",
  "req_id": 5,
  "result": "ok"
}
```

The stock parser ignores attempts to use `command` and `command_id` as inbound
progress commands. IDs 10, 63, 64, 65 and 66 belong to the outgoing
`update_progress` command stream; they are not a server-to-client command API.

### Bind-handler contracts retained

The auth path was also checked so the progress experiment would not hide behind
a social-bind failure:

- `get_binded_ids` returns the authenticated id/hash pair only;
- `check_binded_gc` and `check_binded_id` return the measured empty/unbound
  result shapes and preserve correlation;
- none of these handlers selects a profile from a payload-supplied player id;
- the completed runs received all required bind replies without an undelivered
  frame.

### Rejected delivery routes

- `enabled=0`: maintenance, not synchronization.
- `enabled=1` without an `index` key: state 8 waits forever at 83%.
- `enabled=2` before download: state 13 uploads the old local snapshot.
- a non-empty backend `device_id` or fabricated cursor: opens the two-profile
  chooser and is unacceptable.
- unsolicited `sync_full`: reaches only generic handler `0x294E108`; it does not
  invoke callback `0x17AB704` or the slot applier.
- `replace_progress` reply augmented with slots: the client ignores those slots.
- `player_prefs`: cannot write arbitrary Storager progress keys.
- `update_progress`: carries commands/equipment changes, not a full raw echo.
- `update_player`: carries rank and lobby metadata, not currencies and XP.
- frontend preference, branch, return-value or state mutation: prohibited.

### Run chronology

#### Run 59 - local-upload baseline

The enabled-2 path completed but remained client-authoritative. The client sent
its local progress; the server guard preserved operator-owned counters, but the
client did not adopt the changed gems. This established that durable server
storage alone was not delivery.

```text
backend SID   534168421e62fb6a36c6e418
result        lobby, local upload, no server application proof
```

#### Run 60 - unsolicited push rejected

An uncorrelated `sync_full` was decrypted and dispatched only to the generic
subscriber. Callback `0x17AB704` and applier `0x2701830` were absent. Auth used
state 13, sent a local `replace_progress`, and displayed the old gems.

```text
backend SID   86cb438bf0054964ccbeccfc
result        enabled=2, generic handler only, no application
```

This run also supplied the measured raw-upload baseline: slot 3 carried
`Coins=123123123` but old `GemsCurrency=123123123`; slot 4 carried `l=32,e=470`.

#### Run 61 - missing-index blocker isolated

The backend forced code 1 and sent an unsolicited push. The client reached state
8, but the actual `get_last_command` body omitted both `index` and `device_id`.
The predicate at `0x3DC7584` remained false through the bounded trace and the UI
stayed at 83%.

```text
backend SID   ccd3d53668f8fd71c3757d55
frontend PID  13237
result        failed at state 8; index was absent, not ""
```

The earlier SID `dff2b3080e98a28a08174225` belongs to the preceding connection
and is not run-61 evidence.

#### Run 62 - server values physically applied

The first empty-cursor candidate removed the unsolicited push, answered
`enabled=1`, and supplied `index="", device_id=""`.

Measured sequence:

```text
09:40:40.926  state 8 SendCachedCommands
09:40:40.926  predicate 0x3DC7584 -> true on first poll
09:40:40.960  state 9 SynchronizeProgress
09:40:41.150  client emits sync_full
09:40:41.367  callback 0x17AB704 reads slots
09:40:41.367  applier receives Dictionary count=87
09:40:43.973  state 16 Login
09:40:44.541  UI label = 123123124
09:40:44.545  UI label = 123123123
```

Backend evidence:

```text
SID                         df0dc798d0edade8616d95a6
progress_status             enabled=1
pending revision            2
correlated revision served  true
player_success_init         true
raw replace_progress        absent
```

This proved download and visible application. It intentionally did **not** write
an acknowledgement because stock state 9 does not upload a raw snapshot. The
five subsequent `update_progress` requests and `update_player` were inspected;
none contained all four counters.

#### Run 63 - final candidate stages the raw-echo phase

The final two-phase candidate repeated the safe download and staged revision 2
only after Login:

```text
backend PID    57730
backend SID    4ed94b2bba37f15d0459b872
enabled        1
route          state9_download
state path     2 -> 8 -> 9 -> 16
sync slots     87
staged         progress_revision_delivery_staged=true
ack on disk    absent (correct at this phase)
UI             coins=123123123, gems=123123124, level=32
```

#### Run 64 - exact raw echo and durable acknowledgement

Without restarting the backend, the next stock boot consumed the staged phase:

```text
backend SID    22e97925297b60b366679232
enabled        2
route          state13_raw_echo
state path     1 -> 13 -> 16
chooser        absent
```

The client itself emitted the full 87-slot `replace_progress`:

```json
"3": {"d": {"Coins": 123123123, "GemsCurrency": 123123124}},
"4": {"d": {"l": 32, "e": 470, "cr": 1, "cre": 0}}
```

The server compared these values before enforcing authority and logged:

```text
progress_revision_echoed=true
progress_revision_confirmable=2
progress_revision_delivery_reset=false
progress_revision_applied=true
```

The durable profile then contained:

```json
"progress_revision": 2,
"applied_progress_revisions": {
  "92d091e1c165e08c8bc957c3e3a6a6bc": 2
}
```

The lobby again rendered both changed currency values.

#### Run 65 - unchanged app restart does not replay

```text
backend SID                   653dabd550f082cf3cc357dc
progress_revision_armed       0
progress_sync_server_slots    0
AuthScene state 9             0
AuthScene state 7             0
ordinary state path           2 -> 13 -> 16
revision / acknowledgement    2 / 2
```

The ordinary state-13 upload remained exact and did not change counters or slot
contents. No additive duplication occurred.

#### Run 66 - backend restart also does not replay

The backend was stopped only after verifying PID 57730, its executable, cwd and
`:8080` listener. The same candidate reopened the on-disk store as PID 58852.
Before any client connected, revision 2 and its acknowledgement were present.
The clean client boot then measured:

```text
backend SID                   d7460dc494083199be3e333c
progress_revision_armed       0
progress_sync_server_slots    0
AuthScene state 9             0
AuthScene state 7             0
ordinary state path           1 -> 13 -> 16
revision / acknowledgement    2 / 2
UI                            123123123 / 123123124 / level 32
```

This is the durable no-replay proof: both the app and backend were restarted,
and the acknowledged operator revision was not re-applied.

### Backend implementation

Key source behavior:

- `answerProgressStatus` selects the two revision phases without weakening
  maintenance or ordinary profile routing.
- `answerLastCommand` always supplies safe non-null empty strings and arms only
  the authenticated device's pending revision.
- `answerSyncFull` returns authoritative `slots` through the correlated response
  and marks only the exact current revision as served.
- the old unsolicited `sync_full` push and its queue hook are gone.
- `answerReplaceProgress` compares raw slot 3/4 values before server repair.
- a mismatching staged upload clears the transient phase and returns the next
  boot to state 9; repaired server values can never count as client proof.
- `answerPlayerSuccessInit` stages after the download boot and persists the
  acknowledgement only after both delivery and raw-echo evidence.
- final acknowledgements remain per device and operator revision.

Removed/rejected identifiers:

```text
pushPendingProgressRevision
progressSyncPushed
markProgressSyncPushed
confirmPushedProgressEcho
progressCursorDeviceID
progressRevisionCursor
```

Final evidence fields:

```text
progress_revision
progress_sync_served
progress_revision_delivery_staged
progress_revision_echoed
progress_revision_confirmable
progress_revision_delivery_reset
progress_revision_applied
```

### Automated validation

The final source passes:

```bash
go test ./...
go test -race ./internal/api -count=1
go vet ./...
go build ./...
git diff --check
```

The revision tests cover:

- first-phase code 1 and safe empty cursor;
- correlated authoritative `sync_full`;
- Login staging without premature acknowledgement;
- second-phase code 2 even when the broad mode is disabled;
- exact raw echo followed by per-device acknowledgement;
- mismatch reset back to state 9;
- stale-session/newer-revision rejection;
- no acknowledgement from raw values without prior delivery;
- core authority against non-blank client rewrites;
- no re-arming after acknowledgement.

### Reproducibility artifacts

Final backend candidate:

```text
path      bin/opg3d-backend
SHA-256   d5ae17d3e52df2f18ebda32e8ca34c5137b552a2d44c8df38e101f6c889a5bda
runtime   direct process, :8080, no OPG3D_PROGRESS_SYNCHRONIZE_MODE override
```

Passive frontend tracer used only for observation:

```text
installed SHA-256  194a5f274d626111ace6dea12d9396cb67cd56ada797d320928a94cdbb2926d5
stock backup       /tmp/libopg3d.run49.nodiag.so
stock SHA-256      e56c2b357461a8b106bcadbe9d26dfc719e20e152d5cb61527fb58d431fd2703
```

Immutable capture checksums:

```text
run63 frontend log  89b1d24c7a7ee4235e77820f6e00a6954a5dd6a9ed2e8e1d02ea8d7037f41660
run63 screenshot    59c37c4351e6781aa3d954f0b4139a8bbd52e60d7705767791f96ba8c36dcdf5
run64 frontend log  13ced8d0d6f4fdcffaf04c6239441e0f8dd6b2ecd9a8f27dfc60b8a74cec9415
run64 screenshot    bb6c54d3d1993065009fb917d77f0cf62d2f420eaee5d67c6f060fc26d40eaa1
run65 frontend log  3f66c241c5dfe13a82a47948a5d19606dc991b047b201c350eb1148b142ebde4
run65 screenshot    e1e72a4ef6927521bae277b75ecac5d68261fff29139839fa80c649cd90bac91
run66 frontend log  5eb089691acfb136bd790cc646dd776079463ce57823fede3cd198d92407d073
run66 screenshot    3f4dcebb4e9b5d9774a79811d5cd3fa23c693af8144502b8def912d3992b285c
```

`TASK_PLAN.md` remains an untouched historical document. Runtime state, logs,
binaries, `.env`, bundles and screenshots must not enter the commit.

### Deferred after progress completion

These remain separate work and must not be mixed into the proven progress fix:

- [ ] decide general `update_progress` command ownership/merging;
- [ ] initialize `Boosters` with measured valid content;
- [ ] classify the old reconnect-only `ws_write_failed` line;
- [ ] determine whether the 30-second `get_inapps` cadence is normal polling;
- [ ] replace remaining placeholder/thin handlers only from measured 23.1.3 or
      read-only PGWSD 23.3.2 contracts;
- [ ] continue using PGWSD as read-only reference; never run `TestScript55.py`;
- [ ] never inspect or modify PG-1200.

### Final closure (2026-09-02)

The backend-only progress implementation was committed as:

```text
ec352e6 fix(progress): apply backend revisions without chooser
```

After the proof runs and commit, production instrumentation was removed from the
device:

- the game process was force-stopped by package name only;
- the host `/tmp/libopg3d.run49.nodiag.so` copy had already been removed by
  temporary-file cleanup;
- an exact surviving stock copy was found at
  `/data/local/tmp/libopg3d.so` and independently verified as 402576 bytes with
  SHA-256 `e56c2b357461a8b106bcadbe9d26dfc719e20e152d5cb61527fb58d431fd2703`;
- the installed passive tracer first matched its expected SHA-256
  `194a5f274d626111ace6dea12d9396cb67cd56ada797d320928a94cdbb2926d5`;
- the existing installed inode was restored byte-for-byte to the stock SHA-256;
- mode `0755`, owner `root:root`, and SELinux context
  `u:object_r:apk_data_file:s0` were preserved;
- the game was left stopped, so no instrumented frontend code remains loaded.

Final backend runtime verification:

```text
PID             58852
listener        :8080 owned by PID 58852
executable      bin/opg3d-backend
working dir     OPEN-PIXEL-GUN-3D-BACKEND
binary SHA-256  d5ae17d3e52df2f18ebda32e8ca34c5137b552a2d44c8df38e101f6c889a5bda
mode override   absent
revision / ack  2 / 2
```

Current run50-run58 frontend logs were moved out of the backend repository and
into `OLD-PIXEL-GUN-3D-ONLINE-PROJECT` as passive trace artefacts. They remain
uncommitted. The backend worktree is clean, and historical `TASK_PLAN.md`
remains unchanged.
