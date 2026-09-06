import importlib.util
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("runner", HERE / "run_remote.py")
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)

CONFIG_DIR = HERE.parent / "retained-query-correctness-1c0b8e24"


class QueryRunnerTests(unittest.TestCase):
    def test_frozen_manifest_loads_exactly_six_configs(self):
        manifest_path, manifest, configs = runner.load_configs(CONFIG_DIR)
        self.assertEqual(manifest_path.name, "manifest.json")
        self.assertEqual(manifest["source_commit"], runner.EXPECTED_SOURCE_COMMIT)
        self.assertEqual(set(configs), set(runner.ARM_ORDER))
        for (profile, arm), (_, config) in configs.items():
            self.assertEqual(config["profile"], profile)
            self.assertEqual(config["cache"], arm)
            self.assertEqual(config["query"], runner.EXPECTED_QUERY)

    def _observation(self, config, *, binary=runner.EXPECTED_BINARY_SHA256):
        # The partial records are taken from the retained query observation.  The
        # envelope below is the evaluator's documented flat row contract.
        retained = json.loads(
            (HERE.parent / "retained-linux-05ad9842" / "raw-evidence" /
             "raw" / "off.json").read_text()
        )
        partial = retained["partial_failures"]
        return {
            "format_version": 1,
            "manifest_version": 1,
            "mode": "measure",
            "mutation_id": "cold",
            "operation": "search",
            "partial_failures": partial,
            "partial_failures_count": len(partial),
            "partial_failures_sha256": "a" * 64,
            "completeness": retained["completeness"],
            "profile": config["profile"],
            "provider_version": runner.EXPECTED_PROVIDER,
            "query": runner.EXPECTED_QUERY,
            "repository": runner.EXPECTED_REPOSITORY,
            "repository_path": runner.EXPECTED_REPO_PATH,
            "scenario": "cold",
            "semantic_bytes": 1,
            "semantic_digest": "b" * 64,
            "semantic_sha256": "b" * 64,
            "source_digest": runner.EXPECTED_INPUT_SHA256,
            "stats": {},
            "status": "partial",
            "trial": 0,
            "verb": "search",
            "warnings": retained["warnings"],
            "warnings_count": len(retained["warnings"]),
            "warnings_sha256": "c" * 64,
            "paired_freshness_basis": "source_digest_and_semantic_digest",
            "extraction": {"stale_source": False, "unchanged_reparses": 0},
            "binary_sha256": binary,
            "cache_mode": config["cache"],
            "reuse": config["cache"] == "on",
        }

    def test_full_profile_historical_warning_is_explicitly_reviewed(self):
        config = json.loads((CONFIG_DIR / "request-full-off.json").read_text())
        row = self._observation(config)
        with tarfile.open(HERE.parent / "paused-raw/worker-3.tar.gz") as archive:
            for line in archive.extractfile("results/campaign.ndjson"):
                old = json.loads(line)
                if old.get("verb") == "search" and old.get("trial") == 0 and old.get("reuse") is False:
                    break
        row["warnings"] = old["warnings"]
        row["warnings_count"] = len(old["warnings"])
        runner.validate_observation(row, config, runner.EXPECTED_BINARY_SHA256)
        row["warnings"][1]["detail"] = "new warning detail"
        with self.assertRaisesRegex(RuntimeError, "unreviewed"):
            runner.validate_observation(row, config, runner.EXPECTED_BINARY_SHA256)

    def test_unknown_partial_and_empty_digest_stop_before_on(self):
        config = json.loads((CONFIG_DIR / "request-syntax-only-off.json").read_text())
        for field in ("semantic_digest", "semantic_sha256", "partial_failures_sha256", "warnings_sha256"):
            row = self._observation(config)
            row[field] = ""
            with self.assertRaises(RuntimeError):
                runner.validate_observation(row, config, runner.EXPECTED_BINARY_SHA256)
        row = self._observation(config)
        row["partial_failures"][0]["detail"] = "new unreviewed failure"
        with self.assertRaisesRegex(RuntimeError, "unreviewed"):
            runner.validate_observation(row, config, runner.EXPECTED_BINARY_SHA256)

    def test_retained_fixture_identity_and_full_partial_membership_validate(self):
        config = json.loads((CONFIG_DIR / "request-syntax-only-off.json").read_text())
        observation = self._observation(config)
        validated = runner.validate_observation(
            observation, config, runner.EXPECTED_BINARY_SHA256
        )
        self.assertEqual(validated["partial_failures_count"], 11)
        self.assertEqual(len(validated["partial_failures"]), 11)
        self.assertEqual(validated["partial_failures"][0]["code"], "E_PARSE_ERROR")
        self.assertEqual(validated["query"], runner.EXPECTED_QUERY)

    def test_first_off_failure_does_not_start_on(self):
        execute = mock.Mock(side_effect=RuntimeError("OFF failed"))
        before_on = mock.Mock()
        with self.assertRaisesRegex(RuntimeError, "OFF failed"):
            runner.run_pair("syntax-only", execute, before_on)
        execute.assert_called_once_with("syntax-only", "off")
        before_on.assert_not_called()

    def test_stale_input_before_on_does_not_start_on(self):
        config = json.loads((CONFIG_DIR / "request-syntax-only-off.json").read_text())
        off = self._observation(config)
        execute = mock.Mock(return_value=off)
        before_on = mock.Mock(side_effect=RuntimeError("input changed"))
        with self.assertRaisesRegex(RuntimeError, "input changed"):
            runner.run_pair("syntax-only", execute, before_on)
        execute.assert_called_once_with("syntax-only", "off")
        before_on.assert_called_once_with()

    def test_parity_mismatch_stops_after_on_without_next_profile(self):
        config = json.loads((CONFIG_DIR / "request-syntax-only-off.json").read_text())
        off = self._observation(config)
        on = dict(off)
        on["semantic_digest"] = "d" * 64
        on["semantic_sha256"] = "d" * 64
        execute = mock.Mock(side_effect=[off, on])
        with self.assertRaisesRegex(RuntimeError, "parity mismatch"):
            runner.run_pair("syntax-only", execute, lambda: None)
        self.assertEqual(execute.call_count, 2)

    def _fake_binary(self, root):
        binary = root / "fake-evaluator"
        config = json.loads((CONFIG_DIR / "request-syntax-only-off.json").read_text())
        row = self._observation(config, binary="b" * 64)
        binary.write_text(
            "#!" + sys.executable + "\n"
            "import json, os\n"
            "o = json.loads(" + repr(json.dumps(row)) + ")\n"
            "json.dump(o, open(os.environ['ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT'], 'w'))\n"
        )
        binary.chmod(0o700)
        return binary

    def _fake_time(self, root):
        timer = root / "fake-time"
        timer.write_text(
            "#!" + sys.executable + "\n"
            "import pathlib, subprocess, sys\n"
            "args = sys.argv[1:]\n"
            "output = pathlib.Path(args[args.index('-o') + 1])\n"
            "code = subprocess.run(args[args.index('--') + 1:]).returncode\n"
            "output.write_text('Maximum resident set size (kbytes): 7\\n')\n"
            "raise SystemExit(code)\n"
        )
        timer.chmod(0o700)
        return timer

    def test_fake_process_records_raw_time_and_positive_rss(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = self._fake_binary(root)
            timer = self._fake_time(root)
            config_path = CONFIG_DIR / "request-syntax-only-off.json"
            config = json.loads(config_path.read_text())
            started = []
            with mock.patch.object(runner, "TIME_BINARY", str(timer)):
                observation = runner.run_arm(
                    root, binary, config_path, config, "b" * 64,
                    "syntax-only", "off", started, root
                )
            self.assertEqual(observation["partial_failures_count"], 11)
            self.assertEqual(started, ["syntax-only-off"])
            process = json.loads((root / "process-syntax-only-off.json").read_text())
            self.assertEqual(process["peak_rss_bytes"], 7 * 1024)
            self.assertIn("-test.timeout=130s", process["command"])
            self.assertIn(
                "Maximum resident set size (kbytes): 7",
                (root / "time-syntax-only-off.txt").read_text(),
            )

    def test_observation_missing_partial_membership_fails_closed(self):
        config = json.loads((CONFIG_DIR / "request-syntax-only-off.json").read_text())
        observation = self._observation(config)
        observation["partial_failures"] = None
        with self.assertRaisesRegex(RuntimeError, "partial-failure"):
            runner.validate_observation(observation, config, runner.EXPECTED_BINARY_SHA256)


if __name__ == "__main__":
    unittest.main()
