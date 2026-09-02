#!/usr/bin/env python3
"""Generate dependency-closed Go test-file overlays for one package.

The target-specific test file set comes exclusively from `go list -json` with
the requested GOOS/GOARCH/CGO_ENABLED environment. Selected source files are
never rewritten. Unselected files are replaced by package-only stubs; when a
mixed TestMain/test file is excluded, its exact TestMain declaration and the
package declarations it references are copied into a generated surrogate.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
from collections import defaultdict
from dataclasses import dataclass
from typing import Any, Iterable


SCHEMA = "ci-bench.go-test-overlays.v1"


class GenerationError(RuntimeError):
    pass


def run_json(command: list[str], *, cwd: Path, env: dict[str, str]) -> Any:
    completed = subprocess.run(
        command,
        cwd=cwd,
        env=env,
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
    )
    if completed.returncode != 0:
        raise GenerationError(
            f"command failed ({completed.returncode}): {' '.join(command)}\n"
            f"{completed.stderr.rstrip()}"
        )
    try:
        return json.loads(completed.stdout)
    except json.JSONDecodeError as error:
        raise GenerationError(f"invalid JSON from {' '.join(command)}: {error}") from error


def sha256_bytes(content: bytes) -> str:
    return hashlib.sha256(content).hexdigest()


def stable_json(path: Path, payload: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def safe_relative(path: Path, root: Path) -> str:
    try:
        return path.resolve().relative_to(root.resolve()).as_posix()
    except ValueError as error:
        raise GenerationError(f"target file escapes repository: {path}") from error


@dataclass(frozen=True)
class Component:
    files: tuple[str, ...]
    support: tuple[str, ...]
    tests: tuple[str, ...]
    weight: float


class UnionFind:
    def __init__(self, items: Iterable[str]) -> None:
        self.parent = {item: item for item in items}

    def find(self, item: str) -> str:
        parent = self.parent[item]
        if parent != item:
            self.parent[item] = self.find(parent)
        return self.parent[item]

    def union(self, left: str, right: str) -> None:
        left_root = self.find(left)
        right_root = self.find(right)
        if left_root != right_root:
            self.parent[right_root] = left_root


def declaration_provider(files: list[dict[str, Any]]) -> dict[str, str]:
    providers: dict[str, str] = {}
    for item in files:
        name = Path(item["path"]).name
        for declaration in item["declarations"]:
            if declaration["kind"] == "method":
                continue
            for declared in declaration["names"]:
                previous = providers.get(declared)
                if previous is not None and previous != name:
                    raise GenerationError(
                        f"package-scope declaration {declared!r} occurs in both {previous} and {name}"
                    )
                providers[declared] = name
    return providers


def dependency_graph(files: list[dict[str, Any]]) -> dict[str, set[str]]:
    providers = declaration_provider(files)
    graph: dict[str, set[str]] = {Path(item["path"]).name: set() for item in files}
    for item in files:
        source = Path(item["path"]).name
        imports = set(item["imports"])
        for declaration in item["declarations"]:
            for reference in declaration["references"]:
                if reference in imports:
                    continue
                target = providers.get(reference)
                if target is not None and target != source:
                    graph[source].add(target)
    return graph


def transitive_dependencies(graph: dict[str, set[str]], starts: Iterable[str]) -> set[str]:
    seen: set[str] = set()
    pending = list(starts)
    while pending:
        current = pending.pop()
        for dependency in graph[current]:
            if dependency not in seen:
                seen.add(dependency)
                pending.append(dependency)
    return seen


def build_components(
    files: list[dict[str, Any]], weights: dict[str, float]
) -> tuple[list[Component], set[str]]:
    graph = dependency_graph(files)
    tests_by_file = {
        Path(item["path"]).name: tuple(test["name"] for test in item["tests"])
        for item in files
    }
    test_bearing = {name for name, tests in tests_by_file.items() if tests}
    support_only = set(graph) - test_bearing
    union = UnionFind(test_bearing)

    # If one test-bearing file needs a declaration from another test-bearing
    # file, they must remain in the same binary or the dependency provider's
    # own tests would be duplicated.
    for source in sorted(test_bearing):
        for dependency in transitive_dependencies(graph, [source]):
            if dependency in test_bearing:
                union.union(source, dependency)

    grouped: dict[str, list[str]] = defaultdict(list)
    for name in sorted(test_bearing):
        grouped[union.find(name)].append(name)

    components: list[Component] = []
    for names in grouped.values():
        closure = transitive_dependencies(graph, names)
        unexpected_tests = (closure & test_bearing) - set(names)
        if unexpected_tests:
            raise GenerationError(
                f"internal closure error for {names}: unmerged test files {sorted(unexpected_tests)}"
            )
        support = tuple(sorted(closure & support_only))
        test_names = tuple(sorted(test for name in names for test in tests_by_file[name]))
        weight = sum(float(weights.get(test, 1.0)) for test in test_names)
        components.append(
            Component(tuple(sorted(names)), support, test_names, max(weight, 0.001))
        )

    components.sort(key=lambda item: (-item.weight, item.files))
    return components, support_only


def assign_components(components: list[Component], shard_count: int) -> list[list[Component]]:
    if shard_count < 1:
        raise GenerationError("--shards must be positive")
    if shard_count > len(components):
        raise GenerationError(
            f"requested {shard_count} shards but dependency closure permits only "
            f"{len(components)} non-empty components"
        )
    shards: list[list[Component]] = [[] for _ in range(shard_count)]
    totals = [0.0] * shard_count
    for component in components:
        index = min(range(shard_count), key=lambda candidate: (totals[candidate], candidate))
        shards[index].append(component)
        totals[index] += component.weight
    return shards


def load_weights(path: Path | None) -> dict[str, float]:
    if path is None:
        return {}
    payload = json.loads(path.read_text(encoding="utf-8"))
    if isinstance(payload, dict) and "tests" in payload:
        payload = payload["tests"]
    if not isinstance(payload, dict):
        raise GenerationError("weights JSON must be an object or contain an object named 'tests'")
    result: dict[str, float] = {}
    for name, value in payload.items():
        if not isinstance(name, str) or not isinstance(value, (int, float)) or value < 0:
            raise GenerationError(f"invalid weight entry: {name!r}={value!r}")
        result[name] = float(value)
    return result


def exact_declaration_source(file_item: dict[str, Any], declaration: dict[str, Any]) -> bytes:
    source = Path(file_item["path"]).read_bytes()
    return source[declaration["start"] : declaration["end"]]


def support_surrogate(file_item: dict[str, Any]) -> tuple[bytes, dict[str, Any]]:
    """Return a test-free surrogate with exact package support declarations.

    Keeping helper/type/var/init/TestMain declarations from every excluded file
    avoids turning the package's dense cross-file helper graph into one giant
    shard. Runnable Test/Benchmark/Fuzz/Example declarations are the only
    declarations omitted.
    """
    runnable_names = {test["name"] for test in file_item["tests"]}
    selected = [
        declaration
        for declaration in file_item["declarations"]
        if not (set(declaration["names"]) & runnable_names)
    ]
    import_aliases = set(file_item["imports"])
    used_imports = sorted(
        {
            reference
            for declaration in selected
            for reference in declaration["references"]
            if reference in import_aliases
        }
        | {alias for alias in import_aliases if alias in {"_", "."}}
    )
    import_lines = []
    for alias in used_imports:
        import_path = file_item["imports"][alias]
        default_alias = Path(import_path).name
        prefix = "" if alias == default_alias else f"{alias} "
        import_lines.append(f'\t{prefix}"{import_path}"')

    ordered = sorted(selected, key=lambda declaration: declaration["start"])
    source_parts = [exact_declaration_source(file_item, declaration) for declaration in ordered]
    header_text = (
        "// Code generated by generate-test-overlays.py; DO NOT EDIT.\n\n"
        f"package {file_item['package']}\n"
    )
    if import_lines:
        header_text += "\nimport (\n" + "\n".join(import_lines) + "\n)\n"
    header = (header_text + "\n").encode("utf-8")
    surrogate = header + b"\n\n".join(source_parts) + b"\n"
    test_main = file_item.get("testMain")
    original_main_hash = None
    if test_main is not None:
        main_declaration = next(
            declaration
            for declaration in selected
            if "TestMain" in declaration["names"]
        )
        original_main_hash = sha256_bytes(
            exact_declaration_source(file_item, main_declaration)
        )
    proof = {
        "sourceFile": Path(file_item["path"]).name,
        "sourceFileSHA256": file_item["sha256"],
        "exactTestMainDeclarationSHA256": original_main_hash,
        "surrogateSHA256": sha256_bytes(surrogate),
        "retainedDeclarations": [
            {
                "names": declaration["names"],
                "kind": declaration["kind"],
                "exactSourceSHA256": sha256_bytes(
                    exact_declaration_source(file_item, declaration)
                ),
            }
            for declaration in ordered
        ],
        "omittedRunnables": sorted(runnable_names),
        "imports": {alias: file_item["imports"][alias] for alias in used_imports},
    }
    return surrogate, proof


def generate(args: argparse.Namespace) -> dict[str, Any]:
    repo = args.repo.resolve()
    output = args.output.resolve()
    if output.exists():
        if not args.force:
            raise GenerationError(f"output already exists: {output}")
        shutil.rmtree(output)
    output.mkdir(parents=True)

    target_env = dict(os.environ)
    target_env.update(
        GOOS=args.goos,
        GOARCH=args.goarch,
        CGO_ENABLED=str(args.cgo_enabled),
    )
    package = run_json(
        [args.go, "list", "-json", args.package], cwd=repo, env=target_env
    )
    package_dir = Path(package["Dir"]).resolve()
    selected_names = list(package.get("TestGoFiles", [])) + list(
        package.get("XTestGoFiles", [])
    )
    if not selected_names:
        raise GenerationError(f"target selected no test files for {args.package}")
    if len(selected_names) != len(set(selected_names)):
        raise GenerationError("go list returned a duplicate test file")
    selected_paths = [(package_dir / name).resolve() for name in selected_names]
    for path in selected_paths:
        if not path.is_file():
            raise GenerationError(f"go list selected a missing test file: {path}")

    helper = Path(__file__).with_name("go-test-file-inventory.go").resolve()
    inventory = run_json(
        [
            args.go,
            "run",
            str(helper),
            "--",
            *(f"--file={path}" for path in selected_paths),
        ],
        cwd=repo,
        # The parser consumes the target-selected file list explicitly and is
        # target agnostic. Build it for the host so planning can be audited on
        # a non-Windows coordinator as well as on the Windows benchmark VM.
        env=dict(os.environ),
    )
    file_items = inventory["files"]
    returned_names = [Path(item["path"]).name for item in file_items]
    if sorted(returned_names) != sorted(selected_names):
        raise GenerationError("syntax inventory does not match target go list test files")

    mains = [item for item in file_items if item.get("testMain") is not None]
    if len(mains) > 1:
        raise GenerationError(
            "multiple TestMain declarations are incompatible with one Go test package: "
            + ", ".join(Path(item["path"]).name for item in mains)
        )
    weights = load_weights(args.weights)
    full_file_components, support_only = build_components(file_items, weights)
    # Generated helper-only surrogates preserve cross-file declarations without
    # bringing the provider file's runnable tests into multiple binaries. This
    # makes each test-bearing file independently assignable while keeping the
    # full-file closure in the plan as an explicit audit result.
    components = [
        Component(
            (Path(item["path"]).name,),
            (),
            tuple(sorted(test["name"] for test in item["tests"])),
            max(
                sum(float(weights.get(test["name"], 1.0)) for test in item["tests"]),
                0.001,
            ),
        )
        for item in file_items
        if item["tests"]
    ]
    components.sort(key=lambda item: (-item.weight, item.files))
    shards = assign_components(components, args.shards)
    by_name = {Path(item["path"]).name: item for item in file_items}
    runnable_proofs = {
        test["name"]: {
            "file": Path(item["path"]).name,
            "kind": test["kind"],
            "start": test["start"],
            "end": test["end"],
            "exactDeclarationSHA256": test["sha256"],
        }
        for item in file_items
        for test in item["tests"]
    }
    all_test_bearing = {
        name for name, item in by_name.items() if len(item["tests"]) > 0
    }
    owned_files: dict[str, int] = {}
    all_tests: dict[str, int] = {}
    shard_summaries = []

    for index, shard_components in enumerate(shards, start=1):
        shard_name = f"shard-{index:02d}"
        shard_dir = output / shard_name
        replacements_dir = shard_dir / "replacements"
        replacements_dir.mkdir(parents=True)
        selected_test_files = {
            name for component in shard_components for name in component.files
        }
        required_support = set(support_only)
        # Retaining every target-selected support-only test file is conservative:
        # it preserves helper/init declarations even when static name resolution
        # cannot observe reflection, generated references, or side effects.
        compiled_files = selected_test_files | required_support
        replace: dict[str, str] = {}
        replacement_hashes: dict[str, str] = {}
        surrogate_proofs: dict[str, Any] = {}
        for file_name in sorted(set(selected_names) - compiled_files):
            source_item = by_name[file_name]
            replacement_path = replacements_dir / file_name
            replacement, proof = support_surrogate(source_item)
            replacement_path.write_bytes(replacement)
            replace[str((package_dir / file_name).resolve())] = str(replacement_path.resolve())
            replacement_hashes[file_name] = sha256_bytes(replacement)
            surrogate_proofs[file_name] = proof

        overlay_path = shard_dir / "overlay.json"
        stable_json(overlay_path, {"Replace": replace})
        tests = sorted(test for component in shard_components for test in component.tests)
        weight = sum(component.weight for component in shard_components)
        for file_name in sorted(selected_test_files):
            if file_name in owned_files:
                raise GenerationError(f"test-bearing file assigned twice: {file_name}")
            owned_files[file_name] = index
        for test in tests:
            if test in all_tests:
                raise GenerationError(f"top-level test assigned twice: {test}")
            all_tests[test] = index

        manifest = {
            "schema": SCHEMA,
            "shard": index,
            "package": package["ImportPath"],
            "target": {
                "goos": args.goos,
                "goarch": args.goarch,
                "cgoEnabled": str(args.cgo_enabled),
            },
            "ownedTestFiles": sorted(selected_test_files),
            "compiledSupportFiles": sorted(required_support),
            "compiledOriginalFiles": sorted(compiled_files),
            "topLevelTests": tests,
            "topLevelRunnableProofs": {name: runnable_proofs[name] for name in tests},
            "weight": weight,
            "replacementSHA256": replacement_hashes,
            "supportSurrogateProofs": surrogate_proofs,
            "overlaySHA256": sha256_bytes(overlay_path.read_bytes()),
        }
        stable_json(shard_dir / "manifest.json", manifest)
        shard_summaries.append(manifest)

    expected_tests = sorted(
        test["name"] for item in file_items for test in item["tests"]
    )
    if set(owned_files) != all_test_bearing:
        raise GenerationError("test-bearing file assignment is not exhaustive")
    if sorted(all_tests) != expected_tests:
        raise GenerationError("top-level test assignment is not exhaustive")

    production_names = sorted(package.get("GoFiles", []) + package.get("CgoFiles", []))
    production_hashes = {
        name: sha256_bytes((package_dir / name).read_bytes()) for name in production_names
    }
    test_hashes = {
        name: sha256_bytes((package_dir / name).read_bytes()) for name in sorted(selected_names)
    }
    summary = {
        "schema": SCHEMA,
        "repository": str(repo),
        "package": {
            "argument": args.package,
            "importPath": package["ImportPath"],
            "directory": str(package_dir),
        },
        "target": {
            "goos": args.goos,
            "goarch": args.goarch,
            "cgoEnabled": str(args.cgo_enabled),
        },
        "goList": {
            "testGoFiles": list(package.get("TestGoFiles", [])),
            "xTestGoFiles": list(package.get("XTestGoFiles", [])),
        },
        "sourceProof": {
            "productionFiles": production_hashes,
            "targetSelectedTestFiles": test_hashes,
            "productionTreeSHA256": sha256_bytes(
                "".join(f"{name}\0{production_hashes[name]}\n" for name in production_hashes).encode()
            ),
            "testTreeSHA256": sha256_bytes(
                "".join(f"{name}\0{test_hashes[name]}\n" for name in test_hashes).encode()
            ),
            "topLevelRunnableDeclarations": runnable_proofs,
        },
        "dependencyClosure": {
            "fullFileOnlyComponentCount": len(full_file_components),
            "largestFullFileOnlyComponentFiles": max(
                len(component.files) for component in full_file_components
            ),
            "supportOnlyFiles": sorted(support_only),
            "fullFileOnlyComponents": [
                {
                    "testFiles": list(component.files),
                    "supportFiles": list(component.support),
                    "topLevelTests": list(component.tests),
                    "weight": component.weight,
                }
                for component in full_file_components
            ],
            "strategy": "exact-declaration support surrogates",
            "independentlyAssignableTestFiles": len(components),
        },
        "testMain": {
            "sourceFile": Path(mains[0]["path"]).name if mains else None,
            "sourceFileSHA256": mains[0]["sha256"] if mains else None,
            "surrogateGeneratedInEveryOtherShard": bool(mains),
        },
        "shards": [
            {
                "shard": shard["shard"],
                "ownedTestFiles": shard["ownedTestFiles"],
                "compiledSupportFiles": shard["compiledSupportFiles"],
                "topLevelTests": shard["topLevelTests"],
                "weight": shard["weight"],
                "overlaySHA256": shard["overlaySHA256"],
            }
            for shard in shard_summaries
        ],
        "coverageGuard": {
            "targetSelectedFileCount": len(selected_names),
            "ownedTestBearingFileCount": len(owned_files),
            "supportOnlyFileCount": len(support_only),
            "topLevelRunnableCount": len(expected_tests),
            "allTestBearingFilesAssignedExactlyOnce": True,
            "allTopLevelRunnablesAssignedExactlyOnce": True,
        },
    }
    stable_json(output / "plan.json", summary)
    return summary


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument("--repo", type=Path, required=True)
    result.add_argument("--package", default="./internal/sem")
    result.add_argument("--output", type=Path, required=True)
    result.add_argument("--shards", type=int, required=True)
    result.add_argument("--weights", type=Path)
    result.add_argument("--go", default="go")
    result.add_argument("--goos", default="windows")
    result.add_argument("--goarch", default="amd64")
    result.add_argument("--cgo-enabled", choices=(0, 1), type=int, default=1)
    result.add_argument("--force", action="store_true")
    return result


def main() -> int:
    args = parser().parse_args()
    try:
        summary = generate(args)
    except (GenerationError, OSError, json.JSONDecodeError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    print(
        json.dumps(
            {
                "schema": summary["schema"],
                "package": summary["package"]["importPath"],
                "testFiles": summary["coverageGuard"]["targetSelectedFileCount"],
                "runnables": summary["coverageGuard"]["topLevelRunnableCount"],
                "shards": len(summary["shards"]),
                "plan": str((args.output / "plan.json").resolve()),
            },
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
