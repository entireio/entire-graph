"""Guards for the two kit artefacts that are not importable Python.

``patches/`` is applied to a freshly cloned upstream harness on every CI run, so
a defect in the patch *text* is a defect in every reconstructed harness.
``run_locomo.sh`` is the launcher those runs go through.
"""

import os
import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

_KIT = Path(__file__).resolve().parents[1]
_PATCHES = _KIT / "patches"
_LAUNCHER = _KIT / "run_locomo.sh"

_HUNK = re.compile(r"^@@ -(\d+),(\d+) \+(\d+),(\d+) @@")


def _hunks(patch_text: str):
    """Yield ``(header_match, body_lines)`` for every hunk in a unified diff."""
    header = None
    body: list[str] = []
    for line in patch_text.splitlines():
        match = _HUNK.match(line)
        if match:
            if header is not None:
                yield header, body
            header, body = match, []
        elif header is not None:
            if line[:1] in (" ", "+", "-", "\\") or line == "":
                body.append(line)
            else:
                yield header, body
                header, body = None, []
    if header is not None:
        yield header, body


def _post_image(patch_text: str) -> str:
    """The lines the patch leaves behind (context + additions)."""
    kept = []
    for _header, body in _hunks(patch_text):
        kept.extend(line[1:] for line in body if line[:1] in (" ", "+"))
    return "\n".join(kept)


class PatchIntegrityTest(unittest.TestCase):
    def test_every_hunk_header_matches_its_body(self) -> None:
        """A hand-edited hunk with stale counts is rejected by ``git apply``."""
        checked = 0
        for patch in sorted(_PATCHES.glob("*.patch")):
            text = patch.read_text(encoding="utf-8")
            for header, body in _hunks(text):
                checked += 1
                old = sum(1 for line in body if line[:1] in (" ", "-"))
                new = sum(1 for line in body if line[:1] in (" ", "+"))
                self.assertEqual(
                    (int(header.group(2)), int(header.group(4))),
                    (old, new),
                    f"{patch.name}: hunk '{header.group(0)}' declares counts that "
                    f"do not match its body (actual -{old} +{new})",
                )
        self.assertGreater(checked, 0, "no hunks found; the patch set moved")


class LlmTimeoutPropagationTest(unittest.TestCase):
    """``LLM_TIMEOUT`` must reach the SDK client of every provider.

    ``self.timeout`` is derived from the environment, but each ``_init_*``
    initializer configures its SDK client with the value it is *handed*. When
    the dispatch passed the raw ``timeout`` argument, the documented 600s
    override never reached the OpenAI or classic-Azure clients and they kept
    aborting at the 120s default.
    """

    PATCH = _PATCHES / "0001-llm_client-azure-ai-provider-timeouts-reasoning.patch"

    def test_every_provider_initializer_receives_the_resolved_timeout(self) -> None:
        post = _post_image(self.PATCH.read_text(encoding="utf-8"))
        self.assertIn(
            'self.timeout = float(os.getenv("LLM_TIMEOUT", timeout))',
            post,
            "the env override that the dispatch must forward is gone",
        )
        dispatch = re.findall(r"^\s*self\._init_(\w+)\((.*)\)\s*$", post, re.M)
        forwarding = [
            (name, args) for name, args in dispatch if name != "anthropic"
        ]
        self.assertEqual(
            3,
            len(forwarding),
            f"expected openai/azure/azure_ai dispatch, found {forwarding}",
        )
        for name, args in forwarding:
            with self.subTest(initializer=name):
                self.assertNotIn(
                    " timeout,",
                    f" {args}",
                    f"_init_{name} is handed the raw timeout argument, so "
                    "LLM_TIMEOUT never reaches its SDK client",
                )
                self.assertIn("self.timeout", args)


@unittest.skipIf(shutil.which("bash") is None, "bash is required")
class LauncherResumeTest(unittest.TestCase):
    """``resume`` must not be offered where it cannot work.

    entire/graphify/cmm/bm25 buffer ingested turns in memory and materialize at
    first search. A resumed run sees completed ingestion checkpoints, skips
    every ``add()``, and raises BUFFER_MISSING -- so the advertised resume path
    either fails obscurely or scores a partial corpus as a complete run.
    """

    IN_PROCESS_BUFFERED = ("entire", "graphify", "cmm", "bm25")

    def _run(self, *args: str):
        with tempfile.TemporaryDirectory() as tmp:
            env = {
                "PATH": os.environ.get("PATH", ""),
                "HOME": tmp,
                "AZURE_AI_ENDPOINT": "https://example.invalid",
                "BENCH_STATE_ROOT": os.path.join(tmp, "state"),
            }
            return subprocess.run(
                ["bash", str(_LAUNCHER), *args],
                cwd=tmp,
                env=env,
                capture_output=True,
                text=True,
                timeout=60,
            )

    def test_resume_is_refused_for_in_process_buffered_arms(self) -> None:
        for arm in self.IN_PROCESS_BUFFERED:
            with self.subTest(arm=arm):
                proc = self._run(arm, "resume")
                self.assertEqual(4, proc.returncode, proc.stderr)
                self.assertIn("cannot resume", proc.stderr)

    def test_a_mistyped_resume_argument_is_refused(self) -> None:
        proc = self._run("entire", "--resume")
        self.assertEqual(2, proc.returncode, proc.stderr)
        self.assertIn("exactly 'resume'", proc.stderr)

    def test_arms_without_the_buffer_are_not_refused(self) -> None:
        """Server-backed Mem0 arms keep their memory outside this process."""
        proc = self._run("oss", "resume")
        self.assertNotEqual(4, proc.returncode)
        self.assertNotIn("cannot resume", proc.stderr)

    def test_a_normal_run_is_not_refused(self) -> None:
        proc = self._run("entire")
        self.assertNotIn("cannot resume", proc.stderr)
        self.assertNotEqual(4, proc.returncode)


if __name__ == "__main__":
    unittest.main()
