import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


REPO = Path(__file__).resolve().parents[3]
GENERATOR = REPO / "tools" / "ci-bench" / "generate-test-overlays.py"


class GenerateTestOverlaysTests(unittest.TestCase):
    def setUp(self):
        if shutil.which("go") is None:
            self.skipTest("go is unavailable")
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        (self.root / "go.mod").write_text("module example.test/overlay\n\ngo 1.22\n")
        (self.root / "product.go").write_text(
            "package fixture\n\nfunc Product() int { return 7 }\n", encoding="utf-8"
        )
        (self.root / "main_test.go").write_text(
            """package fixture

import (
    "os"
    "testing"
)

const markerEnvironment = "OVERLAY_MARKER"

func TestMain(m *testing.M) {
    if marker := os.Getenv(markerEnvironment); marker != "" {
        _ = os.WriteFile(marker, []byte("started"), 0o600)
    }
    os.Exit(m.Run())
}

func mainFileHelper() int { return 11 }

func TestMainFile(t *testing.T) {
    if mainFileHelper() != 11 { t.Fatal("bad main helper") }
}
""",
            encoding="utf-8",
        )
        (self.root / "alpha_test.go").write_text(
            """package fixture
import "testing"
func TestAlpha(t *testing.T) { if betaHelper() != 7 { t.Fatal("bad beta helper") } }
""",
            encoding="utf-8",
        )
        (self.root / "beta_test.go").write_text(
            """package fixture
import "testing"
func betaHelper() int { return Product() }
func TestBeta(t *testing.T) { if betaHelper() != 7 { t.Fatal("bad beta") } }
""",
            encoding="utf-8",
        )
        (self.root / "support_test.go").write_text(
            "package fixture\n\nvar SharedSupport = 1\n", encoding="utf-8"
        )

    def tearDown(self):
        self.temporary.cleanup()

    def generate(self, shards=2):
        output = self.root / f"out-{shards}"
        completed = subprocess.run(
            [
                "python3",
                str(GENERATOR),
                "--repo",
                str(self.root),
                "--package",
                ".",
                "--output",
                str(output),
                "--shards",
                str(shards),
                "--goos",
                "darwin" if shutil.which("sw_vers") else "linux",
                "--cgo-enabled",
                "0",
            ],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        return output, json.loads((output / "plan.json").read_text())

    def test_target_inventory_assignment_and_source_hashes(self):
        original = {
            path.name: hashlib.sha256(path.read_bytes()).hexdigest()
            for path in self.root.glob("*.go")
        }
        output, plan = self.generate()
        self.assertEqual(plan["coverageGuard"]["targetSelectedFileCount"], 4)
        self.assertTrue(
            plan["coverageGuard"]["allTestBearingFilesAssignedExactlyOnce"]
        )
        self.assertEqual(
            sorted(
                test
                for shard in plan["shards"]
                for test in shard["topLevelTests"]
            ),
            ["TestAlpha", "TestBeta", "TestMainFile"],
        )
        self.assertIn("support_test.go", plan["dependencyClosure"]["supportOnlyFiles"])
        self.assertEqual(
            plan["sourceProof"]["targetSelectedTestFiles"],
            {name: digest for name, digest in original.items() if name.endswith("_test.go")},
        )
        self.assertTrue((output / "shard-01" / "overlay.json").is_file())

    def test_dependency_closure_uses_exact_helper_surrogate(self):
        output, plan = self.generate()
        owner = {}
        for shard in plan["shards"]:
            for test in shard["topLevelTests"]:
                owner[test] = shard["shard"]
        alpha_shard = owner["TestAlpha"]
        self.assertNotEqual(alpha_shard, owner["TestBeta"])
        beta_replacement = (
            output / f"shard-{alpha_shard:02d}" / "replacements" / "beta_test.go"
        ).read_text(encoding="utf-8")
        self.assertIn("func betaHelper() int { return Product() }", beta_replacement)
        self.assertNotIn("func TestBeta", beta_replacement)
        self.assertEqual(
            plan["dependencyClosure"]["strategy"],
            "exact-declaration support surrogates",
        )

    def test_testmain_surrogate_preserves_exact_declaration(self):
        output, plan = self.generate()
        surrogate_paths = list(output.glob("shard-*/replacements/main_test.go"))
        self.assertEqual(len(surrogate_paths), 1)
        manifest = json.loads(
            (surrogate_paths[0].parents[1] / "manifest.json").read_text(encoding="utf-8")
        )
        proof = manifest["supportSurrogateProofs"]["main_test.go"]
        retained_names = [
            name for declaration in proof["retainedDeclarations"] for name in declaration["names"]
        ]
        self.assertEqual(
            retained_names, ["markerEnvironment", "TestMain", "mainFileHelper"]
        )
        surrogate = surrogate_paths[0].read_text(encoding="utf-8")
        original = (self.root / "main_test.go").read_text(encoding="utf-8")
        original_decl = original[original.index("func TestMain") : original.index("\n}\n", original.index("func TestMain")) + 2]
        self.assertIn(original_decl, surrogate)
        self.assertNotIn("func TestMainFile", surrogate)

    def test_each_overlay_compiles_and_lists_disjoint_tests(self):
        output, plan = self.generate()
        observed = []
        for shard in plan["shards"]:
            index = shard["shard"]
            binary = output / f"fixture-{index}.test"
            completed = subprocess.run(
                [
                    "go",
                    "test",
                    "-vet=off",
                    "-overlay",
                    str(output / f"shard-{index:02d}" / "overlay.json"),
                    "-c",
                    ".",
                    "-o",
                    str(binary),
                ],
                cwd=self.root,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=False,
            )
            self.assertEqual(completed.returncode, 0, completed.stderr)
            listed = subprocess.run(
                [str(binary), "-test.list", "."],
                cwd=self.root,
                check=True,
                text=True,
                stdout=subprocess.PIPE,
            ).stdout.splitlines()
            observed.extend(name for name in listed if name.startswith("Test"))
        self.assertEqual(sorted(observed), ["TestAlpha", "TestBeta", "TestMainFile"])


if __name__ == "__main__":
    unittest.main()
