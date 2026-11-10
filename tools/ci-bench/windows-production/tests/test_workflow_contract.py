import json
import re
import unittest
from pathlib import Path


REPOSITORY = Path(__file__).resolve().parents[4]
WORKFLOW = REPOSITORY / ".github" / "workflows" / "test.yml"
HARNESS = Path(__file__).resolve().parents[1]
SETTINGS = HARNESS / "settings.json"
HARNESS_PATH = "tools/ci-bench/windows-production"

# One row per sharded target: everything the workflow must pin for it.
SHARDED_TARGETS = {
    "windows": {
        "check": "test (windows-latest)",
        "settings": "settings.json",
        "weights": "historical-weights.json",
        "shardCount": 12,
        "target": None,
        "shell": "pwsh",
        "runners": ("depot-windows-2025-8", "depot-windows-2025-4"),
        "bundleCompression": 0,
        "components": {
            "prepare": "compile and plan",
            "shards": "shard ${{ matrix.shard }}",
            "other": "non-heavy packages",
            "vet": "vet",
        },
        # Where the native `go vet ./...` runs.
        "vetJob": "vet",
    },
    "macos": {
        "check": "test (macos-latest)",
        "settings": "settings.darwin-arm64.json",
        "weights": "historical-weights.darwin-arm64.json",
        "shardCount": 12,
        "target": "darwin/arm64",
        "shell": "bash",
        "runners": ("macos-latest",),
        "bundleCompression": 1,
        # No vet job of its own: hosted macOS runners are the scarce resource,
        # so vet is a step of the short non-heavy job.
        "components": {
            "prepare": "compile and plan",
            "shards": "shard ${{ matrix.shard }}",
            "other": "non-heavy packages and vet",
        },
        "vetJob": "other",
    },
}
# The job that runs each harness script, as a suffix of "<target>-test". The
# verifier runs in the aggregate job itself, which is the required check.
SCRIPTS = {
    "-prepare": "prepare.py",
    "-shards": "run_shard.py",
    "-other": "run_other.py",
    "": "verify_results.py",
}


def workflow_text():
    return WORKFLOW.read_text(encoding="utf-8")


def job_block(workflow, job):
    """The text of one top-level job, from its key to the next job key."""
    match = re.search(rf"^  {re.escape(job)}:\n", workflow, flags=re.MULTILINE)
    if match is None:
        raise AssertionError(f"workflow has no job {job!r}")
    following = re.search(r"^  [A-Za-z0-9_-]+:\n", workflow[match.end():], flags=re.MULTILINE)
    end = match.end() + following.start() if following else len(workflow)
    return workflow[match.start():end]


class WorkflowContractTests(unittest.TestCase):
    def test_monoliths_are_replaced_but_native_builds_remain(self):
        workflow = workflow_text()
        test_block = job_block(workflow, "test")
        build_block = job_block(workflow, "build")
        self.assertIn("os: [ubuntu-latest]", test_block)
        self.assertIn("name: test (${{ matrix.os }})", test_block)
        self.assertIn("run: go test -timeout 30m ./...", test_block)
        self.assertNotIn("windows-latest", test_block)
        self.assertNotIn("macos-latest", test_block)
        self.assertIn("os: [ubuntu-latest, macos-latest, windows-latest]", build_block)
        # Exactly one job per sharded target may run a whole-repository go test: none.
        self.assertEqual(workflow.count("go test -timeout 30m ./..."), 1)

    def test_dynamic_matrix_and_stable_aggregate_check_are_present(self):
        workflow = workflow_text()
        for name, expected in SHARDED_TARGETS.items():
            with self.subTest(target=name):
                settings = json.loads((HARNESS / expected["settings"]).read_text(encoding="utf-8"))
                self.assertEqual(settings["shardCount"], expected["shardCount"])
                shards = job_block(workflow, f"{name}-test-shards")
                self.assertIn(
                    f"shard: ${{{{ fromJSON(needs.{name}-test-prepare.outputs.shard-matrix) }}}}",
                    shards,
                )
                self.assertIn(f"needs: {name}-test-prepare\n", shards)
                prepare = job_block(workflow, f"{name}-test-prepare")
                self.assertIn("shard-matrix: ${{ steps.shard-matrix.outputs.value }}", prepare)
                self.assertIn("id: shard-matrix", prepare)
                # The required check is the aggregate job and only the aggregate job.
                aggregate = job_block(workflow, f"{name}-test")
                self.assertIn(f"    name: {expected['check']}\n", aggregate)
                self.assertEqual(workflow.count(f"    name: {expected['check']}\n"), 1)
                self.assertIn("    if: always()\n", aggregate)
                self.assertIn("    timeout-minutes: 10\n", aggregate)
                # The component check is the last step and runs whatever the verifier did.
                require = aggregate[aggregate.index("      - name: Require every "):]
                self.assertIn("        if: always()\n", require)
                self.assertNotIn("      - ", require[len("      - "):])
                self.assertNotIn("continue-on-error", aggregate)
                components = expected["components"]
                jobs = [f"{name}-test-{component}" for component in components]
                self.assertIn(f"      [{', '.join(jobs)}]\n", aggregate)
                self.assertEqual(aggregate.count("_RESULT: ${{ needs."), len(components))
                for component in components:
                    variable = f"{component.upper()}_RESULT"
                    self.assertIn(
                        f"{variable}: ${{{{ needs.{name}-test-{component}.result }}}}\n", aggregate
                    )
                    self.assertEqual(aggregate.count(f'"${variable}"'), 1)
                self.assertIn('if [ "$result" != success ]; then', aggregate)
                self.assertIn("exit 1", aggregate)

    def test_components_follow_the_existing_test_name_scheme(self):
        workflow = workflow_text()
        for name, expected in SHARDED_TARGETS.items():
            with self.subTest(target=name):
                for component, component_name in expected["components"].items():
                    block = job_block(workflow, f"{name}-test-{component}")
                    self.assertIn(f"    name: {expected['check']} / {component_name}\n", block)
        self.assertNotIn("name: Windows test /", workflow)
        self.assertNotIn("name: macOS test /", workflow)

    def test_workflow_uses_only_ephemeral_hosted_runners_and_no_baseline(self):
        workflow = workflow_text().lower()
        self.assertNotIn("self-hosted", workflow)
        self.assertNotIn("monolithic-baseline", workflow)
        self.assertNotIn("baseline-events", workflow)
        self.assertIn("permissions:\n  contents: read", workflow)
        self.assertNotIn("-test.bench", workflow)

    def test_shards_restore_cache_budget_and_verifier_runs_after_prepare(self):
        workflow = workflow_text()
        for name, expected in SHARDED_TARGETS.items():
            with self.subTest(target=name):
                shard_block = job_block(workflow, f"{name}-test-shards")
                verify_block = job_block(workflow, f"{name}-test")
                self.assertEqual(workflow.count(f"  {name}-test-verify:\n"), 0)
                self.assertIn("timeout-minutes: 105", shard_block)
                self.assertIn("fail-fast: false", shard_block)
                self.assertIn(f"max-parallel: {expected['shardCount']}\n", shard_block)
                self.assertIn("cache-dependency-path: go.sum", shard_block)
                self.assertNotIn("cache: false", shard_block)
                # The three downloads, the verifier and its report run whenever the plan
                # exists, even after a failed shard, so a failure still gets a report.
                self.assertEqual(
                    verify_block.count(
                        f"if: ${{{{ always() && needs.{name}-test-prepare.result == 'success' }}}}\n"
                    ),
                    5,
                )
                self.assertEqual(verify_block.count("        if: "), 6)
                self.assertNotIn(f"needs.{name}-test-shards.result == 'success'", verify_block)

    def test_each_component_runs_its_script_for_its_own_target(self):
        workflow = workflow_text()
        for name, expected in SHARDED_TARGETS.items():
            with self.subTest(target=name):
                for suffix, script in SCRIPTS.items():
                    block = job_block(workflow, f"{name}-test{suffix}")
                    self.assertEqual(block.count(f"python -B {HARNESS_PATH}/{script}"), 1)
                    for other_script in set(SCRIPTS.values()) - {script}:
                        self.assertNotIn(other_script, block)
                    # Windows relies on the scripts' default target; macOS must name its own.
                    if expected["target"] is None:
                        self.assertNotIn("--target", block)
                    else:
                        self.assertEqual(block.count(f"--target {expected['target']}"), 1)
                        self.assertEqual(block.count("--target"), 1)
                    if suffix:
                        self.assertIn("--expected-repository-sha '${{ github.sha }}'", block)
                prepare = job_block(workflow, f"{name}-test-prepare")
                self.assertIn(f"--settings {HARNESS_PATH}/{expected['settings']}", prepare)
                self.assertIn(f"--weights {HARNESS_PATH}/{expected['weights']}", prepare)
                self.assertIn("--shard-index '${{ matrix.shard }}'", job_block(workflow, f"{name}-test-shards"))
                vet = job_block(workflow, f"{name}-test-{expected['vetJob']}")
                self.assertEqual(vet.count("run: go vet ./...\n"), 1)
                self.assertEqual(workflow.count(f"  {name}-test-vet:\n"), int(expected["vetJob"] == "vet"))
                if expected["vetJob"] == "vet":
                    self.assertNotIn("needs:", vet)
                else:
                    # A vet step must run, and fail the job, whatever the tests before it did.
                    step = vet[vet.index("      - name: Vet the darwin-selected files natively\n"):]
                    self.assertIn("        if: ${{ !cancelled() }}\n", step)
                    self.assertNotIn("continue-on-error", vet)
                    self.assertLess(vet.index("Upload non-heavy package evidence"), vet.index("go vet ./..."))

    def test_harness_unit_tests_run_natively_off_the_critical_path(self):
        workflow = workflow_text()
        validate = f'python -B -m unittest discover -s {HARNESS_PATH}/tests -p "test_*.py" -v'
        for name, expected in SHARDED_TARGETS.items():
            with self.subTest(target=name):
                # Once per target, on its native runner, in the short non-heavy job that the
                # aggregate requires; never in compile-and-plan, which every shard waits for.
                other = job_block(workflow, f"{name}-test-other")
                self.assertEqual(other.count(validate), 1)
                self.assertLess(other.index(validate), other.index(f"{HARNESS_PATH}/run_other.py"))
                for component in expected["components"]:
                    if component != "other":
                        self.assertNotIn("unittest", job_block(workflow, f"{name}-test-{component}"))
        self.assertEqual(workflow.count(validate), len(SHARDED_TARGETS))

    def test_artifacts_are_named_per_target_and_never_cross(self):
        workflow = workflow_text()
        for name, expected in SHARDED_TARGETS.items():
            with self.subTest(target=name):
                foreign = next(other for other in SHARDED_TARGETS if other != name)
                prepare = job_block(workflow, f"{name}-test-prepare")
                self.assertIn(f"          name: {name}-test-bundle\n", prepare)
                self.assertIn("if-no-files-found: error", prepare)
                self.assertIn(f"compression-level: {expected['bundleCompression']}\n", prepare)
                shards = job_block(workflow, f"{name}-test-shards")
                self.assertIn(f"          name: {name}-test-bundle\n", shards)
                self.assertIn(f"          name: {name}-test-shard-${{{{ matrix.shard }}}}\n", shards)
                other = job_block(workflow, f"{name}-test-other")
                self.assertIn(f"          name: {name}-test-bundle\n", other)
                self.assertIn(f"          name: {name}-test-other\n", other)
                verify = job_block(workflow, f"{name}-test")
                self.assertIn(f"          name: {name}-test-bundle\n", verify)
                self.assertIn(f"          pattern: {name}-test-shard-*\n", verify)
                self.assertIn(f"          name: {name}-test-other\n", verify)
                self.assertIn(f"          name: {name}-test-verification\n", verify)
                for component in expected["components"]:
                    block = job_block(workflow, f"{name}-test-{component}")
                    self.assertNotIn(f"{foreign}-test", block)
                    if component != "vet":
                        for line in block.splitlines():
                            if "actions/upload-artifact@" in line:
                                self.assertRegex(line, r"upload-artifact@[0-9a-f]{40} # v\d")
                        self.assertIn("actions/upload-artifact@", block)
                        self.assertIn("        if: always()\n", block)

    def test_components_run_on_their_native_runner_with_their_native_shell(self):
        workflow = workflow_text()
        for name, expected in SHARDED_TARGETS.items():
            with self.subTest(target=name):
                foreign_shell = "bash" if expected["shell"] == "pwsh" else "pwsh"
                for component in expected["components"]:
                    block = job_block(workflow, f"{name}-test-{component}")
                    runner = re.search(r"^    runs-on: (.+)$", block, flags=re.MULTILINE).group(1)
                    self.assertIn(runner, expected["runners"])
                    self.assertNotIn(f"shell: {foreign_shell}", block)
                    if component != "vet":
                        self.assertIn(f"shell: {expected['shell']}", block)
                        self.assertIn("timeout-minutes:", block)
                # Verification, in the aggregate job, needs no native runner and only the harness.
                aggregate = job_block(workflow, f"{name}-test")
                self.assertIn("    runs-on: depot-ubuntu-24.04-4\n", aggregate)
                self.assertIn(f"          sparse-checkout: {HARNESS_PATH}\n", aggregate)

    def test_macos_jobs_stay_hosted_and_only_shards_compete_for_runners(self):
        workflow = workflow_text()
        settings = json.loads(
            (HARNESS / SHARDED_TARGETS["macos"]["settings"]).read_text(encoding="utf-8")
        )
        shards = job_block(workflow, "macos-test-shards")
        self.assertIn(f"max-parallel: {settings['shardCount']}\n", shards)
        # Once the plan exists, the shards share the hosted macOS runners with exactly
        # one other job of this harness.
        after_plan = [
            job
            for job in re.findall(r"^  (macos-test-[a-z-]+):\n", workflow, flags=re.MULTILINE)
            if "runs-on: macos-latest" in job_block(workflow, job)
            and "needs: macos-test-prepare\n" in job_block(workflow, job)
        ]
        self.assertEqual(sorted(after_plan), ["macos-test-other", "macos-test-shards"])
        for component in SHARDED_TARGETS["macos"]["components"]:
            block = job_block(workflow, f"macos-test-{component}")
            self.assertNotIn("depot-macos", block)
            self.assertNotIn("pwsh", block)
            self.assertNotIn(".exe", block)
        self.assertIn("capped per account", workflow)

    def test_every_workflow_python_command_disables_bytecode_writes(self):
        workflow = workflow_text()
        commands = [
            line.strip()
            for line in workflow.splitlines()
            if re.search(r"(?:^|[& ])python(?: |$)", line)
        ]
        self.assertTrue(commands)
        for command in commands:
            self.assertRegex(command, r"(?:^|[& ])python -B(?: |$)")
        for name, expected in SHARDED_TARGETS.items():
            for suffix in ["", *(f"-{component}" for component in expected["components"])]:
                block = job_block(workflow, f"{name}-test{suffix}")
                self.assertEqual(block.count("python "), block.count("python -B "))


if __name__ == "__main__":
    unittest.main()
