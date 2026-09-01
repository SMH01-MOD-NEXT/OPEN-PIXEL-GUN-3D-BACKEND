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
