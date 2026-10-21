# Bot weapon progression for stock 23.1.3

## Research scope and evidence

This is a curated, period-compatible arsenal, **not a measured universal 2022–2023 tier list**. Modern wiki statistics can include later buffs/nerfs: only historical release/changelog information is used for compatibility; the installed client retains its own damage. A supplementary best-weapons roundup is dated April 2024, not falsely attributed to 2022–2023.

- [Sakura history](https://pg3d.wiki.gg/wiki/Sakura): 22.6.0 rework made it popular; its 16% damage reduction is in 23.5.0, after this client. High-level pick.
- [Ultimatum](https://pg3d.wiki.gg/wiki/Ultimatum): extreme-damage, slow-fire wall-break shotgun; retained at 65 by prior request.
- [Tactical Scorcher history](https://pg3d.wiki.gg/wiki/Tactical_Scorcher): released 21.7.0 with period rebalance entries. Use the 23.1.3 implementation, not current damage.
- [Dislike history](https://pg3d.wiki.gg/wiki/Dislike): released 21.5.0; 22.1 damage/AOE and 22.3 projectile-speed nerfs predate this client; do not import later 24/26 buffs.
- [Rebel history](https://pg3d.wiki.gg/wiki/Rebel): 25% nerf in 22.8.0; excluded rather than treating its pre-nerf reputation as current.
- [June 2023 special-weapon top](https://www.youtube.com/watch?v=25xPBM0luN4): indexed description explicitly includes Necklace of the Ice King, Poison Darts and Laser Bouncer. Period corroboration, not a verified numeric ranking or full transcript.
- [March 2023 melee top](https://www.youtube.com/watch?v=PHyXklmWt_c): indexed description names Heroic Epee among strong picks; video contents/ranking were not retrievable and are not claimed as verified.
- [Supplementary category roundup, April 15 2024](https://www.charlieintel.com/games/best-weapons-in-pixel-gun-3d-317899): Ultimatum, Thunderer, Exterminator, Laser Cycler, Poison Darts, Necklace of the Ice King, Anti-Champion Rifle, Deadly Beat, Destruction System, Invincible Lion, Big Buddy and others. Candidate names only; not authoritative damage/attributes or a period ranking.

Do not use the end-of-2023 weapon-release video as a compatibility list: later 23.x/24.x releases may be absent. For example, Accurate Bubbler (23.5), Dragon-X (24.2), and Deadly Icicle (24.0) are excluded; none has a Weapon record in this 23.1.3 registry.

## Exact client validation

Every selected weapon was joined against both embedded `internal/catalog/stock2313.json` and the original extracted `stock2313-item-records.json`, including technical tag and category. The latter input hash matches `docs/INVENTORY_CATALOG_23_1_3_SHA256.json`. No gallery IDs, guessed IDs, skins, or BR-only records are substituted. Harsh Punisher uses mythical `1197001`, not the separate legendary `1218001`.

Weapon unlocks below are our bot balance policy, **not official purchase levels**. Lower levels deliberately use ordinary starter/intermediate weapons, not the strongest endgame guns. AI aim/dodge presets remain unchanged. No damage, armor, module levels, player inventories or frontend files are modified. No quantitative bot combat benchmark has established that every unlock is strictly stronger in every map/mode.

## Pools by level

Each row lists a rotation, not six weapons simultaneously used by one bot. Slot order is Primary / Backup / Melee / Special / Sniper / Heavy.

### Level 1–5

- **Primary:** Combat Rifle (`10001`), Army Rifle (`53001`).
- **Backup:** Pixel Gun (`1001`), Fast Death (`17001`).
- **Melee:** Combat Knife (`9001`).
- **Special:** Flamethrower (`333001`).
- **Sniper:** Sniper Rifle (`67001`).
- **Heavy:** Signal Pistol (`162001`).

### Level 6–10

- **Primary:** Marksman (`84001`), Hellraiser (`142001`), "Predator" (`163001`).
- **Backup:** Fast Death (`17001`), Red Twins (`380001`).
- **Melee:** Fire Demon (`131001`), Dark Force Saber (`130001`).
- **Special:** Flamethrower (`333001`).
- **Sniper:** Sniper Rifle (`67001`), Prototype (`77001`).
- **Heavy:** Apocalypse (`75001`), Grenade Launcher (`79001`).

### Level 11–20

- **Primary:** Secret Forces Rifle (`220001`), Heavy Shotgun (`167001`), Minigun Shotgun (`598001`).
- **Backup:** Exterminator (`203001`), Alien Blaster (`293001`).
- **Melee:** Combat Yo-Yo (`615001`), Dark Force Saber (`130001`).
- **Special:** Laser Bouncer (`399001`), Reflector (`704001`).
- **Sniper:** Anti-Hero Rifle (`374001`), Charge Rifle (`458001`), Prototype (`77001`).
- **Heavy:** Ghost Lantern (`444001`), Apocalypse (`75001`).

### Level 21–30

- **Primary:** Future Police Rifle (`727001`), Mega Gun (`720001`), Prehistoric Shotgun (`767001`).
- **Backup:** Thunderer (`640001`), Laser Cycler (`1069001`), Alien Blaster (`293001`).
- **Melee:** Heroic Epee (`1110001`), Combat Yo-Yo (`615001`).
- **Special:** Poison Darts (`769001`), Laser Bouncer (`399001`), Necklace of the Ice King (`895001`).
- **Sniper:** Anti-Champion Rifle (`687001`), Digital Sunrise (`1068001`), Charge Rifle (`458001`).
- **Heavy:** Destruction System (`755001`), Deadly Beat (`706001`), Ghost Lantern (`444001`).

### Level 31–45

- **Primary:** Viking (`795001`), Acid Shotgun (`987001`), Storm Trooper Exoskeleton (`711001`), Mega Gun (`720001`).
- **Backup:** Laser Cycler (`1069001`), Sparkling Horn (`1380001`), Thunderer (`640001`).
- **Melee:** Heroic Epee (`1110001`), Double Cashback (`1121001`).
- **Special:** Necklace of the Ice King (`895001`), Poison Darts (`769001`), Reflector (`704001`).
- **Sniper:** Anti-Champion Rifle (`687001`), Digital Sunrise (`1068001`), Demon Catcher (`1167001`).
- **Heavy:** Destruction System (`755001`), Invincible Lion (`1216001`), Deadly Beat (`706001`).

### Level 46–55

- **Primary:** Tactical Scorcher (`1449001`), Viking (`795001`), Acid Shotgun (`987001`), Storm Trooper Exoskeleton (`711001`).
- **Backup:** Sparkling Horn (`1380001`), Laser Cycler (`1069001`), Thunderer (`640001`).
- **Melee:** Heroic Epee (`1110001`), Double Cashback (`1121001`).
- **Special:** Necklace of the Ice King (`895001`), Poison Darts (`769001`), Reflector (`704001`).
- **Sniper:** Sakura (`705001`), Harsh Punisher (`1197001`), Demon Catcher (`1167001`), Anti-Champion Rifle (`687001`).
- **Heavy:** Dislike (`1416001`), Invincible Lion (`1216001`), Destruction System (`755001`), Deadly Beat (`706001`).

### Level 56–64

- **Primary:** Tactical Scorcher (`1449001`), Viking (`795001`), Acid Shotgun (`987001`), Prehistoric Shotgun (`767001`).
- **Backup:** Sparkling Horn (`1380001`), Laser Cycler (`1069001`), Thunderer (`640001`), Alien Blaster (`293001`).
- **Melee:** Double Cashback (`1121001`), Heroic Epee (`1110001`).
- **Special:** Necklace of the Ice King (`895001`), Poison Darts (`769001`), Laser Bouncer (`399001`), Reflector (`704001`).
- **Sniper:** Harsh Punisher (`1197001`), Sakura (`705001`), Demon Catcher (`1167001`), Digital Sunrise (`1068001`).
- **Heavy:** Dislike (`1416001`), Invincible Lion (`1216001`), Deadly Beat (`706001`), Destruction System (`755001`).

### Level 65

- **Primary:** Ultimatum (`834001`), Tactical Scorcher (`1449001`), Viking (`795001`), Acid Shotgun (`987001`).
- **Backup:** Sparkling Horn (`1380001`), Laser Cycler (`1069001`), Thunderer (`640001`), Alien Blaster (`293001`).
- **Melee:** Double Cashback (`1121001`), Heroic Epee (`1110001`).
- **Special:** Vertical Grip Device (`1175001`), Necklace of the Ice King (`895001`), Poison Darts (`769001`), Reflector (`704001`).
- **Sniper:** Harsh Punisher (`1197001`), Sakura (`705001`), Demon Catcher (`1167001`), Anti-Champion Rifle (`687001`).
- **Heavy:** Big Buddy (`94001`), Dislike (`1416001`), Invincible Lion (`1216001`), Deadly Beat (`706001`).

## Delivery and fallbacks

- `Bots` and `MatchmakingBots.AI.weapons`: four category-correct compact-ID fallback strings rotate within the unlocked pools. Legacy AI index zero is still supported.
- `MatchmakingBots.AI_Tiers`: all materialized tiers use unlocked weapon rotations, preserving AI settings and row count.
- `MMBots`: the selected stock preset's active equipment rows are populated with full inventory indices (`compact*1000+1`). Other equipment rows, gadgets and unknown fields are preserved. Its existing default equipment ID remains in the selected range.
- At 65, legacy first/last and modern last/default rows always include Ultimatum + Vertical Grip Device + Big Buddy together. Other rows rotate those and alternative high-end weapons.
- Cached hashes change at every arsenal boundary; poll and push share those fingerprints. The room master's profile remains authoritative. The retired matchmaking fallback may repeatedly choose one default loadout: configuration pool coverage does not prove in-battle randomization.

## Verification

Automated tests enumerate levels 1–65 across all three paths, validate every weapon's catalog/category and ID format, every modern pool's reachability, maximum-level exclusives, boundary/hash transitions, deterministic generation, gadget/inactive-loadout preservation, shared-document isolation and measured delivery encoding.

At implementation time `adb devices` was empty. No new real-device delivery or match/visual verification is claimed. A game restart and real bot match are required to assess actual selection, animation, AI-specific weapon mechanics and balance (including grip movement and charge/healing weapons).

Deployment passed health on port 8080 and preserved the complete state file hash. Both console harnesses, full Go tests, race/vet and both command builds passed; QA log: `/tmp/bot-arsenal-final-qa.log`.
