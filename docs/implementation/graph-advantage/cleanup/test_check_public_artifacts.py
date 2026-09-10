import importlib.util
import pathlib
import subprocess
import tempfile
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("check_public_artifacts.py")
SPEC = importlib.util.spec_from_file_location("check_public_artifacts", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class PublicArtifactGuardTest(unittest.TestCase):
    def test_rejects_raw_and_environment_bound_artifacts(self):
        cases = {
            "docs/implementation/graph-advantage/evidence/run/results.tar.gz":
                "compressed artifact bundle",
            "docs/implementation/graph-advantage/evidence/run/trace.jsonl.gz":
                "compressed artifact",
            "docs/implementation/graph-advantage/evidence/run/trace.pprof":
                "binary profile",
            "docs/implementation/graph-advantage/evidence/run/raw/result.json":
                "raw evidence tree",
            "docs/implementation/graph-advantage/evidence/run/vm-terminal.json":
                "full cloud VM record",
        }
        for raw, expected in cases.items():
            with self.subTest(path=raw):
                self.assertEqual(MODULE.path_violation(pathlib.PurePosixPath(raw)), expected)

    def test_accepts_canonical_records_and_paused_wip(self):
        accepted = [
            "docs/implementation/graph-advantage/evidence/canonical-v1/runs/check.json",
            "docs/implementation/graph-advantage/evidence/canonical-v1/cases/check.ndjson",
            "docs/implementation/graph-advantage/evidence/diagnostic-dispatch-effa358f-completion/controller/controller.py",
        ]
        for raw in accepted:
            with self.subTest(path=raw):
                self.assertIsNone(MODULE.path_violation(pathlib.PurePosixPath(raw)))

    def test_reports_exposure_type_and_line_without_value(self):
        path = pathlib.PurePosixPath(
            "docs/implementation/graph-advantage/evidence/canonical-v1/runs/check.json"
        )
        data = b'{"root":"/' + b'Users/local/project"}\n{"id":"/' + b'subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/x"}\n'
        self.assertEqual(
            MODULE.exposure_violations(path, data),
            [("absolute macOS home path", 1), ("Azure subscription resource ID", 2)],
        )

    def test_rejects_binary_and_checks_paused_wip_configuration(self):
        binary = pathlib.PurePosixPath(
            "docs/implementation/graph-advantage/evidence/canonical-v1/cases/sample.ndjson"
        )
        wip = pathlib.PurePosixPath(
            "docs/implementation/graph-advantage/evidence/diagnostic-dispatch-effa358f-completion/controller/manifest.json"
        )
        self.assertEqual(
            MODULE.exposure_violations(binary, b"\0/" + b"Users/local"),
            [("unsupported binary content", 1)],
        )
        self.assertEqual(
            MODULE.exposure_violations(wip, b"/" + b"Users/local"),
            [("absolute macOS home path", 1)],
        )

    def test_missing_added_file_is_rejected(self):
        path = pathlib.PurePosixPath("docs/implementation/graph-advantage/evidence/missing.json")
        with tempfile.TemporaryDirectory() as directory:
            self.assertEqual(MODULE.check(pathlib.Path(directory), [path]), [f"{path}: added artifact is missing or not a regular file"])

    def test_protected_path_is_exempt_only_while_untracked(self):
        protected = next(iter(MODULE.PROTECTED_LOCAL_UNTRACKED))
        with tempfile.TemporaryDirectory() as directory:
            repo = pathlib.Path(directory)
            subprocess.run(["git", "init", "-q"], cwd=repo, check=True)
            subprocess.run(["git", "config", "user.email", "guard@example.invalid"], cwd=repo, check=True)
            subprocess.run(["git", "config", "user.name", "Guard Test"], cwd=repo, check=True)
            (repo / "seed").write_text("seed\n")
            subprocess.run(["git", "add", "seed"], cwd=repo, check=True)
            subprocess.run(["git", "commit", "-qm", "seed"], cwd=repo, check=True)
            baseline = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=repo, text=True).strip()
            target = repo / protected; target.parent.mkdir(parents=True); target.write_text("local\n")
            self.assertNotIn(protected, MODULE.added_paths(repo, baseline))
            subprocess.run(["git", "add", str(protected)], cwd=repo, check=True)
            self.assertIn(protected, MODULE.added_paths(repo, baseline))


if __name__ == "__main__":
    unittest.main()
