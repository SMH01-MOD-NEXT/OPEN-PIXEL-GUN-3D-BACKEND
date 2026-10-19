# Veteran chest presentation, 23.1.3

## Confirmed defect and repair

The device polled LootBoxes (ConfigId 119) with an empty hash. The backend
had no hosted document, so the client's LootBoxData lookup returned null.
Its big texture getter (0x305F620) consequently used an empty tag under
OfferIcons/. This is independent of reward generation.

Host a JSON definition for item 1027, Universal type 1, with tag
LootBox_Veteran_Red. That texture is embedded in stock Resources at
OfferIcons/LootBox_Veteran_Red. The prefab resource also exists at
LootBoxes/Prefabs/LootBoxUI/Veteran_LootBoxUI. ExpOpen's stock special reward
is 1027:1, confirming the veteran item identity.

LootBoxes uses inline JSON delivery. On the rooted 23.1.3 device, after a
normal app restart the controller held a non-null config containing one
definition and the exact tag. A screenshot confirmed the rendered red/white
veteran chest, replacing the empty square. No APK change, runtime memory
write, inventory reset or fabricated reward was used.

## Deliberately not claimed fixed

The reward showcase is still empty. LootBoxesProviderUniversal (157) is
also absent. Opening, award persistence, replay protection and duplicate-item
handling have NOT been verified. At this verification the UI showed
499/500 XP; the presentation repair is not proof of a successful opening.

Boosters (167) remains a non-null minimal config with empty bs/gm, preserving
the prior post-match repair. Live stock item registry confirms applicable
IDs booster1 through booster5 and eventBooster1; stock embedded resources
contain icon_booster1 through icon_booster4, their _n and _mini variants,
and event-booster variants. The original buff lifetimes, factors, eligible
game modes, drop weights and reward table were not recovered from the 73
stock embedded configs. Do not label guessed defaults as original data.

A decision is required on restoring original server data versus defining an
explicit custom server economy before implementing those missing tables.

## Single-reward roulette follow-up

Config119 `o` is OpenAnimationType: None=0, Default=1, Roulette=2.
`r` is RewardShowCaseType, not the animation selector. Stock LootBoxUI
0x2B56518 selects the existing roulette view for value 2.

Config157 is delivered inline as JSON. A single veteran rate draws once; all
alternatives share one nonempty Item.LootGroup (`rg`). Stock universal provider
0x1503020 groups candidates by that string and chooses within each group;
separate groups caused cumulative rewards. An empty string instead follows an
independent-chance path. Each candidate contains exactly one ItemParams reward.
Weights are reconstructed equal weights, not recovered original probabilities.

Full Go tests, API/SIO race tests and backend build passed. Recovery service
restarted without app-data reset. User confirmed the chest now works normally.
This confirms selection/presentation, not complete server economy persistence.
