# Scheduled maintenance — AWS deployment, 2026-10-25

User-approved executable-only update of the official Sweden backend.

- Source commit: `89c45f7` (isolated `git archive` build).
- Runtime build stamp: `23.1.3-maintenance-89c45f7`.
- Static Linux/amd64 executable SHA-256:
  `2d354c2ab6535f91a20a9c6b23a42b64a80e561e2477792fc7246ed9b0987c2a`.
- Root-only backup: `/var/backups/opg3d/maintenance-20261025T142143Z-89c45f7`.
  Contains previous executable, complete stopped-service data directory,
  environment, frontend active manifest, checksums and pre-update service PIDs.
- Stopped only `opg3d-backend.service`, installed executable atomically and
  restarted it. Current backend PID: `30628`; active/running, `NRestarts=0`.
- Gateway PID `21993` and Caddy PID `10286` unchanged, both active.
- All six live data files were byte-identical after startup; no local account
  snapshot imported. Environment and frontend active manifest hashes unchanged.
- Private operator settings verified using the existing credential without
  displaying it. Maintenance remains **off**, schedule empty. Maintenance tab,
  schedule controls and per-player allowlist controls present in served console.
  No settings or allowlist write was made to production.
- Internal ping/loader healthy; signed frontend release available. Public HTTP
  and HTTPS ping/loader: 200. HTTPS hub: 200. Public `/admin` and
  `/api/v1/admin/state`: 404. Private unauthenticated admin settings: 401.
- Isolated source verification: full Go tests, full race suite, vet and all
  console/hub Node regression scripts passed before static build/deployment.
- No real-device login test: no ADB device was connected. No frontend publication,
  release-date scheduling or maintenance activation was performed.

## Binary-only rollback

Stop `opg3d-backend.service`, install the backup's `opg3d-backend.previous` through
an atomic rename to `/opt/opg3d/bin/opg3d-backend` (root:root, 0755), then start
only the backend and verify private/public health. **Do not restore old player
state, environment or frontend as part of a binary rollback.** An automatic
binary rollback guarded deployment until private validation completed; it was
not needed.

## Superseding fixed-maintenance deployment — 2026-10-25, 15:15 UTC

The preceding record describes the first deployment, not the current state.
After real-device diagnosis and an explicitly authorized restart, deployed:

- Source commit: `78fe484`; isolated git-archive build, full Go tests, full race
  suite and vet passed. Frontend build 49 was already validated locally and was
  not replaced by this deployment.
- Runtime stamp: `23.1.3-maintenance-fix-78fe484`.
- Static Linux/amd64 executable SHA-256:
  `8da415a026020d9adcb8ff2f7862edc280d3d7cb0ae361b21063a1eaaa8998c8`.
- Root-only stopped-service backup:
  `/var/backups/opg3d/maintenance-fix-20261025T151405Z-78fe484`.
  Contains previous binary, complete data, environment, active frontend manifest,
  before-hashes and the final deployment report. No local player snapshot was
  imported.
- Settings were committed atomically **before** startup: manual `maintenance`
  false; schedule start `2026-10-25T15:14:05Z`, end
  `2026-10-26T12:00:00Z` (15:00 Moscow); allowlist `{}`. Thus maintenance is
  effective immediately but does not remain manually latched after the deadline.
  Windows are evaluated on requests and persist through restarts.
- Final backend PID `31156`, active/running, `NRestarts=0`. Gateway PID `21993`
  and Caddy PID `10286` unchanged.
- Private legacy binary HTTP auth using an existing canonical account returned
  HTTP 200, `err_code=3000`, a JSON-string notification with `critical=true`,
  preserved hash and no token/socket URL. No allowlist grant or temporary test
  account was created. This checks the deployed protocol, not a new AWS device run.
- Private authenticated settings confirm effective maintenance true, manual
  false and the exact deadline. The settings API intentionally does not expose
  the whole allowlist; emptiness was checked in the persisted settings file.
- Public HTTP and HTTPS ping/loader: 200; ping advertises maintenance true,
  loader advertises game_maintenance true while allowing native loading.
  Public `/admin`, `/api/v1/admin/settings` and `/api/v1/admin/state`: 404.
  Private unauthenticated settings: 401. Existing SSH-tunnel access preserved.
- All data-file checksums except the intentionally edited settings file match
  the pre-start backup; environment and frontend active manifest also unchanged.
- Initial verification harness assertions incorrectly expected an allowlist
  field in the settings API and 401 for the nonexistent private `/admin/state`
  API route. Each failed check stopped the service fail-closed. Corrected the
  harness against the actual API and completed verification; no application
  change or binary rollback was needed.

**Rollback safety:** the previous executable is known not to enforce the stock
maintenance screen reliably. If this deployment fails, stop the backend and
leave it stopped until a verified maintenance-capable build is installed. Do
not restart the old executable assuming maintenance protects the opening time.
Never restore stale player data as part of an executable rollback.
