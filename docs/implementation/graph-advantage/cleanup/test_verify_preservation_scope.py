import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from verify_preservation_scope import digest, verify


class PreservationScopeTest(unittest.TestCase):
    def test_committed_scope_verifies_current_boundary(self):
        self.assertEqual(verify(HERE.parents[3], HERE / "preservation-scope.json"), [])

    def test_digest_is_order_and_content_sensitive(self):
        records = [{"path": "a", "mode": "100644", "git_blob": "1", "bytes": 1}]
        self.assertNotEqual(digest(records), digest([{**records[0], "bytes": 2}]))
        self.assertNotEqual(digest(records), digest([records[0], records[0]]))


if __name__ == "__main__":
    unittest.main()
