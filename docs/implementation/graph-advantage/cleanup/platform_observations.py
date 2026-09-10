#!/usr/bin/env python3
"""Normalize historical VM and transport wrappers into public-safe facts."""

from __future__ import annotations

import argparse
from collections import Counter
import hashlib
import json
from pathlib import Path
import re
import subprocess


SOURCE_BASENAMES = {
    "build-transport.json",
    "collection-transport.json",
    "evaluator-build-transport.json",
    "paused-vm-state.json",
    "recovery-transport.json",
    "terminal-status-graph-p1-worker-2.json",
    "terminal-status-graph-p1-worker-3.json",
    "terminal-status-graph-validation-linux.json",
    "transport-cleanup.json",
    "transport-metadata.json",
    "transport.json",
    "vm-terminal-final.json",
    "vm-terminal.json",
}
STATUS_WORDS = ("succeeded", "failed", "warning", "error", "timeout")


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def git(*args: str, cwd: Path) -> bytes:
    return subprocess.check_output(["git", *args], cwd=cwd)


def logical_role(value: str) -> str:
    lowered = value.lower()
    if "worker-2" in lowered:
        return "worker-2"
    if "worker-3" in lowered:
        return "worker-3"
    if "validation" in lowered:
        return "validation"
    return "unspecified"


def message_facts(value: str) -> dict[str, object]:
    lowered = value.lower()
    return {
        "sha256": sha256(value.encode()),
        "bytes": len(value.encode()),
        "line_count": len(value.splitlines()),
        "status_word_counts": {
            word: len(re.findall(rf"\b{word}\b", lowered)) for word in STATUS_WORDS
        },
        "mentions_upload": bool(re.search(r"\bupload(?:ed|ing)?\b", lowered)),
    }


def status_entry(value: dict[str, object]) -> dict[str, object]:
    return {
        key: value[key]
        for key in ("code", "displayStatus", "level", "time")
        if key in value and isinstance(value[key], str)
    }


def normalize_payload(path: str, value: object) -> tuple[str, dict[str, object]]:
    basename = Path(path).name
    if isinstance(value, dict) and isinstance(value.get("value"), list):
        statuses = []
        for item in value["value"]:
            if not isinstance(item, dict):
                raise ValueError(f"unexpected transport status item in {path}")
            status = status_entry(item)
            message = item.get("message", "")
            if not isinstance(message, str):
                raise ValueError(f"unexpected transport message in {path}")
            status["message_facts"] = message_facts(message)
            statuses.append(status)
        return "remote-command-status", {"statuses": statuses}

    if basename == "vm-terminal.json" and isinstance(value, dict):
        hardware = value.get("hardwareProfile", {})
        storage = value.get("storageProfile", {})
        image = storage.get("imageReference", {}) if isinstance(storage, dict) else {}
        os_disk = storage.get("osDisk", {}) if isinstance(storage, dict) else {}
        security = value.get("securityProfile", {})
        instance = value.get("instanceView", {})
        disk_statuses: Counter[tuple[str, str, str]] = Counter()
        if isinstance(instance, dict):
            for disk in instance.get("disks", []):
                if not isinstance(disk, dict):
                    continue
                for status in disk.get("statuses", []):
                    if isinstance(status, dict):
                        key = (
                            str(status.get("code", "")),
                            str(status.get("displayStatus", "")),
                            str(status.get("level", "")),
                        )
                        disk_statuses[key] += 1
        return "vm-terminal-state", {
            "role": logical_role(str(value.get("name", ""))),
            "resource_class": hardware.get("vmSize") if isinstance(hardware, dict) else None,
            "created_at": value.get("timeCreated"),
            "provisioning_state": value.get("provisioningState"),
            "security_type": security.get("securityType") if isinstance(security, dict) else None,
            "os_type": os_disk.get("osType") if isinstance(os_disk, dict) else None,
            "image": {
                key: image[key]
                for key in ("publisher", "offer", "sku", "version", "exactVersion")
                if isinstance(image, dict) and key in image
            },
            "instance_statuses": [
                status_entry(item)
                for item in (instance.get("statuses", []) if isinstance(instance, dict) else [])
                if isinstance(item, dict)
            ],
            "disk_status_counts": [
                {
                    "code": key[0],
                    "display_status": key[1],
                    "level": key[2],
                    "count": count,
                }
                for key, count in sorted(disk_statuses.items())
            ],
        }

    if basename == "vm-terminal-final.json" and isinstance(value, list):
        return "vm-power-state", {
            "vms": [
                {
                    "role": logical_role(str(item.get("name", ""))),
                    "power_state": item.get("powerState"),
                }
                for item in value
                if isinstance(item, dict)
            ]
        }

    if basename == "paused-vm-state.json" and isinstance(value, list):
        return "vm-paused-state", {
            "vms": [
                {
                    "role": logical_role(str(item.get("vm", ""))),
                    "states": [str(state) for state in item.get("state", [])],
                }
                for item in value
                if isinstance(item, dict)
            ]
        }

    if basename == "transport-metadata.json" and isinstance(value, dict):
        command = value.get("command", [])
        if command and not isinstance(command, list):
            raise ValueError(f"unexpected command in {path}")
        safe = {
            key: value[key]
            for key in ("purpose", "python_sha256", "script_sha256", "source_commit")
            if key in value
        }
        safe["signed_urls_redacted"] = bool(value.get("signed_urls"))
        if command:
            rendered = json.dumps(command, separators=(",", ":"), ensure_ascii=False)
            safe["command_facts"] = {
                "sha256": sha256(rendered.encode()),
                "argument_count": len(command),
                "operation": "remote-command",
            }
        artifacts = {}
        for key in ("blob", "result_blob", "runner_blob", "runner_origin", "remote_root"):
            if key in value and isinstance(value[key], str):
                artifacts[key] = {"sha256": sha256(value[key].encode()), "present": True}
        safe["redacted_artifact_identifiers"] = artifacts
        return "transport-configuration", safe

    if basename.startswith("terminal-status-") and isinstance(value, dict):
        raw = value.get("raw_response_redacted", "")
        if not isinstance(raw, str):
            raise ValueError(f"unexpected terminal response in {path}")
        return "redacted-terminal-status", {
            "role": logical_role(basename),
            "recorded_raw_sha256": value.get("raw_response_sha256"),
            "response_facts": message_facts(raw),
        }

    if basename == "transport-cleanup.json" and isinstance(value, list):
        return "vm-cleanup-status", {
            "vms": [
                {
                    "role": logical_role(str(item.get("vm", ""))),
                    "output_facts": message_facts("\n".join(str(v) for v in item.get("output", []))),
                }
                for item in value
                if isinstance(item, dict)
            ]
        }

    if isinstance(value, list) and all(isinstance(item, str) for item in value):
        rendered = "\n".join(value)
        operation = "artifact-transfer"
        if basename in {"build-transport.json", "evaluator-build-transport.json"}:
            operation = "evaluator-build"
        elif basename == "collection-transport.json":
            operation = "artifact-collection"
        elif basename == "recovery-transport.json":
            operation = "artifact-recovery"
        return "transport-output", {
            "operation": operation,
            "message_count": len(value),
            "output_facts": message_facts(rendered),
        }

    raise ValueError(f"unsupported platform wrapper schema: {path}")


def generate(repo: Path, revision: str) -> dict[str, object]:
    prefix = "docs/implementation/graph-advantage"
    paths = git("ls-tree", "-r", "--name-only", revision, "--", prefix, cwd=repo).decode().splitlines()
    sources = [path for path in paths if Path(path).name in SOURCE_BASENAMES]
    # all-vms-terminal.json is intentionally excluded: it is a duplicate wrapper family
    # reviewed separately from this exact 86-file set.
    if len(sources) != 86:
        raise ValueError(f"expected 86 platform wrappers, found {len(sources)}")
    observations = []
    for path in sorted(sources):
        data = git("show", f"{revision}:{path}", cwd=repo)
        blob = git("rev-parse", f"{revision}:{path}", cwd=repo).decode().strip()
        kind, facts = normalize_payload(path, json.loads(data))
        observations.append(
            {
                "source_path": path,
                "source_sha256": sha256(data),
                "source_git_blob": blob,
                "source_bytes": len(data),
                "kind": kind,
                "facts": facts,
            }
        )
    return {
        "schema": "graph-advantage-platform-observations-v1",
        "source_revision": revision,
        "observation_count": len(observations),
        "source_bytes": sum(item["source_bytes"] for item in observations),
        "redactions": [
            "Azure subscription, resource-group, VM, NIC, disk, account, container, and blob identifiers",
            "administrator usernames, SSH public keys, local and remote filesystem paths, and signed URLs",
            "raw command output and transport messages; only bounded status counts and content digests remain",
        ],
        "limitations": [
            "The normalized record preserves operational state and configuration facts, not verbatim provider responses.",
            "The fifteen all-vms-terminal.json duplicates are outside this 86-file normalization batch.",
        ],
        "observations": observations,
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, default=Path("."))
    parser.add_argument("--revision", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    value = generate(args.repo, args.revision)
    args.output.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


if __name__ == "__main__":
    main()
