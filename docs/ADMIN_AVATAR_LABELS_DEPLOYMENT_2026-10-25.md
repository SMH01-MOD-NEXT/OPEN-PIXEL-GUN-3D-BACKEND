# Admin avatar category labels — AWS deployment

- Code commit: `0594d65`; runtime: `23.1.3-hub-0594d65`.
- Built from isolated `git archive 0594d65`, static Linux/amd64.
- Binary SHA-256: `184053e31ebb9a2749ae5aa2639878c79e6c754791a76f255ca5ae11d3f7d30f`.
- Root-only EC2 backup: `/var/backups/opg3d/avatar-labels-20261025T074626Z-0594d65`.
- Restarted only `opg3d-backend.service`; backend PID after deployment: `30280`.
- Complete stopped-service data backup, old binary, environment and frontend manifest retained. Every current data file was byte-identical immediately after startup. One current account/profile was preserved; no older local or historical snapshot was imported.
- Environment (including keys), scheduled campaigns and frontend active manifest unchanged. Caddy and game gateway PIDs unchanged; all three services active. Support administrator role for player 740333804 retained.
- Private console HTML verified to contain the RU/EN avatar category labels and actual avatar-specific rendering condition. Inventory type, technical tag and ownership logic unchanged.
- Public HTTPS ping and HUB: 200. Public `/admin` and `/api/v1/admin/state`: 404.
- Isolated-source verification: full Go tests, vet and all nine Node console harnesses passed; static backend build passed. API/store race tests also passed before release. Native grant success was confirmed by the user; this release changes only operator-console labels, not the Android game frontend.
- Binary-only rollback: restore the backed-up executable after stopping the backend, then start and verify health. Never restore old player data as part of code rollback.
