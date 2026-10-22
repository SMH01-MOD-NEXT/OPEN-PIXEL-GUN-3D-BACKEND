# Open Pixel Gun 3D Backend

A real, self-hosted backend for legacy Pixel Gun 3D clients (first target: the
`23.1.3` ARM64 build restored by the *Old Pixel Gun 3D Online Project*).

The retired vendor services are replaced by a server you run and control:
account registration, sessions, bans, remote configuration, server time,
credential delivery and client updates. Licensed under **GPL-3.0-or-later**.

## Why this exists

The previous approach emulated the backend *inside the game process*, in C++
inside `libopg3d.so`. That works for a single-player-style restoration, but it
makes the four things this project actually needs impossible:

| Requirement | In-process C++ emulator | This server |
| --- | --- | --- |
| Full control of traffic | Every device answers itself | One authority every client talks to |
| Real account registration | Id minted on the device | Id minted and owned by the server |
| Bans / blocks | Meaningless (client-side) | Enforced server-side per player, device or IP |
| Private servers | Rebuild the APK per change | Change the server, clients keep working |

The client keeps only what a client should keep: where the server is, and how
to talk to it.

## Architecture

```
Android client (thin shim in libopg3d.so)
    |  HTTP/JSON  (server chosen at first launch: LAN scan or manual host)
    v
opg3d-backend  ──  accounts, sessions, bans, config, time, updates
    |                Photon AppID sealed with AES-256-GCM
    v
state.json (atomic writes)  +  full request/response trace log
```

Photon stays the realtime battle transport (PUN, Photon Cloud). The AppID is
**not** compiled into the APK any more: the client authenticates, asks the
backend for it, and decrypts an AES-256-GCM envelope bound to its own player
id. Rotating or revoking an AppID becomes a server-side change.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/ping` | Liveness, server name, maintenance flag |
| `GET` | `/api/v1/time` | UTC seconds for craft/upgrade timers |
| `GET` | `/api/v1/config` | Remote configuration and feature flags |
| `POST` | `/api/v1/accounts/register` | Registration + login by device id; rotates the session |
| `POST` | `/api/v1/sessions/refresh` | Validate and refresh a session |
| `GET` | `/api/v1/photon/appid` | Sealed Photon AppID (`mode=pun\|realtime\|chat\|voice`) |
| `GET` | `/api/v1/update` | Current build, APK URL, SHA-256, mandatory flag |
| `POST` | `/api/v1/problem` | Client crash/diagnostic sink, logged verbatim |
| `GET` | `/api/v1/admin/accounts` | Account list (admin token) |
| `GET/POST/DELETE` | `/api/v1/admin/bans` | Ban management (admin token) |

Legacy compatibility routes (`/auth_v2/`, `/time`, `/problem`) are served so the
stock client can be pointed here by URL rewriting alone. A rewritten path may
carry the original host as its first segment (`/@server-v2.pixelgun3dserver.com/auth_v2/`);
the server strips it.

Every unknown path the client requests is logged as `http.unhandled` — that log
is the to-do list for endpoint coverage.

## LAN discovery

The client can offer "search on the local network" instead of typing an address.
It broadcasts `OPG3D-DISCOVER-V1` on UDP (default port `47318`); every backend
on the segment answers with a JSON descriptor, and the client learns the address
from the datagram source.

## Configuration

All configuration is environment-based; no credential is ever committed.

| Variable | Default | Meaning |
| --- | --- | --- |
| `OPG3D_ADDR` | `:8080` | HTTP listen address |
| `OPG3D_SERVER_NAME` | `open-pixel-gun-3d-backend` | Name shown to discovery clients |
| `OPG3D_DATA_DIR` | `data` | State directory (`state.json`) |
| `OPG3D_LOG_DIR` | `logs` | Log directory; empty = stdout only |
| `OPG3D_TRACE_BODIES` | `true` | Log request/response bodies |
| `OPG3D_MAX_BODY_LOG` | `8192` | Per-body log cap in bytes |
| `OPG3D_DISCOVERY_PORT` | `47318` | UDP discovery port; `0` disables |
| `OPG3D_ADMIN_TOKEN` | — | Bearer token for `/admin/*`; unset = closed |
| `OPG3D_MAINTENANCE` | `false` | Advertise maintenance mode |
| `OPG3D_APPID_KEY` | — | 64 hex chars, or a passphrase (hashed) |
| `OPG3D_PHOTON_APPID_PUN` | — | PUN AppID |
| `OPG3D_PHOTON_APPID_REALTIME` | — | Realtime AppID |
| `OPG3D_PHOTON_APPID_CHAT` / `_VOICE` | — | Optional |
| `OPG3D_PHOTON_REGION` | `eu` | Region pinned for all clients |
| `OPG3D_UPDATE_VERSION` | — | Current client version |
| `OPG3D_UPDATE_APK_URL` | — | Download URL handed to players |
| `OPG3D_UPDATE_SHA256` | — | APK checksum |
| `OPG3D_UPDATE_NOTES_URL` | — | Release notes |
| `OPG3D_UPDATE_MANDATORY` | `false` | Force the update |

## Run

Step-by-step Docker, native build, configuration, backup and log-reading
instructions live in [`docs/RUN.md`](docs/RUN.md).

### Docker Compose (portable path)

```sh
cp .env.example .env
# Fill OPG3D_APPID_KEY, a Photon AppID and OPG3D_ADMIN_TOKEN in .env.
docker compose up --build -d
docker compose ps
curl http://127.0.0.1:8080/api/v1/ping
```

The image is built in two stages, runs as an unprivileged user, and contains no
`.env`, player state, logs or locally downloaded vendor bundles. Compose keeps
state, logs and fetched bundles in named volumes across container upgrades.

### Native Go build

```sh
go test ./...
go build -o bin/opg3d-backend ./cmd/opg3d-backend

OPG3D_APPID_KEY='choose-a-long-passphrase' \
OPG3D_PHOTON_APPID_PUN='<pun app id>' \
OPG3D_ADMIN_TOKEN='choose-an-admin-token' \
  ./bin/opg3d-backend
```

Ban a device for a day:

```sh
curl -X POST http://127.0.0.1:8080/api/v1/admin/bans \
  -H 'Authorization: Bearer <admin token>' \
  -d '{"kind":"device","subject":"<device id>","reason":"cheating","minutes":1440}'
```

## Storage

Phase 1 keeps state in one JSON document written through a temporary file plus
`rename()`, so a crash cannot leave a half-written file. The storage engine sits
behind `internal/store`; moving to SQLite or Postgres is a change to that
package only.

## Status

Phase 1 (this commit): accounts, sessions, bans, time, config, sealed AppID
delivery, updates, problem sink, LAN discovery, full traffic tracing.

See `TASK_PLAN.md` for what comes next.

## Legal

Not affiliated with Cubic Games, Rilisoft, Lightmap or Photon. This project
implements a server for a discontinued client whose services no longer exist;
it contains no vendor code or assets.

Native APK launcher/update publication: [signed frontend releases](docs/NATIVE_FRONTEND_RELEASES.md).

Official AWS deployment and SSH-only administration: [Sweden operations](docs/AWS_SWEDEN.md).
