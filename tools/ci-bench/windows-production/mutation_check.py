#!/usr/bin/env python3
"""Mutation check for the sharding harness: every listed fail-open edit must fail a unit test.

Each entry below is one way of making the harness accept something it must
refuse (a root that never ran, a mismatched binary hash, an unchecked exit
code, an unanchored run expression, ...). The script applies each edit to a
temporary copy of this directory, runs the harness unit tests against the
copy, and requires that they fail. A mutation that survives means a guarantee
has no test; a mutation whose anchor text no longer occurs means this list is
stale. Either makes the script exit nonzero, so the list is maintained with
the code.

    python3 -B tools/ci-bench/windows-production/mutation_check.py

Nothing outside the temporary copies is modified. The tests that need a Go
toolchain or the workflow file are left out; they do not exercise the code
mutated here.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path


HARNESS = Path(__file__).resolve().parent
TEST_MODULES = [
    "test_plan_shards",
    "test_refresh_weights",
    "test_run_shard_progress",
    "test_macos_target",
    "test_verify_results",
    "test_verifier_guarantees",
    "test_runner_guarantees",
]

ROOT = 'if run_count != 1 or sum(terminals.values()) != 1 or terminals["fail"] != 0:'
FAILEV = '            if action == "fail":\n                failures.append(f"shard {index} has failed test event {package}::{test}")'
MUTATIONS = [
 ("verify_results.py", "missing-root accepted", [(ROOT, 'if run_count > 1 or sum(terminals.values()) > 1 or terminals["fail"] != 0:')]),
 ("verify_results.py", "duplicate-root-run accepted", [(ROOT, 'if run_count < 1 or sum(terminals.values()) < 1 or terminals["fail"] != 0:')]),
 ("verify_results.py", "root fail terminal accepted (counter only)", [(ROOT, 'if run_count != 1 or sum(terminals.values()) != 1:')]),
 ("verify_results.py", "fail test event accepted (event only)", [(FAILEV, '            pass')]),
 ("verify_results.py", "BOTH root fail checks removed", [(ROOT, 'if run_count != 1 or sum(terminals.values()) != 1:'), (FAILEV, '            pass')]),
 ("verify_results.py", "invocation binary hash vs plan skipped", [('if invocation.get("binarySha256") != expected.get("binarySha256"):', 'if False:')]),
 ("verify_results.py", "invocation binary name skipped", [('if invocation.get("binaryName") != expected.get("binaryName"):', 'if False:')]),
 ("verify_results.py", "bundle binary hash vs inventory skipped", [('if plan_shards.sha256_file(binary) != inventory["binarySha256"]:', 'if False:')]),
 ("verify_results.py", "bundle binary size skipped", [('if binary.stat().st_size != inventory["binarySizeBytes"]:', 'if False:')]),
 ("verify_results.py", "shard planSha256 skipped", [('        if metadata.get("planSha256") != plan_sha256:\n            failures.append(f"shard {index} plan hash', '        if False:\n            failures.append(f"shard {index} plan hash')]),
 ("verify_results.py", "other planSha256 skipped", [('    if metadata.get("planSha256") != plan_sha256:\n        failures.append("non-heavy plan hash', '    if False:\n        failures.append("non-heavy plan hash')]),
 ("verify_results.py", "other pkg-inventory sha skipped", [('if metadata.get("packageInventorySha256") != package_inventory_sha256:', 'if False:')]),
 ("verify_results.py", "root ran in wrong shard accepted", [('if assignment_for_root.get(key) != index:', 'if False:')]),
 ("verify_results.py", "metadata count check skipped", [('if len(metadata_paths) != shard_count:', 'if False:')]),
 ("verify_results.py", "shard indexes incomplete skipped", [('if set(by_index) != set(range(shard_count)):', 'if False:')]),
 ("verify_results.py", "BOTH shard completeness skipped", [('if len(metadata_paths) != shard_count:', 'if False:'), ('if set(by_index) != set(range(shard_count)):', 'if False:')]),
 ("verify_results.py", "exit code nonzero accepted", [('    if not present:\n        failures.append(f"{label} is missing required exit field {location}")\n    elif not passed:\n        failures.append(f"{label} has nonzero or non-integer exit at {location}: {value!r}")', '    pass')]),
 ("verify_results.py", "process start/terminal multiplicity skipped", [('if package_starts[(index, package)] != 1 or terminals != {"fail": 0, "pass": 1, "skip": 0}:', 'if False:')]),
 ("verify_results.py", "package non-pass terminal event skipped", [('                    if action != "pass":\n                        failures.append(f"shard {index} package {package} terminated with {action}")', '                    pass')]),
 ("verify_results.py", "BOTH process-level checks skipped", [('if package_starts[(index, package)] != 1 or terminals != {"fail": 0, "pass": 1, "skip": 0}:', 'if False:'), ('                    if action != "pass":\n                        failures.append(f"shard {index} package {package} terminated with {action}")', '                    pass')]),
 ("verify_results.py", "plan omits roots skipped", [('if missing_roots:', 'if False:')]),
 ("verify_results.py", "plan duplicate roots skipped", [('if duplicate_roots:', 'if False:')]),
 ("verify_results.py", "plan regex mismatch skipped", [('if regex_mismatches:', 'if False:')]),
 ("verify_results.py", "plan unknown root skipped", [('                    failures.append(f"plan shard {position} {package} contains an unknown root")', '                    pass')]),
 ("verify_results.py", "plan rootCount skipped", [('if plan.get("rootCount") != len(expected_roots):', 'if False:')]),
 ("verify_results.py", "inventory digest skipped", [('if plan.get("inventorySetSha256") != plan_shards._inventory_digest(inventories):', 'if False:')]),
 ("verify_results.py", "unexpected event roots skipped", [('if unexpected_event_roots:', 'if False:')]),
 ("verify_results.py", "shard repo sha skipped", [('if metadata.get("repositorySha") != plan.get("repositorySha"):\n            failures.append(f"shard {index} repository', 'if False:\n            failures.append(f"shard {index} repository')]),
 ("verify_results.py", "shard errors skipped", [('        if metadata.get("errors") != []:', '        if False:')]),
 ("verify_results.py", "other missing package accepted", [('if starts[package] != 1 or sum(counts.values()) != 1 or counts["fail"] != 0:', 'if starts[package] > 1 or sum(counts.values()) > 1 or counts["fail"] != 0:')]),
 ("verify_results.py", "other package/test fail events accepted (all 3)", [('counts["fail"] != 0:', 'False:'), ('                if action == "fail":\n                    failures.append(f"non-heavy package {package} failed")', '                pass'), ('if failed_tests:', 'if False:')]),
 ("verify_results.py", "other expectedPackages skipped", [('if metadata.get("expectedPackages") != expected_packages:', 'if False:')]),
 ("verify_results.py", "other unexpected packages skipped", [('if unexpected_packages:', 'if False:')]),
 ("verify_results.py", "other benchmarks skipped", [('if benchmark_roots:', 'if False:')]),
 ("verify_results.py", "heavy names vs inventories skipped", [('if heavy_names != set(root_details):', 'if False:')]),
# Not listed: skipping the verifier's `settings_target(settings) is not target` check. It is
# unreachable defence in depth: settings of another target name binaries with that target's
# suffix, which load_settings and load_inventories refuse first, so no test can tell it apart.
 ("verify_results.py", "invocation set vs plan skipped", [('if set(actual_invocations) != set(expected_assignments):', 'if False:')]),
 ("verify_results.py", "invocation rootCount skipped", [('if invocation.get("rootCount") != len(expected.get("roots", [])):', 'if False:')]),
 ("verify_results.py", "paniconexit0 skipped", [('if invocation.get("paniconexit0") is not True:', 'if False:')]),
 ("verify_results.py", "shard event unexpected package skipped", [('            if package not in expected_packages:\n                failures.append(f"shard {index} emitted an unexpected package {package}")\n                continue', '            if package not in expected_packages:\n                continue')]),
 ("verify_results.py", "weights hash skipped", [('if plan.get("historicalWeights", {}).get("sha256") != plan_shards.sha256_file(weights_path):', 'if False:')]),
 ("verify_results.py", "prepare exits skipped", [('        verify_prepare_operation_exits(\n            prepare,\n            {inventory["importPath"] for inventory in inventories},\n            str(prepare_path),\n            failures,\n            exit_report,\n        )', '        pass')]),
 ("verify_results.py", "shardCount vs array skipped", [('        or len(shards) != shard_count\n', '        or False\n')]),
 ("verify_results.py", "empty shard assignments accepted", [('            failures.append(f"plan shard {position} has no assignments")\n', '            pass\n')]),
 ("verify_results.py", "duplicate shard metadata accepted", [('            failures.append(f"shard {index} has duplicate metadata artifacts")\n            continue', '            pass')]),
 ("verify_results.py", "testmain settings-vs-inventory skipped", [('if configured_test_mains != inventory_test_mains:', 'if False:')]),
 ("verify_results.py", "shard cmdline budget skipped", [('                or not 1 <= command_units <= plan["settings"]["commandLineLimit"]\n            ):\n                failures.append(f"shard {index}', '                or False\n            ):\n                failures.append(f"shard {index}')]),
 ("targets.py", "argument area evidence always valid", [('    if not target.posix:\n        return True\n    if not isinstance(record, dict):', '    return True\n    if not isinstance(record, dict):')]),
 ("targets.py", "argument area never enforced at launch", [('    if used > maximum:', '    if False:')]),
 ("run_shard.py", "binary hash mismatch not refused", [('if actual_binary_hash != expected_binary_hash:', 'if False:')]),
 ("run_shard.py", "command budget not enforced", [('if command_units > command_limit:', 'if False:')]),
 ("run_shard.py", "repo sha mismatch not refused", [('if repository_sha != args.expected_repository_sha or repository_sha != plan.get("repositorySha"):', 'if False:')]),
 ("run_shard.py", "final exit ignores test failure", [('            if completed.returncode != 0 and final_exit == 0:\n                final_exit = completed.returncode', '            pass')]),
 ("run_shard.py", "chmod before hash check (moved up)", [('            actual_binary_hash = sha256_file(binary)\n', '            ensure_executable(binary, target)\n            actual_binary_hash = sha256_file(binary)\n')]),
 ("run_other.py", "other budget not enforced", [('if command_units > command_limit:', 'if False:')]),
 ("run_other.py", "other returns 0 regardless", [('        return completed.returncode\n    finally:', '        return 0\n    finally:')]),
 ("plan_shards.py", "regex not anchored at end", [('return "^" + _render_trie(root) + "$"', 'return "^" + _render_trie(root)')]),
 ("plan_shards.py", "regex names not escaped", [('re.escape(character) + _render_trie(child)', 'character + _render_trie(child)')]),
 ("plan_shards.py", "planner drops last root", [('    for item in work:\n        destination', '    for item in work[:-1]:\n        destination')]),
 ("plan_shards.py", "unweighted root dropped", [('                weight = settings["defaultWeightSeconds"]\n                source = "default-current-inventory"', '                continue')]),
 ("plan_shards.py", "testmain drift not checked in plan", [('if inventory["testMainDeclarations"] != expected_test_mains[inventory["importPath"]]:', 'if False:')]),
 ("prepare.py", "prepare testmain drift not checked", [('if test_main_declarations != configured["expectedTestMainDeclarations"]:', 'if False:')]),
 ("prepare.py", "prepare sha mismatch not refused", [('if repository_sha != args.expected_repository_sha:', 'if False:')]),
 ("prepare.py", "prepare listing stderr tolerated", [('        if listing_process.stderr:', '        if False:')]),
 ("verify_results.py", "expected commit not compared with the plan", [('if plan.get("repositorySha") != expected_sha:', 'if False:')]),
 ("verify_results.py", "inventory commit not compared with the expected commit", [('if inventory["repositorySha"] != expected_sha:', 'if False:')]),
 ("verify_results.py", "expected commit format unchecked", [('if not isinstance(expected_sha, str) or not re.fullmatch(r"[0-9a-f]{40}", expected_sha):', 'if False:')]),
 ("verify_results.py", "shard's own listing not compared with the plan", [('if runnable != set(root_details.get(package, {})) or stray or len(listed) != len(set(listed)):', 'if False:')]),
 ("verify_results.py", "shard's own listing may be absent", [('if not isinstance(listed, list) or any(not isinstance(name, str) for name in listed):', 'if False:')]),
 ("verify_results.py", "listed non-test names tolerated", [('if runnable != set(root_details.get(package, {})) or stray or', 'if runnable != set(root_details.get(package, {})) or')]),
 ("verify_results.py", "non-heavy job's own go list not compared", [('if metadata.get("listedPackages") != all_packages:', 'if False:')]),
 ("verify_results.py", "run expression checked by full match", [('name for name in root_details[package] if plan_shards.run_regex_selects(expression, name)', 'name for name in root_details[package] if compiled.fullmatch(name)')]),
 ("verify_results.py", "post-run shard commit skipped", [('if metadata.get("repositoryShaAfter") != plan.get("repositorySha"):\n            failures.append(f"shard {index} post-run', 'if False:\n            failures.append(f"shard {index} post-run')]),
 ("verify_results.py", "shard root count vs assignments skipped", [('if shard.get("rootCount") != counted_roots:', 'if False:')]),
 ("verify_results.py", "plan package set skipped", [('if set(normalized_plan_packages) != set(inventory_by_package):', 'if False:')]),
 ("verify_results.py", "non-heavy errors skipped", [('    if metadata.get("errors") != []:\n        failures.append("non-heavy metadata contains errors")', '    if False:\n        failures.append("non-heavy metadata contains errors")')]),
 ("verify_results.py", "prepare operation set skipped", [('    if actual != expected:\n        failures.append("prepare operation phase/package set differs', '    if False:\n        failures.append("prepare operation phase/package set differs')]),
 ("plan_shards.py", "run expression selection is a full match", [('return re.search(expression, name) is not None', 'return re.fullmatch(expression, name) is not None')]),
 ("plan_shards.py", "subtest separator in a run expression tolerated", [('    if "/" in expression:\n        raise ValueError(f"run expression addresses subtests: {expression!r}")\n', '')]),
 ("plan_shards.py", "regex not anchored at start", [('return "^" + _render_trie(root) + "$"', 'return _render_trie(root) + "$"')]),
 ("plan_shards.py", "parallel roots planned as serial", [('if item["parallel"]:\n            parallel += item["weightSeconds"]', 'if False:\n            parallel += item["weightSeconds"]')]),
 ("run_shard.py", "shard listing failure tolerated", [('if completed.returncode != 0 or completed.stderr:\n        raise RuntimeError(\n            f"listing the compiled roots', 'if False:\n        raise RuntimeError(\n            f"listing the compiled roots')]),
 ("run_shard.py", "shard listing not recorded", [('                "listedNames": listed_names,\n', '')]),
 ("run_shard.py", "plan toolchain mismatch not refused", [('if go_version != plan.get("goVersion"):', 'if False:')]),
 ("run_other.py", "non-heavy package listing not recorded", [('            "listedPackages": listed_packages,\n', '')]),
 ("run_other.py", "non-heavy sha mismatch not refused", [('        repository_sha != args.expected_repository_sha\n        or repository_sha != plan.get("repositorySha")', '        False\n        or repository_sha != plan.get("repositorySha")')]),
 ("prepare.py", "prepare dirty worktree tolerated", [('    require_tracked_worktree_clean(clean_before, "tracked worktree precondition")', '    pass')]),
 ("prepare.py", "prepare target environment unchecked", [('    if target_environment != target.environment:\n        raise RuntimeError', '    if False:\n        raise RuntimeError')]),
 ("prepare.py", "subtest path accepted as a root", [(' or "/" in name:', ':')]),
 ("refresh_weights.py", "failed shard evidence accepted for weights", [('if metadata.get("exitCode") != 0 or metadata.get("errors") != []:', 'if False:')]),
 ("refresh_weights.py", "incomplete shard evidence accepted for weights", [('if sorted(indexes, key=str) != sorted(range(shard_count), key=str):', 'if False:')]),
]


def run_tests(directory: Path) -> bool:
    """Whether the harness unit tests pass against the copy in directory."""
    completed = subprocess.run(
        [sys.executable, "-B", "-m", "unittest", *TEST_MODULES],
        cwd=directory / "tests",
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )
    return completed.returncode == 0


def check(mutation: tuple[str, str, list[tuple[str, str]]], scratch: Path) -> tuple[str, str]:
    """Apply one mutation to a private copy and report killed, survived or stale."""
    file_name, label, replacements = mutation
    with tempfile.TemporaryDirectory(dir=scratch) as directory:
        copy = Path(directory) / "harness"
        shutil.copytree(HARNESS, copy, ignore=shutil.ignore_patterns("__pycache__"))
        path = copy / file_name
        text = path.read_text(encoding="utf-8")
        for old, new in replacements:
            if old not in text:
                return label, "stale"
            text = text.replace(old, new, 1)
        path.write_text(text, encoding="utf-8", newline="\n")
        return label, "survived" if run_tests(copy) else "killed"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--jobs", type=int, default=os.cpu_count() or 2)
    parser.add_argument("--list", action="store_true", help="print the mutations and exit")
    args = parser.parse_args(sys.argv[1:] if argv is None else argv)
    if args.list:
        for file_name, label, _ in MUTATIONS:
            print(f"{file_name}: {label}")
        return 0
    with tempfile.TemporaryDirectory() as scratch_name:
        scratch = Path(scratch_name)
        baseline = scratch / "baseline"
        shutil.copytree(HARNESS, baseline, ignore=shutil.ignore_patterns("__pycache__"))
        if not run_tests(baseline):
            print("error: the unmutated harness does not pass its unit tests", file=sys.stderr)
            return 2
        with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, args.jobs)) as pool:
            results = list(pool.map(lambda mutation: check(mutation, scratch), MUTATIONS))
    bad = [(label, outcome) for label, outcome in results if outcome != "killed"]
    for label, outcome in bad:
        print(f"{outcome.upper()}: {label}")
    killed = len(results) - len(bad)
    print(f"{killed} of {len(results)} mutations killed")
    return 0 if not bad else 1


if __name__ == "__main__":
    raise SystemExit(main())
