import os
import runpy
import tempfile
import unittest
from pathlib import Path
from unittest import mock


GRAPH_ADVANTAGE = Path(__file__).resolve().parents[1]
REPOSITORY_ROOT = Path(__file__).resolve().parents[4]
CORPUS = GRAPH_ADVANTAGE / "corpus"


class ActiveConfigurationTest(unittest.TestCase):
    def load_corpus_helpers(self, override: Path | None = None):
        environment = {} if override is None else {"P1_CORPUS_ROOT": str(override)}
        with mock.patch.dict(os.environ, environment, clear=True):
            return {
                name: runpy.run_path(str(CORPUS / name), run_name="configuration_test")
                for name in ("prepare_p1_corpus.py", "p1_scenario.py", "smoke_p1_corpus.py")
            }

    def test_corpus_helpers_share_external_sibling_default(self):
        helpers = self.load_corpus_helpers()
        expected = REPOSITORY_ROOT.parent / "graph-advantage-p1-corpus"
        self.assertEqual(expected, helpers["prepare_p1_corpus.py"]["DEFAULT_DEST"])
        self.assertEqual(expected, helpers["p1_scenario.py"]["DEST"])
        self.assertEqual(expected, helpers["smoke_p1_corpus.py"]["CORPUS_ROOT"])

    def test_corpus_helpers_honor_the_same_override(self):
        with tempfile.TemporaryDirectory() as directory:
            expected = Path(directory).resolve()
            helpers = self.load_corpus_helpers(expected)
        self.assertEqual(expected, helpers["prepare_p1_corpus.py"]["DEFAULT_DEST"])
        self.assertEqual(expected, helpers["p1_scenario.py"]["DEST"])
        self.assertEqual(expected, helpers["smoke_p1_corpus.py"]["CORPUS_ROOT"])

    def test_review_outputs_use_the_ignored_local_run_tree(self):
        source = (GRAPH_ADVANTAGE / "probes" / "run_review_linux.py").read_text()
        self.assertIn("evidence_root/'.local-runs'/('review-'+commit[:12])", source)
        self.assertNotIn("evidence=ROOT/'docs/implementation/graph-advantage/evidence'", source)


if __name__ == "__main__":
    unittest.main()
