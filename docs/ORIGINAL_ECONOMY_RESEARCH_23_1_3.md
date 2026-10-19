# Original 23.1.3 economy: verified evidence and remaining gaps

## Status

This is a recovery reference, **not a deployed reward table**. The prior chest presentation fix is working on the ADB device. Chest opening, rewards persistence and booster activation are not completed by this research. No runtime config was changed during this research step.

The user selected original 23.1.3 behavior, not a custom economy. Unknown values must not silently become guessed defaults.

## Primary client evidence

The stock 23.1.3 APK/OBB contains the four XP booster icons: +25%, +50%, +100%, +200%. A read-only lookup of the running client's item registry maps booster1–4 to Applicable indices 1030, 2030, 3030, 4030. Full resource paths and evidence are recorded in `recovery/23.1.3-economy-evidence.json`.

Config 167 uses `bs` as a list of settings (`id`, `lt`, `t`, `st`, `mi`, `bp`), `gm` as a list of game mode enums, `ms` as migration settings, and `bes` as event settings. XP property is enum 3; the reward calculation treats its integer as an additive percentage (divide by 100, add one). Keep `gm` non-null and preserve inline JSON delivery: these are required by the previously verified post-match repair.

Only texture names, percentages and item indices have been established for all four boosters. No icon reveals its duration. The presence of booster5 and eventBooster1 in the item registry does not establish their settings.

Stock Config 116 ExpOpen establishes level 65 and 500 XP for the veteran chest, index 1027. Config 119 presentation is already restored separately. Universal provider Config 157 contains rate data, reward items, eligibility filters and modifiers; it must not be substituted by an arbitrary flat reward list.

## Historical evidence: 2022–2024 only

- Veteran Chest revision 383731 (2022-01-07): https://pixelgun.fandom.com/wiki/Veteran_Chest?oldid=383731 . Its 22.0.0 section records three prizes per chest: one guaranteed parts-group prize and two other prizes; discrete coin amounts 22/30/38, gems 8/12/16, 400 clan silver, 30 coupons, and a now-obsolete VIP reward. It describes two unlock tiers. See the JSON reference for names and counts. It does **not** publish drop weights or prove that the table remained unchanged in 23.1.3.
- Booster revision 398787 (2023-06-05): https://pixelgun.fandom.com/wiki/Booster?oldid=398787 . Boosters replaced VIP in 22.7.0. Therefore the 2022 VIP prize cannot be copied directly into the target version. The exact replacement reward is unknown.
- Pixel Gun 3D Guide revision 401541 (2023-10-01): https://pixelgun.fandom.com/wiki/Pixel_Gun_3D_Guide?oldid=401541 . States that boosters do not increase XP in minigames. This is not sufficient to reconstruct the exact Config 167 game-mode allowlist.
- GameRant (2024-04-07): https://gamerant.com/pixel-gun-3d-best-ways-to-level-up/ . Mentions +200% XP for one hour. This later secondary source alone does not establish all four lifetimes in 23.1.3.

Wiki revision content was retrieved using the read-only MediaWiki API with explicit revision timestamps. Current wiki pages contain later changes and are not treated as exact 23.1.3 data. Historical season lists corroborate booster tiers but do not specify lifetimes.

## Implementation gates

1. Recover Config 167 from a target-version server dump/cache, or obtain corroborating tier-specific duration and mode evidence. Do not silently set all lifetimes to 3600 or zero.
2. Recover Config 157, or explicitly distinguish any reconstruction from an exact original table. Equal probabilities are not historical evidence. Never grant the unidentified analytics value 7201 as an item.
3. Trace a genuinely eligible opening; the last observed device screen had 499/500 XP. Do not treat a disabled button as a protocol failure.
4. Implement and test authenticated, idempotent persistence. The existing update_progress acknowledgement is not evidence that rewards are persisted.
5. Validate booster artwork/activation and chest rewards after restart on the real device; run Go tests, race tests and build before any functional commit/deployment.

A missing reward table is a data-recovery gap, not a reason to alter the original-economy requirement without the user's approval. No APK patch, runtime memory write, inventory grant, or service restart was performed in this research step.
