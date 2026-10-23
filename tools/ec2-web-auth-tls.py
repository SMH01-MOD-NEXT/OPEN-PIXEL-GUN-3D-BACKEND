#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
"""Add an isolated Certbot webroot/vhost to the existing EC2 Caddy JSON.
Run as root: prepare; after successful Certbot issuance: activate.
Never replaces unrelated routes, services, automation policies or certificates.
"""
import datetime
import grp
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

DOMAIN = "opg3d.smh01.net.eu.org"
CONFIG = Path("/etc/caddy/caddy.json")
WEBROOT = Path("/var/www/opg3d-acme")
TLS_DIR = Path("/etc/caddy/opg3d-tls")
LINEAGE = Path("/etc/letsencrypt/live") / DOMAIN


def main():
    if os.geteuid() != 0 or sys.argv[1:] not in (["prepare"], ["activate"]):
        raise SystemExit("Run as root: ec2-web-auth-tls.py prepare|activate")
    active = sys.argv[1] == "activate"
    old_bytes = CONFIG.read_bytes()
    cfg = json.loads(old_bytes)
    http = cfg["apps"]["http"]["servers"]
    # Fail rather than invent a replacement if the shared deployment changed.
    if "fwd" not in http or ":443" not in http["fwd"]["listen"]:
        raise SystemExit("Shared Caddy structure changed; inspect before editing")
    WEBROOT.mkdir(parents=True, exist_ok=True)
    (WEBROOT / ".well-known/acme-challenge").mkdir(parents=True, exist_ok=True)
    for path in [WEBROOT, WEBROOT / ".well-known", WEBROOT / ".well-known/acme-challenge"]:
        os.chmod(path, 0o755)
    acme = {"@id": "opg3d-acme-webroot", "match": [{"host": [DOMAIN], "path": ["/.well-known/acme-challenge/*"]}],
            "handle": [{"handler": "file_server", "root": str(WEBROOT)}], "terminal": True}
    if "opg3d-http" not in http:
        http["opg3d-http"] = {"listen": [":80"], "routes": []}
    elif http["opg3d-http"]["listen"] != [":80"]:
        raise SystemExit("Unexpected opg3d HTTP listener")
    routes = http["opg3d-http"]["routes"]
    routes[:] = [r for r in routes if r.get("@id") not in ["opg3d-acme-webroot", "opg3d-http-redirect"]]
    routes.insert(0, acme)
    if active:
        TLS_DIR.mkdir(mode=0o750, exist_ok=True)
        gid = grp.getgrnam("caddy").gr_gid
        os.chown(TLS_DIR, 0, gid)
        os.chmod(TLS_DIR, 0o750)
        for name in ["fullchain.pem", "privkey.pem"]:
            # Certbot files stay private; Caddy receives a dedicated restricted copy.
            source = LINEAGE / name
            if not source.is_file():
                raise SystemExit("Certificate missing; run Certbot webroot issuance first")
            target = TLS_DIR / name
            fd, tmp = tempfile.mkstemp(dir=TLS_DIR, prefix=".renew-")
            try:
                with os.fdopen(fd, "wb") as f:
                    f.write(source.read_bytes())
                    f.flush()
                    os.fsync(f.fileno())
                os.chown(tmp, 0, gid)
                os.chmod(tmp, 0o640)
                os.replace(tmp, target)
            finally:
                if os.path.exists(tmp):
                    os.unlink(tmp)
        tls = cfg["apps"].setdefault("tls", {})
        certs = tls.setdefault("certificates", {}).setdefault("load_files", [])
        certs[:] = [c for c in certs if "opg3d-certbot" not in c.get("tags", [])]
        certs.append({"certificate": str(TLS_DIR / "fullchain.pem"), "key": str(TLS_DIR / "privkey.pem"), "tags": ["opg3d-certbot"]})
        vhost = {"@id": "opg3d-web-auth", "match": [{"host": [DOMAIN]}], "terminal": True, "handle": [{"handler": "subroute", "routes": [
            {"match": [{"path": ["/admin", "/admin/*", "/api/v1/admin", "/api/v1/admin/*"]}], "handle": [{"handler": "static_response", "status_code": 404}], "terminal": True},
            {"match": [{"path": ["/"]}], "handle": [{"handler": "static_response", "status_code": 302, "headers": {"Location": ["/account/login"]}}], "terminal": True},
            {"handle": [{"handler": "headers", "response": {"set": {"Strict-Transport-Security": ["max-age=86400"]}}}, {"handler": "reverse_proxy", "upstreams": [{"dial": "127.0.0.1:18080"}]}]}
        ]}]}
        https_routes = http["fwd"]["routes"]
        https_routes[:] = [r for r in https_routes if r.get("@id") != "opg3d-web-auth"]
        https_routes.append(vhost)
        routes.append({"@id": "opg3d-http-redirect", "match": [{"host": [DOMAIN]}], "handle": [{"handler": "static_response", "status_code": 308, "headers": {"Location": ["https://" + DOMAIN + "{http.request.uri}"]}}], "terminal": True})
    # Preserve every unrelated original route exactly, including order/content.
    original = json.loads(old_bytes)
    unrelated_before = [r for r in original["apps"]["http"]["servers"]["fwd"]["routes"] if r.get("@id") != "opg3d-web-auth"]
    unrelated_after = [r for r in http["fwd"]["routes"] if r.get("@id") != "opg3d-web-auth"]
    assert unrelated_before == unrelated_after
    if cfg == original:
        subprocess.run(["systemctl", "reload", "caddy"], check=True)
        print("Caddy configuration unchanged; reloaded certificate")
        return
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
    backup_dir = Path("/var/backups/opg3d") / ("tls-" + stamp)
    backup_dir.mkdir(parents=True, mode=0o700)
    os.chmod(backup_dir, 0o700)
    shutil.copy2(CONFIG, backup_dir / "caddy.json")
    fd, tmp = tempfile.mkstemp(dir=CONFIG.parent, prefix=".opg3d-")
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(cfg, f, indent=2)
            f.write("\n")
            f.flush()
            os.fsync(f.fileno())
        os.chmod(tmp, CONFIG.stat().st_mode & 0o777)
        subprocess.run(["caddy", "validate", "--config", tmp], check=True, stdout=subprocess.DEVNULL)
        os.replace(tmp, CONFIG)
        try:
            subprocess.run(["systemctl", "reload", "caddy"], check=True)
        except Exception:
            shutil.copy2(backup_dir / "caddy.json", CONFIG)
            subprocess.run(["systemctl", "reload", "caddy"], check=True)
            raise
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)
    print("Caddy " + ("HTTPS vhost activated" if active else "ACME webroot prepared") + "; shared routes preserved")


if __name__ == "__main__":
    main()
