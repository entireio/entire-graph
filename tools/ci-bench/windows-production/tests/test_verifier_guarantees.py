"""One test per verifier guarantee, on a tiny synthetic bundle, for both targets.

Each case starts from evidence that verifies, makes ONE change that must not
verify, and requires the verifier to name that change. Requiring the specific
failure (not just a nonzero exit) is what ties a test to its guarantee: if the
check is removed, some other check may still fail the run, but this test will
not find its message. `mutation_check.py` removes the checks one by one to
prove it.
"""

import json
import shutil
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPT_DIR))
sys.path.insert(0, str(Path(__file__).resolve().parent))

import plan_shards  # noqa: E402
import targets  # noqa: E402
import test_verify_results as verify_fixtures  # noqa: E402
import verify_results  # noqa: E402


HEAVY = "example/heavy"
OTHER_SHA = "c" * 40


def read(path):
    return json.loads(path.read_text(encoding="utf-8"))


def write(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


class Evidence:
    """A verifying evidence set on disk, with helpers that change one thing."""

    def __init__(self, root, target):
        self.target = target
        fixtures = verify_fixtures.VerifyResultsTests()
        self.arguments, self.output, self.bundle, self.shards_root, self.other_root = fixtures.make_fixture(
            root, target
        )
        self.run_verifier = fixtures.run_verifier
        self.plan_path = self.bundle / "plan.json"
        self.other_metadata = self.other_root / "other-metadata.json"
        self.other_events = self.other_root / "other-events.jsonl"

    # -- reading and running -------------------------------------------------
    def verify(self, arguments=None):
        code = self.run_verifier(self.arguments if arguments is None else arguments)
        return code, read(self.output)["failures"]

    def shard_directories(self):
        return sorted(path.parent for path in self.shards_root.rglob("shard-metadata.json"))

    def shard_of(self, root_name):
        """The (directory, index) of the shard the plan assigns root_name to."""
        for shard in read(self.plan_path)["shards"]:
            for assignment in shard["assignments"]:
                if any(root["name"] == root_name for root in assignment["roots"]):
                    for directory in self.shard_directories():
                        if read(directory / "shard-metadata.json")["shardIndex"] == shard["index"]:
                            return directory, shard["index"]
        raise AssertionError(f"{root_name} is not planned")

    def other_shard(self, root_name):
        directory, _ = self.shard_of(root_name)
        return next(path for path in self.shard_directories() if path != directory)

    # -- changing one thing --------------------------------------------------
    def events(self, directory, change):
        path = directory / "shard-events.jsonl"
        events = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]
        events = change(events) or events
        path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")

    def metadata(self, directory, change):
        path = directory / "shard-metadata.json"
        value = read(path)
        change(value)
        write(path, value)

    def invocation(self, directory, change):
        self.metadata(directory, lambda value: change(value["invocations"][0]))

    def plan(self, change):
        value = read(self.plan_path)
        change(value)
        write(self.plan_path, value)

    def other(self, change):
        value = read(self.other_metadata)
        change(value)
        write(self.other_metadata, value)

    def non_heavy_events(self, change):
        events = [json.loads(line) for line in self.other_events.read_text(encoding="utf-8").splitlines()]
        events = change(events) or events
        self.other_events.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")

    def rehash(self):
        """Make every recorded hash agree with the files as they now are."""
        plan_hash = plan_shards.sha256_file(self.plan_path)
        for directory in self.shard_directories():
            self.metadata(directory, lambda value: value.__setitem__("planSha256", plan_hash))
        package_hash = plan_shards.sha256_file(self.bundle / "package-inventory.json")
        self.other(
            lambda value: value.update({"planSha256": plan_hash, "packageInventorySha256": package_hash})
        )


def terminal_of(events, name):
    return next(e for e in events if e.get("Test") == name and e["Action"] in ("pass", "skip", "fail"))


# Each case: name -> (change(evidence), fragments every one of which must be reported).
CASES = {}


def case(*fragments):
    def register(function):
        CASES[function.__name__] = (function, fragments)
        return function

    return register


ROOT_COVERAGE = "planned roots lack exactly one run and successful terminal"


# ---- every planned root ran exactly once, successfully, in its own shard ----
@case(ROOT_COVERAGE)
def root_that_never_ran(e):
    directory, _ = e.shard_of("FuzzC")
    e.events(directory, lambda events: [event for event in events if event.get("Test") != "FuzzC"])


@case(ROOT_COVERAGE)
def root_that_ran_twice(e):
    directory, _ = e.shard_of("FuzzC")
    e.events(directory, lambda events: events.append({"Action": "run", "Package": HEAVY, "Test": "FuzzC"}))


@case(ROOT_COVERAGE)
def root_with_two_terminal_events(e):
    directory, _ = e.shard_of("FuzzC")
    e.events(directory, lambda events: events.append({"Action": "pass", "Package": HEAVY, "Test": "FuzzC"}))


@case(ROOT_COVERAGE)
def root_with_a_run_but_no_terminal_event(e):
    directory, _ = e.shard_of("FuzzC")
    e.events(directory, lambda events: events.remove(terminal_of(events, "FuzzC")))


@case(ROOT_COVERAGE, "has failed test event example/heavy::FuzzC")
def root_that_failed(e):
    directory, _ = e.shard_of("FuzzC")
    e.events(directory, lambda events: terminal_of(events, "FuzzC").__setitem__("Action", "fail"))


@case("has failed test event example/heavy::TestA/sub")
def subtest_that_failed_under_a_passing_root(e):
    directory, _ = e.shard_of("TestA")
    e.events(directory, lambda events: events.append({"Action": "fail", "Package": HEAVY, "Test": "TestA/sub"}))


@case("emitted unassigned root example/heavy::FuzzC")
def root_that_ran_in_another_shard(e):
    source, _ = e.shard_of("FuzzC")
    moved = []

    def take(events):
        moved.extend(event for event in events if event.get("Test") == "FuzzC")
        return [event for event in events if event.get("Test") != "FuzzC"]

    e.events(source, take)
    e.events(e.other_shard("FuzzC"), lambda events: events.extend(moved))


@case("roots absent from compiled inventories")
def root_the_inventory_never_listed(e):
    directory, _ = e.shard_of("TestA")
    e.events(
        directory,
        lambda events: events.extend(
            [
                {"Action": "run", "Package": HEAVY, "Test": "TestUnlisted"},
                {"Action": "pass", "Package": HEAVY, "Test": "TestUnlisted"},
            ]
        ),
    )


@case("emitted an unexpected package example/stranger")
def event_from_a_package_the_shard_was_not_assigned(e):
    directory, _ = e.shard_of("TestA")
    e.events(directory, lambda events: events.append({"Action": "start", "Package": "example/stranger"}))


# ---- every shard reported, once -------------------------------------------
@case("found 1 shard metadata files, want 2", "shard result indexes are incomplete")
def shard_with_no_evidence(e):
    shutil.rmtree(e.shard_directories()[0])


@case("found 3 shard metadata files, want 2", "has duplicate metadata artifacts")
def shard_reported_twice(e):
    first = e.shard_directories()[0]
    shutil.copytree(first, first.with_name(first.name + "-again"))


@case("has duplicate metadata artifacts", "shard result indexes are incomplete")
def shard_reported_twice_in_place_of_another(e):
    first, second = e.shard_directories()
    index = read(first / "shard-metadata.json")["shardIndex"]
    e.metadata(second, lambda value: value.__setitem__("shardIndex", index))


@case("invalid shard index 7")
def shard_index_outside_the_plan(e):
    e.metadata(e.shard_directories()[0], lambda value: value.__setitem__("shardIndex", 7))


# ---- every test process started once and passed ----------------------------
PROCESS = "shard package processes have wrong start/terminal multiplicity"


@case(PROCESS)
def package_process_that_started_twice(e):
    directory, _ = e.shard_of("TestA")
    e.events(directory, lambda events: events.append({"Action": "start", "Package": HEAVY}))


@case(PROCESS)
def package_process_that_never_terminated(e):
    directory, _ = e.shard_of("TestA")
    e.events(directory, lambda events: [e_ for e_ in events if not (e_["Action"] == "pass" and "Test" not in e_)])


@case(PROCESS, "package example/heavy terminated with skip")
def package_process_that_was_skipped(e):
    directory, _ = e.shard_of("TestA")

    def skip(events):
        next(e_ for e_ in events if e_["Action"] == "pass" and "Test" not in e_)["Action"] = "skip"

    e.events(directory, skip)


@case(PROCESS, "package example/heavy terminated with fail")
def package_process_that_failed(e):
    directory, _ = e.shard_of("TestA")

    def fail(events):
        next(e_ for e_ in events if e_["Action"] == "pass" and "Test" not in e_)["Action"] = "fail"

    e.events(directory, fail)


# ---- what each shard says it launched ---------------------------------------
@case("binary hash differs from plan")
def shard_ran_a_binary_with_another_hash(e):
    e.invocation(e.shard_directories()[0], lambda value: value.__setitem__("binarySha256", "0" * 64))


@case("binary name differs from plan")
def shard_ran_a_binary_with_another_name(e):
    e.invocation(e.shard_directories()[0], lambda value: value.__setitem__("binaryName", "other.test"))


@case("root count differs from plan")
def shard_reports_another_root_count(e):
    e.invocation(e.shard_directories()[0], lambda value: value.__setitem__("rootCount", 99))


@case("invocation package set differs from plan")
def shard_launched_no_process(e):
    e.metadata(e.shard_directories()[0], lambda value: value.__setitem__("invocations", []))


@case("invoked unexpected package example/stranger")
def shard_launched_an_unplanned_package(e):
    def add(value):
        value["invocations"].append({**value["invocations"][0], "package": "example/stranger"})

    e.metadata(e.shard_directories()[0], add)


@case("omitted -test.paniconexit0")
def shard_launched_without_paniconexit0(e):
    e.invocation(e.shard_directories()[0], lambda value: value.__setitem__("paniconexit0", False))


@case("did not set PWD to its package directory")
def shard_launched_outside_the_package_directory(e):
    e.invocation(e.shard_directories()[0], lambda value: value.__setitem__("pwdMatchesPackageDirectory", False))


@case("did not prepend the Go tool directory to PATH")
def shard_launched_without_the_go_tool_directory(e):
    e.invocation(
        e.shard_directories()[0], lambda value: value.__setitem__("goToolDirectoryPrependedToPath", False)
    )


@case("working directory differs from plan")
def shard_launched_in_another_directory(e):
    e.invocation(e.shard_directories()[0], lambda value: value.__setitem__("workingDirectoryRelative", "elsewhere"))


@case("command line exceeded or omitted its budget")
def shard_command_line_over_its_budget(e):
    limit = read(e.plan_path)["settings"]["commandLineLimit"]
    e.invocation(
        e.shard_directories()[0], lambda value: value.__setitem__(e.target.command_line_metric, limit + 1)
    )


@case("nonzero or non-integer exit at $.invocations[0].exitCode")
def shard_process_exit_code_nonzero(e):
    e.invocation(e.shard_directories()[0], lambda value: value.__setitem__("exitCode", 1))


@case("nonzero or non-integer exit at $.exitCode")
def shard_exit_code_nonzero(e):
    e.metadata(e.shard_directories()[0], lambda value: value.__setitem__("exitCode", 1))


@case("missing required exit field $.cleanTestCacheExitCode")
def shard_without_a_cache_clean_exit(e):
    e.metadata(e.shard_directories()[0], lambda value: value.pop("cleanTestCacheExitCode"))


@case("metadata contains errors")
def shard_that_recorded_an_error(e):
    e.metadata(e.shard_directories()[0], lambda value: value.__setitem__("errors", ["boom"]))


@case("plan hash differs from the downloaded bundle")
def shard_that_ran_another_plan(e):
    e.metadata(e.shard_directories()[0], lambda value: value.__setitem__("planSha256", "0" * 64))


@case("repository SHA differs from plan")
def shard_that_ran_another_commit(e):
    e.metadata(e.shard_directories()[0], lambda value: value.__setitem__("repositorySha", OTHER_SHA))


@case("post-run repository SHA differs from plan")
def shard_whose_checkout_moved(e):
    e.metadata(e.shard_directories()[0], lambda value: value.__setitem__("repositoryShaAfter", OTHER_SHA))


@case("Go version differs from plan")
def shard_with_another_toolchain(e):
    e.metadata(e.shard_directories()[0], lambda value: value.__setitem__("goVersion", "go version go1.0 x/y"))


@case("metadata has the wrong shard count")
def shard_that_believes_in_another_shard_count(e):
    e.metadata(e.shard_directories()[0], lambda value: value.__setitem__("shardCount", 3))


@case("metadata setting timeout differs from plan")
def shard_that_ran_with_another_timeout(e):
    e.metadata(e.shard_directories()[0], lambda value: value.__setitem__("timeout", "1h"))


# ---- the shard's own listing of its binary (independent of the bundle) ------
LISTING = "binary lists a different root set than the plan"


@case(LISTING)
def shard_binary_lists_a_root_the_plan_lacks(e):
    e.invocation(e.shard_directories()[0], lambda value: value["listedNames"].append("TestUnplanned"))


@case(LISTING)
def shard_binary_does_not_list_a_planned_root(e):
    e.invocation(e.shard_directories()[0], lambda value: value["listedNames"].remove("FuzzC"))


@case(LISTING)
def shard_binary_lists_something_that_is_no_test(e):
    e.invocation(e.shard_directories()[0], lambda value: value["listedNames"].append("main"))


@case("omitted its own listing of the compiled binary")
def shard_without_its_own_listing(e):
    e.invocation(e.shard_directories()[0], lambda value: value.pop("listedNames"))


# ---- the plan is a complete, disjoint partition of the inventory ------------
def first_assignment(plan, root_name):
    return next(
        assignment
        for shard in plan["shards"]
        for assignment in shard["assignments"]
        if any(root["name"] == root_name for root in assignment["roots"])
    )


@case("plan omits 1 compiled runnable roots")
def plan_that_omits_a_root(e):
    def omit(plan):
        assignment = first_assignment(plan, "FuzzC")
        assignment["roots"] = [root for root in assignment["roots"] if root["name"] != "FuzzC"]

    e.plan(omit)


@case("plan assigns 1 runnable roots more than once")
def plan_that_assigns_a_root_twice(e):
    def twice(plan):
        source = first_assignment(plan, "FuzzC")
        root = next(root for root in source["roots"] if root["name"] == "FuzzC")
        first_assignment(plan, "TestA")["roots"].append(root)

    e.plan(twice)


@case("contains an unknown root")
def plan_with_a_root_the_inventory_lacks(e):
    e.plan(lambda plan: first_assignment(plan, "TestA")["roots"].append({"name": "TestGhost", "kind": "Test"}))


@case("plan regexes select a different root set")
def plan_whose_expression_selects_other_roots(e):
    e.plan(lambda plan: first_assignment(plan, "TestA").__setitem__("runRegex", "^(TestA|FuzzC)$"))


@case("plan regexes select a different root set")
def plan_whose_expression_selects_nothing(e):
    e.plan(lambda plan: first_assignment(plan, "TestA").__setitem__("runRegex", "^TestNothing$"))


@case("has an unusable regex")
def plan_whose_expression_addresses_subtests(e):
    e.plan(lambda plan: first_assignment(plan, "TestA").__setitem__("runRegex", "^TestA$/^sub$"))


@case("plan rootCount differs from compiled inventories")
def plan_with_another_root_count(e):
    e.plan(lambda plan: plan.__setitem__("rootCount", 2))


@case("plan shardCount does not match the shard array")
def plan_whose_shard_count_is_not_its_shard_list(e):
    e.plan(lambda plan: plan.__setitem__("shardCount", 3))


@case("has no assignments")
def plan_with_an_empty_shard(e):
    e.plan(lambda plan: plan["shards"][0].__setitem__("assignments", []))


@case("rootCount differs from assignments")
def plan_shard_with_another_root_count(e):
    e.plan(lambda plan: plan["shards"][0].__setitem__("rootCount", 9))


@case("plan inventory digest does not match compiled inventories")
def plan_made_from_another_inventory(e):
    e.plan(lambda plan: plan.__setitem__("inventorySetSha256", "0" * 64))


@case("plan package set differs from compiled inventories")
def plan_that_declares_another_package(e):
    e.plan(lambda plan: plan["packages"][0].__setitem__("importPath", "example/stranger"))


@case("binarySha256 differs from compiled inventory")
def plan_that_names_another_binary_hash(e):
    e.plan(lambda plan: plan["packages"][0].__setitem__("binarySha256", "0" * 64))


@case("plan does not preserve shuffle=off semantics")
def plan_that_shuffles(e):
    e.plan(lambda plan: plan["settings"].__setitem__("shuffle", "on"))


# ---- the bundle is what the plan says it is ---------------------------------
def heavy_binary(e):
    return next((e.bundle / "binaries").iterdir())


@case("compiled binary hash differs from inventory")
def bundle_binary_with_other_bytes(e):
    path = heavy_binary(e)
    content = bytearray(path.read_bytes())
    content[0] ^= 0xFF
    path.write_bytes(bytes(content))


@case("compiled binary size differs from inventory")
def bundle_binary_with_another_size(e):
    with heavy_binary(e).open("ab") as handle:
        handle.write(b"x")


@case("compiled binary is missing")
def bundle_without_its_binary(e):
    heavy_binary(e).unlink()


@case("bundled historical-weight hash differs from plan")
def bundle_with_other_weights(e):
    path = e.bundle / "historical-weights.json"
    value = read(path)
    value["weights"][0]["seconds"] = 99.0
    write(path, value)


@case("bundled settings shard count differs from plan")
def bundle_with_another_shard_count_in_settings(e):
    path = e.bundle / "settings.json"
    value = read(path)
    value["shardCount"] = 3
    write(path, value)


@case("bundled setting timeout differs from plan")
def bundle_with_another_timeout_in_settings(e):
    path = e.bundle / "settings.json"
    value = read(path)
    value["timeout"] = "1h"
    write(path, value)


@case("heavy package declarations differ from compiled inventories")
def package_inventory_that_marks_another_package_heavy(e):
    path = e.bundle / "package-inventory.json"
    value = read(path)
    for row in value["packages"]:
        row["heavy"] = row["importPath"] == "example/other"
    write(path, value)


@case("nonzero or non-integer exit at $.operations[7].exitCode")
def prepare_operation_that_failed(e):
    path = e.bundle / "prepare-metadata.json"
    value = read(path)
    value["operations"][7]["exitCode"] = 1
    write(path, value)


@case("prepare operation phase/package set differs from the required schema")
def prepare_that_skipped_an_operation(e):
    path = e.bundle / "prepare-metadata.json"
    value = read(path)
    value["operations"] = [operation for operation in value["operations"] if operation["phase"] != "compile"]
    write(path, value)


@case("prepare repository SHA differs from plan")
def prepare_for_another_commit(e):
    path = e.bundle / "prepare-metadata.json"
    value = read(path)
    value["repositorySha"] = OTHER_SHA
    write(path, value)


@case("package inventory repository SHA differs from plan")
def package_inventory_for_another_commit(e):
    path = e.bundle / "package-inventory.json"
    value = read(path)
    value["repositorySha"] = OTHER_SHA
    write(path, value)


# ---- every non-heavy package ran once and passed ----------------------------
NON_HEAVY_COVERAGE = "non-heavy packages lack exactly one start and successful terminal"


@case("non-heavy package example/other failed", NON_HEAVY_COVERAGE)
def non_heavy_package_that_failed(e):
    def fail(events):
        next(
            event for event in events if event["Package"] == "example/other" and event["Action"] == "pass" and "Test" not in event
        )["Action"] = "fail"

    e.non_heavy_events(fail)


@case("non-heavy stream contains 1 failed test events")
def non_heavy_test_that_failed_under_a_passing_package(e):
    e.non_heavy_events(
        lambda events: events.append({"Action": "fail", "Package": "example/other", "Test": "TestOther/sub"})
    )


@case(NON_HEAVY_COVERAGE)
def non_heavy_package_that_never_ran(e):
    e.non_heavy_events(lambda events: [event for event in events if event["Package"] != "example/no-tests"])


@case(NON_HEAVY_COVERAGE)
def non_heavy_package_that_ran_twice(e):
    e.non_heavy_events(
        lambda events: events.extend(
            [{"Action": "start", "Package": "example/other"}, {"Action": "pass", "Package": "example/other"}]
        )
    )


@case(NON_HEAVY_COVERAGE)
def non_heavy_package_that_started_twice(e):
    e.non_heavy_events(lambda events: events.append({"Action": "start", "Package": "example/other"}))


@case("non-heavy metadata package list differs")
def non_heavy_run_that_was_given_another_package_list(e):
    e.other(lambda value: value.__setitem__("expectedPackages", ["example/other"]))


@case("non-heavy stream emitted 1 unexpected packages")
def non_heavy_run_that_ran_an_unlisted_package(e):
    e.non_heavy_events(lambda events: events.append({"Action": "start", "Package": "example/stranger"}))


@case("non-heavy default invocation ran 1 benchmarks")
def non_heavy_run_that_ran_a_benchmark(e):
    e.non_heavy_events(
        lambda events: events.append({"Action": "run", "Package": "example/other", "Test": "BenchmarkX"})
    )


@case("non-heavy plan hash differs from the downloaded bundle")
def non_heavy_run_of_another_plan(e):
    e.other(lambda value: value.__setitem__("planSha256", "0" * 64))


@case("non-heavy package-inventory hash differs from the downloaded bundle")
def non_heavy_run_of_another_package_inventory(e):
    e.other(lambda value: value.__setitem__("packageInventorySha256", "0" * 64))


@case("non-heavy repository SHA differs from plan")
def non_heavy_run_of_another_commit(e):
    e.other(lambda value: value.__setitem__("repositorySha", OTHER_SHA))


@case("nonzero or non-integer exit at $.testExitCode")
def non_heavy_run_whose_tests_exited_nonzero(e):
    e.other(lambda value: value.__setitem__("testExitCode", 1))


@case("missing required exit field $.testExitCode")
def non_heavy_run_without_a_test_exit_code(e):
    e.other(lambda value: value.pop("testExitCode"))


@case("non-heavy metadata contains errors")
def non_heavy_run_that_recorded_an_error(e):
    e.other(lambda value: value.__setitem__("errors", ["boom"]))


@case("non-heavy command line exceeded or omitted its budget")
def non_heavy_command_line_over_its_budget(e):
    limit = read(e.plan_path)["settings"]["commandLineLimit"]
    e.other(lambda value: value.__setitem__(e.target.command_line_metric, limit + 1))


@case("own go list differs from the bundled package inventory")
def non_heavy_job_lists_a_package_the_bundle_lacks(e):
    e.other(lambda value: value["listedPackages"].append("example/uninventoried"))


@case("own go list differs from the bundled package inventory")
def non_heavy_job_without_its_own_package_list(e):
    e.other(lambda value: value.pop("listedPackages"))


class VerifierGuaranteeTests(unittest.TestCase):
    """Generated below: one test method per case, each run for both targets."""

    def check(self, name):
        change, fragments = CASES[name]
        for target in (targets.WINDOWS_AMD64, targets.DARWIN_ARM64):
            with self.subTest(target=target.name), tempfile.TemporaryDirectory() as directory:
                evidence = Evidence(Path(directory), target)
                self.assertEqual(evidence.verify(), (0, []), "the unchanged evidence must verify")
                change(evidence)
                code, failures = evidence.verify()
                self.assertEqual(code, 1)
                for fragment in fragments:
                    self.assertTrue(
                        any(fragment in failure for failure in failures),
                        f"{fragment!r} not reported; got {failures!r}",
                    )


def _make(name):
    def test(self):
        self.check(name)

    test.__name__ = f"test_{name}_is_refused"
    return test


for _name in CASES:
    setattr(VerifierGuaranteeTests, f"test_{_name}_is_refused", _make(_name))


class CommitAnchorTests(unittest.TestCase):
    """The verifier is told which commit CI ran for; the evidence cannot choose it."""

    def with_expected(self, evidence, sha):
        arguments = list(evidence.arguments)
        arguments[arguments.index("--expected-repository-sha") + 1] = sha
        return arguments

    def test_evidence_for_another_commit_is_refused(self):
        for target in (targets.WINDOWS_AMD64, targets.DARWIN_ARM64):
            with self.subTest(target=target.name), tempfile.TemporaryDirectory() as directory:
                evidence = Evidence(Path(directory), target)
                code, failures = evidence.verify(self.with_expected(evidence, OTHER_SHA))
                self.assertEqual(code, 1)
                self.assertIn("plan repository SHA differs from the expected commit", failures)
                self.assertTrue(any("is for another commit" in failure for failure in failures))

    def test_evidence_rewritten_consistently_for_another_commit_is_refused(self):
        for target in (targets.WINDOWS_AMD64, targets.DARWIN_ARM64):
            with self.subTest(target=target.name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                evidence = Evidence(root, target)
                original = read(evidence.plan_path)["repositorySha"]
                for path in root.rglob("*.json*"):
                    path.write_text(path.read_text(encoding="utf-8").replace(original, OTHER_SHA), encoding="utf-8")
                inventories = plan_shards.load_inventories(
                    sorted((evidence.bundle / "inventories").glob("*.inventory.json")), target
                )
                evidence.plan(
                    lambda plan: plan.__setitem__("inventorySetSha256", plan_shards._inventory_digest(inventories))
                )
                evidence.rehash()
                # The rewrite is internally consistent: told that commit, the verifier accepts it.
                self.assertEqual(evidence.verify(self.with_expected(evidence, OTHER_SHA)), (0, []))
                # Told the commit CI actually ran for, it refuses, and for that reason only.
                code, failures = evidence.verify(self.with_expected(evidence, original))
                self.assertEqual(code, 1)
                self.assertEqual(
                    failures,
                    [
                        "plan repository SHA differs from the expected commit",
                        "compiled inventory of example/heavy is for another commit",
                    ],
                )

    def test_expected_commit_is_required_and_well_formed(self):
        with tempfile.TemporaryDirectory() as directory:
            evidence = Evidence(Path(directory), targets.WINDOWS_AMD64)
            code, failures = evidence.verify(self.with_expected(evidence, "main"))
            self.assertEqual(code, 1)
            self.assertTrue(any("40 lowercase hexadecimal" in failure for failure in failures))
            index = evidence.arguments.index("--expected-repository-sha")
            without = evidence.arguments[:index] + evidence.arguments[index + 2:]
            with self.assertRaises(SystemExit):
                evidence.run_verifier(without)


class IndependentInventoryTests(unittest.TestCase):
    """The bundle is not the only witness of what exists to be run."""

    def test_root_dropped_from_inventory_and_plan_alike_is_refused(self):
        for target in (targets.WINDOWS_AMD64, targets.DARWIN_ARM64):
            with self.subTest(target=target.name), tempfile.TemporaryDirectory() as directory:
                evidence = Evidence(Path(directory), target)
                holder, _ = evidence.shard_of("FuzzC")
                inventory_path = next((evidence.bundle / "inventories").glob("*.inventory.json"))
                inventory = read(inventory_path)
                inventory["roots"] = [root for root in inventory["roots"] if root["name"] != "FuzzC"]
                write(inventory_path, inventory)
                # Re-plan from the shortened inventory, as a listing bug in prepare would have.
                settings = plan_shards.load_settings(evidence.bundle / "settings.json")
                weights, weight_metadata = plan_shards.load_weights(evidence.bundle / "historical-weights.json")
                inventories = plan_shards.load_inventories([inventory_path], target)
                write(evidence.plan_path, plan_shards.create_plan(inventories, settings, weights, weight_metadata))
                evidence.events(holder, lambda events: [e for e in events if e.get("Test") != "FuzzC"])
                for shard in read(evidence.plan_path)["shards"]:
                    for candidate in evidence.shard_directories():
                        if read(candidate / "shard-metadata.json")["shardIndex"] == shard["index"]:
                            evidence.invocation(
                                candidate,
                                lambda value: value.__setitem__("rootCount", len(shard["assignments"][0]["roots"])),
                            )
                evidence.rehash()
                code, failures = evidence.verify()
                self.assertEqual(code, 1)
                # Everything in the bundle agrees with itself; only each shard's own
                # listing of the binary, which still names FuzzC, gives it away.
                self.assertEqual(len(failures), 2, failures)
                for failure in failures:
                    self.assertIn("binary lists a different root set than the plan", failure)

    def test_package_dropped_from_the_package_inventory_is_refused(self):
        for target in (targets.WINDOWS_AMD64, targets.DARWIN_ARM64):
            with self.subTest(target=target.name), tempfile.TemporaryDirectory() as directory:
                evidence = Evidence(Path(directory), target)
                path = evidence.bundle / "package-inventory.json"
                value = read(path)
                value["packages"] = [row for row in value["packages"] if row["importPath"] != "example/no-tests"]
                write(path, value)
                evidence.other(lambda metadata: metadata.__setitem__("expectedPackages", ["example/other"]))
                evidence.non_heavy_events(lambda events: [e for e in events if e["Package"] != "example/no-tests"])
                evidence.rehash()
                code, failures = evidence.verify()
                self.assertEqual(code, 1)
                self.assertEqual(
                    failures, ["non-heavy job's own go list differs from the bundled package inventory"]
                )


class PlannerRefusalTests(unittest.TestCase):
    def test_planning_refuses_a_testmain_the_settings_do_not_pin(self):
        declaration = {"file": "new_test.go", "normalizedSourceSha256": "d" * 64}
        inventory = {
            "importPath": HEAVY, "binaryName": "heavy.test.exe", "binarySha256": "a" * 64, "binarySizeBytes": 1,
            "packageDirectoryRelative": "heavy", "repositorySha": "b" * 40, "goVersion": "go version go1.27.1 x/y",
            "targetEnvironment": targets.WINDOWS_AMD64.environment, "testMainDeclarations": [declaration],
            "roots": [{"name": "TestA", "kind": "Test"}], "excludedBenchmarks": [],
        }
        settings = {
            "shardCount": 1, "defaultWeightSeconds": 1.0, "timeout": "30m", "commandLineLimit": 30000,
            "shuffle": "off", "testParallel": None, "goMaxProcs": None,
            "heavyPackages": [{"importPath": HEAVY, "binaryName": "heavy.test.exe", "expectedTestMainDeclarations": []}],
        }
        with self.assertRaisesRegex(ValueError, "TestMain declarations drifted"):
            plan_shards.create_plan([inventory], settings, {}, {})
        settings["heavyPackages"][0]["expectedTestMainDeclarations"] = [declaration]
        self.assertEqual(plan_shards.create_plan([inventory], settings, {}, {})["rootCount"], 1)


class RunExpressionTests(unittest.TestCase):
    """`-test.run` SEARCHES each name; only the expression's own anchors bound it."""

    def test_selection_is_a_search_not_a_full_match(self):
        self.assertTrue(plan_shards.run_regex_selects("TestA", "MyTestAB"))
        self.assertTrue(plan_shards.run_regex_selects("^TestA", "TestAB"))
        self.assertFalse(plan_shards.run_regex_selects("^TestA$", "TestAB"))
        self.assertFalse(plan_shards.run_regex_selects("^TestA$", "MyTestA"))
        with self.assertRaisesRegex(ValueError, "subtests"):
            plan_shards.run_regex_selects("^TestA$/^sub$", "TestA")

    def test_generated_expression_selects_exactly_its_roots_among_prefixes_and_suffixes(self):
        everything = [
            "Test", "TestA", "TestAB", "TestABC", "TestB", "XTestA", "TestA_", "ExampleTestA",
            "FuzzTestA", "TestParser", "TestParserDepth", "BenchmarkTestA",
        ]
        for chosen in (["TestA"], ["Test"], ["TestA", "TestABC"], ["TestParser"], ["TestAB", "TestB", "Test"]):
            expression = plan_shards.compressed_run_regex(chosen)
            self.assertTrue(expression.startswith("^") and expression.endswith("$"))
            selected = [name for name in everything if plan_shards.run_regex_selects(expression, name)]
            self.assertEqual(sorted(selected), sorted(chosen), expression)

    def test_generated_expression_takes_every_name_literally(self):
        # Go test names are identifiers, so none of these can occur today; the
        # expression must still never treat a name as a pattern.
        literal = ["Test.A", "Test+", "Test(A)", "Test[ab]", "TestA|TestB", "Test$", "Test^A", "Test\\d", "Test*"]
        decoys = ["TestXA", "Testt", "TestA", "Testa", "TestB", "Test", "TestA|", "Test5", "Tes"]
        for name in literal:
            expression = plan_shards.compressed_run_regex([name])
            selected = [candidate for candidate in literal + decoys if plan_shards.run_regex_selects(expression, candidate)]
            self.assertEqual(selected, [name], expression)

    def test_root_names_that_address_subtests_are_refused(self):
        with self.assertRaisesRegex(ValueError, "subtest separator"):
            plan_shards.compressed_run_regex(["TestA/sub"])

    def test_verifier_refuses_an_expression_that_is_not_anchored_at_its_end(self):
        # TestA and TestAB land in different shards; an unanchored `^TestA` would
        # run TestAB in both, and a full-match check would not notice.
        inventory = {
            "importPath": HEAVY,
            "packageArgument": "./heavy",
            "packageDirectoryRelative": "heavy",
            "binaryName": "heavy.test.exe",
            "binarySha256": "a" * 64,
            "binarySizeBytes": 1,
            "repositorySha": "b" * 40,
            "goVersion": "go version go1.27.1 windows/amd64",
            "targetEnvironment": targets.WINDOWS_AMD64.environment,
            "listingContract": {},
            "testMainDeclarations": [],
            "roots": [{"name": "TestA", "kind": "Test"}, {"name": "TestAB", "kind": "Test"}],
            "excludedBenchmarks": [],
        }
        settings = {
            "shardCount": 2, "defaultWeightSeconds": 1.0, "timeout": "30m", "commandLineLimit": 30000,
            "shuffle": "off", "testParallel": None, "goMaxProcs": None,
            "heavyPackages": [{"importPath": HEAVY, "binaryName": "heavy.test.exe", "expectedTestMainDeclarations": []}],
        }
        plan = plan_shards.create_plan([inventory], settings, {}, {})
        failures = []
        verify_results.validate_plan(plan, [inventory], failures)
        self.assertEqual(failures, [])
        for shard in plan["shards"]:
            assignment = shard["assignments"][0]
            if [root["name"] for root in assignment["roots"]] == ["TestA"]:
                self.assertEqual(assignment["runRegex"], "^TestA$")
                assignment["runRegex"] = "^TestA"
        verify_results.validate_plan(plan, [inventory], failures)
        self.assertEqual(failures, ["1 plan regexes select a different root set"])


if __name__ == "__main__":
    unittest.main()
