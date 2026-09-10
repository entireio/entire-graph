import hashlib, json, stat, sys, tarfile, tempfile
from pathlib import Path
import unittest
HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from audit_archive_provenance import verify

class TestArchiveProvenance(unittest.TestCase):
    def test_verifies_member_hash_mode_and_replacement(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d); f = root / "retained.txt"; f.write_text("kept\n"); f.chmod(0o644)
            archive = root / "bundle.tar.gz"
            with tarfile.open(archive, "w:gz") as t: t.add(f, arcname="member.txt")
            payload = f.read_bytes(); digest = hashlib.sha256(payload).hexdigest()
            p = root / "provenance.json"; p.write_text(json.dumps({"archives":[{"archive_path":"bundle.tar.gz","archive_sha256":hashlib.sha256(archive.read_bytes()).hexdigest(),"member_count":1,"members":[{"member":"member.txt","sha256":digest,"member_mode":stat.S_IMODE(f.stat().st_mode),"replacement_paths":["retained.txt"],"replacement_modes":{"retained.txt":stat.S_IMODE(f.stat().st_mode)}}]}]}))
            self.assertEqual(verify(root, p), [])

    def test_deleted_archive_reconstructs_from_pinned_tree_in_offline_mode(self):
        # A missing current archive is recoverable from the declared Git ref only
        # for offline reproduction; normal validation still requires replacements.
        with tempfile.TemporaryDirectory() as d:
            root = Path(d); f = root / "retained.txt"; f.write_text("kept\n")
            archive = root / "bundle.tar.gz"
            with tarfile.open(archive, "w:gz") as t: t.add(f, arcname="member.txt")
            self.assertTrue(archive.is_file())
            # The normal verifier rejects a missing replacement rather than using a
            # stale pinned-tree fallback. This synthetic case has no Git ref.
            archive.unlink()
            p = root / "provenance.json"; p.write_text(json.dumps({"archives":[{"archive_path":"bundle.tar.gz","archive_sha256":"0"*64,"member_count":1,"members":[]}]}))
            self.assertTrue(verify(root, p))

    def test_missing_live_replacement_is_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d); f = root / "retained.txt"; f.write_text("kept\n")
            archive = root / "bundle.tar.gz"
            with tarfile.open(archive, "w:gz") as t: t.add(f, arcname="member.txt")
            payload = f.read_bytes(); digest = hashlib.sha256(payload).hexdigest()
            p = root / "provenance.json"; p.write_text(json.dumps({"archives":[{"archive_path":"bundle.tar.gz","archive_sha256":hashlib.sha256(archive.read_bytes()).hexdigest(),"member_count":1,"members":[{"member":"member.txt","sha256":digest,"member_mode":stat.S_IMODE(f.stat().st_mode),"replacement_paths":["missing.txt"],"replacement_modes":{}}]}]}))
            self.assertTrue(verify(root, p))

    def test_chain_requires_live_file_and_source_mapping(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d); source = root / "source.txt"; source.write_text("original\n")
            archive = root / "bundle.tar.gz"
            with tarfile.open(archive, "w:gz") as t: t.add(source, arcname="member.txt")
            replacement = root / "summary.json"; replacement.write_text('{"summary":true}\n')
            source_sha = hashlib.sha256(source.read_bytes()).hexdigest(); retained_sha = hashlib.sha256(replacement.read_bytes()).hexdigest()
            removal = {"candidates":[{"path":"source.txt","source_sha256":source_sha,"disposition":"applied","replacement":{"path":"summary.json","retained_sha256":retained_sha}}]}
            (root / "removal-map.json").write_text(json.dumps(removal))
            member = {"member":"member.txt","sha256":source_sha,"member_mode":0o644,"replacement_paths":[],"replacement_modes":{},"canonical_replacement_chains":[{"path":"summary.json","current_mode":0o644,"current_sha256":retained_sha,"declared_retained_sha256":retained_sha,"source_map_path":"source.txt"}]}
            data = {"removal_map_path":"removal-map.json","archives":[{"archive_path":"bundle.tar.gz","archive_sha256":hashlib.sha256(archive.read_bytes()).hexdigest(),"member_count":1,"members":[member]}]}
            p = root / "provenance.json"; p.write_text(json.dumps(data))
            self.assertEqual(verify(root, p), [])
            replacement.unlink()
            self.assertTrue(verify(root, p))

    def test_evidence_record_can_prove_projected_member(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d); source = root / "source.txt"; source.write_text("original\n")
            archive = root / "bundle.tar.gz"
            with tarfile.open(archive, "w:gz") as t: t.add(source, arcname="member.txt")
            source_sha = hashlib.sha256(source.read_bytes()).hexdigest()
            evidence = root / "mapping.json"
            evidence.write_text(json.dumps({"records":[{"archive_path":"bundle.tar.gz","member":"member.txt","source_sha256":source_sha,"facts":{"preserved":True}}]}))
            evidence_sha = hashlib.sha256(evidence.read_bytes()).hexdigest()
            member = {"member":"member.txt","sha256":source_sha,"member_mode":0o644,"replacement_paths":[],"replacement_modes":{},"canonical_replacement_chains":[{"path":"mapping.json","evidence_record_path":"mapping.json","current_mode":0o644,"current_sha256":evidence_sha,"declared_retained_sha256":evidence_sha}]}
            data = {"archives":[{"archive_path":"bundle.tar.gz","archive_sha256":hashlib.sha256(archive.read_bytes()).hexdigest(),"member_count":1,"members":[member]}]}
            p = root / "provenance.json"; p.write_text(json.dumps(data))
            self.assertEqual(verify(root, p), [])
            evidence.write_text('{"records":[]}')
            self.assertTrue(verify(root, p))

if __name__ == "__main__": unittest.main()
