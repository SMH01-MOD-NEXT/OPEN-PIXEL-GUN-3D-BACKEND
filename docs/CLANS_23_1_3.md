# Clans (23.1.3)

## Scope

Backend implementation for the existing Android 23.1.3 clan UI: creation,
search, join rules (open/request-only, minimum rank, member limit, cooldown),
roles, invitations and requests, clan chat, the shared fort / tank / storage /
building state used by fort and tank crafting, clan store content, and an
explicit "no season" answer for clan wars. No game frontend file, APK asset or
client patch is part of this change.

Evidence is read-only static analysis of `analys2313/libil2cpp.so` and
`dump2313.cs` from the frontend checkout. Addresses are stock 23.1.3 RVAs.
**None of this has been verified on a device yet.** Request/response shapes
come from the native parsers; config *values* (prices, levels, store rows)
are authored, not original data - see "Authored configs".

## Implementation map

| Area | Files |
| --- | --- |
| Clan state, membership, roles, chat, cooldowns | `internal/store/clans.go` |
| Fort field, decor, tank, storage, upgrades, buildings, progress commands | `internal/store/clans_fort.go` |
| Socket requests | `internal/api/socketio_clans.go` |
| Subscriptions and pushes (compact sections) | `internal/api/socketio_clan_push.go` |
| Clan war requests | `internal/api/socketio_clan_war.go` |
| Hosted configs | `content/sio-configs/ClanCommon.json`, `content/sio-configs/ClanBuildings.json` |
| Tests | `internal/store/clans_test.go`, `internal/api/socketio_clans_test.go`, `internal/api/socketio_clan_config_test.go` |

Clans are server-owned shared state persisted in the normal store document.
The actor always comes from the authenticated socket session; client-supplied
player ids never select who acts. Existing ban/session checks still apply.

## Measured native contracts

All ids travel as JSON strings. Successful answers omit `err_code`; failures
set `error:1`, a native `err_code` and a `result` label. Codes the client
branches on: 1702 member limit, 1705 already in a clan, 1708 name taken,
1710 cooldown (+`time` seconds; client fallback 1200), 1711 upgrade mismatch,
1730 promo, 1740 request refused, 1400 invalid. Anything else is generic (1).

| Request | Shape | Evidence |
| --- | --- | --- |
| `create_clan_v2` | `name, logo (base64 PNG), slogan, type, continent, min_rank` -> `data.clan_id` | client pays the 300 Coins itself |
| `change_clan_params` | `clan_id` + optional `name, slogan, logo, leader_message, min_rank, limit, type, continent` | |
| `get_possible_clans` / `get_clan_info_by_param` | `continent` / `param` -> `data:[clan]` | list parser 0x283D5F4 |
| `get_clan_info_v2` | `clan_ids, clan_id` -> `data:[clan + has_request]` | |
| `get_clan_data` | `clan_id, params:["cr","mb",...]` -> compact sections | ParseSections 0x3DB785C |
| `join_clan`, `accept_invite_to_join_clan` | `clan_id` -> full `clan` | full parser 0x283D050 |
| `reject_invite_to_join_clan` | `clan_id` | |
| `ask_to_join_clan` | `clan_id, text` | 1740 makes the client forget the request |
| `accept/reject_request_to_join_clan` | `clan_id, receiver_id`; answer must echo `receiver_id` | |
| `reject_request_to_join_clan_all`, `invite_to_clan` | `clan_id` (+`receiver_id`) | |
| `change_clan_role` | `clan_id, role_id, player_id`; answer echoes both | leader check = `creator_id` |
| `kick_from_clan`, `leave_clan`, `delete_clan` | `clan_id` (+`player_id`) | |
| `get_clan_status` | -> `clan_status, clan_war_status, clan_timeout, clan_war_timout, clan_war_timeout` | reader 0x294DD50 (reads err_code) |
| `get_leader_clan` | -> `id, name, logo` | ClanLeaderBunner 0x27A5D5C |
| `send_clan_message` | `text, timeStamp` -> server `timestamp` | |
| `update_clan_field` | `clan_id, field_cells:[{p:{x,y,z}, s, r, i}], power` | field parse 0x27B5A94 |
| `update_clan_field_decor` | `clan_id, field_decor:[int]` | 0x3DB9DE8 |
| `update_clan_tank` | `clan_id, clan_tank:[{p, in}], power` | 0x3DBA218 |
| `set_clan_building_levels` | `clan_id, field_level?, field_points?, tank_level?, tank_points?` | bl 0x3DBA630 |
| `add_upgrading_item` / `finish_upgrading_item` | upgrade timers; finish -> `upgrade_data, current_level`, 1711 on mismatch | up 0x3DBA7E4 |
| `create_clan_building` | `building_id, up` | bn 0x3DBAE58 |
| `update_clan_war_mark` | `unset:[...]` | |
| `activate_clan_promo` | answer echoes `player_id`; 1730/1400 | |

Subscriptions: `change_clan_state` (pushes `existed_clan`, `added_clan`,
`removed_clan`), `get_start_clan` / `get_lobby_clan` / `get_clan_builds`
(compact sections `cr, mb, ir, or, ms, it, fl, fd, tn, bl, up, wm, bn`;
unchanged sections are not re-sent), `get_invites_from_clans` and
`get_clans_core`. Mutations refresh every subscribed member.

Progress commands inside `update_progress`: **182 CreateClanItems**
(`items:[{id, clan, item_index, count, level, buy, from_chest}]`, crafted /
bought / chest items land in the clan storage) and **206
ClanBuildingsAddItems** (`items:[{i, c, sp, ci, up, mu}]`, building
contributions). Commands addressed to another clan are refused as a whole;
retries are applied once (content receipt). The player's own acknowledgement
never fails because of the clan side.

## Rules enforced by the server

- Clan types: 1 Public (join directly), 2 Private (request only). `min_rank`
  is checked against the player's level. Member limit 20 (purchasable up to
  100 via `limit`), 50 pending requests/invites per clan, 20 per player,
  pending rows expire after 14 days. Unique names (case/space-insensitive).
- Roles: Leader 1, Officer 2, Soldier 0. Only the leader changes roles;
  role 1 hands leadership over (old leader becomes officer, `creator_id`
  moves). Leader kicks anyone, officers kick soldiers only. Leader/officers
  edit fort, decor and tank. A departing leader hands the clan to the next
  officer or oldest member; the last member leaving dissolves the clan.
- Cooldown: leaving or deleting starts a 20 minute cooldown for creating,
  joining or asking to join (1710 + `time`). **Being kicked does not** start
  a cooldown. Requests are rate limited per player.
- Fort/tank: placeable indexes must be stock ClanPlaceable/FortItem catalog
  items, cell sizes are SquareFieldCellSize values, lists are bounded. The
  client computes layouts, power and prices; the server validates identity,
  permissions and bounds and is the single source of truth all members merge.
- Clan chat bodies are never written to logs (`<clan chat redacted>`).

## Authored configs

The client polls these clan configs with an empty hash (missing on the
client): ClanCommon 69, ClanBuffs 73, ClanForge 82, ClanTasks 83,
ClanBuildings 84, ClanWar_0 67, ClanWar_1 71, ClanWarV2Even 165,
ClanWarV2Odd 166, LootBoxesProviderClan 158, ClanChests 38. ClanLootBoxes 68,
ClanPlaceableItems 76 and ClanRank 77 are embedded and need nothing.

Two are now hosted. Their loaders (ClanCommon 0x36FE4E8, ClanBuildings
0x3709570) call ConfigSystem.GetText 0x24981B0 - the accessor Boosters was
measured through - so both are on the inline-body allow-list. **Values are
authored for this server, not recovered originals.**

- `ClanBuildings.json` - `Dictionary<int, building>` (no label key: a
  non-int key fails the whole parse). Only the Black Market (102015) exists
  in the 23.1.3 item registry; Governor/Forge/Laboratory ids are not stock
  items. Five levels `s:[{fl, tl, p, pr}]` (required fort level, tank level,
  building points, ClanSilver price) and `pbp` (converter 0x33D1C94, object
  of int->int; read here as currency -> price per building point).
- `ClanCommon.json` - ClanCommonData. Store rows `tsi` (tab names of
  ClanStoreTab): Clothes = the four clan armors (Wooden/Steel/Golden/Crystal)
  and the three clan skins, Weapon = Clan Hero Shovel; all priced in
  ClanSilver (8015). Division gating `d` is -1 (none) because no war season
  ever assigns divisions. Packs `p`, offers `o`, store data `cs`, promotions
  `tsp` and time restrictions `rs` are present but empty so no list is null.

Not hosted (deliberately): **LootBoxesProviderClan** (ClanSilver store
lootboxes per category/division with escalating prices and super-box points;
needs lootbox items and LootBoxes 119 presentation rows that were not
recovered), **ClanBuffs** (buff data plus war capture chances),
**ClanTasks** (Governor building is not a stock item), **ClanForge** and
**ClanChests** (no 23.1.3 consumer found). The embedded ClanLootBoxes (68)
still drives craft/war/gift clan chests on the client.

## Clan wars

Not run. There is no original season, map (ClanWarMapCellConfig), event,
raid, tribute or division-reward data, and both V1 and V2 configs are absent
on the client. `get_info_clan_war` / `get_info_clan_warV2` answer an empty
season (`enable:false` for V2, `war:0, reg:false`, zero times, `clans:[]`,
no `reward`; reader 0x3D2ED1C uses defaulting getters). `get_tribute`,
`get_auto_tribute`, `log_war` succeed empty. Registration, event start /
progress / finish, `update_war_progress_v2` and reward clears fail with
`result: clan_war_unavailable`. A real season would need an authored
ClanWarConfig (dates, cells, events, rewards, tasks, raids), server-side
matchmaking between registered clans, progress/score aggregation and reward
delivery - a separate project requiring device verification.

## Validation

- `go test ./...`, `go vet ./...`, `go test -race` for store/api/sio pass.
- Store tests: lifecycle, roles, cooldown (leave starts it, kick does not),
  fort/tank validation, progress receipts.
- Socket tests: create/join/chat/subscription pushes, search/requests/cores,
  war answers; config tests check every hosted clan row against the stock
  catalog and the inline delivery contract.
- Device verification is still required for: store rendering and purchase,
  Black Market levels, crafting persistence across members, and the war UI
  with the empty season.
