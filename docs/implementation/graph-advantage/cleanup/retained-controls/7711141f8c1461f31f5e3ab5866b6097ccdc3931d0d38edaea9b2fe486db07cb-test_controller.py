import json
import tarfile
import unittest
from pathlib import Path

import controller


class DispatchPreparationTests(unittest.TestCase):
    def test_positive_script_and_collector_paths(self):
        manifest = controller.read_manifest()
        script = controller.remote_script(
            manifest, "https://example.invalid/control", "https://example.invalid/result"
        )
        self.assertEqual(controller.GRAPH, Path(__file__).resolve().parents[2])
        self.assertIn(
            "/docs/implementation/graph-advantage/p1-corpus-20260905/"
            "full-diagnostics-collector/run_remote.py",
            script,
        )
        self.assertIn(
            "/docs/implementation/graph-advantage/corpus/p1_scenario.py", script
        )
        self.assertIn(
            "/docs/implementation/graph-advantage/evidence/"
            "diagnostics-linux-1f20f694/manifest.json",
            script,
        )
        self.assertIn("--batch-manifest", script)
        self.assertIn('mkdir -p "$REMOTE"', script)
        self.assertNotIn('mkdir -p "$REMOTE" "$CONTROL" "$OUTPUT"', script)
        self.assertIn('test ! -e "$CLAIM_PARENT"', script)
        self.assertIn('chown graphcheck:graphcheck "$CLAIM_PARENT"', script)
        self.assertIn('if test -e "$REMOTE/output"; then', script)
        self.assertIn(
            'tar czf "$REMOTE/results.tar.gz" -C "$REMOTE" control output '
            'environment.txt collector.log collector-exit.txt claim-worker-1.json',
            script,
        )
        self.assertIn('control environment.txt collector.log collector-exit.txt claim-worker-1.json', script)

    def test_control_archive_has_hash_manifest_and_required_members(self):
        manifest = json.loads(controller.MANIFEST_PATH.read_text())
        archive_path = controller.PREP / manifest["control_archive"]
        with tarfile.open(archive_path) as archive:
            names = set(archive.getnames())
            hashes = archive.extractfile("control-hashes.txt").read().decode()
        required = {
            "batch-manifest.json",
            "package-manifest.json",
            "docs/implementation/graph-advantage/corpus/corpus-manifest.json",
            "docs/implementation/graph-advantage/corpus/p1_scenario.py",
            "docs/implementation/graph-advantage/evidence/diagnostics-linux-1f20f694/manifest.json",
            "docs/implementation/graph-advantage/p1-corpus-20260905/full-diagnostics-collector/run_remote.py",
        }
        self.assertTrue(required.issubset(names))
        self.assertEqual(len(hashes.splitlines()), manifest["control_member_count"] - 1)


if __name__ == "__main__":
    unittest.main()
