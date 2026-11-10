#!/usr/bin/env python3
"""Compile, hash, inventory, and plan the production Windows test shards."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import time
from pathlib import Path
from typing import Any, Iterable

import plan_shards


PACKAGE_SCHEMA = "entire-graph.windows-ci.package-inventory.v1"
PREPARE_SCHEMA = "entire-graph.windows-ci.prepare-metadata.v1"
ROOT_PREFIXES = ("Test", "Example", "Fuzz")
TARGET_ENVIRONMENT = {"CGO_ENABLED": "1", "GOARCH": "amd64", "GOOS": "windows"}
TESTMAIN_SCHEMA = "entire-graph.windows-ci.testmain-inventory.v1"


def write_text(path: Path, value: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(value, encoding="utf-8", newline="\n")


def write_json(path: Path, value: Any) -> None:
    plan_shards.write_json(path, value)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def decode_json_stream(value: str, label: str) -> list[dict[str, Any]]:
    decoder = json.JSONDecoder()
    position = 0
    result: list[dict[str, Any]] = []
    while True:
        match = re.search(r"\S", value[position:])
        if match is None:
            break
        position += match.start()
        try:
            item, position = decoder.raw_decode(value, position)
        except json.JSONDecodeError as error:
            raise ValueError(f"{label}: invalid concatenated JSON at offset {position}: {error}") from error
        if not isinstance(item, dict):
            raise ValueError(f"{label}: JSON stream item is not an object")
        result.append(item)
    if not result:
        raise ValueError(f"{label}: JSON stream is empty")
    return result


def run_command(
    arguments: list[str],
    *,
    cwd: Path,
    environment: dict[str, str],
    stdout_path: Path | None = None,
    stderr_path: Path | None = None,
    stdin_text: str | None = None,
) -> tuple[subprocess.CompletedProcess[str], float]:
    started = time.monotonic()
    completed = subprocess.run(
        arguments,
        cwd=cwd,
        env=environment,
        text=True,
        encoding="utf-8",
        errors="strict",
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        input=stdin_text,
        check=False,
    )
    duration = time.monotonic() - started
    if stdout_path is not None:
        write_text(stdout_path, completed.stdout)
    if stderr_path is not None:
        write_text(stderr_path, completed.stderr)
    return completed, duration


def require_success(completed: subprocess.CompletedProcess[str], label: str) -> None:
    if completed.returncode != 0:
        detail = completed.stderr.strip() or completed.stdout.strip()
        raise RuntimeError(f"{label} exited {completed.returncode}: {detail[:2000]}")


def require_tracked_worktree_clean(
    completed: subprocess.CompletedProcess[str], label: str
) -> None:
    require_success(completed, label)
    if completed.stdout.strip():
        raise RuntimeError(f"{label}: tracked worktree modifications detected")


def direct_test_environment(
    environment: dict[str, str], package_directory: Path, go_tool_directory: Path
) -> dict[str, str]:
    if not package_directory.is_absolute() or not go_tool_directory.is_absolute():
        raise ValueError("direct test environment requires absolute package and Go tool directories")
    result = dict(environment)
    result["PWD"] = str(package_directory)
    existing_path = result.get("PATH", "")
    result["PATH"] = str(go_tool_directory) + (os.pathsep + existing_path if existing_path else "")
    return result


def direct_listing_arguments(binary: Path) -> list[str]:
    return [str(binary), "-test.paniconexit0", "-test.list=."]


def repository_relative_directory(repository: Path, directory: str, label: str) -> str:
    resolved = Path(directory).resolve(strict=True)
    try:
        relative = resolved.relative_to(repository)
    except ValueError as error:
        raise ValueError(f"{label} directory is outside the repository: {resolved}") from error
    if not relative.parts:
        return "."
    if ".." in relative.parts:
        raise ValueError(f"{label} directory is unsafe: {relative}")
    return relative.as_posix()


def classify_listing(lines: Iterable[str], label: str) -> tuple[list[dict[str, str]], list[str]]:
    roots: list[dict[str, str]] = []
    benchmarks: list[str] = []
    unexpected: list[str] = []
    seen: set[str] = set()
    for raw in lines:
        name = raw.strip()
        if not name:
            continue
        if name in seen:
            raise ValueError(f"{label}: duplicate -test.list output {name!r}")
        seen.add(name)
        if name == "TestMain":
            unexpected.append(name)
        elif name.startswith("Benchmark"):
            benchmarks.append(name)
        else:
            kind = next((prefix for prefix in ROOT_PREFIXES if name.startswith(prefix)), None)
            if kind is None or any(character.isspace() for character in name):
                unexpected.append(name)
            else:
                roots.append({"name": name, "kind": kind})
    if unexpected:
        raise ValueError(f"{label}: unexpected -test.list output: {unexpected[:20]!r}")
    if not roots:
        raise ValueError(f"{label}: compiled binary listed no default-runnable roots")
    return sorted(roots, key=lambda item: item["name"]), sorted(benchmarks)


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--settings", type=Path, required=True)
    parser.add_argument("--weights", type=Path, required=True)
    parser.add_argument("--expected-repository-sha", required=True)
    parser.add_argument("--go-command", default="go")
    return parser.parse_args(argv)


def prepare(args: argparse.Namespace) -> None:
    if sys.platform != "win32":
        raise RuntimeError("prepare.py must run on native Windows")
    repository = args.repository.resolve(strict=True)
    output = args.output.resolve()
    binaries = output / "binaries"
    inventories_dir = output / "inventories"
    logs = output / "logs"
    for directory in (binaries, inventories_dir, logs):
        directory.mkdir(parents=True, exist_ok=True)

    settings_path = args.settings.resolve(strict=True)
    weights_path = args.weights.resolve(strict=True)
    settings = plan_shards.load_settings(settings_path)
    if not re.fullmatch(r"[0-9a-f]{40}", args.expected_repository_sha):
        raise ValueError("expected repository SHA must be 40 lowercase hexadecimal characters")

    environment = dict(os.environ)
    environment.update({"GOOS": "windows", "GOARCH": "amd64", "CGO_ENABLED": "1"})
    go = shutil.which(args.go_command, path=environment.get("PATH"))
    if go is None:
        raise RuntimeError(f"cannot resolve Go command {args.go_command!r}")
    go = str(Path(go).resolve(strict=True))
    operations: list[dict[str, Any]] = []

    clean_before, clean_before_seconds = run_command(
        ["git", "-C", str(repository), "status", "--porcelain=v1", "--untracked-files=no"],
        cwd=repository,
        environment=environment,
    )
    operations.append(
        {
            "phase": "worktree-clean-before",
            "exitCode": clean_before.returncode,
            "durationSeconds": round(clean_before_seconds, 6),
        }
    )
    require_tracked_worktree_clean(clean_before, "tracked worktree precondition")

    sha_process, sha_seconds = run_command(
        ["git", "-C", str(repository), "rev-parse", "HEAD"],
        cwd=repository,
        environment=environment,
    )
    operations.append(
        {"phase": "repository-sha", "exitCode": sha_process.returncode, "durationSeconds": round(sha_seconds, 6)}
    )
    require_success(sha_process, "git rev-parse HEAD")
    repository_sha = sha_process.stdout.strip()
    if repository_sha != args.expected_repository_sha:
        raise RuntimeError(
            f"checkout SHA {repository_sha!r} does not match expected {args.expected_repository_sha!r}"
        )

    version_process, version_seconds = run_command(
        [go, "version"], cwd=repository, environment=environment
    )
    operations.append(
        {"phase": "go-version", "exitCode": version_process.returncode, "durationSeconds": round(version_seconds, 6)}
    )
    require_success(version_process, "go version")
    go_version = version_process.stdout.strip()
    env_process, env_seconds = run_command(
        [go, "env", "-json", "GOOS", "GOARCH", "CGO_ENABLED", "GOTOOLDIR"],
        cwd=repository,
        environment=environment,
    )
    operations.append(
        {"phase": "go-environment", "exitCode": env_process.returncode, "durationSeconds": round(env_seconds, 6)}
    )
    require_success(env_process, "go env")
    go_environment = json.loads(env_process.stdout)
    target_environment = {
        key: go_environment.get(key) for key in ("CGO_ENABLED", "GOARCH", "GOOS")
    }
    if target_environment != TARGET_ENVIRONMENT:
        raise RuntimeError(f"unexpected target Go environment: {target_environment!r}")
    go_tool_value = go_environment.get("GOTOOLDIR")
    if not isinstance(go_tool_value, str) or not go_tool_value:
        raise RuntimeError(f"go env returned an invalid GOTOOLDIR: {go_tool_value!r}")
    go_tool_directory = Path(go_tool_value).resolve(strict=True)
    if not go_tool_directory.is_dir():
        raise RuntimeError(f"Go tool directory is not a directory: {go_tool_directory}")

    all_packages_process, all_packages_seconds = run_command(
        [go, "list", "-json", "./..."],
        cwd=repository,
        environment=environment,
        stderr_path=logs / "go-list-all.stderr.log",
    )
    operations.append(
        {
            "phase": "go-list-all",
            "exitCode": all_packages_process.returncode,
            "durationSeconds": round(all_packages_seconds, 6),
        }
    )
    require_success(all_packages_process, "target-Windows go list ./...")
    package_objects = decode_json_stream(all_packages_process.stdout, "go list ./...")
    configured_heavy = {item["importPath"] for item in settings["heavyPackages"]}
    package_rows: list[dict[str, Any]] = []
    seen_packages: set[str] = set()
    for item in package_objects:
        import_path = item.get("ImportPath")
        directory = item.get("Dir")
        if not isinstance(import_path, str) or not import_path or import_path in seen_packages:
            raise ValueError(f"target-Windows package has missing or duplicate ImportPath: {import_path!r}")
        if not isinstance(directory, str) or not directory:
            raise ValueError(f"target-Windows package {import_path} has no directory")
        seen_packages.add(import_path)
        package_rows.append(
            {
                "importPath": import_path,
                "packageDirectoryRelative": repository_relative_directory(
                    repository, directory, import_path
                ),
                "heavy": import_path in configured_heavy,
                "testGoFileCount": len(item.get("TestGoFiles") or []),
                "externalTestGoFileCount": len(item.get("XTestGoFiles") or []),
            }
        )
    if not configured_heavy.issubset(seen_packages):
        raise ValueError(
            f"configured heavy packages absent from target-Windows go list: {sorted(configured_heavy - seen_packages)!r}"
        )
    package_rows.sort(key=lambda item: item["importPath"])
    package_inventory = {
        "schema": PACKAGE_SCHEMA,
        "repositorySha": repository_sha,
        "goVersion": go_version,
        "goos": "windows",
        "goarch": "amd64",
        "cgoEnabled": "1",
        "packages": package_rows,
    }
    write_json(output / "package-inventory.json", package_inventory)

    inventory_paths: list[Path] = []
    for configured in settings["heavyPackages"]:
        package_argument = configured["argument"]
        expected_import = configured["importPath"]
        binary_name = configured["binaryName"]
        package_process, package_seconds = run_command(
            [go, "list", "-json", package_argument],
            cwd=repository,
            environment=environment,
            stderr_path=logs / f"{binary_name}.go-list.stderr.log",
        )
        operations.append(
            {
                "phase": "go-list-package",
                "package": expected_import,
                "exitCode": package_process.returncode,
                "durationSeconds": round(package_seconds, 6),
            }
        )
        require_success(package_process, f"go list {package_argument}")
        package_items = decode_json_stream(package_process.stdout, f"go list {package_argument}")
        if len(package_items) != 1:
            raise ValueError(f"go list {package_argument} returned {len(package_items)} packages")
        package_item = package_items[0]
        if package_item.get("ImportPath") != expected_import:
            raise ValueError(
                f"{package_argument} resolved to {package_item.get('ImportPath')!r}, want {expected_import!r}"
            )
        package_directory = Path(package_item["Dir"]).resolve(strict=True)
        package_relative = repository_relative_directory(
            repository, str(package_directory), expected_import
        )
        testmain_process, testmain_seconds = run_command(
            [
                go,
                "run",
                str(repository / "tools/ci-bench/windows-production/inventory_testmain.go"),
            ],
            cwd=repository,
            environment=environment,
            stdout_path=logs / f"{binary_name}.testmain.json",
            stderr_path=logs / f"{binary_name}.testmain.stderr.log",
            stdin_text=package_process.stdout,
        )
        operations.append(
            {
                "phase": "testmain-inventory",
                "package": expected_import,
                "exitCode": testmain_process.returncode,
                "durationSeconds": round(testmain_seconds, 6),
            }
        )
        require_success(testmain_process, f"inventory TestMain declarations for {package_argument}")
        try:
            testmain_inventory = json.loads(testmain_process.stdout)
        except json.JSONDecodeError as error:
            raise ValueError(f"invalid TestMain inventory for {expected_import}: {error}") from error
        if (
            not isinstance(testmain_inventory, dict)
            or testmain_inventory.get("schema") != TESTMAIN_SCHEMA
            or not isinstance(testmain_inventory.get("declarations"), list)
        ):
            raise ValueError(f"malformed TestMain inventory for {expected_import}")
        test_main_declarations = testmain_inventory["declarations"]
        if test_main_declarations != configured["expectedTestMainDeclarations"]:
            raise RuntimeError(
                f"TestMain declaration drift for {expected_import}: "
                f"actual={test_main_declarations!r}, expected={configured['expectedTestMainDeclarations']!r}"
            )
        binary_path = (binaries / binary_name).resolve()
        compile_process, compile_seconds = run_command(
            [go, "test", "-vet=off", "-c", "-o", str(binary_path), package_argument],
            cwd=repository,
            environment=environment,
            stdout_path=logs / f"{binary_name}.compile.stdout.log",
            stderr_path=logs / f"{binary_name}.compile.stderr.log",
        )
        operations.append(
            {
                "phase": "compile",
                "package": expected_import,
                "exitCode": compile_process.returncode,
                "durationSeconds": round(compile_seconds, 6),
            }
        )
        require_success(compile_process, f"go test -c {package_argument}")
        if not binary_path.is_file() or binary_path.stat().st_size <= 0:
            raise RuntimeError(f"compiled binary is missing or empty: {binary_path}")

        listing_environment = direct_test_environment(
            environment, package_directory, go_tool_directory
        )
        listing_arguments = direct_listing_arguments(binary_path)
        listing_process, listing_seconds = run_command(
            listing_arguments,
            cwd=package_directory,
            environment=listing_environment,
            stdout_path=logs / f"{binary_name}.list.stdout.log",
            stderr_path=logs / f"{binary_name}.list.stderr.log",
        )
        operations.append(
            {
                "phase": "list",
                "package": expected_import,
                "exitCode": listing_process.returncode,
                "durationSeconds": round(listing_seconds, 6),
            }
        )
        require_success(listing_process, f"{binary_name} -test.list=.")
        if listing_process.stderr:
            raise RuntimeError(f"{binary_name} wrote unexpected listing stderr")
        roots, benchmarks = classify_listing(
            listing_process.stdout.splitlines(), f"{binary_name} -test.list=."
        )
        inventory = {
            "schema": plan_shards.INVENTORY_SCHEMA,
            "repositorySha": repository_sha,
            "goVersion": go_version,
            "goos": "windows",
            "goarch": "amd64",
            "cgoEnabled": "1",
            "packageArgument": package_argument,
            "importPath": expected_import,
            "packageDirectoryRelative": package_relative,
            "binaryName": binary_name,
            "binarySha256": sha256_file(binary_path),
            "binarySizeBytes": binary_path.stat().st_size,
            "listExitCode": listing_process.returncode,
            "listingContract": {
                "arguments": listing_arguments[1:],
                "paniconexit0": True,
                "pwdMatchesPackageDirectory": listing_environment.get("PWD")
                == str(package_directory),
                "goToolDirectoryPrependedToPath": listing_environment.get("PATH", "").split(
                    os.pathsep, 1
                )[0]
                == str(go_tool_directory),
            },
            "testMainDeclarations": test_main_declarations,
            "roots": roots,
            "excludedBenchmarks": benchmarks,
        }
        inventory_path = inventories_dir / f"{binary_name}.inventory.json"
        write_json(inventory_path, inventory)
        inventory_paths.append(inventory_path)

    weights, weight_metadata = plan_shards.load_weights(weights_path)
    inventories = plan_shards.load_inventories(inventory_paths)
    plan = plan_shards.create_plan(inventories, settings, weights, weight_metadata)
    write_json(output / "plan.json", plan)
    shutil.copyfile(settings_path, output / "settings.json")
    shutil.copyfile(weights_path, output / "historical-weights.json")
    clean_after, clean_after_seconds = run_command(
        ["git", "-C", str(repository), "status", "--porcelain=v1", "--untracked-files=no"],
        cwd=repository,
        environment=environment,
    )
    operations.append(
        {
            "phase": "worktree-clean-after",
            "exitCode": clean_after.returncode,
            "durationSeconds": round(clean_after_seconds, 6),
        }
    )
    require_tracked_worktree_clean(clean_after, "tracked worktree postcondition")
    sha_after_process, sha_after_seconds = run_command(
        ["git", "-C", str(repository), "rev-parse", "HEAD"],
        cwd=repository,
        environment=environment,
    )
    operations.append(
        {
            "phase": "repository-sha-after",
            "exitCode": sha_after_process.returncode,
            "durationSeconds": round(sha_after_seconds, 6),
        }
    )
    require_success(sha_after_process, "git rev-parse HEAD after prepare")
    repository_sha_after = sha_after_process.stdout.strip()
    if repository_sha_after != repository_sha:
        raise RuntimeError(
            f"checkout SHA changed during prepare: {repository_sha!r} -> {repository_sha_after!r}"
        )
    write_json(
        output / "prepare-metadata.json",
        {
            "schema": PREPARE_SCHEMA,
            "repositorySha": repository_sha,
            "repositoryShaAfter": repository_sha_after,
            "goVersion": go_version,
            "targetEnvironment": target_environment,
            "goToolDirectory": str(go_tool_directory),
            "trackedWorktreeCleanBefore": True,
            "trackedWorktreeCleanAfter": True,
            "exitCode": 0,
            "operations": operations,
            "packageCount": len(package_rows),
            "heavyPackageCount": len(inventory_paths),
            "rootCount": plan["rootCount"],
            "shardCount": plan["shardCount"],
        },
    )


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    try:
        prepare(args)
    except Exception as error:  # A partial artifact must carry a machine-readable failure.
        write_json(
            output / "prepare-metadata.json",
            {
                "schema": PREPARE_SCHEMA,
                "exitCode": 2,
                "error": str(error),
            },
        )
        print(f"error: {error}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
