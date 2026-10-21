# Server inventory and administration — stock 23.1.3

## Scope

The canonical record is the player's existing `ProgressSlots` snapshot, persisted
in `data/state.json`. There is no separate ownership table that can disagree with
stock slots. Full slot uploads retain unrecognized sections and fields. Existing
numeric `init_progress` projections follow the canonical slots without injecting
an entire raw snapshot into the legacy initialization channel.

This is compatibility persistence, **not a complete server-authoritative economy
or anticheat system**: the game still reports transactions/snapshots, prices are
not validated against every shop, and unsupported command schemas are not guessed.
All progress *received in snapshots* is retained; this does not claim that every
stock delta command has been reverse-engineered.

## Administration

Open `/admin`, select one player, then **Инвентарь · клиент 23.1.3**.
Search by item name, numeric index, internal ID and category. Names extracted from
stock localization are English; UI labels are in `inventory.ru.json`.
Quantity means **set the stored quantity**, not add it. Level and rarity apply to
weapon records; armor/gadgets use level. Pet grants use a fixed stock baseline
(quantity 1, level 1, rarity 0, zero cards and no custom nickname). Confirmations include the target player
and exact item type/index. Full indices must be used: `727001`, not ordinal `727`.

Authenticated routes:

- `GET /api/v1/admin/catalog?q=&type=&offset=&limit=` (maximum 100/page;
  `editable=1` restricts to supported adapters).
- `GET /api/v1/admin/players/{id}/inventory` returns items plus `revision` and
  `updated_at`.
- `POST` to the same inventory route with `type`, `index`, `action` (`grant` or
  `revoke`), `quantity` (1–1000000), `level` (1–65), `rarity` (0–5), and the exact
  revision/timestamp from the preceding read.

Edits are atomic, compare-and-swap protected, and roll back on persistence errors.
A real change advances only this player's progress revision and uses the existing
online relogin/offline full-sync mechanism. Stale sessions cannot overwrite a
pending admin revision. No-op edits do not trigger a relogin. Conflict/equipped
returns 409; failed persistence returns 503 without exposing storage paths.
**Equipped items cannot be revoked**; first remove them from equipment and all
loadouts in game. The UI refreshes the entire selected profile after an edit so
its raw editors cannot accidentally restore a pre-edit snapshot.

## Confirmed ownership adapters

| Type | Category | Canonical storage |
| --- | --- | --- |
| 10 | Weapons, including level/rarity | `28.0[index] = {l,r,...}` |
| 20 | Armor | `43.0[index] = level` |
| 30/40/50/60 | Masks/hats/boots/capes | `13.w` integer list |
| 65 | Character skins | `10` integer list |
| 70 | Gadgets | `16[index] = level` |
| 80 | Pets | `53.p` record list (`{i:index,c:cards,l:level,n:nickname}`) |
| 1070 | Weapon parts | `24[index] = quantity` |
| 1080 | Royale items | `11` integer list |
| 1150 | Available modules | `27[module_type]` index list; level in `37[index]` |
| 1155/1520 | Module points / applicable consumables | `26.c[type][index]` |
| 1350/1470/1510 | Armor skins / graffiti / wear skins | `26.s[index] = 1` |

Pet ownership records preserve all existing fields; new records use the observed
23.1.3 shape and the equipped pet reference at `9.6` is protected from revoke.
Pocket Harpoon has two editable gadget definitions (indices `63007` and `76007`);
both persist through the same measured slot-16 gadget adapter. The similarly named
`44007` record has a different stock display label and is not relabeled.

Other catalog categories, including lobby/fort objects and weapon skins, remain
**read-only** until their ownership schemas are verified. Module entries have an
individual grant/update adapter: an available module is stored as its index in the
module-type list at `27[module_type]`, and `37[index]` stores level 1–10. Slot `26`
is the generic inventory and must not receive module indices: a live cold start with
`26.s[326009]` repeatedly reached an incompatible inventory-item cast and left the
lobby loading overlay stuck at 90%. Configuration-shaped `27.modules_v2` records
are preserved but are not treated as progress ownership or migrated. Repeated
module grants do not duplicate the canonical slot-27 index. Module revocation and
bulk module grants remain disabled. See the correction addendum below for the
tested boundaries. Unknown raw sections remain preserved even without an inventory
projection.

Measured command reducers: `10,25,33,64,65,66,81,84,85,87,120,132,139,191,197`.
A measured purchase batch (currency charge + weapon ownership) commits together
before ACK. Receipts survive restart and prevent retry double-charging. Malformed
batches and disk errors do not partially apply. Unsupported commands retain the
pre-existing snapshot path, **not an assertion of durable delta interpretation**.
Progress command id 80 remains unsupported pending a verified delta-payload contract;
that is separate from catalog item type 80 (pets), which uses the verified slot adapter above.

## Catalog provenance and limits

`internal/catalog/stock2313.json` contains **7457 registry records** from the local
stock Android 23.1.3 (versionCode 268411) APK/OBB, not current web/game data.
**4613** records have stock English names; **94** pet records have UI-only labels
derived from their codenames; **2750** explicitly retain a technical ID.
**3398** records have a verified write adapter.
These are registry records, not 7457 unique weapons or 7457 fully editable items.

Sources: stock item registry (type enum TypeDefIndex 8931), weapon definition tags
and localization keys, WearDescriptors, SkinsController SkinItem localization
keys, gadget prefab components, and embedded Config160 wear-skin labels. Labels
are joined by verified stock IDs, never inferred from a player's private prefs.
Pet display labels follow the requested codename rule: strip the leading `pet_`,
replace underscores with spaces, and title-case (e.g. `pet_fox` → `Fox`,
`pet_alien_cat` → `Alien Cat`). Their `name_source` is `derived_codename`, not
stock localization. This never changes the stored ID, numeric index or `53.p.n`
custom nickname.

`tools/build_stock2313_catalog.py` deterministically joins extraction outputs:

```sh
python3 tools/build_stock2313_catalog.py --inputs /path/to/extracted-public-config \
  --dump /path/to/23.1.3/dump2313.cs --output internal/catalog/stock2313.json
```

Required inputs: `stock2313-item-catalog-raw.json`, `stock2313-localization.json`
(with an English dictionary), `stock2313-item-records.json`,
`stock2313-wear-records.json`, `stock2313-skin-records.json`,
`stock2313-target-component-records.json`, `stock-embedded-configs/160.json`.
The join tool is not an APK extraction tool; recreate these inputs from the same
client analysis before regenerating. APK/OBB, live-memory captures and private
player backups are deliberately not committed. Input/output SHA-256 values are
recorded in `INVENTORY_CATALOG_23_1_3_SHA256.json`.

Useful measured client references: wear serialization at RVA `0x35AA440`;
SkinsController ownership check `0x4E934A8` delegates to slot-10 storage through
`0x26FD3E4` / `0x5112E24`, then HashSet membership at `0x4D8AEE0`.

## Verification (2026-10-20)

Automated: all adapters grant/revoke/reopen, unknown-field preservation, equipped
protection, CAS, pending-revision fencing, measured transaction replay/rollback,
API authentication/isolation/strict JSON, disk-error 503 and non-success gameplay
ACK, catalog identity/immutable returned values, localization embedding. Full Go
tests, vet, store/API race tests and backend build are required before deployment.
The join generator reproduces the checked-in catalog byte-for-byte.

ADB, stock 23.1.3: online and offline targeted revision downloads acknowledged;
Headphones grant changed its stock action from Buy to Equip after cold launch;
Zombie grant was present in the actual slot-10 runtime ownership HashSet (not
merely a catalog list). Four temporary item records survived backend restart.
After revocation Headphones returned to Buy and the skin ownership HashSet was
empty again. Temporary grants were removed afterwards; original owned records and money/XP
were checked unchanged, all 87 progress sections retained. A default Pixel Gun
record required precise test cleanup through the existing raw-state API because
an original survival loadout implicitly references that free weapon; no loadout
was edited. Not every category's UI has been individually exercised, and the
admin panel has not been end-to-end browser tested.

## Verification addendum (2026-10-21)

Added the type-80 pet adapter using measured stock slot data: ownership lives in
slot `53.p` as records with fields `c/i/l/n`; the equipped pet is referenced by
slot `9.6`. New admin grants are level 1 with zero cards and an empty nickname;
other pet records and unknown slot fields are preserved. The admin catalog now
exposes all 94 stock pet entries as editable. Regression tests cover persist/reopen,
revoke, idempotency, equipped-pet rejection, malformed pet lists, unknown pet IDs,
and both Pocket Harpoon indices.

Full `go test -count=1 ./...`, `go test -race -count=1 ./...`, `go vet ./...`,
and builds of both backend commands passed. A device is connected, but no live
inventory was changed for this work; per-category in-game confirmation remains
outstanding. Module ownership itself remains read-only pending a verified
server-side storage/delta contract.

## Pet administration completion (2026-10-21)

The reported read-only pets were reproduced against the running server: its old
binary returned `editable:false` for every pet despite the newer code in the
repository. Rebuilt and deployed the backend with private state/config/binary
backups, without clearing application data or changing the frontend.

All 94 catalog pets now have editable ownership and derived UI labels. Existing
pets are never reset by a repeat grant. A new pet remains level 1, zero cards,
empty custom nickname; no UI display label is written into the nickname. Revoke
still refuses an equipped pet. Refresh `/admin` to load the updated embedded UI,
select a player, open the catalog, filter Pets, and use the grant action.

Automated: full uncached Go suite, full race suite, vet and both command builds
passed. Catalog reproduction is byte-for-byte. Regressions exercise all 94 pet
identities through grant, disk reopen and revoke; nickname/card preservation,
persistence rollback, strict baseline validation, retry/CAS and stale-upload
fencing, and full-sync delivery/acknowledgement. Run
`node tools/console_pets_test.cjs` for the dependency-free DOM/form harness;
it executes the actual console functions but is not a real-browser E2E test.

Live Android 23.1.3 (versionCode 268411): temporary Fox (`20003`) was granted,
synced and acknowledged at revision 64. Read-only process-memory inspection of
the stock slot-53 pet list confirmed its actual record (level 1, cards 0, blank
nickname), not merely a catalog or server projection. The grant survived a backend
restart. Targeted revoke was synced/acknowledged at revision 65, without restarting
the Android process; read-only inspection confirmed Fox was absent afterwards.
The final pet/egg slot exactly matched the original, and all original pet levels,
cards and nicknames, currency, XP and 87 progress sections were retained.

One unrelated live-client change occurred in slot 21 (`GatchaGifts`, field `c`)
while the game was running; it was preserved, not overwritten with an old profile.
All other final slots matched the pre-test baseline. No temporary pet remains.
This proves the admin ownership lifecycle, not every stock pet purchase/upgrade
or card delta command; those remain governed by the stated economy limits above.

## Module ownership correction after cold-start regression (2026-10-21)

The earlier `26.s` conclusion was rejected by a full cold-start test. Although the
client could deserialize, acknowledge and persist that record, the next complete
lobby initialization repeatedly logged an `InvalidCastException` in inventory-item
handling and remained at `Loading 90%`. This is why login acknowledgement and a
read-only memory membership check are not sufficient acceptance criteria.

Corrected evidence:
- Progress enum slot `27` is `ModuleStorage`; live canonical data is grouped as
  module-type keys (`1`–`24`) whose values are module-index lists. For example,
  Armor Regeneration `326009` is under regenerator group `5`.
- `ModulesInfo` slot `37[index]` stores level 1–10 separately. A level without the
  matching storage index is not treated as available ownership.
- `ModulesContainer.modules_v2` is configuration data, not the canonical progress
  ownership list. Configuration-shaped records previously written under slot 27 are
  retained verbatim but ignored by the adapter.
- Slot `26` is generic inventory. Neither `26.s` nor `26.u` is a valid module grant
  destination in this client.
- Module upgrade factory `0x1B54F8C` emits command `87` with `{i:index}`. The
  reducer increments an available module's slot-37 level and rejects unowned,
  malformed and maximum-level upgrades atomically. Generic command `84` is not used
  as proof of module ownership. Commands `88/89` remain unimplemented; full stock
  snapshots preserve their unmeasured placement/storage transitions.

Emergency recovery removed only the bad `26.s[326009]` membership, advanced the
profile to revision 87 and preserved all 87 sections, currency, level, XP, canonical
slot-27 storage and the existing slot-37 level. A private pre-repair snapshot was
saved. The client acknowledged revision 87 and the user confirmed a cold launch
reached the lobby without the 90% hang.

The corrected backend was then deployed from the tested binary. A clean launch
reached the lobby with no `InvalidCastException`. Re-granting the already available
Armor Regeneration module changed only its slot-37 level from 1 to 7 at revision 88;
slot 27 retained exactly one `326009` and slot 26 remained unchanged. A cold launch
again reached the lobby without the exception. Level 1 was restored at revision 89
and verified by one more cold launch. Revision 89 is acknowledged; currency, level,
XP and all 87 sections remain intact. This validates idempotent level updates for an
already available module, not an arbitrary new-module device grant.

Regression coverage exercises all 42 module identities at levels 1–10, store reopen,
idempotent retries, raw-slot mirroring, malformed storage, failed persistence,
command-87 replay/rollback, stale snapshots and preservation of unrelated slots.
A new-module device grant, equip/remove, in-game purchase/upgrade and real-browser
admin E2E remain separate verification steps.

## Remaining work

- Resolve names for 2750 technical-ID records from additional exact-version assets.
- Verify remaining ownership adapters and delta schemas before enabling writes.
- Define safe explicit unequip/revoke semantics for active items if desired.
- Complete per-category device and admin-browser end-to-end tests.
