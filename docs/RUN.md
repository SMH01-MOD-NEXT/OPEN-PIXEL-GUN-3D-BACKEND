# Building and running the backend

The server is plain Go with no third-party dependencies and no database server
to install. One binary, one data directory, one log directory.

## Requirements

- Go 1.24 or newer (`go version`)
- A machine on the same network as the phone for LAN play, or any reachable host
  for remote play
- Nothing else: no CGO, no Docker, no Photon self-hosted licence

## 1. Build

```bash
cd "$HOME/Studio canaryProjects/OPEN-PIXEL-GUN-3D-BACKEND"
go build -o bin/opg3d-backend ./cmd/opg3d-backend
```

Check it the same way CI will:

```bash
gofmt -l .          # must print nothing
go vet ./...
go test ./...
```

Cross-compiling for a server or a router-class box needs no toolchain:

```bash
GOOS=linux GOARCH=amd64 go build -o bin/opg3d-backend-linux-amd64 ./cmd/opg3d-backend
GOOS=linux GOARCH=arm64 go build -o bin/opg3d-backend-linux-arm64 ./cmd/opg3d-backend
```

## 2. Configure

Everything is environment variables. Copy the template and fill it in:

```bash
cp .env.example .env
chmod 600 .env
```

Generate the two secrets:

```bash
openssl rand -hex 32   # -> OPG3D_APPID_KEY   (must match the APK build)
openssl rand -hex 32   # -> OPG3D_ADMIN_TOKEN (bans and account listing)
```

The variables that matter on day one:

| Variable | Default | What it does |
| --- | --- | --- |
| `OPG3D_ADDR` | `:8080` | Listen address. Keep the port in sync with what the client is told. |
| `OPG3D_DATA_DIR` | `data` | `state.json` with accounts and bans. Back this up; it is the players. |
| `OPG3D_LOG_DIR` | `logs` | One `backend-YYYY-MM-DD.log` per day. |
| `OPG3D_CONTENT_DIR` | `content` | Static remote config served under `/pixelgun3d-config/`. See `content/README.md`. |
| `OPG3D_TRACE_BODIES` | `true` | Log request and response bodies. Leave on while porting. |
| `OPG3D_DISCOVERY_PORT` | `47318` | UDP port that answers the client's `OPG3D-DISCOVER-V1` probe. |
| `OPG3D_APPID_KEY` | — | Seals Photon AppIDs. Must equal the APK's `OPG3D_APPID_KEY`. |
| `OPG3D_PHOTON_APPID_PUN` / `_REALTIME` / `_CHAT` / `_VOICE` | — | Photon Cloud AppIDs. Only the ones you set are served. PUN is a client library, not a Photon product: if `_PUN` is empty and `_REALTIME` is set, `mode=pun` is answered with the Realtime AppID (logged as `from_realtime=true`), because a PUN client connects to a Realtime application. Otherwise the client waits out its AppID timeout and the loading bar stalls around 70 %. |
| `OPG3D_PHOTON_REGION` | `eu` | Region handed to the client with the AppID. |
| `OPG3D_ADMIN_TOKEN` | empty | Empty keeps `/api/v1/admin/*` closed. |
| `OPG3D_MAINTENANCE` | `false` | Answers `ping` with maintenance so the client can say so. |
| `OPG3D_UPDATE_VERSION` / `_APK_URL` / `_SHA256` / `_NOTES_URL` / `_MANDATORY` | empty | The update feed the game reads instead of the dead store link. |

## 3. Run

```bash
set -a; . ./.env; set +a
./bin/opg3d-backend
```

The first lines tell you whether the important things are on:

```
boot accounts=0 addr=:8080 admin_enabled=true appid_sealing=true \
     data_dir=data discovery_port=47318 log_dir=logs trace_bodies=true
listening on :8080
discovery.listen name=open-pixel-gun-3d-backend port=47318
```

`appid_sealing=true` means `OPG3D_APPID_KEY` is set; `false` means the client
will not be able to get a Photon credential. Stop with Ctrl+C — shutdown is
clean and `state.json` is written atomically, so a kill never truncates it.

### Smoke test

```bash
curl -s localhost:8080/api/v1/ping; echo

R=$(curl -s -X POST localhost:8080/api/v1/accounts/register \
      -d '{"device_id":"smoke-test"}')
echo "$R"
PID=$(echo "$R" | python3 -c 'import sys,json;print(json.load(sys.stdin)["player_id"])')
TOK=$(echo "$R" | python3 -c 'import sys,json;print(json.load(sys.stdin)["session"])')

curl -s -H "Authorization: Bearer $TOK" \
  "localhost:8080/api/v1/photon/appid?mode=realtime&player_id=$PID"; echo
```

The last call returns an `AES-256-GCM` envelope whose `aad` is
`<player_id>|realtime`. The phone opens it with the build key; an envelope
issued for one player cannot be used by another.

### Let the phone find it

Discovery is UDP broadcast, so both devices must be on the same subnet and the
host firewall must let it through:

```bash
sudo firewall-cmd --add-port=8080/tcp --add-port=47318/udp   # or:
sudo ufw allow 8080/tcp && sudo ufw allow 47318/udp
```

If broadcast is blocked (many hotel and campus networks, and most Wi-Fi client
isolation setups), skip discovery and name the server on the phone:

```bash
adb shell 'echo 192.168.1.50:8080 > \
  /sdcard/Android/data/com.pixel.gun3d/files/opg3d-server.txt'
```

## 4. Administration

```bash
TOKEN=<OPG3D_ADMIN_TOKEN>

# who has registered
curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/admin/accounts

# ban a device for a day
curl -s -X POST localhost:8080/api/v1/admin/bans \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"kind":"device","subject":"<device id>","reason":"cheating","minutes":1440}'

# list and lift
curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/admin/bans
curl -s -X DELETE -H "Authorization: Bearer $TOKEN" \
  "localhost:8080/api/v1/admin/bans?kind=device&subject=<device id>"
```

`kind` is `player`, `device` or `ip`. A banned client gets `403` with the reason
and expiry, and logs it plainly instead of failing in a way the player cannot
read.

## 5. Running it as a service

```ini
# /etc/systemd/system/opg3d-backend.service
[Unit]
Description=Open Pixel Gun 3D backend
After=network-online.target

[Service]
User=opg3d
WorkingDirectory=/opt/opg3d
EnvironmentFile=/opt/opg3d/.env
ExecStart=/opt/opg3d/bin/opg3d-backend
Restart=on-failure
RestartSec=2
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/opt/opg3d/data /opt/opg3d/logs

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now opg3d-backend
journalctl -u opg3d-backend -f
```

Behind nginx or Caddy for TLS, keep the plain HTTP listener bound to localhost
and let the proxy terminate. The client speaks plain HTTP by design (that is
what removes certificate pinning from the picture), so if you put TLS in front,
give the phone the proxy's address and let it serve HTTP on the LAN, or accept
that the traffic between phone and proxy is unencrypted.

## 6. Reading the log

Every line is `key=value`, one event per line:

| Event | Meaning |
| --- | --- |
| `http.request` / `http.response` | The full exchange, with a bounded body preview. |
| `http.unhandled` | A route no handler claims yet. **This is the porting to-do list**: whatever the game asks for and does not get. |
| `account.login` | A device registered or came back. |
| `account.ban_enforced` | A ban actually stopped someone. |
| `photon.appid_issued` | A sealed credential went out. |
| `client.problem` | The game (or the native library) reported a failure. |
| `client.event` | A loading milestone from `add_event.php`: `progress=FirstRun|Loading1|loading10|loading30|ReturnOldID|...`. The most precise map of how far a boot got. |
| `client.stat` | A gameplay/diagnostic statistics batch. |
| `content.served` | Remote config answered from a file in the content directory. |
| `content.fallback_served` | Remote config answered with an empty document because no file exists yet. **This is the content to-do list.** |
| `discovery.reply` | A phone found the server by broadcast. |

The fastest way to grow the server is to play, then grep:

```bash
grep http.unhandled logs/backend-$(date +%F).log | sed 's/.*path=//' | sort | uniq -c | sort -rn
```

That list, ordered by how often the game asks, is exactly the order in which the
remaining services are worth implementing.
