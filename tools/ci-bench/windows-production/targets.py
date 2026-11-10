#!/usr/bin/env python3
"""Target identities served by the production test-sharding harness.

Everything that differs between the supported native targets lives here, so
the prepare, plan, run, and verify code paths stay shared. A target is always
named explicitly (the `--target` argument, pinned by the workflow) and must
agree with the settings file and every artifact; nothing infers it from the
host, and an unknown or mismatched target fails closed.
"""

from __future__ import annotations

import os
import struct
import subprocess
from dataclasses import dataclass
from typing import Any, Mapping, Sequence


# Windows: CreateProcess rejects a serialized command line longer than 32767
# UTF-16 code units, including the terminating NUL.
WINDOWS_COMMAND_LINE_MAXIMUM = 32767

# macOS: execve(2) fails with E2BIG when the argument area exceeds ARG_MAX
# (NCARGS in <sys/syslimits.h>, `getconf ARG_MAX`), which is 1048576 bytes on
# every supported Darwin release. That area holds the NUL-terminated argv AND
# environment strings plus one pointer per string, so a settings limit is a
# budget for argv alone and may never claim the whole area. Darwin has no
# separate per-string limit (Linux does: MAX_ARG_STRLEN, 131072 bytes).
DARWIN_ARG_MAX = 1048576


@dataclass(frozen=True)
class Target:
    name: str
    goos: str
    goarch: str
    host_platform: str
    host_label: str
    binary_suffix: str
    weight_source: str
    posix: bool
    command_line_metric: str
    command_line_unit: str
    command_line_limit_maximum: int

    @property
    def environment(self) -> dict[str, str]:
        return {"CGO_ENABLED": "1", "GOARCH": self.goarch, "GOOS": self.goos}

    @property
    def description(self) -> str:
        return f"{self.name} with CGO_ENABLED=1"

    def valid_command_line_limit(self, value: Any) -> bool:
        return (
            isinstance(value, int)
            and not isinstance(value, bool)
            and 1024 <= value <= self.command_line_limit_maximum
        )

    def command_line_cost(self, executable: str, arguments: Sequence[str]) -> int:
        """Size of one launch in the unit the target's kernel limit is stated in."""
        if self.posix:
            return sum(len(os.fsencode(item)) + 1 for item in (executable, *arguments))
        serialized = subprocess.list2cmdline([executable, *arguments])
        return len(serialized.encode("utf-16-le")) // 2 + 1


WINDOWS_AMD64 = Target(
    name="windows/amd64",
    goos="windows",
    goarch="amd64",
    host_platform="win32",
    host_label="Windows",
    binary_suffix=".test.exe",
    weight_source="historical-windows",
    posix=False,
    command_line_metric="commandLineUtf16Units",
    command_line_unit="UTF-16 units",
    command_line_limit_maximum=WINDOWS_COMMAND_LINE_MAXIMUM,
)

DARWIN_ARM64 = Target(
    name="darwin/arm64",
    goos="darwin",
    goarch="arm64",
    host_platform="darwin",
    host_label="macOS",
    binary_suffix=".test",
    weight_source="historical-darwin",
    posix=True,
    command_line_metric="commandLineArgvBytes",
    command_line_unit="argv bytes",
    # Half of ARG_MAX is the most a settings file may hand to argv; the rest
    # is reserved for the environment and the pointer arrays.
    command_line_limit_maximum=DARWIN_ARG_MAX // 2,
)

DEFAULT = WINDOWS_AMD64
TARGETS = {target.name: target for target in (WINDOWS_AMD64, DARWIN_ARM64)}


def resolve(name: Any) -> Target:
    if not isinstance(name, str) or name not in TARGETS:
        raise ValueError(f"unsupported target {name!r}; supported: {sorted(TARGETS)!r}")
    return TARGETS[name]


def argument_area_maximum() -> int:
    """The running kernel's exec argument-area size (POSIX hosts only)."""
    return int(os.sysconf("SC_ARG_MAX"))


def argument_area_bytes(arguments: Sequence[str], environment: Mapping[str, str]) -> int:
    """Bytes execve must copy: argv and environment strings plus their pointers."""
    strings = sum(len(os.fsencode(item)) + 1 for item in arguments)
    strings += sum(
        len(os.fsencode(key)) + len(os.fsencode(value)) + 2 for key, value in environment.items()
    )
    pointers = struct.calcsize("P") * (len(arguments) + len(environment) + 2)
    return strings + pointers


def checked_argument_area(
    target: Target, arguments: Sequence[str], environment: Mapping[str, str], label: str
) -> dict[str, int]:
    """Fail closed when a POSIX launch cannot fit beside its environment.

    The settings limit bounds argv; this bounds argv plus the runner's actual
    environment against the kernel's own figure, so a large environment can
    never turn an in-budget command into a truncated or refused launch.
    """
    if not target.posix:
        return {}
    used = argument_area_bytes(arguments, environment)
    maximum = argument_area_maximum()
    if used > maximum:
        raise RuntimeError(
            f"{label} needs {used} bytes of exec argument area, above the kernel limit {maximum}; "
            "refusing implicit batching"
        )
    return {"execArgumentAreaBytes": used, "execArgumentAreaLimit": maximum}


def valid_argument_area(target: Target, record: Any) -> bool:
    """Whether a run record carries the argument-area evidence its target requires."""
    if not target.posix:
        return True
    if not isinstance(record, dict):
        return False
    used, maximum = record.get("execArgumentAreaBytes"), record.get("execArgumentAreaLimit")
    return (
        isinstance(used, int)
        and not isinstance(used, bool)
        and isinstance(maximum, int)
        and not isinstance(maximum, bool)
        and 1 <= used <= maximum
    )
