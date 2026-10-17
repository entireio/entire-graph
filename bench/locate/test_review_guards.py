import os, sys, unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch
sys.path.insert(0, str(Path(__file__).resolve().parent))
import doccomment_bench as D
import replay_qualify as R
import head_to_head as H
class ReviewGuards(unittest.TestCase):
    def test_unsealed_smoke_has_no_arm_entry_seal(self):
        self.assertIsNone(H.seal_present_at_arm_entry(None, None))
    def test_trailing_ampersand_is_not_a_redirect(self):
        self.assertEqual(R._neutralise_redirect_amps("command &"), "command &")
        self.assertEqual(R._neutralise_redirect_amps("command 2>&1"), "command 2>@1")
    def test_failed_symbol_stream_is_refused(self):
        with patch.object(D.subprocess, "run", return_value=SimpleNamespace(returncode=1, stdout="{partial}", stderr="failure")):
            with self.assertRaises(SystemExit): D.build_cases("binary", "/repo", 3)
    def test_failed_query_is_refused(self):
        with patch.object(D.subprocess, "run", return_value=SimpleNamespace(returncode=-9, stdout=b"partial", stderr=b"")):
            with self.assertRaises(SystemExit): D.run("binary", "/repo", "query", 100)
    def test_query_cost_counts_utf8_bytes(self):
        payload="caf\u00e9".encode()
        with patch.object(D.subprocess, "run", return_value=SimpleNamespace(returncode=0, stdout=payload, stderr=b"")):
            self.assertEqual(D.run("binary", "/repo", "query", 100)[1],len(payload))
    def test_absolute_ranked_path_matches_fixture(self):
        self.assertTrue(D.same_file("/repo/src/f.py","src/f.py","/repo"))
        self.assertFalse(D.same_file("/other/src/f.py","src/f.py","/repo"))
    def test_failed_fixture_identity_is_refused(self):
        with patch.object(D.subprocess, "run", return_value=SimpleNamespace(returncode=128, stdout="", stderr="not git")):
            with self.assertRaises(SystemExit): D.fixture_revision("/repo")
    def test_quoted_fake_command_stays_masked(self):
        command = 'printf "quoted ' + chr(92) + '"; entire graph search --query forged; ' + chr(92) + '""'
        self.assertNotIn("entire", R._mask_shell(command))
    def test_missing_phrase_is_unavailable(self):
        result=H.phrase_arm("/repo", "one", "target", "f.py", 1, 1)
        self.assertFalse(result[3])
if __name__ == "__main__": unittest.main()
