#!/usr/bin/env python3
"""Run every target-Windows package outside the compiled shard set exactly once."""

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
from typing import Any


PLAN_SCHEMA = "entire-graph.windows-ci.shard-plan.v1"
PACKAGE_SCHEMA = "entire-graph.windows-ci.package-inventory.v1"
RUN_SCHEMA = "entire-graph.windows-ci.other-run.v1"
TARGET_ENVIRONMENT = {"CGO_ENABLED": "1", "GOARCH": "amd64", "GOOS": "windows"}


def read_json(path: Path) -> Any:
    with path.open(encoding="utf-8-sig") as handle:
        return json.load(handle)


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    with temporary.open("w", encoding="utf-8", newline="\n") as handle:
        json.dump(value, handle, indent=2, ensure_ascii=False)
        handle.write("\n")
    temporary.replace(path)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def command_line_utf16_units(executable: str, arguments: list[str]) -> int:
    serialized = subprocess.list2cmdline([executable, *arguments])
    return len(serialized.encode("utf-16-le")) // 2 + 1


def checked_output(
    arguments: list[str], repository: Path, environment: dict[str, str], label: str
) -> str:
    completed = subprocess.run(
        arguments,
        cwd=repository,
        env=environment,
        text=True,
        encoding="utf-8",
        errors="strict",
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if completed.returncode != 0:
        raise RuntimeError(f"{label} exited {completed.returncode}: {completed.stderr.strip()}")
    return completed.stdout.strip()


def checked_target_environment(
    go: str, repository: Path, environment: dict[str, str]
) -> dict[str, str]:
    raw = checked_output(
        [go, "env", "-json", "GOOS", "GOARCH", "CGO_ENABLED"],
        repository,
        environment,
        "go env",
    )
    value = json.loads(raw)
    target = {key: value.get(key) for key in ("CGO_ENABLED", "GOARCH", "GOOS")}
    if target != TARGET_ENVIRONMENT:
        raise RuntimeError(f"unexpected target Go environment: {target!r}")
    return target


def require_tracked_worktree_clean(
    repository: Path, environment: dict[str, str], label: str
) -> None:
    status = checked_output(
        ["git", "-C", str(repository), "status", "--porcelain=v1", "--untracked-files=no"],
        repository,
        environment,
        label,
    )
    if status:
        raise RuntimeError(f"{label}: tracked worktree modifications detected")


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", type=Path, required=True)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--expected-repository-sha", required=True)
    parser.add_argument("--go-command", default="go")
    return parser.parse_args(argv)


def run(args: argparse.Namespace, metadata: dict[str, Any]) -> int:
    if sys.platform != "win32":
        raise RuntimeError("run_other.py must run on native Windows")
    repository = args.repository.resolve(strict=True)
    bundle = args.bundle.resolve(strict=True)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    plan_path = bundle / "plan.json"
    package_path = bundle / "package-inventory.json"
    plan = read_json(plan_path)
    package_inventory = read_json(package_path)
    if not isinstance(plan, dict) or plan.get("schema") != PLAN_SCHEMA:
        raise ValueError("unexpected or missing shard plan schema")
    if not isinstance(package_inventory, dict) or package_inventory.get("schema") != PACKAGE_SCHEMA:
        raise ValueError("unexpected or missing package inventory schema")
    if plan.get("targetEnvironment") != TARGET_ENVIRONMENT:
        raise ValueError("shard plan target is not exactly windows/amd64 with CGO_ENABLED=1")
    package_target = {
        "GOOS": package_inventory.get("goos"),
        "GOARCH": package_inventory.get("goarch"),
        "CGO_ENABLED": package_inventory.get("cgoEnabled"),
    }
    if package_target != TARGET_ENVIRONMENT:
        raise ValueError("package inventory target is not exactly windows/amd64 with CGO_ENABLED=1")
    settings = plan.get("settings")
    if not isinstance(settings, dict) or settings.get("shuffle") != "off":
        raise ValueError("plan does not preserve shuffle=off semantics")
    timeout = settings.get("timeout")
    command_limit = settings.get("commandLineLimit")
    if not isinstance(timeout, str) or not re.fullmatch(r"[1-9][0-9]*(?:ns|us|µs|ms|s|m|h)", timeout):
        raise ValueError("plan has an invalid timeout")
    if not isinstance(command_limit, int) or not 1024 <= command_limit <= 32767:
        raise ValueError("plan has an invalid Windows command-line limit")

    rows = package_inventory.get("packages")
    if not isinstance(rows, list) or not rows:
        raise ValueError("target-Windows package inventory is empty")
    packages: list[str] = []
    seen: set[str] = set()
    for position, row in enumerate(rows):
        if not isinstance(row, dict) or not isinstance(row.get("heavy"), bool):
            raise ValueError(f"package inventory row {position} is malformed")
        package = row.get("importPath")
        if not isinstance(package, str) or not package or package in seen:
            raise ValueError(f"package inventory row {position} has a missing or duplicate import path")
        seen.add(package)
        if not row["heavy"]:
            packages.append(package)
    packages.sort()
    if not packages:
        raise ValueError("package inventory contains no non-heavy packages")
    declared_heavy = {row["importPath"] for row in rows if row["heavy"]}
    planned_heavy = {row.get("importPath") for row in plan.get("packages", [])}
    if declared_heavy != planned_heavy:
        raise ValueError("package inventory and shard plan disagree on heavy packages")

    environment = dict(os.environ)
    environment.update({"GOOS": "windows", "GOARCH": "amd64", "CGO_ENABLED": "1"})
    go_max_procs = settings.get("goMaxProcs")
    if go_max_procs is not None:
        if not isinstance(go_max_procs, int) or isinstance(go_max_procs, bool) or go_max_procs <= 0:
            raise ValueError("plan has invalid goMaxProcs")
        environment["GOMAXPROCS"] = str(go_max_procs)
    go = shutil.which(args.go_command, path=environment.get("PATH"))
    if go is None:
        raise RuntimeError(f"cannot resolve Go command {args.go_command!r}")
    go = str(Path(go).resolve(strict=True))
    repository_sha = checked_output(
        ["git", "-C", str(repository), "rev-parse", "HEAD"],
        repository,
        environment,
        "git rev-parse HEAD",
    )
    go_version = checked_output([go, "version"], repository, environment, "go version")
    if (
        repository_sha != args.expected_repository_sha
        or repository_sha != plan.get("repositorySha")
        or repository_sha != package_inventory.get("repositorySha")
    ):
        raise RuntimeError("repository SHA differs between checkout, workflow, plan, or package inventory")
    if go_version != plan.get("goVersion") or go_version != package_inventory.get("goVersion"):
        raise RuntimeError("Go version differs between runner, plan, or package inventory")
    target_environment = checked_target_environment(go, repository, environment)
    if target_environment != plan.get("targetEnvironment"):
        raise RuntimeError("runner target Go environment differs from the plan")
    require_tracked_worktree_clean(repository, environment, "tracked worktree precondition")

    metadata.update(
        {
            "repositorySha": repository_sha,
            "goVersion": go_version,
            "targetEnvironment": target_environment,
            "trackedWorktreeCleanBefore": True,
            "planSha256": sha256_file(plan_path),
            "packageInventorySha256": sha256_file(package_path),
            "expectedPackages": packages,
            "packageCount": len(packages),
            "timeout": timeout,
            "shuffle": "off",
            "testParallel": settings.get("testParallel"),
            "goMaxProcs": go_max_procs,
            "commandLineLimit": command_limit,
        }
    )

    try:
        clean = subprocess.run(
            [go, "clean", "-testcache"],
            cwd=repository,
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        (output / "clean-testcache.stdout.log").write_bytes(clean.stdout)
        (output / "clean-testcache.stderr.log").write_bytes(clean.stderr)
        metadata["cleanTestCacheExitCode"] = clean.returncode
        if clean.returncode != 0:
            return clean.returncode

        command_arguments = [
            "test",
            "-json",
            "-vet=off",
            f"-timeout={timeout}",
            "-shuffle=off",
        ]
        test_parallel = settings.get("testParallel")
        if test_parallel is not None:
            if not isinstance(test_parallel, int) or isinstance(test_parallel, bool) or test_parallel <= 0:
                raise ValueError("plan has invalid testParallel")
            command_arguments.append(f"-parallel={test_parallel}")
        command_arguments.extend(packages)
        command_units = command_line_utf16_units(go, command_arguments)
        metadata["commandLineUtf16Units"] = command_units
        if command_units > command_limit:
            raise RuntimeError(
                f"serialized Windows command line is {command_units} UTF-16 units, above {command_limit}; refusing implicit package batching"
            )
        started = time.monotonic()
        with (output / "other-events.jsonl").open("wb") as stdout, (
            output / "other.stderr.log"
        ).open("wb") as stderr:
            completed = subprocess.run(
                [go, *command_arguments],
                cwd=repository,
                env=environment,
                stdout=stdout,
                stderr=stderr,
                check=False,
            )
        metadata["durationSeconds"] = round(time.monotonic() - started, 6)
        metadata["testExitCode"] = completed.returncode
        return completed.returncode
    finally:
        require_tracked_worktree_clean(repository, environment, "tracked worktree postcondition")
        metadata["trackedWorktreeCleanAfter"] = True
        repository_sha_after = checked_output(
            ["git", "-C", str(repository), "rev-parse", "HEAD"],
            repository,
            environment,
            "git rev-parse HEAD after non-heavy tests",
        )
        metadata["repositoryShaAfter"] = repository_sha_after
        if repository_sha_after != repository_sha:
            raise RuntimeError(
                f"repository SHA changed during non-heavy tests: {repository_sha!r} -> {repository_sha_after!r}"
            )


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    metadata: dict[str, Any] = {
        "schema": RUN_SCHEMA,
        "exitCode": None,
        "errors": [],
        "cleanTestCacheExitCode": None,
        "testExitCode": None,
        "trackedWorktreeCleanBefore": None,
        "trackedWorktreeCleanAfter": None,
        "repositoryShaAfter": None,
    }
    try:
        exit_code = run(args, metadata)
    except Exception as error:
        metadata["errors"].append(str(error))
        exit_code = 2
        print(f"error: {error}", file=sys.stderr)
    metadata["exitCode"] = exit_code
    write_json(output / "other-metadata.json", metadata)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
