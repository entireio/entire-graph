#!/usr/bin/env python3
"""Summarize a ``go test -json`` event stream.

The parser is intentionally dependency-free so it can run on a stock Python 3
installation on a disposable Windows benchmark host.  It treats each input
line independently: malformed or truncated records are reported as
diagnostics while the remaining events are still summarized.
"""

from __future__ import print_function

import argparse
import codecs
import io
import json
import math
import sys
from collections import Counter
from contextlib import contextmanager
from datetime import datetime
from pathlib import Path


TERMINAL_ACTIONS = frozenset(("pass", "fail", "skip"))
KNOWN_ACTIONS = frozenset(
    ("start", "run", "pause", "cont", "output", "bench", "pass", "fail", "skip")
)
STATUS_KEYS = ("pass", "fail", "skip", "incomplete", "unknown")


def _rounded_seconds(value):
    if value is None:
        return None
    return round(float(value), 9)


def _elapsed_value(value):
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    value = float(value)
    if not math.isfinite(value) or value < 0:
        return None
    return _rounded_seconds(value)


def _parse_timestamp(value):
    if not isinstance(value, str) or not value:
        return None
    normalized = value[:-1] + "+00:00" if value.endswith("Z") else value
    try:
        parsed = datetime.fromisoformat(normalized)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        return None
    return parsed


def _timestamp_duration(start, end):
    if start is None or end is None:
        return None
    seconds = (end - start).total_seconds()
    if seconds < 0:
        return None
    return _rounded_seconds(seconds)


def _excerpt(line, limit=240):
    rendered = line.rstrip("\r\n")
    if len(rendered) <= limit:
        return rendered
    return rendered[: limit - 1] + "…"


class EventState:
    """Mutable timing and terminal-state accumulator for a package or test."""

    def __init__(self, name):
        self.name = name
        self.first_time = None
        self.first_timestamp = None
        self.last_time = None
        self.last_timestamp = None
        self.start_time = None
        self.start_timestamp = None
        self.end_time = None
        self.end_timestamp = None
        self.status = None
        self.reported_elapsed = None
        self.event_count = 0
        self.terminal_line = None

    def observe(self, time_text, timestamp):
        self.event_count += 1
        if timestamp is None:
            return
        if self.first_timestamp is None or timestamp < self.first_timestamp:
            self.first_time = time_text
            self.first_timestamp = timestamp
        if self.last_timestamp is None or timestamp > self.last_timestamp:
            self.last_time = time_text
            self.last_timestamp = timestamp

    def mark_start(self, time_text, timestamp):
        if self.start_timestamp is None or (
            timestamp is not None and timestamp < self.start_timestamp
        ):
            self.start_time = time_text
            self.start_timestamp = timestamp

    def mark_terminal(self, action, elapsed, time_text, timestamp, line_number):
        previous = self.status
        self.status = action
        self.reported_elapsed = elapsed
        self.end_time = time_text
        self.end_timestamp = timestamp
        old_line = self.terminal_line
        self.terminal_line = line_number
        return previous, old_line

    def effective_start(self):
        if self.start_timestamp is not None:
            return self.start_time, self.start_timestamp
        return self.first_time, self.first_timestamp

    def effective_end(self):
        if self.end_timestamp is not None:
            return self.end_time, self.end_timestamp
        return self.last_time, self.last_timestamp

    def duration(self, fallback=None):
        if self.reported_elapsed is not None:
            return self.reported_elapsed, "reported_elapsed"
        _, start = self.effective_start()
        _, end = self.effective_end()
        wall = _timestamp_duration(start, end)
        if wall is not None:
            return wall, "event_timestamps"
        if fallback is not None:
            return fallback, "max_subtest_elapsed"
        return None, "unavailable"


class PackageState(EventState):
    def __init__(self, name):
        super().__init__(name)
        self.tests = {}
        self.action_counts = Counter()


class GoTestJSONParser:
    def __init__(self, input_name):
        self.input_name = input_name
        self.packages = {}
        self.diagnostics = []
        self.action_counts = Counter()
        self.total_lines = 0
        self.blank_lines = 0
        self.event_count = 0
        self.ignored_records = 0
        self.malformed_lines = 0
        self.global_first_time = None
        self.global_first_timestamp = None
        self.global_last_time = None
        self.global_last_timestamp = None

    def diagnostic(self, line, code, message, excerpt=None, severity="warning"):
        item = {
            "severity": severity,
            "line": line,
            "code": code,
            "message": message,
        }
        if excerpt is not None:
            item["excerpt"] = excerpt
        self.diagnostics.append(item)

    def _observe_global_time(self, time_text, timestamp):
        if timestamp is None:
            return
        if self.global_first_timestamp is None or timestamp < self.global_first_timestamp:
            self.global_first_time = time_text
            self.global_first_timestamp = timestamp
        if self.global_last_timestamp is None or timestamp > self.global_last_timestamp:
            self.global_last_time = time_text
            self.global_last_timestamp = timestamp

    def consume_line(self, raw_line, line_number, terminated=True):
        self.total_lines += 1
        if "\ufffd" in raw_line:
            self.diagnostic(
                line_number,
                "invalid_utf8",
                "input contained undecodable bytes replaced with U+FFFD",
                _excerpt(raw_line),
            )

        candidate = raw_line.strip()
        if line_number == 1:
            candidate = candidate.lstrip("\ufeff")
        if not candidate:
            self.blank_lines += 1
            return

        try:
            event = json.loads(candidate)
        except json.JSONDecodeError as error:
            code = "truncated_json" if not terminated else "invalid_json"
            message = "line is not valid JSON at column {}".format(error.colno)
            if code == "truncated_json":
                message += "; final line was not newline-terminated"
            self.diagnostic(line_number, code, message, _excerpt(raw_line))
            self.malformed_lines += 1
            return

        if not isinstance(event, dict):
            self.diagnostic(
                line_number,
                "invalid_event",
                "JSON record must be an object",
                _excerpt(raw_line),
            )
            self.ignored_records += 1
            return

        action = event.get("Action")
        if not isinstance(action, str) or not action:
            self.diagnostic(
                line_number,
                "missing_action",
                "event has no non-empty string Action field",
                _excerpt(raw_line),
            )
            self.ignored_records += 1
            return

        package_name = event.get("Package")
        if not isinstance(package_name, str) or not package_name:
            self.diagnostic(
                line_number,
                "missing_package",
                "event has no non-empty string Package field",
                _excerpt(raw_line),
            )
            self.ignored_records += 1
            return

        if action not in KNOWN_ACTIONS:
            self.diagnostic(
                line_number,
                "unknown_action",
                "unrecognized go test action {!r}; event timing was retained".format(action),
            )

        test_name = event.get("Test")
        if test_name is not None and (not isinstance(test_name, str) or not test_name):
            self.diagnostic(
                line_number,
                "invalid_test_name",
                "Test field must be a non-empty string when present",
                _excerpt(raw_line),
            )
            test_name = None

        time_text = event.get("Time")
        timestamp = _parse_timestamp(time_text)
        if time_text is not None and timestamp is None:
            self.diagnostic(
                line_number,
                "invalid_time",
                "Time field is not a timezone-qualified ISO-8601 timestamp",
            )

        elapsed = None
        if "Elapsed" in event:
            elapsed = _elapsed_value(event.get("Elapsed"))
            if elapsed is None:
                self.diagnostic(
                    line_number,
                    "invalid_elapsed",
                    "Elapsed field must be a non-negative number",
                )

        self.event_count += 1
        self.action_counts[action] += 1
        self._observe_global_time(time_text, timestamp)

        package = self.packages.setdefault(package_name, PackageState(package_name))
        package.observe(time_text, timestamp)
        package.action_counts[action] += 1
        if action == "start" and test_name is None:
            package.mark_start(time_text, timestamp)
        if action in TERMINAL_ACTIONS and test_name is None:
            previous, previous_line = package.mark_terminal(
                action, elapsed, time_text, timestamp, line_number
            )
            if previous is not None:
                self.diagnostic(
                    line_number,
                    "duplicate_package_terminal",
                    "package {!r} already ended with {} on line {}".format(
                        package_name, previous, previous_line
                    ),
                )

        if test_name is None:
            return

        test = package.tests.setdefault(test_name, EventState(test_name))
        test.observe(time_text, timestamp)
        if action == "run":
            test.mark_start(time_text, timestamp)
        if action in TERMINAL_ACTIONS:
            previous, previous_line = test.mark_terminal(
                action, elapsed, time_text, timestamp, line_number
            )
            if previous is not None:
                self.diagnostic(
                    line_number,
                    "duplicate_test_terminal",
                    "test {!r} in {!r} already ended with {} on line {}".format(
                        test_name, package_name, previous, previous_line
                    ),
                )

    def parse(self, lines):
        for line_number, raw_line in enumerate(lines, 1):
            self.consume_line(
                raw_line,
                line_number,
                terminated=raw_line.endswith("\n") or raw_line.endswith("\r"),
            )
        return self.result()

    def _subtest_result(self, state, top_name):
        start_time, _ = state.effective_start()
        end_time, _ = state.effective_end()
        duration, source = state.duration()
        parent_name = state.name.rsplit("/", 1)[0]
        return {
            "name": state.name,
            "relative_name": state.name[len(top_name) + 1 :],
            "parent": parent_name,
            "status": state.status or "incomplete",
            "duration_seconds": duration,
            "duration_source": source,
            "reported_elapsed_seconds": state.reported_elapsed,
            "start_time": start_time,
            "end_time": end_time,
            "event_count": state.event_count,
        }

    def _top_level_result(self, top_name, states):
        root = next((item for item in states if item.name == top_name), None)
        timestamps = [
            item.first_timestamp for item in states if item.first_timestamp is not None
        ]
        end_timestamps = [
            item.last_timestamp for item in states if item.last_timestamp is not None
        ]
        first_timestamp = min(timestamps) if timestamps else None
        last_timestamp = max(end_timestamps) if end_timestamps else None
        first_state = next(
            (item for item in states if item.first_timestamp == first_timestamp), None
        )
        last_state = next(
            (item for item in states if item.last_timestamp == last_timestamp), None
        )

        subtests = [
            self._subtest_result(item, top_name)
            for item in sorted(states, key=lambda candidate: candidate.name)
            if item.name != top_name
        ]
        descendant_elapsed = [
            item.reported_elapsed
            for item in states
            if item.name != top_name and item.reported_elapsed is not None
        ]
        fallback = max(descendant_elapsed) if descendant_elapsed else None

        if root is not None:
            start_time, start_timestamp = root.effective_start()
            reported_elapsed = root.reported_elapsed
            status = root.status
            if status is None:
                if start_timestamp is None:
                    start_time = first_state.first_time if first_state is not None else None
                    start_timestamp = first_timestamp
                end_time = last_state.last_time if last_state is not None else None
                end_timestamp = last_timestamp
                duration = _timestamp_duration(start_timestamp, end_timestamp)
                source = "event_timestamps" if duration is not None else "unavailable"
                if duration is None and fallback is not None:
                    duration = fallback
                    source = "max_subtest_elapsed"
            else:
                end_time, end_timestamp = root.effective_end()
                duration, source = root.duration(fallback=fallback)
        else:
            start_time = first_state.first_time if first_state is not None else None
            start_timestamp = first_timestamp
            end_time = last_state.last_time if last_state is not None else None
            end_timestamp = last_timestamp
            duration = _timestamp_duration(start_timestamp, end_timestamp)
            source = "event_timestamps" if duration is not None else "unavailable"
            if duration is None and fallback is not None:
                duration = fallback
                source = "max_subtest_elapsed"
            reported_elapsed = None
            status = None

        if status is None:
            status = "fail" if any(item.status == "fail" for item in states) else "incomplete"

        return {
            "name": top_name,
            "status": status,
            "duration_seconds": duration,
            "duration_source": source,
            "reported_elapsed_seconds": reported_elapsed,
            "start_time": start_time,
            "end_time": end_time,
            "event_count": sum(item.event_count for item in states),
            "subtest_count": len(subtests),
            "subtests": subtests,
        }

    def _package_result(self, package):
        grouped = {}
        for test_name, state in package.tests.items():
            top_name = test_name.split("/", 1)[0]
            grouped.setdefault(top_name, []).append(state)
        top_level_tests = [
            self._top_level_result(name, grouped[name]) for name in sorted(grouped)
        ]

        status = package.status
        if status is None:
            status = (
                "fail"
                if any(test["status"] == "fail" for test in top_level_tests)
                else "incomplete"
            )
        start_time, start_timestamp = package.effective_start()
        end_time, end_timestamp = package.effective_end()
        duration, source = package.duration()
        wall_duration = _timestamp_duration(start_timestamp, end_timestamp)
        status_counts = Counter(test["status"] for test in top_level_tests)

        return {
            "package": package.name,
            "status": status,
            "duration_seconds": duration,
            "duration_source": source,
            "reported_elapsed_seconds": package.reported_elapsed,
            "wall_duration_seconds": wall_duration,
            "start_time": start_time,
            "end_time": end_time,
            "event_count": package.event_count,
            "actions": dict(sorted(package.action_counts.items())),
            "top_level_test_count": len(top_level_tests),
            "top_level_test_status_counts": {
                key: status_counts.get(key, 0) for key in STATUS_KEYS
            },
            "top_level_tests": top_level_tests,
        }

    def result(self):
        packages = [
            self._package_result(self.packages[name]) for name in sorted(self.packages)
        ]
        package_status_counts = Counter(package["status"] for package in packages)
        top_level_tests = [
            test for package in packages for test in package["top_level_tests"]
        ]
        top_level_status_counts = Counter(test["status"] for test in top_level_tests)

        statuses = [package["status"] for package in packages]
        has_truncated_input = any(
            diagnostic["code"] == "truncated_json" for diagnostic in self.diagnostics
        )
        complete = bool(packages) and not has_truncated_input and all(
            status in TERMINAL_ACTIONS for status in statuses
        )
        if "fail" in statuses:
            suite_status = "fail"
        elif not complete:
            suite_status = "incomplete" if packages or self.event_count else "unknown"
        elif "pass" in statuses:
            suite_status = "pass"
        elif statuses and all(status == "skip" for status in statuses):
            suite_status = "skip"
        else:
            suite_status = "unknown"

        return {
            "schema_version": "ci-bench.go-test-json.v1",
            "source": {
                "input": self.input_name,
                "line_count": self.total_lines,
                "blank_line_count": self.blank_lines,
                "event_count": self.event_count,
                "ignored_record_count": self.ignored_records,
                "malformed_line_count": self.malformed_lines,
                "diagnostic_count": len(self.diagnostics),
                "actions": dict(sorted(self.action_counts.items())),
            },
            "suite": {
                "status": suite_status,
                "complete": complete,
                "start_time": self.global_first_time,
                "end_time": self.global_last_time,
                "wall_duration_seconds": _timestamp_duration(
                    self.global_first_timestamp, self.global_last_timestamp
                ),
                "package_count": len(packages),
                "package_status_counts": {
                    key: package_status_counts.get(key, 0) for key in STATUS_KEYS
                },
                "top_level_test_count": len(top_level_tests),
                "top_level_test_status_counts": {
                    key: top_level_status_counts.get(key, 0) for key in STATUS_KEYS
                },
            },
            "packages": packages,
            "diagnostics": self.diagnostics,
        }


def parse_lines(lines, input_name="<stream>"):
    """Parse an iterable of JSON event lines and return the versioned result."""

    return GoTestJSONParser(input_name).parse(lines)


def _format_duration(seconds):
    if seconds is None:
        return "-"
    if seconds >= 60:
        minutes = int(seconds // 60)
        return "{}m{:.3f}s".format(minutes, seconds - minutes * 60)
    return "{:.3f}s".format(seconds)


def render_summary(result, top=20):
    """Render a deterministic, human-oriented summary."""

    suite = result["suite"]
    source = result["source"]
    lines = [
        "Go test JSON summary",
        "Status: {} ({})".format(
            suite["status"].upper(), "complete" if suite["complete"] else "incomplete"
        ),
        "Suite wall time: {}".format(_format_duration(suite["wall_duration_seconds"])),
        "Window: {} -> {}".format(suite["start_time"] or "-", suite["end_time"] or "-"),
        "Events: {} across {} lines; diagnostics: {}; malformed: {}".format(
            source["event_count"],
            source["line_count"],
            source["diagnostic_count"],
            source["malformed_line_count"],
        ),
        "Packages: {} (pass {}, fail {}, skip {}, incomplete {})".format(
            suite["package_count"],
            suite["package_status_counts"]["pass"],
            suite["package_status_counts"]["fail"],
            suite["package_status_counts"]["skip"],
            suite["package_status_counts"]["incomplete"],
        ),
        "Top-level tests: {} (pass {}, fail {}, skip {}, incomplete {})".format(
            suite["top_level_test_count"],
            suite["top_level_test_status_counts"]["pass"],
            suite["top_level_test_status_counts"]["fail"],
            suite["top_level_test_status_counts"]["skip"],
            suite["top_level_test_status_counts"]["incomplete"],
        ),
        "",
        "Packages (slowest first):",
    ]

    packages = sorted(
        result["packages"],
        key=lambda item: (
            item["duration_seconds"] is None,
            -(item["duration_seconds"] or 0),
            item["package"],
        ),
    )
    if packages:
        lines.append("  {:<10} {:>12} {:>7}  {}".format("STATUS", "DURATION", "TESTS", "PACKAGE"))
        for package in packages:
            lines.append(
                "  {:<10} {:>12} {:>7}  {}".format(
                    package["status"].upper(),
                    _format_duration(package["duration_seconds"]),
                    package["top_level_test_count"],
                    package["package"],
                )
            )
    else:
        lines.append("  (none)")

    tests = [
        (package["package"], test)
        for package in result["packages"]
        for test in package["top_level_tests"]
    ]
    tests.sort(
        key=lambda item: (
            item[1]["duration_seconds"] is None,
            -(item[1]["duration_seconds"] or 0),
            item[0],
            item[1]["name"],
        )
    )
    lines.extend(("", "Slowest top-level tests (up to {}):".format(top)))
    if tests and top:
        for package_name, test in tests[:top]:
            lines.append(
                "  {:<10} {:>12}  {} :: {}{}".format(
                    test["status"].upper(),
                    _format_duration(test["duration_seconds"]),
                    package_name,
                    test["name"],
                    " ({} subtests)".format(test["subtest_count"])
                    if test["subtest_count"]
                    else "",
                )
            )
    else:
        lines.append("  (none)")

    if result["diagnostics"]:
        lines.extend(("", "Diagnostics:"))
        for diagnostic in result["diagnostics"][:10]:
            lines.append(
                "  line {} [{}]: {}".format(
                    diagnostic["line"], diagnostic["code"], diagnostic["message"]
                )
            )
        remaining = len(result["diagnostics"]) - 10
        if remaining:
            lines.append("  ... and {} more".format(remaining))

    return "\n".join(lines) + "\n"


@contextmanager
def _open_input(path):
    if path == "-":
        yield sys.stdin
        return

    raw = open(path, "rb")
    try:
        prefix = raw.read(4)
        raw.seek(0)
        if prefix.startswith((codecs.BOM_UTF16_LE, codecs.BOM_UTF16_BE)):
            encoding = "utf-16"
        elif prefix.startswith(codecs.BOM_UTF8):
            encoding = "utf-8-sig"
        else:
            encoding = "utf-8"
        wrapper = io.TextIOWrapper(raw, encoding=encoding, errors="replace", newline="")
        try:
            yield wrapper
        finally:
            wrapper.detach()
    finally:
        raw.close()


def _write_text(path, content):
    if path == "-":
        sys.stdout.write(content)
        return
    output_path = Path(path)
    output_path.parent.mkdir(parents=True, exist_ok=True)
    with output_path.open("w", encoding="utf-8", newline="\n") as handle:
        handle.write(content)


def _argument_parser():
    parser = argparse.ArgumentParser(
        description="Parse a go test -json stream into deterministic timing and status data."
    )
    parser.add_argument(
        "--input",
        required=True,
        metavar="PATH",
        help="input go test JSON-lines file, or - for stdin",
    )
    parser.add_argument(
        "--output",
        required=True,
        metavar="PATH",
        help="primary output file, or - for stdout",
    )
    parser.add_argument(
        "--format",
        choices=("json", "summary"),
        default="json",
        help="primary output format (default: json)",
    )
    parser.add_argument(
        "--summary-output",
        metavar="PATH",
        help="also write a human-readable summary (only with --format json)",
    )
    parser.add_argument(
        "--top",
        type=int,
        default=20,
        metavar="N",
        help="number of slow top-level tests shown in summaries (default: 20)",
    )
    return parser


def main(argv=None):
    parser = _argument_parser()
    args = parser.parse_args(argv)
    if args.top < 0:
        parser.error("--top must be non-negative")
    if args.format != "json" and args.summary_output:
        parser.error("--summary-output requires --format json")
    if args.output == "-" and args.summary_output == "-":
        parser.error("primary output and summary output cannot both use stdout")

    try:
        with _open_input(args.input) as lines:
            result = parse_lines(lines, input_name=args.input)
        if args.format == "json":
            content = json.dumps(
                result, indent=2, sort_keys=True, ensure_ascii=False, allow_nan=False
            ) + "\n"
        else:
            content = render_summary(result, top=args.top)
        _write_text(args.output, content)
        if args.summary_output:
            _write_text(args.summary_output, render_summary(result, top=args.top))
    except (OSError, UnicodeError) as error:
        print("parse-go-test-json.py: {}".format(error), file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
