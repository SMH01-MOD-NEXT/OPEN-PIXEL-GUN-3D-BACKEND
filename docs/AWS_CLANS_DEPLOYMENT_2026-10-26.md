# Clans - AWS deployment, 2026-10-26

User-approved update of the official Sweden backend after the user confirmed
clans work on a local device run.

- Source commit: `b2f982d` (isolated `git archive` build). `go vet`, full
  `go test` and full `go test -race` passed (14 packages each).
- Runtime stamp: `23.1.3-clans-b2f982d`.
- Static Linux/amd64 executable SHA-256:
  `e715f55d1798431e2a9d7abcf70d10178b0aae500fbedf5f4105190495b67d61`.
- New content files in `/opt/opg3d/content/sio-configs` (root:opg3d 0644):
  `ClanCommon.json` `6d3be49c...cd71cd5`, `ClanBuildings.json`
  `4927c95f...76d317b4fd` (as in the repository). All other hosted
  configs were already identical to the repository.
- Root-only stopped-service backup:
  `/var/backups/opg3d/clans-20261026T091635Z-b2f982d` (previous executable,
  complete data directory, environment, frontend active manifest, before/after
  hashes, PIDs).
- Backend PID `32478`, active, `NRestarts=0`. Gateway PID `21993` and Caddy PID
  `10286` unchanged. Environment and frontend active manifest unchanged.
- Every data file (including `settings.json`) is byte-identical before stop and
  after start: the maintenance window ending `2026-10-26T12:00:00Z`, the manual
  latch (off) and the operator's allowlist entry are preserved. Clan state is
  added to `state.json` on the first clan mutation.
- Public HTTP ping/loader and HTTPS ping/hub: 200, maintenance still advertised.
  Public `/admin` and `/api/v1/admin/state`: 404. No panic/fatal in the journal.
- Not tested on a device against AWS (maintenance is active until 12:00 UTC).

## Rollback

Stop `opg3d-backend.service`, atomically install the backup's
`opg3d-backend.previous` (root:root 0755), remove the two clan configs from
`/opt/opg3d/content/sio-configs` and start only the backend. Never restore old
player data as part of an executable rollback (clans created after deployment
would be lost). The previous executable `8da415a0...` is the verified
maintenance-capable build.
