# Official Sweden deployment

Endpoint: `http://13.51.187.37:8080`, country `SE`, client23.1.3/ARM64.
EC2 region `eu-north-1`, Debian13/x86_64. Photon Cloud remains external.

## Layout and security

- `/opt/opg3d/bin/opg3d-backend`: statically linked tested Go executable; no Go compiler/runtime installation required.
- `/opt/opg3d/content`: stock configurations, required assets and signed native releases. Signing private key is NOT deployed.
- `/opt/opg3d/data`: current account/profile/settings snapshot, separate from executable/content.
- `/etc/opg3d/backend.env`: restricted root/opg3d configuration, original compatible Photon sealing key and credentials. Body traces and deterministic diagnostic sessions disabled.
- `opg3d-backend.service`: unprivileged, restart-on-failure, loopback18080, restricted writable paths,512MiB memory limit.
- `opg3d-gateway.service`: separate Caddy instance on8080 with its admin API disabled. Denies `/admin` and `/api/v1/admin/*` publicly. Does not modify the pre-existing Caddy or game services.
- Only TCP8080 added to EC2 security group. No public backend18080 or UDP discovery port is required; Photon game ports are managed by Photon Cloud.
- Game protocol remains HTTP, not TLS. Signed frontend updates authenticate code but do not encrypt account traffic. Do not send admin credentials over the public endpoint.

## Admin and health

```sh
ssh -i "$HOME/.ssh/aws-eu.pem" -N -L 18081:127.0.0.1:18080 admin@13.51.187.37
# Open http://127.0.0.1:18081/admin on that computer.
# Authenticate with the existing admin token; never put it in public docs.

curl -f http://13.51.187.37:8080/api/v1/ping
curl -f http://13.51.187.37:8080/api/v1/loader/info
ssh -i "$HOME/.ssh/aws-eu.pem" admin@13.51.187.37 \
  'sudo systemctl status opg3d-backend opg3d-gateway --no-pager'
```

## Persistence, logs and backups

`opg3d-backup.timer` creates validated JSON backups daily around03:10UTC,
retaining14 snapshots under `/var/backups/opg3d` (root-only). These include all
JSON state/settings files; backups on the same EC2 disk are NOT disaster recovery.
Export them off-instance separately. Logrotate retains bounded compressed log
rotations; body traces are disabled for the public server.

```sh
sudo systemctl start opg3d-backup.service
sudo journalctl -u opg3d-backend -u opg3d-gateway -n100 --no-pager
sudo systemctl list-timers opg3d-backup.timer
```

The initial copy preserves existing players, purchased pass/premium, tasks,
statistics and command receipts. Local and AWS state are independent after this
snapshot: use the official server for continued play. Never overwrite live AWS
state with an older local copy during an executable/content update.

## Updating

Stop/backup before replacing an executable; preserve `/opt/opg3d/data` and
`/etc/opg3d/backend.env`. Install the tested linux/amd64 binary atomically and
restart only `opg3d-backend.service`; check internal and public health. Keep the
previous binary for rollback. Do not roll back player data with a binary rollback.
Publish signed native client releases offline using `opg3d-release`; copy binary,
public key and finally `active.json` atomically into `content/frontend`.
Do not copy private signing keys to the server.

Deployment templates are in `deploy/aws-sweden/` and contain no secrets. A fresh
host needs Caddy, Python3 and logrotate; the application is statically linked.

## Native launcher

The permanent native launcher seeds `13.51.187.37:8080` automatically, including
on fresh installs. The official badge is based on this compiled endpoint, not a
server-supplied claim; country is pinnedSE and localized by the UI. Manual/LAN
servers remain usable and undergo the same version/signature checks.

An existing APK must receive the newly built permanent `libopg3d.so` once to get
this built-in entry. Old launchers can already connect manually. Downloadable
`libopg3d_client.so` updates cannot replace the APK-resident launcher itself.
The address already has an Elastic IP (`eipalloc-014bea790bc0e10e6`); the existing
allocation is reused unchanged. No new Elastic IP is provisioned. Keep that
allocation associated with the instance so the compiled endpoint stays valid.

## Current gameplay release — build33

User-approved signed native publication on2026-10-23: build33, version `23.1.3-bounded-bot-aim`, SHA256 `170c1211bbb5107c7f02fb641caa3a2bbe68c81e0d96859499a0b134d4c20664`,461680bytes. Includes verified online Escort bots/objective movement and smoother, more accurate stock bot tracking across modes, with stronger Team Strike/Escort tuning. Frontend implementation commits: `a358600` and `6964a74`.

Published artifact first and active.json last, atomically. Public manifest signature, artifact size/hash, ping200 and loader-info200 verified; `/admin` and `/api/v1/admin/state` remain404. Backend/gateway PIDs22117/21993 unchanged and both active. No backend restart, executable/configuration replacement or account-data copy.

Retained previous build20 artifact, with manifest/key backup and pre-signed monotonic rollback34 under root-only `/var/backups/opg3d/aws-release33-20261023-080204`. Rollback not published. Do not downgrade active build numbers or restore old player state as a code rollback. Clients download33 at their next full launcher start; no running-game hot swap or APK reinstall.
