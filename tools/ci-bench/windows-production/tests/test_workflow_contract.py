"""The `test` workflow's contract for the sharded targets, asserted on the parsed file.

`contract_failures(text)` parses a workflow and compares every job on the test
path of both sharded targets (compile-and-plan, shards, non-heavy packages,
vet, and the aggregate that carries the required check name) with the job this
module builds from one table: every key of every job and step, so an edit that
could let a required check pass without the tests having passed (a dropped
`needs`, a `continue-on-error`, an `if`, an `|| true`, a hardcoded matrix)
changes the parsed structure and is reported. `MUTATIONS` is the durable proof:
each entry is such an edit, applied to the real file, and must be reported.

Changing the workflow's test path therefore means changing the table here in
the same commit, which is the point of a contract.

The one thing the table does not fix is an action's release: a step must run the
same action, pinned at least as firmly, and `ACCEPTED_ACTION_BUMPS` proves that a
version bump alone is not reported.
"""

import json
import os
import re
import shutil
import subprocess
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import workflow_yaml  # noqa: E402


REPOSITORY = Path(__file__).resolve().parents[4]
WORKFLOW = REPOSITORY / ".github" / "workflows" / "test.yml"
HARNESS = Path(__file__).resolve().parents[1]
H = "tools/ci-bench/windows-production"

# The contract names which action each step runs and how it must be pinned, not
# which release: a version bump (Dependabot's or anyone's) is not a change to the
# test path. `<version>` accepts a full release tag (`v7.0.1`) or a commit SHA,
# never a moving ref such as `v7` or `main`. `<sha>` accepts only a 40-character
# commit SHA, for the actions that move artifacts and caches between jobs.
VERSION = "<version>"
SHA = "<sha>"
CHECKOUT = f"actions/checkout@{VERSION}"
SETUP_GO = f"actions/setup-go@{VERSION}"
SETUP_PYTHON = f"actions/setup-python@{VERSION}"
UPLOAD = f"actions/upload-artifact@{SHA}"
DOWNLOAD = f"actions/download-artifact@{SHA}"
CACHE = SHA
COMMIT_SHA = re.compile(r"[0-9a-f]{40}")
RELEASE_TAG = re.compile(r"v\d+\.\d+\.\d+")
UNIT_TESTS = f'python -B -m unittest discover -s {H}/tests -p "test_*.py" -v'
AGGREGATE_RUNNER = "ubuntu-latest"

# One row per sharded target: everything that differs between them.
TARGETS = {
    "windows": {
        "check": "test (windows-latest)",
        "label": "Windows",
        "settings": "settings.json",
        "weights": "historical-weights.json",
        "shardCount": 16,
        "target": None,
        "shell": "pwsh",
        "runners": {
            "prepare": "windows-latest",
            "shards": "windows-latest",
            "other": "windows-latest",
            "vet": "windows-latest",
        },
        "bundleCompression": 0,
        "cachePlatform": "Windows-x64",
        "components": ["prepare", "shards", "other", "vet"],
        "otherName": "non-heavy packages",
    },
    "macos": {
        "check": "test (macos-latest)",
        "label": "macOS",
        "settings": "settings.darwin-arm64.json",
        "weights": "historical-weights.darwin-arm64.json",
        "shardCount": 12,
        "target": "darwin/arm64",
        "shell": "bash",
        "runners": {"prepare": "macos-latest", "shards": "macos-latest", "other": "macos-latest"},
        "bundleCompression": 1,
        "cachePlatform": "macOS-arm64",
        # No vet job of its own: vet is a step of the short non-heavy job, so that
        # once the plan exists only the shards compete for hosted macOS runners.
        "components": ["prepare", "shards", "other"],
        "otherName": "non-heavy packages and vet",
    },
}
# Job timeouts, with the reasoning in the workflow comment: a hung shard or
# non-heavy run is cut soon after the harness's own 30-minute package timeout.
TIMEOUTS = {"prepare": 15, "shards": 35, "other": 35, "vet": 10, "aggregate": 10}


def checkout(**extra):
    return {"uses": CHECKOUT, "with": {"persist-credentials": False, **extra}}


def setup_python():
    return {"uses": SETUP_PYTHON, "with": {"python-version": "3.12"}}


def setup_go_shared_cache():
    return {"uses": SETUP_GO, "with": {"go-version": "1.27.x", "cache-dependency-path": "go.sum"}}


def download(name, directory, **keys):
    with_ = {"pattern": name} if name.endswith("*") else {"name": name}
    return {"uses": DOWNLOAD, "with": {**with_, "path": "${{ runner.temp }}/" + directory}, **keys}


def upload(step_name, name, path, **options):
    return {
        "name": step_name,
        "if": "always()",
        "uses": UPLOAD,
        "with": {
            "name": name,
            "path": "${{ runner.temp }}/" + path,
            "if-no-files-found": "error",
            **options,
            "retention-days": 3,
        },
    }


def prepare_job(prefix, target):
    cache_prefix = f"test-prepare-{target['cachePlatform']}-go-${{{{ steps.setup-go.outputs.go-version }}}}-"
    cache_key = cache_prefix + "${{ hashFiles('go.sum') }}-${{ github.sha }}"
    cache_paths = "${{ steps.go-caches.outputs.modcache }}\n${{ steps.go-caches.outputs.buildcache }}\n"
    bundle = f"{prefix}-test-bundle"
    if target["shell"] == "bash":
        locate = (
            'echo "modcache=$(go env GOMODCACHE)" >> "$GITHUB_OUTPUT"\n'
            'echo "buildcache=$(go env GOCACHE)" >> "$GITHUB_OUTPUT"\n'
        )
        compile_ = (
            f"python -B {H}/prepare.py \\\n"
            f"  --target {target['target']} \\\n"
            '  --repository "$PWD" \\\n'
            f'  --output "$RUNNER_TEMP/{bundle}" \\\n'
            f"  --settings {H}/{target['settings']} \\\n"
            f"  --weights {H}/{target['weights']} \\\n"
            "  --expected-repository-sha '${{ github.sha }}'\n"
        )
        export = (
            "matrix=\"$(python -B -c '\n"
            "import json, sys\n"
            'plan = json.load(open(sys.argv[1], encoding="utf-8"))\n'
            'indexes = [shard["index"] for shard in plan["shards"]]\n'
            'if not indexes or len(indexes) != plan["shardCount"]:\n'
            '    raise SystemExit("plan shard indexes do not match shardCount")\n'
            'print(json.dumps(indexes, separators=(",", ":")))\n'
            f"' \"$RUNNER_TEMP/{bundle}/plan.json\")\"\n"
            'echo "value=$matrix" >> "$GITHUB_OUTPUT"\n'
        )
    else:
        out_file = "Out-File -LiteralPath $env:GITHUB_OUTPUT -Encoding utf8 -Append\n"
        locate = (
            "$ErrorActionPreference = 'Stop'\n"
            '"modcache=$(go env GOMODCACHE)" | ' + out_file + '"buildcache=$(go env GOCACHE)" | ' + out_file
        )
        compile_ = (
            "$ErrorActionPreference = 'Stop'\n"
            "$repository = (Resolve-Path -LiteralPath '.').Path\n"
            f"$output = Join-Path $env:RUNNER_TEMP '{bundle}'\n"
            f"& python -B {H}/prepare.py `\n"
            "  --repository $repository `\n"
            "  --output $output `\n"
            f"  --settings {H}/{target['settings']} `\n"
            f"  --weights {H}/{target['weights']} `\n"
            "  --expected-repository-sha '${{ github.sha }}'\n"
            "if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }\n"
        )
        export = (
            "$ErrorActionPreference = 'Stop'\n"
            f"$planPath = Join-Path $env:RUNNER_TEMP '{bundle}/plan.json'\n"
            "$plan = Get-Content -LiteralPath $planPath -Raw | ConvertFrom-Json\n"
            "$indexes = @($plan.shards | ForEach-Object { [int] $_.index })\n"
            "if ($indexes.Count -ne [int] $plan.shardCount -or $indexes.Count -eq 0) {\n"
            "  throw 'plan shard indexes do not match shardCount'\n"
            "}\n"
            "$matrix = ConvertTo-Json -InputObject $indexes -Compress\n"
            '"value=$matrix" | ' + out_file
        )
    shell = target["shell"]
    return {
        "name": f"{target['check']} / compile and plan",
        "runs-on": target["runners"]["prepare"],
        "timeout-minutes": TIMEOUTS["prepare"],
        "outputs": {"shard-matrix": "${{ steps.shard-matrix.outputs.value }}"},
        "steps": [
            checkout(),
            {"uses": SETUP_GO, "id": "setup-go", "with": {"go-version": "1.27.x", "cache": False}},
            {"name": "Locate the Go caches", "id": "go-caches", "shell": shell, "run": locate},
            {
                "name": "Restore the newest compile-and-plan build cache",
                "uses": f"actions/cache/restore@{CACHE}",
                "with": {
                    "path": cache_paths,
                    "key": cache_key,
                    "restore-keys": (
                        cache_prefix + "${{ hashFiles('go.sum') }}-\n"
                        + cache_prefix + "\n"
                        + f"setup-go-{target['cachePlatform']}-go-${{{{ steps.setup-go.outputs.go-version }}}}-\n"
                    ),
                },
            },
            setup_python(),
            {"name": "Compile once and inventory default-runnable roots", "shell": shell, "run": compile_},
            {
                "name": "Export the authoritative shard matrix",
                "id": "shard-matrix",
                "shell": shell,
                "run": export,
            },
            upload(
                f"Upload immutable {target['label']} test bundle",
                bundle,
                bundle,
                **{"compression-level": target["bundleCompression"]},
            ),
            {
                "name": "Save the build cache for later runs",
                # Pull requests restore only; a push (to main, the only push trigger) saves.
                "if": "${{ github.event_name == 'push' }}",
                "uses": f"actions/cache/save@{CACHE}",
                "with": {"path": cache_paths, "key": cache_key},
            },
        ],
    }


def launch(prefix, target, script, output, shard):
    """The run script of run_shard.py or run_other.py in the target's shell."""
    bundle = f"{prefix}-test-bundle"
    if target["shell"] == "bash":
        lines = [
            f"python -B {H}/{script} \\",
            f"  --target {target['target']} \\",
            '  --repository "$PWD" \\',
            f'  --bundle "$RUNNER_TEMP/{bundle}" \\',
            f'  --output "$RUNNER_TEMP/{output}" \\',
        ]
        if shard:
            lines.append("  --shard-index '${{ matrix.shard }}' \\")
        lines.append("  --expected-repository-sha '${{ github.sha }}'")
        return "\n".join(lines) + "\n"
    lines = [
        "$ErrorActionPreference = 'Stop'",
        "$repository = (Resolve-Path -LiteralPath '.').Path",
        "$bundle = (Resolve-Path -LiteralPath '${{ runner.temp }}/" + bundle + "').Path",
        f"$output = Join-Path $env:RUNNER_TEMP '{output}'",
        f"& python -B {H}/{script} `",
        "  --repository $repository `",
        "  --bundle $bundle `",
        "  --output $output `",
    ]
    if shard:
        lines.append("  --shard-index '${{ matrix.shard }}' `")
    lines += ["  --expected-repository-sha '${{ github.sha }}'", "exit $LASTEXITCODE"]
    return "\n".join(lines) + "\n"


def shards_job(prefix, target):
    evidence = f"{prefix}-test-shard-${{{{ matrix.shard }}}}"
    return {
        "name": f"{target['check']} / shard ${{{{ matrix.shard }}}}",
        "needs": f"{prefix}-test-prepare",
        "runs-on": target["runners"]["shards"],
        "timeout-minutes": TIMEOUTS["shards"],
        "strategy": {
            "fail-fast": False,
            "max-parallel": target["shardCount"],
            # The matrix is the plan's own shard list, never a literal.
            "matrix": {"shard": f"${{{{ fromJSON(needs.{prefix}-test-prepare.outputs.shard-matrix) }}}}"},
        },
        "steps": [
            checkout(),
            setup_go_shared_cache(),
            setup_python(),
            download(f"{prefix}-test-bundle", f"{prefix}-test-bundle"),
            {
                "name": "Execute the assigned compiled roots",
                "shell": target["shell"],
                "run": launch(prefix, target, "run_shard.py", evidence, shard=True),
            },
            upload("Upload shard evidence", evidence, evidence),
        ],
    }


def other_job(prefix, target):
    unit_tests = {"name": "Validate the sharding harness", "run": UNIT_TESTS}
    if target["shell"] == "bash":
        unit_tests = {"name": "Validate the sharding harness", "shell": "bash", "run": UNIT_TESTS}
    steps = [
        checkout(),
        setup_go_shared_cache(),
        setup_python(),
        download(f"{prefix}-test-bundle", f"{prefix}-test-bundle"),
        unit_tests,
        {
            "name": f"Run every target-{target['label']} non-heavy package once",
            "shell": target["shell"],
            "run": launch(prefix, target, "run_other.py", f"{prefix}-test-other", shard=False),
        },
        upload("Upload non-heavy package evidence", f"{prefix}-test-other", f"{prefix}-test-other"),
    ]
    if "vet" not in target["components"]:
        steps.append(
            {
                "name": "Vet the darwin-selected files natively",
                # Vet runs, and can fail the job, whatever the tests before it did.
                "if": "${{ !cancelled() }}",
                "shell": "bash",
                "run": "go vet ./...",
            }
        )
    return {
        "name": f"{target['check']} / {target['otherName']}",
        "needs": f"{prefix}-test-prepare",
        "runs-on": target["runners"]["other"],
        "timeout-minutes": TIMEOUTS["other"],
        "steps": steps,
    }


def vet_job(prefix, target):
    return {
        "name": f"{target['check']} / vet",
        "runs-on": target["runners"]["vet"],
        "timeout-minutes": TIMEOUTS["vet"],
        "steps": [checkout(), setup_go_shared_cache(), {"run": "go vet ./..."}],
    }


def require_script(target):
    variables = " ".join(f'"${component.upper()}_RESULT"' for component in target["components"])
    return (
        f"for result in {variables}; do\n"
        '  if [ "$result" != success ]; then\n'
        f'    echo "A required {target["label"]} test component ended with: $result" >&2\n'
        "    exit 1\n"
        "  fi\n"
        "done\n"
    )


def aggregate_job(prefix, target):
    # The verifier and its inputs run whenever the plan exists, even after a failed
    # shard, so a failure still gets a coverage report.
    planned = f"${{{{ always() && needs.{prefix}-test-prepare.result == 'success' }}}}"
    verify = f"python -B {H}/verify_results.py \\\n"
    if target["target"]:
        verify += f"  --target {target['target']} \\\n"
    verify += (
        f'  --bundle "${{{{ runner.temp }}}}/{prefix}-test-bundle" \\\n'
        f'  --shard-results "${{{{ runner.temp }}}}/{prefix}-test-shards" \\\n'
        f'  --other-results "${{{{ runner.temp }}}}/{prefix}-test-other" \\\n'
        f'  --output "${{{{ runner.temp }}}}/{prefix}-test-verification.json" \\\n'
        # The one input that is not evidence: the commit this run is for.
        "  --expected-repository-sha '${{ github.sha }}'\n"
    )
    report = upload(
        "Upload verification report",
        f"{prefix}-test-verification",
        f"{prefix}-test-verification.json",
    )
    report["if"] = planned
    return {
        # The required check. It must run, and so be able to fail, whatever
        # happened to the jobs it needs: a skipped required check reads as passing.
        "name": target["check"],
        "needs": [f"{prefix}-test-{component}" for component in target["components"]],
        "if": "always()",
        "runs-on": AGGREGATE_RUNNER,
        "timeout-minutes": TIMEOUTS["aggregate"],
        "steps": [
            checkout(**{"sparse-checkout": H}),
            setup_python(),
            download(f"{prefix}-test-bundle", f"{prefix}-test-bundle", **{"if": planned}),
            download(f"{prefix}-test-shard-*", f"{prefix}-test-shards", **{"if": planned}),
            download(f"{prefix}-test-other", f"{prefix}-test-other", **{"if": planned}),
            {
                "name": "Verify exact root, package, process, and artifact coverage",
                "if": planned,
                "run": verify,
            },
            report,
            {
                "name": f"Require every {target['label']} test component",
                "if": "always()",
                "env": {
                    f"{component.upper()}_RESULT": f"${{{{ needs.{prefix}-test-{component}.result }}}}"
                    for component in target["components"]
                },
                "run": require_script(target),
            },
        ],
    }


BUILDERS = {"prepare": prepare_job, "shards": shards_job, "other": other_job, "vet": vet_job}

EXPECTED_TOP_LEVEL = {
    "name": "test",
    # Runs for every pull request and every push to main; a missing trigger
    # would leave the required checks unreported.
    "on": {"push": {"branches": ["main"]}, "pull_request": None, "workflow_dispatch": None},
    "permissions": {"contents": "read"},
    # Only a pull request's superseded run is cancelled; every other run has a
    # group of its own and completes.
    "concurrency": {
        "group": "test-${{ github.event.pull_request.number || github.run_id }}",
        "cancel-in-progress": "${{ github.event_name == 'pull_request' }}",
    },
}

EXPECTED_LINUX_TEST_JOB = {
    "strategy": {
        "matrix": {
            "os": ["ubuntu-latest"],
            "include": [{"os": "ubuntu-latest", "runner": "depot-ubuntu-24.04-8"}],
        }
    },
    "name": "test (${{ matrix.os }})",
    "runs-on": "${{ matrix.runner }}",
    "steps": [
        checkout(),
        {"uses": SETUP_GO, "with": {"go-version": "1.27.x"}},
        {"run": "go test -timeout 30m ./..."},
    ],
}


def expected_jobs():
    jobs = {}
    for prefix, target in TARGETS.items():
        for component in target["components"]:
            jobs[f"{prefix}-test-{component}"] = BUILDERS[component](prefix, target)
        jobs[f"{prefix}-test"] = aggregate_job(prefix, target)
    return jobs


def uses_differences(expected, actual, where):
    """A step's action: the same action, pinned at least as firmly as the contract asks."""
    name, _, pin = expected.partition("@")
    if not isinstance(actual, str) or "@" not in actual:
        return [f"{where}: {actual!r}, want {expected!r}"]
    actual_name, _, ref = actual.partition("@")
    if actual_name != name:
        return [f"{where}: {actual!r} is not the action {name!r}"]
    if COMMIT_SHA.fullmatch(ref):
        return []
    if pin == SHA:
        return [f"{where}: {actual!r} must be pinned to a 40-character commit SHA"]
    if pin == VERSION and RELEASE_TAG.fullmatch(ref):
        return []
    return [f"{where}: {actual!r} must name a full release tag (vX.Y.Z) or a commit SHA"]


def differences(expected, actual, where):
    """Every path at which two parsed values differ, as readable lines."""
    if isinstance(expected, dict) and isinstance(actual, dict):
        found = []
        for key in sorted(set(expected) | set(actual)):
            if key not in actual:
                found.append(f"{where}.{key}: missing, want {expected[key]!r}")
            elif key not in expected:
                found.append(f"{where}.{key}: unexpected {actual[key]!r}")
            elif key == "uses" and isinstance(expected[key], str):
                found += uses_differences(expected[key], actual[key], f"{where}.{key}")
            else:
                found += differences(expected[key], actual[key], f"{where}.{key}")
        return found
    if isinstance(expected, list) and isinstance(actual, list):
        if len(expected) != len(actual):
            def label(item):
                if isinstance(item, dict):
                    return item.get("name") or item.get("uses") or item.get("run")
                return item

            return [
                f"{where}: has {len(actual)} entries {[label(item) for item in actual]!r}, "
                f"want {len(expected)} {[label(item) for item in expected]!r}"
            ]
        found = []
        for position, (want, got) in enumerate(zip(expected, actual)):
            found += differences(want, got, f"{where}[{position}]")
        return found
    if type(expected) is not type(actual) or expected != actual:
        return [f"{where}: {actual!r}, want {expected!r}"]
    return []


EXPECTED_MUTATION_CHECK_JOB = {
    "runs-on": AGGREGATE_RUNNER,
    "timeout-minutes": 15,
    "steps": [checkout(), setup_python(), {"run": f"python -B {H}/mutation_check.py"}],
}


def contract_failures(text):
    """Every way the workflow text departs from the contract; empty when it holds."""
    try:
        workflow = workflow_yaml.load(text)
    except workflow_yaml.UnsupportedYaml as error:
        return [f"workflow is outside the YAML subset the contract can read: {error}"]
    if not isinstance(workflow, dict) or not isinstance(workflow.get("jobs"), dict):
        return ["workflow has no jobs mapping"]
    failures = []
    for key, expected in EXPECTED_TOP_LEVEL.items():
        if key not in workflow:
            failures.append(f"{key}: missing")
        else:
            failures += differences(expected, workflow[key], key)
    unexpected_top = set(workflow) - set(EXPECTED_TOP_LEVEL) - {"jobs"}
    if unexpected_top:
        failures.append(f"unexpected top-level keys {sorted(unexpected_top)!r}")
    jobs = workflow["jobs"]
    expected = expected_jobs()
    sharded = {name for name in jobs if name.startswith(tuple(f"{prefix}-test" for prefix in TARGETS))}
    for name in sorted(sharded - set(expected)):
        failures.append(f"jobs.{name}: unexpected job on a sharded target's test path")
    for name, job in expected.items():
        if name not in jobs:
            failures.append(f"jobs.{name}: missing")
        else:
            failures += differences(job, jobs[name], f"jobs.{name}")
    if "harness-mutation-check" not in jobs:
        failures.append("jobs.harness-mutation-check: missing")
    else:
        failures += differences(
            EXPECTED_MUTATION_CHECK_JOB, jobs["harness-mutation-check"], "jobs.harness-mutation-check"
        )
    if "test" not in jobs:
        failures.append("jobs.test: missing")
    else:
        failures += differences(EXPECTED_LINUX_TEST_JOB, jobs["test"], "jobs.test")
    # The required check names exist exactly once across the whole workflow.
    names = [job.get("name") for job in jobs.values() if isinstance(job, dict)]
    for target in TARGETS.values():
        if names.count(target["check"]) != 1:
            failures.append(f"check name {target['check']!r} is carried by {names.count(target['check'])} jobs")
    # The plan a workflow asks for is the one the committed settings describe.
    for prefix, target in TARGETS.items():
        settings = json.loads((HARNESS / target["settings"]).read_text(encoding="utf-8"))
        if settings["shardCount"] != target["shardCount"]:
            failures.append(f"{target['settings']}: shardCount {settings['shardCount']} is not pinned")
    return failures


def replace_once(old, new):
    def mutate(text):
        if text.count(old) != 1:
            raise AssertionError(f"mutation anchor occurs {text.count(old)} times: {old!r}")
        return text.replace(old, new)

    return mutate


def replace_every(pattern, new):
    """Replace every match of a regular expression; at least one must match."""

    def mutate(text):
        mutated, count = re.subn(pattern, new, text)
        if count == 0:
            raise AssertionError(f"mutation anchor never occurs: {pattern!r}")
        return mutated

    return mutate


# Edits to action references that are not a change to the test path: a newer
# release tag, a newer commit, or a tag tightened to a commit.
ACCEPTED_ACTION_BUMPS = [
    ("setup-python release bump", replace_every(r"actions/setup-python@v\d+\.\d+\.\d+", "actions/setup-python@v99.1.2")),
    ("checkout release bump", replace_every(r"actions/checkout@v\d+\.\d+\.\d+", "actions/checkout@v99.0.0")),
    ("setup-go release bump", replace_every(r"actions/setup-go@v\d+\.\d+\.\d+", "actions/setup-go@v99.0.0")),
    ("checkout tightened to a commit", replace_every(
        r"actions/checkout@v\d+\.\d+\.\d+", "actions/checkout@" + "ab" * 20 + " # v99.0.0"
    )),
    ("download-artifact commit bump", replace_every(
        r"actions/download-artifact@[0-9a-f]{40} # v\d+\.\d+\.\d+", "actions/download-artifact@" + "cd" * 20 + " # v99.0.0"
    )),
    ("upload-artifact commit bump", replace_every(
        r"actions/upload-artifact@[0-9a-f]{40} # v\d+\.\d+\.\d+", "actions/upload-artifact@" + "ef" * 20 + " # v99.0.0"
    )),
    ("cache commit bump", replace_every(
        r"actions/cache/(restore|save)@[0-9a-f]{40} # v\d+\.\d+\.\d+", r"actions/cache/\1@" + "01" * 20 + " # v99.0.0"
    )),
]


def replace_in_job(job, old, new, count=1):
    """Replace old with new inside one top-level job's text."""

    def mutate(text):
        start = text.index(f"\n  {job}:\n") + 1
        following = [
            text.index(f"\n  {other}:\n", start) + 1
            for other in list(expected_jobs()) + ["bm25-adapter"]
            if f"\n  {other}:\n" in text[start + 1:]
        ]
        end = min(following)
        block = text[start:end]
        if block.count(old) != count:
            raise AssertionError(f"mutation anchor occurs {block.count(old)} times in {job}: {old!r}")
        return text[:start] + block.replace(old, new) + text[end:]

    return mutate


def all_targets(build):
    """One mutation per sharded target, so both are proven symmetrically."""
    return [(f"{label} [{prefix}]", mutate) for prefix in TARGETS for label, mutate in build(prefix)]


def per_target_mutations(prefix):
    target = TARGETS[prefix]
    aggregate, prepare, shards, other = (f"{prefix}-test{suffix}" for suffix in ("", "-prepare", "-shards", "-other"))
    planned = f"        if: ${{{{ always() && needs.{prefix}-test-prepare.result == 'success' }}}}\n"
    verify_tail = "            --expected-repository-sha '${{ github.sha }}'\n"
    first_result = f'"${target["components"][0].upper()}_RESULT" '
    last_component = target["components"][-1]
    # The leading newline anchors the job-level key, not a step's deeper `if`.
    job_if = "\n    if: always()\n"
    yield "aggregate job-level if removed", replace_in_job(aggregate, job_if, "\n")
    yield "aggregate job-level if is !cancelled()", replace_in_job(
        aggregate, job_if, "\n    if: ${{ !cancelled() }}\n"
    )
    yield "aggregate job-level if requires prepare success", replace_in_job(
        aggregate, job_if, f"\n    if: needs.{prefix}-test-prepare.result == 'success'\n"
    )
    yield "aggregate job continue-on-error", replace_in_job(
        aggregate, job_if, job_if + "    continue-on-error: true\n"
    )
    yield "shards job continue-on-error", replace_in_job(
        shards, "    strategy:\n", "    continue-on-error: true\n    strategy:\n"
    )
    yield "shard step continue-on-error", replace_in_job(
        shards,
        "      - name: Execute the assigned compiled roots\n",
        "      - name: Execute the assigned compiled roots\n        continue-on-error: true\n",
    )
    yield "shard step if: false", replace_in_job(
        shards,
        "      - name: Execute the assigned compiled roots\n",
        "      - name: Execute the assigned compiled roots\n        if: false\n",
    )
    yield "shard job if: false", replace_in_job(shards, "    strategy:\n", "    if: false\n    strategy:\n")
    yield "fail-fast: false removed", replace_in_job(shards, "      fail-fast: false\n", "")
    yield "max-parallel changed", replace_in_job(
        shards, f"      max-parallel: {target['shardCount']}\n", "      max-parallel: 1\n"
    )
    yield "matrix is a literal", replace_in_job(
        shards, f"${{{{ fromJSON(needs.{prefix}-test-prepare.outputs.shard-matrix) }}}}", "[0]"
    )
    yield "matrix output is hardcoded", replace_in_job(
        prepare, "${{ steps.shard-matrix.outputs.value }}", "'[0]'"
    )
    yield "shards do not need the plan", replace_in_job(shards, f"    needs: {prefix}-test-prepare\n", "")
    yield "shard job timeout raised", replace_in_job(shards, "    timeout-minutes: 35\n", "    timeout-minutes: 105\n")
    yield "verifier escapes with || true", replace_in_job(
        aggregate, verify_tail, verify_tail.rstrip("\n") + " || true\n"
    )
    yield "verifier loses its commit anchor", replace_in_job(
        aggregate,
        f'--output "${{{{ runner.temp }}}}/{prefix}-test-verification.json" \\\n' + verify_tail,
        f'--output "${{{{ runner.temp }}}}/{prefix}-test-verification.json"\n',
    )
    yield "verifier deleted", replace_in_job(
        aggregate,
        "      - name: Verify exact root, package, process, and artifact coverage\n" + planned,
        "      - name: Verify exact root, package, process, and artifact coverage\n" + planned.replace(
            "always() &&", "false &&"
        ),
    )
    yield "verifier step continue-on-error", replace_in_job(
        aggregate,
        "      - name: Verify exact root, package, process, and artifact coverage\n",
        "      - name: Verify exact root, package, process, and artifact coverage\n        continue-on-error: true\n",
    )
    yield "final step exits 0", replace_in_job(aggregate, "              exit 1\n", "              exit 0\n")
    yield "final step if removed", replace_in_job(
        aggregate,
        f"      - name: Require every {target['label']} test component\n        if: always()\n",
        f"      - name: Require every {target['label']} test component\n",
    )
    yield "result dropped from the final loop", replace_in_job(aggregate, first_result, "")
    yield "result env mapped to another job", replace_in_job(
        aggregate,
        f"SHARDS_RESULT: ${{{{ needs.{prefix}-test-shards.result }}}}",
        f"SHARDS_RESULT: ${{{{ needs.{prefix}-test-prepare.result }}}}",
    )
    yield "job dropped from needs", replace_in_job(aggregate, f", {prefix}-test-{last_component}]", "]")
    yield "check renamed", replace_in_job(
        aggregate, f"    name: {target['check']}\n", f"    name: {target['check']} (all)\n"
    )
    yield "unit-test step if: false", replace_in_job(
        other,
        "      - name: Validate the sharding harness\n",
        "      - name: Validate the sharding harness\n        if: false\n",
    )
    yield "unit-test step removed", replace_in_job(other, "discover -s", "discover --help; echo -s")
    yield "non-heavy job continue-on-error", replace_in_job(
        other, f"    needs: {prefix}-test-prepare\n", f"    needs: {prefix}-test-prepare\n    continue-on-error: true\n"
    )
    yield "go vet || true", replace_in_job(
        f"{prefix}-test-vet" if "vet" in target["components"] else other,
        "run: go vet ./...\n",
        "run: go vet ./... || true\n",
    )
    yield "prepare escapes with || true", replace_in_job(
        prepare, "prepare.py", "prepare.py --help || true; python -B prepare.py"
    )


def workflow_mutations():
    mutations = all_targets(per_target_mutations)
    mutations += [
        ("windows vet job continue-on-error", replace_in_job(
            "windows-test-vet", "    timeout-minutes: 10\n", "    timeout-minutes: 10\n    continue-on-error: true\n"
        )),
        ("windows shard exit code discarded", replace_in_job("windows-test-shards", "          exit $LASTEXITCODE\n", "          exit 0\n")),
        ("windows prepare exit code unchecked", replace_in_job(
            "windows-test-prepare", "          if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }\n", ""
        )),
        ("macos vet step if: false", replace_in_job(
            "macos-test-other", "        if: ${{ !cancelled() }}\n", "        if: false\n"
        )),
        ("pull_request trigger removed", replace_once("  pull_request:\n", "")),
        ("push trigger narrowed", replace_once("    branches: [main]\n", "    branches: [release]\n")),
        ("pull requests no longer cancel superseded runs", replace_once(
            "  cancel-in-progress: ${{ github.event_name == 'pull_request' }}\n", "  cancel-in-progress: true\n"
        )),
        ("runs on main share a concurrency group", replace_once(
            "  group: test-${{ github.event.pull_request.number || github.run_id }}\n",
            "  group: test-${{ github.ref }}\n",
        )),
        ("permissions widened", replace_once("permissions:\n  contents: read\n", "permissions:\n  contents: write\n")),
        ("linux monolith gains -short", replace_once(
            "      - run: go test -timeout 30m ./...\n", "      - run: go test -short -timeout 30m ./...\n"
        )),
        ("a second verify job appears", replace_once(
            "\n  bm25-adapter:\n",
            "\n  macos-test-verify:\n    runs-on: depot-ubuntu-24.04-4\n    steps:\n      - run: exit 0\n\n  bm25-adapter:\n",
        )),
        ("harness mutation check may fail", replace_once(
            "      - run: python -B tools/ci-bench/windows-production/mutation_check.py\n",
            "      - run: python -B tools/ci-bench/windows-production/mutation_check.py || true\n",
        )),
        ("an anchor the reader cannot parse", replace_once("permissions:\n", "permissions: &p\n")),
        ("download-artifact unpinned to a tag", replace_every(
            r"actions/download-artifact@[0-9a-f]{40} # (v\d+\.\d+\.\d+)", r"actions/download-artifact@\1"
        )),
        ("upload-artifact unpinned to a moving tag", replace_every(
            r"actions/upload-artifact@[0-9a-f]{40} # v\d+\.\d+\.\d+", "actions/upload-artifact@v7"
        )),
        ("cache unpinned to a tag", replace_every(
            r"actions/cache/(restore|save)@[0-9a-f]{40} # (v\d+\.\d+\.\d+)", r"actions/cache/\1@\2"
        )),
        ("download-artifact swapped for another action", replace_every(
            r"actions/download-artifact@", "someone-else/download-artifact@"
        )),
        ("checkout swapped for another action", replace_every(r"actions/checkout@", "actions/checkout-fork@")),
        ("checkout on a moving major tag", replace_every(r"actions/checkout@v\d+\.\d+\.\d+", "actions/checkout@v7")),
        ("setup-go on a branch", replace_every(r"actions/setup-go@v\d+\.\d+\.\d+", "actions/setup-go@main")),
        ("setup-python without a ref", replace_every(r"actions/setup-python@v\d+\.\d+\.\d+", "actions/setup-python")),
    ]
    return mutations


MUTATIONS = workflow_mutations()


class WorkflowContractTests(unittest.TestCase):
    def setUp(self):
        self.text = WORKFLOW.read_text(encoding="utf-8")

    def test_workflow_meets_the_contract(self):
        self.assertEqual(contract_failures(self.text), [])

    def test_every_fail_open_edit_is_reported(self):
        self.assertGreater(len(MUTATIONS), 70)
        for label, mutate in MUTATIONS:
            with self.subTest(mutation=label):
                mutated = mutate(self.text)
                self.assertNotEqual(mutated, self.text)
                self.assertNotEqual(contract_failures(mutated), [], f"{label}: not reported")

    def test_action_version_bumps_are_not_contract_changes(self):
        for label, mutate in ACCEPTED_ACTION_BUMPS:
            with self.subTest(bump=label):
                bumped = mutate(self.text)
                self.assertNotEqual(bumped, self.text)
                self.assertEqual(contract_failures(bumped), [], label)

    def test_mutations_cover_both_targets_equally(self):
        counts = {prefix: sum(1 for label, _ in MUTATIONS if label.endswith(f"[{prefix}]")) for prefix in TARGETS}
        self.assertEqual(len(set(counts.values())), 1, counts)

    def test_aggregates_are_the_only_jobs_with_the_required_check_names(self):
        jobs = workflow_yaml.load(self.text)["jobs"]
        for prefix, target in TARGETS.items():
            with self.subTest(target=prefix):
                job = jobs[f"{prefix}-test"]
                self.assertEqual(job["name"], target["check"])
                self.assertEqual(job["if"], "always()")
                self.assertEqual(
                    job["needs"], [f"{prefix}-test-{component}" for component in target["components"]]
                )
                # Everything on the path to the check is in `needs`: each component, and
                # nothing a component needs that the aggregate does not.
                for component in target["components"]:
                    needed = jobs[f"{prefix}-test-{component}"].get("needs", [])
                    for upstream in [needed] if isinstance(needed, str) else needed:
                        self.assertIn(upstream, job["needs"])
        self.assertEqual(jobs["test"]["name"], "test (${{ matrix.os }})")
        self.assertEqual(jobs["test"]["strategy"]["matrix"]["os"], ["ubuntu-latest"])

    def test_no_test_path_job_or_step_can_pass_without_running(self):
        jobs = workflow_yaml.load(self.text)["jobs"]
        # The only conditions on the test path, each of which widens when a step runs.
        allowed_conditions = {"always()", "${{ !cancelled() }}", "${{ github.event_name == 'push' }}"}
        for prefix in TARGETS:
            allowed = allowed_conditions | {
                f"${{{{ always() && needs.{prefix}-test-prepare.result == 'success' }}}}"
            }
            for name, job in jobs.items():
                if not name.startswith(f"{prefix}-test"):
                    continue
                with self.subTest(job=name):
                    self.assertNotIn("continue-on-error", job)
                    self.assertEqual("if" in job, name == f"{prefix}-test")
                    for step in job["steps"]:
                        self.assertNotIn("continue-on-error", step)
                        if "if" in step:
                            self.assertIn(step["if"], allowed)
                        script = step.get("run", "")
                        for escape in ("||", "exit 0", "set +e", "SilentlyContinue", "; true"):
                            self.assertNotIn(escape, script)
                        if step.get("shell") == "pwsh":
                            self.assertTrue(script.startswith("$ErrorActionPreference = 'Stop'\n"))

    @unittest.skipUnless(os.name == "posix" and shutil.which("bash"), "needs a POSIX bash")
    def test_final_step_fails_for_any_result_other_than_success(self):
        jobs = workflow_yaml.load(self.text)["jobs"]
        for prefix, target in TARGETS.items():
            step = jobs[f"{prefix}-test"]["steps"][-1]
            variables = sorted(step["env"])
            self.assertEqual(variables, sorted(f"{c.upper()}_RESULT" for c in target["components"]))

            def run(results):
                environment = {"PATH": os.environ["PATH"], **dict(zip(variables, results))}
                # GitHub runs `run:` steps with `bash -e`.
                return subprocess.run(
                    ["bash", "-e", "-c", step["run"]], env=environment, capture_output=True, text=True
                ).returncode

            with self.subTest(target=prefix):
                self.assertEqual(run(["success"] * len(variables)), 0)
                for position in range(len(variables)):
                    for result in ("failure", "cancelled", "skipped", ""):
                        results = ["success"] * len(variables)
                        results[position] = result
                        self.assertEqual(run(results), 1, (variables[position], result))

    def test_every_action_that_moves_artifacts_or_caches_is_pinned_to_a_commit(self):
        for line in self.text.splitlines():
            if "uses: actions/" in line and any(
                name in line for name in ("upload-artifact", "download-artifact", "actions/cache")
            ):
                self.assertRegex(line, r"@[0-9a-f]{40} # v\d+\.\d+\.\d+$")

    def test_every_workflow_python_command_disables_bytecode_writes(self):
        scripts = [
            step["run"]
            for job in workflow_yaml.load(self.text)["jobs"].values()
            for step in job["steps"]
            if "run" in step
        ]
        commands = [line for script in scripts for line in script.splitlines() if "python " in line]
        self.assertTrue(commands)
        for command in commands:
            self.assertEqual(command.count("python "), command.count("python -B "), command)

    def test_workflow_uses_only_ephemeral_hosted_runners_and_no_baseline(self):
        lowered = self.text.lower()
        for forbidden in ("self-hosted", "monolithic-baseline", "baseline-events", "-test.bench"):
            self.assertNotIn(forbidden, lowered)


if __name__ == "__main__":
    unittest.main()
