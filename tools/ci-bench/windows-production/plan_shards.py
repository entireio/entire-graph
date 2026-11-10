#!/usr/bin/env python3
"""Build a deterministic, fail-closed plan from compiled native test binaries."""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import re
import sys
from collections import defaultdict
from pathlib import Path, PurePosixPath
from typing import Any, Iterable

import targets


# The schema identifiers keep their historical `windows-ci` prefix for every
# target; the target itself is carried by `targetEnvironment`.
SETTINGS_SCHEMA = "entire-graph.windows-ci.settings.v1"
WEIGHTS_SCHEMA = "entire-graph.windows-ci.historical-weights.v1"
INVENTORY_SCHEMA = "entire-graph.windows-ci.compiled-inventory.v1"
PLAN_SCHEMA = "entire-graph.windows-ci.shard-plan.v1"
ROOT_KINDS = ("Test", "Example", "Fuzz")
_TIMEOUT = re.compile(r"^[1-9][0-9]*(?:ns|us|µs|ms|s|m|h)$")
_SHA = re.compile(r"^[0-9a-f]{40}$")
_SHA256 = re.compile(r"^[0-9a-f]{64}$")
# The default target's identity, kept as a module constant for existing callers.
TARGET_ENVIRONMENT = targets.DEFAULT.environment


def settings_target(settings: dict[str, Any]) -> targets.Target:
    """The target a settings object names; a missing key is the Windows default."""
    return targets.resolve(settings.get("target", targets.DEFAULT.name))


def read_json(path: Path) -> Any:
    try:
        with path.open(encoding="utf-8-sig") as handle:
            return json.load(handle)
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError(f"cannot read JSON {path}: {error}") from error


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    try:
        with path.open("rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                digest.update(chunk)
    except OSError as error:
        raise ValueError(f"cannot hash {path}: {error}") from error
    return digest.hexdigest()


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    with temporary.open("w", encoding="utf-8", newline="\n") as handle:
        json.dump(value, handle, indent=2, ensure_ascii=False)
        handle.write("\n")
    temporary.replace(path)


def _safe_relative_directory(value: Any, label: str) -> str:
    if not isinstance(value, str) or not value or "\\" in value:
        raise ValueError(f"{label} must be a non-empty normalized POSIX path")
    path = PurePosixPath(value)
    if path.is_absolute() or ".." in path.parts or "." in path.parts:
        raise ValueError(f"{label} is unsafe: {value!r}")
    return value


def load_settings(path: Path) -> dict[str, Any]:
    value = read_json(path)
    if not isinstance(value, dict) or value.get("schema") != SETTINGS_SCHEMA:
        raise ValueError(f"{path}: unexpected settings schema")
    target = settings_target(value)
    shard_count = value.get("shardCount")
    default_weight = value.get("defaultWeightSeconds")
    command_limit = value.get("commandLineLimit")
    timeout = value.get("timeout")
    if not isinstance(shard_count, int) or isinstance(shard_count, bool) or not 1 <= shard_count <= 64:
        raise ValueError(f"{path}: shardCount must be an integer from 1 through 64")
    if (
        not isinstance(default_weight, (int, float))
        or isinstance(default_weight, bool)
        or not math.isfinite(float(default_weight))
        or float(default_weight) <= 0
    ):
        raise ValueError(f"{path}: defaultWeightSeconds must be finite and positive")
    if not target.valid_command_line_limit(command_limit):
        raise ValueError(
            f"{path}: commandLineLimit must be from 1024 through {target.command_line_limit_maximum}"
        )
    if not isinstance(timeout, str) or not _TIMEOUT.fullmatch(timeout):
        raise ValueError(f"{path}: timeout is not a positive Go duration")
    if value.get("shuffle") != "off":
        raise ValueError(f"{path}: production sharding currently requires shuffle='off'")
    for name in ("testParallel", "goMaxProcs"):
        setting = value.get(name)
        if setting is not None and (
            not isinstance(setting, int) or isinstance(setting, bool) or not 1 <= setting <= 1024
        ):
            raise ValueError(f"{path}: {name} must be null or an integer from 1 through 1024")

    heavy = value.get("heavyPackages")
    if not isinstance(heavy, list) or not heavy:
        raise ValueError(f"{path}: heavyPackages must be a non-empty list")
    seen_arguments: set[str] = set()
    seen_imports: set[str] = set()
    seen_binaries: set[str] = set()
    normalized: list[dict[str, Any]] = []
    for position, item in enumerate(heavy):
        if not isinstance(item, dict):
            raise ValueError(f"{path}: heavyPackages[{position}] is not an object")
        argument = item.get("argument")
        import_path = item.get("importPath")
        binary_name = item.get("binaryName")
        expected_test_mains = item.get("expectedTestMainDeclarations")
        if not isinstance(argument, str) or not argument.startswith("./") or " " in argument:
            raise ValueError(f"{path}: unsafe heavy package argument {argument!r}")
        if not isinstance(import_path, str) or not import_path or any(c.isspace() for c in import_path):
            raise ValueError(f"{path}: unsafe heavy package importPath {import_path!r}")
        if (
            not isinstance(binary_name, str)
            or Path(binary_name).name != binary_name
            or "/" in binary_name
            or "\\" in binary_name
            or not binary_name.endswith(target.binary_suffix)
        ):
            raise ValueError(f"{path}: unsafe heavy package binaryName {binary_name!r}")
        if argument in seen_arguments or import_path in seen_imports or binary_name in seen_binaries:
            raise ValueError(f"{path}: duplicate heavy package field at index {position}")
        if not isinstance(expected_test_mains, list):
            raise ValueError(
                f"{path}: heavyPackages[{position}].expectedTestMainDeclarations must be a list"
            )
        normalized_test_mains: list[dict[str, str]] = []
        seen_test_main_files: set[str] = set()
        for declaration_position, declaration in enumerate(expected_test_mains):
            if not isinstance(declaration, dict):
                raise ValueError(
                    f"{path}: heavyPackages[{position}].expectedTestMainDeclarations[{declaration_position}] is not an object"
                )
            file_name = declaration.get("file")
            source_hash = declaration.get("normalizedSourceSha256")
            if (
                not isinstance(file_name, str)
                or not file_name
                or Path(file_name).name != file_name
                or "/" in file_name
                or "\\" in file_name
                or file_name in seen_test_main_files
            ):
                raise ValueError(
                    f"{path}: invalid or duplicate expected TestMain file {file_name!r}"
                )
            if not isinstance(source_hash, str) or not _SHA256.fullmatch(source_hash):
                raise ValueError(
                    f"{path}: invalid expected TestMain normalized source hash for {file_name!r}"
                )
            seen_test_main_files.add(file_name)
            normalized_test_mains.append(
                {"file": file_name, "normalizedSourceSha256": source_hash}
            )
        seen_arguments.add(argument)
        seen_imports.add(import_path)
        seen_binaries.add(binary_name)
        normalized.append(
            {
                "argument": argument,
                "importPath": import_path,
                "binaryName": binary_name,
                "expectedTestMainDeclarations": sorted(
                    normalized_test_mains, key=lambda declaration: declaration["file"]
                ),
            }
        )
    result = dict(value)
    result["target"] = target.name
    result["defaultWeightSeconds"] = float(default_weight)
    result["heavyPackages"] = normalized
    return result


class Weights(dict):
    """Per-root elapsed seconds, plus what is known about `t.Parallel()` roots.

    `parallel` names the roots that call `t.Parallel()`; `parallelism` is how
    many of them effectively run at once on the target's runner. A plain dict
    carries neither, which plans every root as serial.
    """

    def __init__(self, *args: Any, **kwargs: Any) -> None:
        super().__init__(*args, **kwargs)
        self.parallel: set[tuple[str, str]] = set()
        self.parallelism = 1.0


def process_seconds(serial: float, parallel: float, longest_parallel: float, parallelism: float) -> float:
    """Estimated wall time of one test process.

    `go test` runs a binary's serial roots first, one at a time, and then its
    `t.Parallel()` roots together. The parallel phase can end no sooner than
    its longest root and no sooner than its total work spread over the cores.
    """
    return serial + max(longest_parallel, parallel / parallelism)


def load_weights(path: Path) -> tuple[Weights, dict[str, Any]]:
    value = read_json(path)
    if not isinstance(value, dict) or value.get("schema") != WEIGHTS_SCHEMA:
        raise ValueError(f"{path}: unexpected historical-weight schema")
    rows = value.get("weights")
    if not isinstance(rows, list):
        raise ValueError(f"{path}: weights must be a list")
    parallelism = value.get("parallelism", 1.0)
    if (
        not isinstance(parallelism, (int, float))
        or isinstance(parallelism, bool)
        or not math.isfinite(float(parallelism))
        or not 1.0 <= float(parallelism) <= 1024.0
    ):
        raise ValueError(f"{path}: parallelism must be from 1 through 1024")
    weights = Weights()
    weights.parallelism = float(parallelism)
    for position, row in enumerate(rows):
        if not isinstance(row, dict):
            raise ValueError(f"{path}: weights[{position}] is not an object")
        package, name, seconds = row.get("package"), row.get("name"), row.get("seconds")
        if not isinstance(package, str) or not package or not isinstance(name, str) or not name:
            raise ValueError(f"{path}: weights[{position}] has an invalid key")
        if (
            not isinstance(seconds, (int, float))
            or isinstance(seconds, bool)
            or not math.isfinite(float(seconds))
            or float(seconds) < 0
        ):
            raise ValueError(f"{path}: weights[{position}] seconds must be finite and non-negative")
        key = (package, name)
        if key in weights:
            raise ValueError(f"{path}: duplicate weight for {package}::{name}")
        parallel = row.get("parallel", False)
        if not isinstance(parallel, bool):
            raise ValueError(f"{path}: weights[{position}] parallel must be a boolean")
        weights[key] = float(seconds)
        if parallel:
            weights.parallel.add(key)
    metadata = {
        "path": path.name,
        "sha256": sha256_file(path),
        "sourceCommit": value.get("sourceCommit"),
        "source": value.get("source"),
        "entryCount": len(weights),
        "parallelism": weights.parallelism,
        "parallelCount": len(weights.parallel),
    }
    return weights, metadata


def _root_kind(name: str) -> str | None:
    return next((kind for kind in ROOT_KINDS if name.startswith(kind)), None)


def load_inventories(
    paths: Iterable[Path], target: targets.Target = targets.DEFAULT
) -> list[dict[str, Any]]:
    result: list[dict[str, Any]] = []
    seen_packages: set[str] = set()
    for path in paths:
        value = read_json(path)
        if not isinstance(value, dict) or value.get("schema") != INVENTORY_SCHEMA:
            raise ValueError(f"{path}: unexpected compiled-inventory schema")
        package = value.get("importPath")
        binary = value.get("binaryName")
        roots = value.get("roots")
        if not isinstance(package, str) or not package or package in seen_packages:
            raise ValueError(f"{path}: missing or duplicate importPath")
        if (
            not isinstance(binary, str)
            or Path(binary).name != binary
            or "/" in binary
            or "\\" in binary
            or not binary.endswith(target.binary_suffix)
        ):
            raise ValueError(f"{path}: unsafe binaryName")
        if value.get("listExitCode") != 0:
            raise ValueError(f"{path}: compiled binary listing did not exit zero")
        if not isinstance(value.get("repositorySha"), str) or not _SHA.fullmatch(value["repositorySha"]):
            raise ValueError(f"{path}: invalid repositorySha")
        if not isinstance(value.get("goVersion"), str) or not value["goVersion"].startswith("go version go"):
            raise ValueError(f"{path}: invalid goVersion")
        if not isinstance(value.get("binarySha256"), str) or not _SHA256.fullmatch(value["binarySha256"]):
            raise ValueError(f"{path}: invalid binarySha256")
        if not isinstance(value.get("binarySizeBytes"), int) or value["binarySizeBytes"] <= 0:
            raise ValueError(f"{path}: invalid binarySizeBytes")
        target_environment = {
            "GOOS": value.get("goos"),
            "GOARCH": value.get("goarch"),
            "CGO_ENABLED": value.get("cgoEnabled"),
        }
        if target_environment != target.environment:
            raise ValueError(
                f"{path}: compiled inventory target is {target_environment!r}, want {target.environment!r}"
            )
        listing_contract = value.get("listingContract")
        if listing_contract != {
            "arguments": ["-test.paniconexit0", "-test.list=."],
            "paniconexit0": True,
            "pwdMatchesPackageDirectory": True,
            "goToolDirectoryPrependedToPath": True,
        }:
            raise ValueError(f"{path}: direct binary listing contract is missing or unexpected")
        relative = _safe_relative_directory(
            value.get("packageDirectoryRelative"), f"{path}: packageDirectoryRelative"
        )
        if not isinstance(roots, list) or not roots:
            raise ValueError(f"{path}: roots must be a non-empty list")
        names: set[str] = set()
        normalized_roots: list[dict[str, str]] = []
        for position, root in enumerate(roots):
            if not isinstance(root, dict):
                raise ValueError(f"{path}: roots[{position}] is not an object")
            name, kind = root.get("name"), root.get("kind")
            if (
                not isinstance(name, str)
                or not name
                or any(character.isspace() for character in name)
                or "/" in name
                or kind not in ROOT_KINDS
                or _root_kind(name) != kind
                or name in names
            ):
                raise ValueError(f"{path}: malformed or duplicate runnable root at index {position}")
            names.add(name)
            normalized_roots.append({"name": name, "kind": kind})
        benchmarks = value.get("excludedBenchmarks")
        if (
            not isinstance(benchmarks, list)
            or any(not isinstance(name, str) or not name.startswith("Benchmark") for name in benchmarks)
            or len(benchmarks) != len(set(benchmarks))
        ):
            raise ValueError(f"{path}: excludedBenchmarks must contain unique Benchmark names")
        test_main_declarations = value.get("testMainDeclarations")
        if not isinstance(test_main_declarations, list):
            raise ValueError(f"{path}: testMainDeclarations must be a list")
        normalized_test_mains: list[dict[str, str]] = []
        seen_test_main_files: set[str] = set()
        for position, declaration in enumerate(test_main_declarations):
            if not isinstance(declaration, dict):
                raise ValueError(f"{path}: testMainDeclarations[{position}] is not an object")
            file_name = declaration.get("file")
            source_hash = declaration.get("normalizedSourceSha256")
            if (
                not isinstance(file_name, str)
                or not file_name
                or Path(file_name).name != file_name
                or "/" in file_name
                or "\\" in file_name
                or file_name in seen_test_main_files
            ):
                raise ValueError(f"{path}: invalid or duplicate TestMain file {file_name!r}")
            if not isinstance(source_hash, str) or not _SHA256.fullmatch(source_hash):
                raise ValueError(f"{path}: invalid TestMain normalized source hash for {file_name!r}")
            seen_test_main_files.add(file_name)
            normalized_test_mains.append(
                {"file": file_name, "normalizedSourceSha256": source_hash}
            )
        seen_packages.add(package)
        result.append(
            {
                "importPath": package,
                "packageArgument": value.get("packageArgument"),
                "packageDirectoryRelative": relative,
                "binaryName": binary,
                "binarySha256": value["binarySha256"],
                "binarySizeBytes": value["binarySizeBytes"],
                "repositorySha": value["repositorySha"],
                "goVersion": value["goVersion"],
                "targetEnvironment": {
                    key: target.environment[key] for key in ("GOOS", "GOARCH", "CGO_ENABLED")
                },
                "listingContract": dict(listing_contract),
                "roots": sorted(normalized_roots, key=lambda item: item["name"]),
                "excludedBenchmarks": sorted(benchmarks),
                "testMainDeclarations": sorted(
                    normalized_test_mains, key=lambda declaration: declaration["file"]
                ),
            }
        )
    return sorted(result, key=lambda item: item["importPath"])


class _Trie:
    __slots__ = ("children", "terminal")

    def __init__(self) -> None:
        self.children: dict[str, _Trie] = {}
        self.terminal = False


def _render_trie(node: _Trie) -> str:
    alternatives = [
        re.escape(character) + _render_trie(child)
        for character, child in sorted(node.children.items())
    ]
    if not alternatives:
        return ""
    body = alternatives[0] if len(alternatives) == 1 else "(" + "|".join(alternatives) + ")"
    return "(" + body + ")?" if node.terminal else body


def run_regex_selects(expression: str, name: str) -> bool:
    """Whether `-test.run=expression` selects the top-level root `name`.

    Go splits the expression on `/` into one pattern per subtest level and
    SEARCHES each level's name for its pattern; nothing anchors it unless the
    expression does. A plan expression has a single level, so a `/` in it is
    refused rather than interpreted.
    """
    if "/" in expression:
        raise ValueError(f"run expression addresses subtests: {expression!r}")
    return re.search(expression, name) is not None


def compressed_run_regex(names: Iterable[str]) -> str:
    ordered = sorted(set(names))
    if not ordered:
        raise ValueError("cannot create a run regex for no roots")
    if any("/" in name for name in ordered):
        raise ValueError("a root name contains '/', which -test.run reads as a subtest separator")
    root = _Trie()
    for name in ordered:
        node = root
        for character in name:
            node = node.children.setdefault(character, _Trie())
        node.terminal = True
    return "^" + _render_trie(root) + "$"


def _inventory_digest(inventories: list[dict[str, Any]]) -> str:
    encoded = json.dumps(
        inventories, sort_keys=True, ensure_ascii=False, separators=(",", ":")
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def create_plan(
    inventories: list[dict[str, Any]],
    settings: dict[str, Any],
    weights: dict[tuple[str, str], float],
    weight_metadata: dict[str, Any],
) -> dict[str, Any]:
    if not inventories:
        raise ValueError("at least one compiled inventory is required")
    target = settings_target(settings)
    if any(item.get("targetEnvironment") != target.environment for item in inventories):
        raise ValueError(f"compiled inventories are not all {target.description}")
    expected_config = {
        (item["importPath"], item["binaryName"])
        for item in settings["heavyPackages"]
    }
    actual_config = {(item["importPath"], item["binaryName"]) for item in inventories}
    if actual_config != expected_config:
        raise ValueError("compiled inventories do not exactly match configured heavy packages")
    expected_test_mains = {
        item["importPath"]: item["expectedTestMainDeclarations"]
        for item in settings["heavyPackages"]
    }
    for inventory in inventories:
        if inventory["testMainDeclarations"] != expected_test_mains[inventory["importPath"]]:
            raise ValueError(
                f"compiled TestMain declarations drifted for {inventory['importPath']}"
            )
    repository_shas = {item["repositorySha"] for item in inventories}
    go_versions = {item["goVersion"] for item in inventories}
    if len(repository_shas) != 1 or len(go_versions) != 1:
        raise ValueError("compiled inventories disagree on repository or Go version")

    parallel_roots = getattr(weights, "parallel", frozenset())
    parallelism = float(getattr(weights, "parallelism", 1.0))
    work: list[dict[str, Any]] = []
    used_weight_keys: set[tuple[str, str]] = set()
    for inventory in inventories:
        for root in inventory["roots"]:
            key = (inventory["importPath"], root["name"])
            if key in weights:
                weight = weights[key]
                source = target.weight_source
                used_weight_keys.add(key)
            else:
                weight = settings["defaultWeightSeconds"]
                source = "default-current-inventory"
            work.append(
                {
                    "importPath": inventory["importPath"],
                    "packageDirectoryRelative": inventory["packageDirectoryRelative"],
                    "binaryName": inventory["binaryName"],
                    "binarySha256": inventory["binarySha256"],
                    "name": root["name"],
                    "kind": root["kind"],
                    "weightSeconds": round(float(weight), 6),
                    "weightSource": source,
                    # A root with no history is planned as serial.
                    "parallel": key in weights and key in parallel_roots,
                }
            )

    shard_count = settings["shardCount"]
    if len(work) < shard_count:
        raise ValueError(f"{len(work)} runnable roots cannot populate {shard_count} non-empty shards")
    work.sort(key=lambda item: (-item["weightSeconds"], item["importPath"], item["name"]))
    bins: list[list[dict[str, Any]]] = [[] for _ in range(shard_count)]
    totals = [0.0] * shard_count
    # A shard runs one process per package, one after another, so its estimated
    # wall time is the sum of its processes'. Each root, slowest first, goes to
    # the shard that would finish soonest with it. With no parallel roots this
    # is longest-processing-time-first; a parallel root is free wherever it
    # hides beneath a longer one or beneath the cores' share of the work.
    processes: list[dict[str, list[float]]] = [{} for _ in range(shard_count)]

    def placed(index: int, item: dict[str, Any]) -> tuple[float, list[float]]:
        serial, parallel, longest = processes[index].get(item["importPath"], [0.0, 0.0, 0.0])
        before = process_seconds(serial, parallel, longest, parallelism)
        if item["parallel"]:
            parallel += item["weightSeconds"]
            longest = max(longest, item["weightSeconds"])
        else:
            serial += item["weightSeconds"]
        after = process_seconds(serial, parallel, longest, parallelism)
        return totals[index] - before + after, [serial, parallel, longest]

    for item in work:
        destination = min(
            range(shard_count),
            key=lambda index: (placed(index, item)[0], len(bins[index]), index),
        )
        totals[destination], processes[destination][item["importPath"]] = placed(destination, item)
        bins[destination].append(item)

    shards: list[dict[str, Any]] = []
    for index, roots in enumerate(bins):
        by_package: dict[str, list[dict[str, Any]]] = defaultdict(list)
        for root in roots:
            by_package[root["importPath"]].append(root)
        assignments: list[dict[str, Any]] = []
        for package in sorted(by_package):
            package_roots = sorted(by_package[package], key=lambda item: item["name"])
            assignments.append(
                {
                    "importPath": package,
                    "packageDirectoryRelative": package_roots[0]["packageDirectoryRelative"],
                    "binaryName": package_roots[0]["binaryName"],
                    "binarySha256": package_roots[0]["binarySha256"],
                    "runRegex": compressed_run_regex(item["name"] for item in package_roots),
                    "roots": [
                        {
                            "name": item["name"],
                            "kind": item["kind"],
                            "weightSeconds": item["weightSeconds"],
                            "weightSource": item["weightSource"],
                            **({"parallel": True} if item["parallel"] else {}),
                        }
                        for item in package_roots
                    ],
                }
            )
        shards.append(
            {
                "index": index,
                "estimatedWeightSeconds": round(totals[index], 6),
                "rootCount": len(roots),
                "assignments": assignments,
            }
        )

    unused = sorted(f"{package}::{name}" for package, name in set(weights) - used_weight_keys)
    packages = [
        {
            "importPath": item["importPath"],
            "packageDirectoryRelative": item["packageDirectoryRelative"],
            "binaryName": item["binaryName"],
            "binarySha256": item["binarySha256"],
            "binarySizeBytes": item["binarySizeBytes"],
            "rootCount": len(item["roots"]),
            "excludedBenchmarkCount": len(item["excludedBenchmarks"]),
            "testMainDeclarations": item["testMainDeclarations"],
        }
        for item in inventories
    ]
    return {
        "schema": PLAN_SCHEMA,
        "algorithm": "deterministic-greedy-process-wall-time-v2",
        "repositorySha": next(iter(repository_shas)),
        "goVersion": next(iter(go_versions)),
        "targetEnvironment": {
            key: target.environment[key] for key in ("GOOS", "GOARCH", "CGO_ENABLED")
        },
        "inventorySetSha256": _inventory_digest(inventories),
        "shardCount": shard_count,
        "rootCount": len(work),
        "packageCount": len(inventories),
        "settings": {
            "timeout": settings["timeout"],
            "commandLineLimit": settings["commandLineLimit"],
            "shuffle": settings["shuffle"],
            "testParallel": settings["testParallel"],
            "goMaxProcs": settings["goMaxProcs"],
            "defaultWeightSeconds": settings["defaultWeightSeconds"],
        },
        "historicalWeights": {
            **weight_metadata,
            "usedCount": len(used_weight_keys),
            "defaultedCount": len(work) - len(used_weight_keys),
            "unusedKeys": unused,
        },
        "packages": packages,
        "shards": shards,
    }


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--settings", type=Path, required=True)
    parser.add_argument("--weights", type=Path, required=True)
    parser.add_argument("--inventory", action="append", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    try:
        settings = load_settings(args.settings)
        weights, metadata = load_weights(args.weights)
        inventories = load_inventories(args.inventory, settings_target(settings))
        plan = create_plan(inventories, settings, weights, metadata)
        write_json(args.output, plan)
    except (ValueError, OSError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
