#!/usr/bin/python3
"""Run with python3 -m unittest discover -s deploy/aws-sweden."""
import json
import pathlib
import subprocess
import tempfile
import unittest

class BackupTests(unittest.TestCase):
 def test_snapshot_retention_permissions_and_invalid_input(self):
  source=pathlib.Path(__file__).with_name('backup.py').read_text()
  with tempfile.TemporaryDirectory() as tmp:
   root=pathlib.Path(tmp);data=root/'data';backups=root/'backups'
   data.mkdir();backups.mkdir()
   (data/'state.json').write_text('{"profiles":{}}')
   (data/'settings.json').write_text('{}')
   for i in range(16):(backups/f'20200101T0000{i:02}Z').mkdir()
   runner=root/'backup.py'
   runner.write_text(source.replace('/opt/opg3d/data',str(data)).replace('/var/backups/opg3d',str(backups)))
   subprocess.run(['python3',str(runner)],check=True)
   folders=sorted(backups.iterdir());self.assertEqual(len(folders),14)
   latest=folders[-1]
   self.assertEqual(json.loads((latest/'state.json').read_text()),{'profiles':{}})
   self.assertTrue((latest/'settings.json').is_file())
   self.assertEqual((latest/'state.json').stat().st_mode & 0o077,0)
   before={p.name for p in folders}
   (data/'state.json').write_text('invalid json')
   result=subprocess.run(['python3',str(runner)],capture_output=True)
   self.assertNotEqual(result.returncode,0)
   self.assertEqual({p.name for p in backups.iterdir()},before)

if __name__=='__main__':unittest.main()
