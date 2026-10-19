# 23.1.3 post-match Boosters delivery repair

## Evidence

The native client exception occurs in reward preparation, shared null branch
0x4851A5C in NetworkStartTableNGUIController method 0x4850494. Rooted read-only
inspection found ConfigId 167's wrapper with null payload. Its GameModes getter
0x366C0AC returns null; preparation has an unchecked list use at 0x4850BE4.
The original results iterator unwinds rather than waiting for a server table.
Animations and the coroutine host are working. Do not restart the iterator,
force UI alpha, auto-Continue or grant local rewards.

Boosters is a JSON-backed config: generic loader 0x26065A0 calls
ConfigSystem.GetText (0x24981B0). The existing binary attachment populates the
hash but leaves cached text empty. Extend selective inline delivery ONLY to
Boosters; preserve other contracts and the hosted document. Its bs/gm arrays
are deliberately empty: no boost multipliers or events are fabricated.

## Validation

- Full `go test ./...` passes, including regression coverage for selective
  delivery, unchanged body/hash and empty non-null arrays.
- `go build ./cmd/opg3d-backend` passes. An initial invocation using the
  nonexistent ./cmd/server target failed; the corrected target succeeds.
- Recovery user service restarted with backup of its previous binary.
- Existing focused frontend library remains installed, no data reset.
- After restart, actual device ConfigId 167 payload is non-null; GameModes
  and BoosterSettings are non-null empty lists. Other incomplete service
  configs are outside this fix.
- Completed real match (PID 2188): results iterator returned false normally
  at state -1 at device 14:44:39, rather than unwinding through the null helper;
  reward queue/delay also finish normally. No scoped post-match exception.
- User confirmed that both rewards and the player list are now visible.
- Subsequent ADB screenshot shows lobby, level 2 / 30 XP / 28 coins; returning
  to lobby is observed, without an injected Continue action or UI workaround.
- `go test -race ./internal/api ./internal/sio` passes.

Private raw logs/screenshots and binaries are under ~/.cache/opg3d-recovery;
do not commit them. Preserve unrelated working-tree recovery changes.

## Four stock XP boosters restored

Config167 now contains `booster1`–`booster4` settings, matching stock item type
1520 and encoded indices 1030/2030/3030/4030. The `id` is the stock string ID,
not the encoded numeric index. `bp` property 3 is FactorExp: additive bonuses
25, 50, 100 and 200 percent, corresponding to 1.25x/1.5x/2x/3x total XP.

Each row points to bundled ResourceManager paths:
- `offericons/booster/icon_boosterN` (texture)
- `offericons/booster/icon_boosterN_n` (simple texture)
- `offerminiicons/icon_boosterN_mini` (mini icon)

No client patch or replacement art is needed. Config loader stays inline JSON;
bs/gm must remain non-null arrays to preserve the post-match repair. The original
empty-list regression was updated to assert that invariant, and separate tests
now verify the four complete rows, factor keys, paths and payload fingerprint.

### Reconstruction boundaries

No original Config167 server dump exists. Lifetime is explicitly set to a
compatibility default of 1800 seconds (30 minutes) for each booster, not claimed
as an original 23.1.3 duration. Supported mode IDs 0,2,3,4,5,8,19,28 select standard
PvP modes from the stock enum; this list is also reconstructed, not historical
proof. Seasonal booster events stay disabled. No migration gifts are invented.
Original event schedules, migration ratios and exact lifetime/mode tables remain
unrecovered. Searches for historical duration evidence did not establish them.

### Validation

- Full Go tests, API/SIO race tests and backend executable build pass.
- Installed backend executable SHA-256 matches the tested build; recovery user
  service is active.
- Rooted read-only inspection of Config167 in the running client confirms four
  settings with correct string IDs, paths, lifetimes and eight supported modes.
- User explicitly confirmed booster pictures, activation/application, and chest
  rewards all work. Further device actions stopped after confirmation.
- App data/cache were not cleared. This change does not claim to repair the
  separate incomplete server-side command/economy persistence implementation.
