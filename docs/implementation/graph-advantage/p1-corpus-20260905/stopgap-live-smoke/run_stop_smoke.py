#!/usr/bin/env python3
"""Bounded live smoke for exact three-worker stop propagation.

This helper starts only a sleeping systemd service on each worker. It never
runs the evaluator, reads the corpus, or issues a product query.
"""
import argparse
import concurrent.futures
import hashlib
import json
import pathlib
import re
import shlex
import sys
import threading
import time

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
import cloud
import supervise

VMS = ["graph-validation-linux", "graph-p1-worker-2", "graph-p1-worker-3"]
SERVICE_RUNTIME_SECONDS = 300
DEFAULT_DEADLINE_SECONDS = 360
EMERGENCY_CLEANUP_SECONDS = 30
_ACK_RE = re.compile(r"^P1_SMOKE_READY [a-z0-9][a-z0-9-]{0,63}$")


def redact(value):
    text = value if isinstance(value, str) else json.dumps(value, sort_keys=True)
    return re.sub(r"(https?://[^\s\"'?]+)\?[^\s\"']*", r"\1?<redacted-sas>", text)


def save_raw(path, raw):
    text = raw if isinstance(raw, str) else json.dumps(raw, sort_keys=True)
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("x") as stream:
        json.dump(
            {
                "raw_response_sha256": hashlib.sha256(text.encode()).hexdigest(),
                "raw_response_redacted": redact(text),
            },
            stream,
            sort_keys=True,
        )
        stream.write("\n")


def decode(raw, path, ack=None):
    save_raw(path, raw)
    decoded = json.loads(raw) if isinstance(raw, str) else raw
    if ack is not None:
        if not isinstance(decoded, list) or not any(
            isinstance(message, str)
            and any(line.strip() == ack for line in message.splitlines())
            for message in decoded
        ):
            raise RuntimeError("smoke response missing acknowledgement: " + ack)
    return decoded


def run_dir(run_id):
    return "/opt/p1/runs/" + run_id


def unit_name(run_id):
    return "p1-campaign-" + run_id


def setup_script(run_id):
    remote = run_dir(run_id)
    unit = unit_name(run_id)
    return "\n".join(
        [
            "set -eu",
            f"test ! -e {shlex.quote(remote)}",
            f"mkdir -p {shlex.quote(remote)}",
            f"test ! -e {shlex.quote(remote + '/STOP')}",
            f"cat > {shlex.quote(remote + '/fake-worker.sh')} <<'P1_FAKE'",
            "#!/bin/sh",
            "trap 'exit 0' TERM INT",
            "while :; do /bin/sleep 1; done",
            "P1_FAKE",
            f"chmod 0755 {shlex.quote(remote + '/fake-worker.sh')}",
            f"printf '%s\\n' '{{\"stage\":\"campaign\",\"done\":false}}' > {shlex.quote(remote + '/progress.tmp')}",
            f"mv {shlex.quote(remote + '/progress.tmp')} {shlex.quote(remote + '/progress.json')}",
            f"test ! -e {shlex.quote(remote + '/STOP')}",
            f"systemd-run --unit={shlex.quote(unit)} --collect --property=RuntimeMaxSec={SERVICE_RUNTIME_SECONDS} --property=WorkingDirectory={shlex.quote(remote)} -- {shlex.quote(remote + '/fake-worker.sh')}",
            f"printf 'P1_SMOKE_READY %s\\n' {shlex.quote(run_id)}",
        ]
    ) + "\n"


def pause_script(run_id):
    remote = run_dir(run_id)
    return "set -eu\n" + "\n".join(
        [
            "python3 - <<'P1_PAUSE'",
            "import json, os, pathlib",
            f"p=pathlib.Path({remote!r}); t=p/'pause.json.tmp'",
            "t.write_text(json.dumps({'reason':'live stop smoke injected pause'}))",
            "os.replace(t, p/'pause.json')",
            "print('P1_SMOKE_PAUSED')",
            "P1_PAUSE",
        ]
    ) + "\n"


def record_call(output, name, vm, script, ack=None):
    raw = cloud.run(vm, script)
    return decode(raw, pathlib.Path(output) / f"{name}-{vm}.json", ack=ack)


def parallel_calls(output, name, calls, timeout=None):
    pool = concurrent.futures.ThreadPoolExecutor(max_workers=len(calls))
    futures = {
        pool.submit(record_call, output, name, vm, script, ack): vm
        for vm, script, ack in calls
    }
    try:
        results = {}
        for future in concurrent.futures.as_completed(futures, timeout=timeout):
            results[futures[future]] = future.result()
    except BaseException:
        pool.shutdown(wait=False, cancel_futures=True)
        raise
    else:
        pool.shutdown(wait=True)
        return results


def stop_all(output, stage, run_id, attempt=1, timeout=None):
    suffix = "" if attempt == 1 else f"-retry-{attempt}"
    reason = "live stop smoke cleanup"
    calls = [
        (
            vm,
            supervise.stop_script(stage, reason, run_dir(run_id), unit_name(run_id)),
            None,
        )
        for vm in VMS
    ]
    return parallel_calls(output, "stop" + suffix, calls, timeout=timeout)


def status_all(output, name, run_id, timeout=None):
    def query(vm):
        raw = cloud.run(vm, supervise.status_script("campaign", run_dir(run_id), unit_name(run_id)))
        save_raw(pathlib.Path(output) / f"{name}-{vm}.json", raw)
        return supervise.parse_status(raw)

    pool = concurrent.futures.ThreadPoolExecutor(max_workers=len(VMS))
    futures = {pool.submit(query, vm): vm for vm in VMS}
    try:
        states = {}
        for future in concurrent.futures.as_completed(futures, timeout=timeout):
            states[futures[future]] = future.result()
    except BaseException:
        pool.shutdown(wait=False, cancel_futures=True)
        raise
    else:
        pool.shutdown(wait=True)
        return states


def assert_active(states):
    for vm, state in states.items():
        if state.get("state") != "active" or state.get("progress", {}).get("stage") != "campaign" or state.get("progress", {}).get("done"):
            raise RuntimeError(f"worker is not active before pause: {vm}")


def assert_stopped(states, run_id):
    for vm, state in states.items():
        if state.get("state") != "inactive" or not state.get("stop"):
            raise RuntimeError(f"worker did not stop cleanly: {vm}")


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--deadline-seconds", type=int, default=DEFAULT_DEADLINE_SECONDS)
    args = parser.parse_args(argv)
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,63}", args.run_id):
        raise SystemExit("run-id must be a simple isolated identifier")
    if args.deadline_seconds <= 0:
        raise SystemExit("deadline-seconds must be positive")
    output = args.output.resolve()
    if output.exists() and any(output.iterdir()):
        raise SystemExit("output must be empty; refusing overwrite")
    output.mkdir(parents=True, exist_ok=True)
    env = cloud.environment()
    deadline = time.monotonic() + args.deadline_seconds
    issue = None
    supervisor_result = None
    states = {}
    started_at = time.time()
    try:
        parallel_calls(
            output,
            "setup",
            [(vm, setup_script(args.run_id), f"P1_SMOKE_READY {args.run_id}") for vm in VMS],
            timeout=max(0.0, deadline - time.monotonic()),
        )
        states = status_all(
            output,
            "initial-status",
            args.run_id,
            timeout=max(0.0, deadline - time.monotonic()),
        )
        assert_active(states)
        parallel_calls(
            output,
            "pause",
            [(VMS[0], pause_script(args.run_id), "P1_SMOKE_PAUSED")],
            timeout=max(0.0, deadline - time.monotonic()),
        )
        response_lock = threading.Lock()
        response_number = 0

        def supervised_run(vm, script):
            nonlocal response_number
            raw = cloud.run(vm, script)
            with response_lock:
                response_number += 1
                number = response_number
            save_raw(output / f"supervisor-{number:03d}-{vm}.json", raw)
            return raw

        supervisor_pool = concurrent.futures.ThreadPoolExecutor(max_workers=1)
        future = supervisor_pool.submit(
            supervise.supervise,
            VMS,
            "campaign",
            output,
            run=supervised_run,
            results_dir=run_dir(args.run_id),
            unit_name=unit_name(args.run_id),
        )
        try:
            supervisor_result = future.result(timeout=max(0.0, deadline - time.monotonic()))
        except concurrent.futures.TimeoutError:
            supervisor_pool.shutdown(wait=False, cancel_futures=True)
            stop_all(
                output,
                "campaign",
                args.run_id,
                attempt=1,
                timeout=EMERGENCY_CLEANUP_SECONDS,
            )
            stop_all(
                output,
                "campaign",
                args.run_id,
                attempt=2,
                timeout=EMERGENCY_CLEANUP_SECONDS,
            )
            raise RuntimeError("whole-smoke deadline expired")
        except BaseException:
            supervisor_pool.shutdown(wait=False, cancel_futures=True)
            raise
        else:
            supervisor_pool.shutdown(wait=True)
        if supervisor_result is not False:
            raise RuntimeError("supervisor did not fail closed after injected pause")
        states = status_all(
            output,
            "terminal-status",
            args.run_id,
            timeout=max(0.0, deadline - time.monotonic()),
        )
        assert_stopped(states, args.run_id)
    except BaseException as error:
        issue = str(error)
        try:
            stop_all(
                output,
                "campaign",
                args.run_id,
                attempt=3,
                timeout=EMERGENCY_CLEANUP_SECONDS,
            )
        except BaseException as cleanup_error:
            issue += "; cleanup: " + type(cleanup_error).__name__
    finally:
        summary = {
            "status": "pass" if issue is None else "issue",
            "issue": issue,
            "run_id": args.run_id,
            "vms": VMS,
            "unit": unit_name(args.run_id),
            "service_runtime_max_seconds": SERVICE_RUNTIME_SECONDS,
            "whole_deadline_seconds": args.deadline_seconds,
            "supervisor_result": supervisor_result,
            "terminal_states": states,
            "elapsed_seconds": time.time() - started_at,
            "scope": "fake sleeping services only; no evaluator, corpus, or product query",
        }
        (output / "summary.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n")
    if issue:
        raise SystemExit(issue)


if __name__ == "__main__":
    main()
