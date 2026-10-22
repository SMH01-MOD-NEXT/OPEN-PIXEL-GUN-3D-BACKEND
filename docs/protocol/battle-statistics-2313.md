# Stock 23.1.3 battle statistics

Backend only; the client/decompiled frontend is a read-only protocol reference.

## Profile display

The Stats tab's **Global Wins Stat** is the player's all-time wins by mode,
not a server-wide leaderboard. The server previously acknowledged
`subscribe {type:"info_by_id_v3",list_v2:[{id,s_h,cl_h}]}` but never delivered
its separate `info_by_id_v3` event. Stock registers that event at 0x24FF3A0;
0x2501514 reads `{id:playerID,info:PlayerData}` and 0x2502660 parses `w`. Do not confuse the bulk callback 0x2500D28
(`all_short_info_by_id_v3`) with this per-player event.
Success must omit `err_code` entirely, even zero. An empty `mods` report is
not evidence that the player has never won: import real slot-57 history.

PlayerData uses compact keys. Wins use native mode strings: `2` Teamfight,
`0` Deathmatch, `16` Team Strike, `22e/22m/22h` raid difficulties, etc.
`t` means tournament wins, **not total**. Native sums the other keys for total.
Never add V1 slot80 and V2 slot84; they can be identical snapshots.
The General tab uses calendar-day stats (30 days); preserve real hit/shot
counts even when hits exceed shots (area/multitarget damage).

## Combat commands and durability

- 192 CompleteCommonMatch / 193 CompleteBRDGMatch: kills, deaths, win; slot45.
- 254 SaveMatchInHistory: `p.m` native match; slot57, last ten entries.
- 336 BattleStatisticEndMatch: `u` Unix time, `w` 0/1; slot77 counters,
  per-day results and last-five result history.
- 352 UpdatePlayerStats / 370 UpdatePlayerStatsV2: `k,d,w,hds,h,s,ks,c,tw,sw,sid,dk`;
  slots80/84 respectively. `dk` is month-day-year, `tw/sw` are current
  streaks (take maxima); season state is `ss:{seasonID:PlayerStats}`.

The ledger is bootstrapped lazily from existing native slots, then projected
into both progress channels. Uploading old snapshots cannot erase accepted
results. Results share the existing atomic persist-before-ack transaction and
bounded durable command receipts. Timestamp receipts additionally protect
history/results resubmitted with a different command ID. Mode reports and
history counters are reconciled by maximum, not added (either can arrive first).
Admin full raw replacement explicitly resets this projection; ordinary
wallet/pass updates do not. Profile transfer deep-copies the ledger and clears
it from the source.

This is compatibility for **client-reported** statistics, not authoritative
combat simulation or an anti-cheat claim. Unknown historical per-mode wins
cannot be reconstructed from a truncated history alone; do not invent them.

## Native mixed end-of-match batch

On-device verification exposed a separate compatibility blocker: stock reports
`153 GameTaskChangeInfoInBattle` alongside 254/336/192. Rejecting all pass keys
on 153 rolled back the entire match. The native task storage at 0x3CB5D8C
accumulates field changes (`p`, `c`, `r`); 153 flushes the batch (151 when tasks complete). Accept bounded,
monotonic fields for open pass tasks, preserve existing completion/claim flags,
and ignore known unopened calendar entries in these automatic channels (stock can include
these cached entries). Do not unlock future tasks or grant rewards from the
flush. Explicit reset commands 154/155 remain prohibited for pass keys.
A regression test covers that exact mixed packet and restart persistence.

## Device verification

The unmodified 23.1.3 Stats tab displayed **8 total wins**, including **6 Team
Fight** and **2 Deathmatch**, with the loading placeholder gone. History showed
the existing Team Fight results with their real timestamps and player data.
Empty opponent positions are retained when the stock report contains only the
local player; the backend does not fabricate missing participants.

Two real native end-of-match packets rejected during diagnosis were replayed
unchanged through authenticated encrypted Socket.IO on the corrected backend.
The server persisted 4 and 6 supported child commands respectively; exact
retries applied zero commands. Results became 8 wins / 3 losses; native last-ten
history retained both new timestamps (1792650112, 1792651147). The ledger was
byte-equivalent after a backend restart and structurally identical after a
stock-client relaunch. The History tab showed the new defeat entries. These
zero-kill control packets do not contain 370; daily statistics ingestion is
covered separately by V1/V2 integration tests rather than fabricated reports.
