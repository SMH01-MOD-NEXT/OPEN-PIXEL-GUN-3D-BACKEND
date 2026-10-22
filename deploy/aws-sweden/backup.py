#!/usr/bin/python3
"""Atomic JSON snapshots; keep fourteen daily backups without pausing service."""
import datetime, json, os, pathlib, shutil
src=pathlib.Path('/opt/opg3d/data')
dest=pathlib.Path('/var/backups/opg3d')
os.umask(0o077)
dest.mkdir(parents=True,exist_ok=True)
stamp=datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
folder=dest/(stamp+'.tmp');folder.mkdir()
try:
 for path in src.glob('*.json'):
  raw=path.read_bytes();json.loads(raw)
  out=folder/path.name
  with out.open('wb') as handle:
   handle.write(raw);handle.flush();os.fsync(handle.fileno())
 if not (folder/'state.json').exists():raise RuntimeError('state missing')
 folder.rename(dest/stamp)
except Exception:
 shutil.rmtree(folder);raise
for old in sorted(p for p in dest.iterdir() if p.is_dir() and not p.name.endswith('.tmp'))[:-14]:shutil.rmtree(old)
