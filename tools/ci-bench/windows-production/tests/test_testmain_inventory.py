import hashlib
import json
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parents[1]
HELPER = SCRIPT_DIR / "inventory_testmain.go"


class TestMainInventoryTests(unittest.TestCase):
    def run_helper(self, directory: Path, source: bytes):
        file_name = "lifecycle_windows_test.go"
        (directory / file_name).write_bytes(source)
        payload = {
            "Dir": str(directory.resolve()),
            "TestGoFiles": [file_name],
            "XTestGoFiles": [],
        }
        completed = subprocess.run(
            ["go", "run", str(HELPER)],
            input=json.dumps(payload),
            text=True,
            encoding="utf-8",
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        return json.loads(completed.stdout)

    def test_lf_and_crlf_declarations_have_the_same_normalized_hash(self):
        declaration = (
            "func TestMain(m *testing.M) {\n"
            "\tif os.Getenv(\"MARKER\") != \"\" {\n"
            "\t\tos.Exit(23)\n"
            "\t}\n"
            "\tos.Exit(m.Run())\n"
            "}"
        )
        source = (
            "package fixture\n\n"
            "import (\n\t\"os\"\n\t\"testing\"\n)\n\n"
            + declaration
            + "\n"
        )
        expected = hashlib.sha256(declaration.encode("utf-8")).hexdigest()
        with tempfile.TemporaryDirectory() as first, tempfile.TemporaryDirectory() as second:
            lf = self.run_helper(Path(first), source.encode("utf-8"))
            crlf = self.run_helper(Path(second), source.replace("\n", "\r\n").encode("utf-8"))
        self.assertEqual(lf, crlf)
        self.assertEqual(
            lf["declarations"],
            [
                {
                    "file": "lifecycle_windows_test.go",
                    "normalizedSourceSha256": expected,
                }
            ],
        )


if __name__ == "__main__":
    unittest.main()
