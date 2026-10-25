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
