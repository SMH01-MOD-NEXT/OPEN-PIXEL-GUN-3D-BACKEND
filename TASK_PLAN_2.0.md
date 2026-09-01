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
