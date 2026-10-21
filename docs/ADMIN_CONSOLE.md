# Admin console · client 23.1.3

The embedded console is served at `/admin`. Refresh an already open tab after updating the server. No separate frontend build or external asset CDN is required. Complete Russian and English interface resources live in `internal/api/admin_assets/console.ru.json` and `console.en.json`; their key sets are regression-tested. Item names come from the frozen client catalog, not current-game guesses.

## Navigation and search

- **Overview:** server/account summary and links to the working sections.
- **Players:** search by player ID or nickname, then separate Profile, Inventory, Bans and Advanced data tabs.
- **Global catalog:** independent search across the client catalog, with the selected recipient shown above the results.
- **Account operations:** separate change-ID, transfer and reconciliation tabs.
- **Logs:** explicit start/stop of an authenticated stream, bounded to 2,000 displayed lines.
- **Settings:** browser-local interface theme and language. Dark is the deterministic default; Light can be selected explicitly. Russian and English choices persist in local browser storage and never enter the administrator API or player profile.

Player-owned inventory and global catalog have independent query/category/page state. Search accepts English display names, internal IDs or numeric IDs, ignores case and repeated whitespace, and matches all query words in any order (for example `police future`). Technical names remain clearly marked when a client label is unavailable. Changing the player resets the owned-inventory filter. Owned results use 20-item pages, global results 25-item pages; table scrolling stays inside its own region. Narrow layouts keep wide tables horizontally scrollable without overflowing the page. The console defaults to the dark theme independently of the operating-system preference; Settings can switch to the light theme.

## Inventory operations

The player Inventory tab exposes single-item grant/edit/revoke plus:

1. **Grant all weapons:** adds missing editable weapon definitions only. Existing weapons, including their levels and rarity, remain unchanged.
2. **Grant all wear:** adds missing masks, hats, boots and capes.
3. **Grant all armor:** adds missing armor at the selected level; already-owned armor is preserved.
4. **Grant all gadgets:** adds missing gadgets at the selected level; already-owned gadgets are preserved.
5. **Grant all pets:** adds missing pets at stock level 1, with no cards or custom nickname; existing pet progress is preserved.
6. **Grant all modules:** adds only missing type-1150 modules at the selected level 1–10. Existing modules and their levels are preserved.
7. **Revoke all weapons:** removes supported, unequipped weapons.
8. **Clear inventory:** removes supported, unequipped inventory entries across categories. This is deliberately **not an account/progress reset**. Equipped items and unverified/unknown formats are retained.

Module catalog entries have readable labels derived from stock IDs. Individual grant/update supports levels 1–10 and atomically writes the complete 23.1.3 linkage: the module index in `27[type]`, at least one corresponding ModulePoint in `26.c[1155][point_index]`, and the level in `37[module_index]`. The exact catalog relation is `point_index = module_index + 42000` with matching `mod_…`/`modp_…` IDs. Existing positive point quantities are preserved and storage indices are not duplicated. Module indices must never be written directly to `26.s` or `26.u`; that incompatible format caused a verified 90% lobby hang. Configuration-shaped `27.modules_v2` records are preserved but ignored. Module revocation remains disabled. Bulk module grants use the same complete linkage as individual grants, apply all missing modules atomically, persist once, advance one revision, and preserve already-owned module levels. See `INVENTORY_23_1_3.md` for evidence and limits. Labels never change the stored ID.

Bulk actions require a fresh server preview, display affected/preserved/skipped counts, and require the exact canonical player ID before applying. Editing parameters invalidates the preview. Each apply checks both progress revision and profile update timestamp and binds the preview token to the recipient, parameters and affected entries. A conflict requires another preview; the console never silently retries destructive operations.

The server applies all entries under one store lock, persists once, and advances the progress revision once. Persistence failure restores the old in-memory profile and returns an error. Only the selected player is marked for full progress resynchronization/relogin, not other connected accounts. No-op operations do not schedule a resync. Audit events contain IDs/action/count/revision, not credentials or complete player snapshots.

The API is `POST /api/v1/admin/players/{id}/inventory/bulk/preview` and `/apply`. Both require the normal admin bearer token. Apply additionally requires the returned `plan_token` and `confirm_player_id`, with the unchanged action/level/rarity/revision/updated_at values. Tokens are consistency confirmations, not a substitute for authentication.

## Safety and verification

Credentials stay in the current tab session and are sent only in Authorization headers, never in URLs. Signing out clears cached player data. Advanced JSON editing remains a diagnostic escape hatch with an explicit warning and confirmation; normal changes should use Profile or Inventory.

Regression coverage includes tokenized catalog search, bulk confirmation/CAS/replay protection, all 42 linked module grants at levels 1–10, preservation of existing/equipped/unknown items, durable reload, persistence rollback, locale key parity, browser-local preference defaults/persistence and isolation of unrelated accounts/relogin transports. Run `go test ./...`, `go vet ./...` and `go test -race ./internal/api ./internal/store ./internal/catalog`.

Browser QA passed 29 scripted checks using synthetic accounts only: desktop 1440px, narrow 390px, light/dark schemes, navigation, independent searches, pagination, item dialogs, bulk preview/confirmation/conflict, invalid JSON, network errors, empty results and logout. Do not test a grant-all or revoke-all against a live player merely to validate the UI; use an isolated store/fixture.
