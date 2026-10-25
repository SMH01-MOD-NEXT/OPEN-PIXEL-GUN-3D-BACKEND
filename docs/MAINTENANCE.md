# Scheduled maintenance

In `/admin`, open **Технические работы / Maintenance**. All form times are
**UTC**, not the browser's local time zone.

- **Save schedule** replaces the current mode with one automatic `[start, end)`
  window. At the start, ordinary game logins receive the stock 23.1.3
  TechnicalWorks HTTP-auth response (`err_code: 3000`,
  `notification: "{\"critical\":true}"`). At the end, normal profile-dependent
  login resumes. Saving a schedule also turns off the indefinite manual switch.
- **Start now** begins early while preserving a future scheduled end. Without a
  future end it enables indefinite maintenance, until explicitly stopped.
- **Stop and cancel schedule** immediately opens login and removes the window.
- In a selected player's card, beside the hub administrator role, enable
  **Allow login during maintenance** and save. This grants only a maintenance
  exception to the canonical player ID, not hub administrator rights or a ban
  exemption. Disable and save to revoke it. Existing sessions see changes on
  their next progress-status/login request; this is not a mass-disconnect tool.
  Restart the game after changing access to re-run HTTP authorization.

Settings and the allowlist persist in the existing `data/settings.json` through
serialized atomic writes. A failed save rolls back the in-memory change. Windows
are evaluated against UTC server time on requests, so a restart or idle server
cannot lose the transition. Keep the host clock synchronized. Expired windows
remain visible for inspection but are inactive. Only one window is configured
at a time; saving another replaces it.

## API

All writes below require the existing operator Bearer token and JSON content.

- `GET /api/v1/admin/settings`: effective `maintenance`, `maintenance_manual`
  and `maintenance_schedule: {start, end}`. Photon credentials remain masked.
- `PATCH /api/v1/admin/settings`: existing `maintenance` remains the manual
  switch; `maintenance_schedule: {start, end}` accepts RFC3339 timestamps with
  explicit UTC/offset. Both endpoints are required, and end must follow start.
  `{start: "", end: ""}` clears the window. Effective maintenance is manual OR
  inside the window. Send `maintenance: false` too when stopping all maintenance.
- `PUT /api/v1/admin/players/{id}/maintenance-access` with `{"enabled": true}`
  grants the exception; false revokes it. The account must exist. Player views
  include `maintenance_allowed`. The UI never replaces the whole allowlist.

Anonymous `/api/v1/ping` and `/api/v1/config` advertise the global effective
flag. A client-supplied player ID alone never bypasses that flag. The game wire
reply uses the server-resolved canonical account for the exception. Bootstrap
credentials are validated first, and bans always take priority.

The native loader has no player identity yet. `/api/v1/loader/info` therefore
keeps its compatibility `maintenance` flag false and separately advertises
`game_maintenance`. This allows signed native code to load before account
resolution; the game then applies TechnicalWorks per player. The release and
signature checks are unchanged. No frontend source changes are required.

These controls do not automatically configure the public test dates or publish
builds. They gate the existing game-login flow, not all HTTP traffic, downloads,
web hub use or already-running Photon matches.

## Verification

```sh
go test ./...
go test -race ./internal/api -run 'TestMaintenance|TestAdminSettings'
go vet ./...
go build ./...
TZ=UTC node tools/console_maintenance_test.cjs
TZ=Europe/Volgograd node tools/console_maintenance_test.cjs
```

## 23.1.3 device verification (2026-10-25)

The old `progress_status.enabled=0` response did **not** block real login:
with maintenance on and an empty allowlist, the attached A059P reached the lobby.
Socket auth `error=4` alone failed closed but displayed "No internet connection".
Do not use either result as evidence that the maintenance screen works.

Static analysis of the exact frontend `analys2313/libil2cpp.so` established:
HTTP auth callback `0x3C1483C` routes `err_code=3000` to `0x3C154AC`;
`0x33F8914` parses the `notification` JSON string and its `critical` boolean.
`AuthSceneController.Update` then enters TechnicalWorks/state 15. Keep HTTP 200,
the encrypted legacy envelope and player_id/hash fields; omit token/socket URL.

Verified locally on the attached device using the unchanged signed build 49:
manual maintenance displays the stock maintenance/offline screen; granting the
canonical ID bypass reaches the lobby; revoking it restores the stock screen;
stopping maintenance restores ordinary login. A live timed schedule also showed
normal HTTP auth before start, the stock screen inside the window, and ordinary
auth/lobby after expiry without restarting the backend. Socket auth/progress also reject
races after activation, but that fallback is not a replacement for HTTP auth.
AWS stayed stopped throughout; no frontend source or production data was changed.
