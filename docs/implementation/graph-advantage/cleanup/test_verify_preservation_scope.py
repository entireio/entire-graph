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

    def fixture(self):
        temp=tempfile.TemporaryDirectory(); repo=Path(temp.name)
        subprocess.run(["git","init","-q"],cwd=repo,check=True)
        subprocess.run(["git","config","user.email","test@example.invalid"],cwd=repo,check=True); subprocess.run(["git","config","user.name","Test"],cwd=repo,check=True)
        (repo/"product.txt").write_text("base\n"); subprocess.run(["git","add","."],cwd=repo,check=True); subprocess.run(["git","commit","-qm","base"],cwd=repo,check=True)
        baseline=subprocess.check_output(["git","rev-parse","HEAD"],cwd=repo,text=True).strip()
        (repo/"added.txt").write_text("added\n"); subprocess.run(["git","add","."],cwd=repo,check=True); subprocess.run(["git","commit","-qm","pre"],cwd=repo,check=True)
        pre=subprocess.check_output(["git","rev-parse","HEAD"],cwd=repo,text=True).strip(); blob=subprocess.check_output(["git","hash-object","added.txt"],cwd=repo,text=True).strip()
        cleanup=repo/"docs/implementation/graph-advantage/cleanup"; cleanup.mkdir(parents=True)
        boundary={"schema":"graph-advantage-preservation-boundary-v1","baseline_commit":baseline,"precleanup_commit":pre,"added":[{"path":"added.txt","mode":"100644","git_blob":blob,"bytes":6}],"modified":[]}
        bp=cleanup/"boundary.json"; bp.write_text(json.dumps(boundary,separators=(",",":"))+"\n")
        (cleanup/"removal-map.json").write_text('{"candidates":[]}')
        scope={"baseline_commit":baseline,"precleanup_commit":pre,"added_boundary":{"count":1,"records_sha256":digest(boundary["added"])},"modified_boundary":{"count":0,"records_sha256":digest([])},"boundary_fixture":{"path":str(bp.relative_to(repo)),"sha256":__import__('hashlib').sha256(bp.read_bytes()).hexdigest()},"approved_post_precleanup_files":[],"approved_cleanup_paths":[str(bp.relative_to(repo)),str((cleanup/"removal-map.json").relative_to(repo))],"approved_retained_changes":[],"optional_local_artifacts":[]}
        sp=cleanup/"scope.json"; sp.write_text(json.dumps(scope)); return temp,repo,sp

    def test_product_change_is_rejected(self):
        temp,repo,scope=self.fixture(); self.addCleanup(temp.cleanup); (repo/"product.txt").write_text("changed\n")
        self.assertIn("change outside admitted boundary: product.txt",verify(repo,scope))

    def test_unadmitted_new_file_is_rejected(self):
        temp,repo,scope=self.fixture(); self.addCleanup(temp.cleanup); (repo/"surprise.txt").write_text("new\n")
        self.assertIn("change outside admitted boundary: surprise.txt",verify(repo,scope))

    def test_deletion_outside_removal_map_is_rejected(self):
        temp,repo,scope=self.fixture(); self.addCleanup(temp.cleanup); (repo/"added.txt").unlink()
        self.assertIn("unapproved admitted deletion: added.txt",verify(repo,scope))

    def test_executable_bit_change_is_rejected(self):
        temp,repo,scope=self.fixture(); self.addCleanup(temp.cleanup); (repo/"added.txt").chmod(0o755)
        self.assertIn("unapproved admitted change: added.txt",verify(repo,scope))


if __name__ == "__main__":
    unittest.main()
