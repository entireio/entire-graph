import json, tempfile, unittest
from pathlib import Path
import sys
HERE=Path(__file__).resolve().parent; sys.path.insert(0,str(HERE))
from validate_archive_operational import validate

class TestOperationalValidation(unittest.TestCase):
 def test_accepts_redacted_record_and_rejects_path(self):
  with tempfile.TemporaryDirectory() as d:
   p=Path(d)/"x.json"; base={"schema":"x","records":[{"source_sha256":"a"*64,"kind":"operational_json","facts":{"status":"failed","path":"<redacted-path>"}}]}; p.write_text(json.dumps(base)); self.assertEqual(validate(p),[])
   base["records"][0]["facts"]["bad"]="/Users/private"; p.write_text(json.dumps(base)); self.assertTrue(validate(p))
if __name__=='__main__': unittest.main()
