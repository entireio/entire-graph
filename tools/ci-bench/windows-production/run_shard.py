#!/usr/bin/env python3
"""Execute one immutable native test shard through go tool test2json."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import threading
import time
from pathlib import Path, PurePosixPath
from typing import Any

import targets


PLAN_SCHEMA = "entire-graph.windows-ci.shard-plan.v1"
RUN_SCHEMA = "entire-graph.windows-ci.shard-run.v1"
TARGET_ENVIRONMENT = targets.DEFAULT.environment
HEARTBEAT_INTERVAL_SECONDS = 60.0


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
    return targets.WINDOWS_AMD64.command_line_cost(executable, arguments)


def safe_repository_directory(repository: Path, relative: Any) -> Path:
    if not isinstance(relative, str) or not relative or "\\" in relative:
        raise ValueError(f"unsafe package directory {relative!r}")
    value = PurePosixPath(relative)
    if value.is_absolute() or ".." in value.parts:
        raise ValueError(f"unsafe package directory {relative!r}")
    resolved = repository.joinpath(*value.parts).resolve(strict=True)
    try:
        resolved.relative_to(repository)
    except ValueError as error:
        raise ValueError(f"package directory escaped repository: {resolved}") from error
    if not resolved.is_dir():
        raise ValueError(f"package directory is not a directory: {resolved}")
    return resolved


def checked_go_version(go: str, repository: Path, environment: dict[str, str]) -> str:
    completed = subprocess.run(
        [go, "version"],
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
        raise RuntimeError(f"go version exited {completed.returncode}: {completed.stderr.strip()}")
    return completed.stdout.strip()


def checked_repository_sha(repository: Path, environment: dict[str, str]) -> str:
    completed = subprocess.run(
        ["git", "-C", str(repository), "rev-parse", "HEAD"],
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
        raise RuntimeError(f"git rev-parse exited {completed.returncode}: {completed.stderr.strip()}")
    return completed.stdout.strip()


def checked_go_environment(
    go: str,
    repository: Path,
    environment: dict[str, str],
    target: targets.Target = targets.DEFAULT,
) -> tuple[dict[str, str], Path]:
    completed = subprocess.run(
        [go, "env", "-json", "GOOS", "GOARCH", "CGO_ENABLED", "GOTOOLDIR"],
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
        raise RuntimeError(f"go env exited {completed.returncode}: {completed.stderr.strip()}")
    value = json.loads(completed.stdout)
    actual = {key: value.get(key) for key in ("CGO_ENABLED", "GOARCH", "GOOS")}
    if actual != target.environment:
        raise RuntimeError(f"unexpected target Go environment: {actual!r}")
    tool_value = value.get("GOTOOLDIR")
    if not isinstance(tool_value, str) or not tool_value:
        raise RuntimeError(f"go env returned an invalid GOTOOLDIR: {tool_value!r}")
    tool_directory = Path(tool_value).resolve(strict=True)
    if not tool_directory.is_dir():
        raise RuntimeError(f"Go tool directory is not a directory: {tool_directory}")
    return actual, tool_directory


def require_tracked_worktree_clean(
    repository: Path, environment: dict[str, str], label: str
) -> None:
    completed = subprocess.run(
        ["git", "-C", str(repository), "status", "--porcelain=v1", "--untracked-files=no"],
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


def ensure_executable(binary: Path, target: targets.Target) -> bool:
    """Restore the owner execute bit that artifact transport drops on POSIX targets.

    actions/upload-artifact stores files in a ZIP without POSIX modes, so a
    downloaded test binary arrives non-executable. The mode is not part of the
    binary's identity: the caller has already matched its SHA-256 to the plan,
    so setting the bit changes nothing that was verified.
    """
    if not target.posix or os.access(binary, os.X_OK):
        return False
    binary.chmod(binary.stat().st_mode | 0o500)
    if not os.access(binary, os.X_OK):
        raise RuntimeError(f"cannot make the compiled test binary executable: {binary.name}")
    return True


def list_binary_names(
    binary: Path, package_directory: Path, environment: dict[str, str], package: str
) -> list[str]:
    """Everything the compiled test binary itself lists, as this runner sees it.

    The plan was made from a listing taken in the compile-and-plan job. Each
    shard asks its copy of the binary again, with the same launch contract,
    and records the raw names; the verifier requires every shard's listing to
    name exactly the plan's roots, so the bundle's inventory is no longer the
    only witness of what the binary contains.
    """
    completed = subprocess.run(
        [str(binary), "-test.paniconexit0", "-test.list=."],
        cwd=package_directory,
        env=environment,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if completed.returncode != 0 or completed.stderr:
        raise RuntimeError(
            f"listing the compiled roots of {package} exited {completed.returncode} or wrote to stderr"
        )
    names = [line.strip() for line in completed.stdout.decode("utf-8").splitlines() if line.strip()]
    if len(names) != len(set(names)):
        raise RuntimeError(f"the compiled binary of {package} lists a root more than once")
    return sorted(names)


def shard_command_arguments(
    package: str,
    binary: Path,
    timeout: str,
    run_regex: str,
    test_parallel: int | None,
) -> list[str]:
    arguments = [
        "tool",
        "test2json",
        "-t",
        "-p",
        package,
        str(binary),
        "-test.paniconexit0",
        f"-test.timeout={timeout}",
        f"-test.run={run_regex}",
        "-test.shuffle=off",
    ]
    if test_parallel is not None:
        arguments.append(f"-test.parallel={test_parallel}")
    arguments.append("-test.v=test2json")
    return arguments


def start_heartbeat(
    prefix: str, detail: str, started: float, interval: float
) -> tuple[threading.Event, threading.Thread]:
    """Print a liveness line to the runner console until the returned event is set."""
    stop = threading.Event()
    stream = sys.stderr

    def beat() -> None:
        while not stop.wait(interval):
            elapsed = time.monotonic() - started
            print(f"{prefix} heartbeat {detail} elapsed={elapsed:.1f}s", file=stream, flush=True)

    thread = threading.Thread(target=beat, name="run-shard-heartbeat", daemon=True)
    thread.start()
    return stop, thread


def stop_heartbeat(stop: threading.Event, thread: threading.Thread, timeout: float = 1.0) -> bool:
    """Do not let a daemon blocked on console output hold up an assignment or exception."""
    stop.set()
    thread.join(timeout=timeout)
    return not thread.is_alive()


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", type=Path, required=True)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--shard-index", type=int, required=True)
    parser.add_argument("--expected-repository-sha", required=True)
    parser.add_argument("--go-command", default="go")
    parser.add_argument("--target", default=targets.DEFAULT.name, choices=sorted(targets.TARGETS))
    return parser.parse_args(argv)


def run(
    args: argparse.Namespace,
    metadata: dict[str, Any],
    heartbeat_interval: float = HEARTBEAT_INTERVAL_SECONDS,
) -> int:
    target = targets.resolve(getattr(args, "target", targets.DEFAULT.name))
    if sys.platform != target.host_platform:
        raise RuntimeError(f"run_shard.py must run on native {target.host_label}")
    repository = args.repository.resolve(strict=True)
    bundle = args.bundle.resolve(strict=True)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    plan_path = bundle / "plan.json"
    plan = read_json(plan_path)
    if not isinstance(plan, dict) or plan.get("schema") != PLAN_SCHEMA:
        raise ValueError("unexpected or missing shard plan schema")
    if plan.get("targetEnvironment") != target.environment:
        raise ValueError(f"shard plan target is not exactly {target.description}")
    if not isinstance(args.shard_index, int) or not 0 <= args.shard_index < plan.get("shardCount", -1):
        raise ValueError(f"shard index {args.shard_index} is outside the plan")
    shards = [item for item in plan.get("shards", []) if item.get("index") == args.shard_index]
    if len(shards) != 1:
        raise ValueError(f"plan does not contain exactly one shard {args.shard_index}")
    shard = shards[0]
    assignments = shard.get("assignments")
    if not isinstance(assignments, list) or not assignments:
        raise ValueError(f"shard {args.shard_index} has no assignments")
    settings = plan.get("settings")
    if not isinstance(settings, dict) or settings.get("shuffle") != "off":
        raise ValueError("plan does not preserve shuffle=off semantics")
    timeout = settings.get("timeout")
    command_limit = settings.get("commandLineLimit")
    if not isinstance(timeout, str) or not re.fullmatch(r"[1-9][0-9]*(?:ns|us|µs|ms|s|m|h)", timeout):
        raise ValueError("plan has an invalid timeout")
    if not target.valid_command_line_limit(command_limit):
        raise ValueError(f"plan has an invalid {target.host_label} command-line limit")

    environment = dict(os.environ)
    environment.update(target.environment)
    go_max_procs = settings.get("goMaxProcs")
    if go_max_procs is not None:
        if not isinstance(go_max_procs, int) or isinstance(go_max_procs, bool) or go_max_procs <= 0:
            raise ValueError("plan has invalid goMaxProcs")
        environment["GOMAXPROCS"] = str(go_max_procs)
    go = shutil.which(args.go_command, path=environment.get("PATH"))
    if go is None:
        raise RuntimeError(f"cannot resolve Go command {args.go_command!r}")
    go = str(Path(go).resolve(strict=True))
    repository_sha = checked_repository_sha(repository, environment)
    if repository_sha != args.expected_repository_sha or repository_sha != plan.get("repositorySha"):
        raise RuntimeError(
            f"repository SHA mismatch: checkout={repository_sha!r}, expected={args.expected_repository_sha!r}, plan={plan.get('repositorySha')!r}"
        )
    go_version = checked_go_version(go, repository, environment)
    if go_version != plan.get("goVersion"):
        raise RuntimeError(f"Go toolchain mismatch: runner={go_version!r}, plan={plan.get('goVersion')!r}")
    target_environment, go_tool_directory = checked_go_environment(
        go, repository, environment, target
    )
    if target_environment != plan.get("targetEnvironment"):
        raise RuntimeError("runner target Go environment differs from the plan")
    require_tracked_worktree_clean(repository, environment, "tracked worktree precondition")

    metadata.update(
        {
            "repositorySha": repository_sha,
            "goVersion": go_version,
            "targetEnvironment": target_environment,
            "goToolDirectory": str(go_tool_directory),
            "trackedWorktreeCleanBefore": True,
            "planSha256": sha256_file(plan_path),
            "shardIndex": args.shard_index,
            "shardCount": plan["shardCount"],
            "commandLineLimit": command_limit,
            "timeout": timeout,
            "shuffle": "off",
            "testParallel": settings.get("testParallel"),
            "goMaxProcs": go_max_procs,
        }
    )
    combined_events = output / "shard-events.jsonl"
    combined_events.write_bytes(b"")
    final_exit = 0
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

        for position, assignment in enumerate(assignments):
            package = assignment.get("importPath")
            binary_name = assignment.get("binaryName")
            expected_binary_hash = assignment.get("binarySha256")
            run_regex = assignment.get("runRegex")
            roots = assignment.get("roots")
            if not isinstance(package, str) or not package:
                raise ValueError(f"assignment {position} has no import path")
            if (
                not isinstance(binary_name, str)
                or Path(binary_name).name != binary_name
                or "/" in binary_name
                or "\\" in binary_name
            ):
                raise ValueError(f"assignment {position} has unsafe binary name")
            if not isinstance(expected_binary_hash, str) or not re.fullmatch(r"[0-9a-f]{64}", expected_binary_hash):
                raise ValueError(f"assignment {position} has invalid binary hash")
            if not isinstance(run_regex, str) or not run_regex.startswith("^") or not run_regex.endswith("$"):
                raise ValueError(f"assignment {position} has invalid run regex")
            if not isinstance(roots, list) or not roots:
                raise ValueError(f"assignment {position} has no runnable roots")
            package_directory = safe_repository_directory(
                repository, assignment.get("packageDirectoryRelative")
            )
            binary = (bundle / "binaries" / binary_name).resolve(strict=True)
            try:
                binary.relative_to((bundle / "binaries").resolve(strict=True))
            except ValueError as error:
                raise ValueError(f"binary escaped bundle: {binary}") from error
            actual_binary_hash = sha256_file(binary)
            if actual_binary_hash != expected_binary_hash:
                raise RuntimeError(
                    f"binary hash mismatch for {package}: {actual_binary_hash} != {expected_binary_hash}"
                )

            test_parallel = settings.get("testParallel")
            if test_parallel is not None and (
                not isinstance(test_parallel, int)
                or isinstance(test_parallel, bool)
                or test_parallel <= 0
            ):
                raise ValueError("plan has invalid testParallel")
            command_arguments = shard_command_arguments(
                package, binary, timeout, run_regex, test_parallel
            )
            command_units = target.command_line_cost(go, command_arguments)
            if command_units > command_limit:
                raise RuntimeError(
                    f"serialized {target.host_label} command line for {package} is {command_units} {target.command_line_unit}, above {command_limit}; refusing implicit batching"
                )

            stem = f"{position:02d}-{re.sub(r'[^A-Za-z0-9_.-]', '_', package)}"
            stdout_path = output / f"{stem}.jsonl"
            stderr_path = output / f"{stem}.stderr.log"
            direct_environment = direct_test_environment(
                environment, package_directory, go_tool_directory
            )
            argument_area = targets.checked_argument_area(
                target, [go, *command_arguments], direct_environment, f"launch of {package}"
            )
            executable_bit_restored = ensure_executable(binary, target)
            listed_names = list_binary_names(binary, package_directory, direct_environment, package)
            prefix = f"shard {args.shard_index} assignment {position + 1}/{len(assignments)}"
            detail = f"package={json.dumps(package)} roots={len(roots)}"
            print(f"{prefix} start {detail}", file=sys.stderr, flush=True)
            started = time.monotonic()
            with stdout_path.open("wb") as stdout, stderr_path.open("wb") as stderr:
                heartbeat_stop, heartbeat = start_heartbeat(
                    prefix, detail, started, heartbeat_interval
                )
                failure = None
                try:
                    completed = subprocess.run(
                        [go, *command_arguments],
                        cwd=package_directory,
                        env=direct_environment,
                        stdout=stdout,
                        stderr=stderr,
                        check=False,
                    )
                except BaseException as error:
                    # Report only the class: the message can carry argv or paths.
                    failure = type(error).__name__
                    raise
                finally:
                    duration = time.monotonic() - started
                    heartbeat_stopped = stop_heartbeat(heartbeat_stop, heartbeat)
                    if failure is not None and heartbeat_stopped:
                        # The heartbeat is joined, so this line is terminal.
                        print(
                            f"{prefix} end {detail} elapsed={duration:.3f}s "
                            f"exit=exception:{failure}",
                            file=sys.stderr,
                            flush=True,
                        )
            if heartbeat_stopped:
                print(
                    f"{prefix} end {detail} elapsed={duration:.3f}s exit={completed.returncode}",
                    file=sys.stderr,
                    flush=True,
                )
            content = stdout_path.read_bytes()
            with combined_events.open("ab") as combined:
                combined.write(content)
                if content and not content.endswith(b"\n"):
                    combined.write(b"\n")
            invocation = {
                "package": package,
                "binaryName": binary_name,
                "binarySha256": actual_binary_hash,
                "rootCount": len(roots),
                "listedNames": listed_names,
                target.command_line_metric: command_units,
                "durationSeconds": round(duration, 6),
                "exitCode": completed.returncode,
                "paniconexit0": "-test.paniconexit0" in command_arguments,
                "workingDirectoryRelative": assignment.get("packageDirectoryRelative"),
                "pwdMatchesPackageDirectory": direct_environment.get("PWD")
                == str(package_directory),
                "goToolDirectoryPrependedToPath": direct_environment.get("PATH", "").split(
                    os.pathsep, 1
                )[0]
                == str(go_tool_directory),
            }
            if target.posix:
                invocation.update(argument_area)
                invocation["executableBitRestored"] = executable_bit_restored
            metadata["invocations"].append(invocation)
            write_json(output / "shard-metadata.json", metadata)
            if completed.returncode != 0 and final_exit == 0:
                final_exit = completed.returncode
        return final_exit
    finally:
        require_tracked_worktree_clean(repository, environment, "tracked worktree postcondition")
        metadata["trackedWorktreeCleanAfter"] = True
        repository_sha_after = checked_repository_sha(repository, environment)
        metadata["repositoryShaAfter"] = repository_sha_after
        if repository_sha_after != repository_sha:
            raise RuntimeError(
                f"repository SHA changed during shard execution: {repository_sha!r} -> {repository_sha_after!r}"
            )


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    metadata: dict[str, Any] = {
        "schema": RUN_SCHEMA,
        "exitCode": None,
        "errors": [],
        "invocations": [],
        "cleanTestCacheExitCode": None,
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
    write_json(output / "shard-metadata.json", metadata)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
