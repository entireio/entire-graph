#!/usr/bin/env python3
"""Complete archive member provenance from frozen inventory and live cleanup records."""
from __future__ import annotations

import argparse
import gzip
import hashlib
import io
import json
import stat
import subprocess
import tarfile
from pathlib import Path, PurePosixPath


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def load_records(path: Path) -> list[dict]:
    return json.loads(path.read_text()).get("records", [])


def inventory_from_provenance(repo: Path, previous: dict) -> dict:
    ref = previous["audited_head"]
    archives = []
    for old in previous["archives"]:
        payload = subprocess.check_output(["git", "show", f"{ref}:{old['archive_path']}"], cwd=repo)
        members = []
        if old.get("archive_format") == "gzip-single-member":
            prior = old["members"][0]
            raw = gzip.decompress(payload)
            members.append({"name": prior["member"], "size": len(raw), "sha256": hashlib.sha256(raw).hexdigest(), "mode": prior["member_mode"]})
            member_count = 1
        else:
            with tarfile.open(fileobj=io.BytesIO(payload), mode="r:gz") as bundle:
                tar_members = bundle.getmembers(); member_count = len(tar_members)
                for item in tar_members:
                    if item.isfile():
                        raw = bundle.extractfile(item).read()
                        members.append({"name": item.name, "size": len(raw), "sha256": hashlib.sha256(raw).hexdigest(), "mode": stat.S_IMODE(item.mode)})
                    else:
                        members.append({"name": item.name, "type": "nonregular", "mode": stat.S_IMODE(item.mode)})
        archives.append({"path": old["archive_path"], "archive_sha256": hashlib.sha256(payload).hexdigest(), "bytes": len(payload), "member_count": member_count, "members": members})
    return {"archives": archives}


def complete(repo: Path, inventory_path: Path | None, provenance_path: Path, removal_map_path: Path,
             evidence_paths: list[Path]) -> tuple[dict, list[dict]]:
    previous = json.loads(provenance_path.read_text()) if provenance_path.is_file() else {"archives": []}
    inventory = json.loads(inventory_path.read_text()) if inventory_path else inventory_from_provenance(repo, previous)
    previous_members = {
        (a["archive_path"], m["member"]): m
        for a in previous.get("archives", []) for m in a.get("members", [])
    }
    removal = json.loads(removal_map_path.read_text())
    removal_candidates = [x for x in removal.get("candidates", []) if x.get("disposition") == "applied"]
    evidence = []
    for path in evidence_paths:
        for record in load_records(path):
            evidence.append((path, record))

    archives = []
    unresolved = []
    for source_archive in inventory["archives"]:
        archive_path = source_archive["path"]
        archive = {
            "archive_bytes": source_archive["bytes"],
            "archive_path": archive_path,
            "archive_sha256": source_archive["archive_sha256"],
            "member_count": source_archive["member_count"],
            "members": [],
        }
        if archive_path.endswith(".jsonl.gz"):
            archive["archive_format"] = "gzip-single-member"
        for item in source_archive["members"]:
            if "sha256" not in item:
                continue
            key = (archive_path, item["name"])
            old = previous_members.get(key, {})
            member = {
                "canonical_replacement_chains": [],
                "member": item["name"],
                "member_mode": item["mode"],
                "mode_contract": "member_mode_preserved_in archive; replacement_file_mode recorded independently",
                "replacement_modes": {},
                "replacement_paths": [],
                "sha256": item["sha256"],
            }
            direct_paths = set(old.get("replacement_paths", []))
            if item.get("sibling_match"):
                direct_paths.add(item["sibling_match"])
            direct_paths.update(item.get("global_git_matches", []))
            for path in sorted(direct_paths):
                fp = repo / path
                if fp.is_file() and sha256(fp) == item["sha256"]:
                    member["replacement_paths"].append(path)
                    member["replacement_modes"][path] = stat.S_IMODE(fp.stat().st_mode)

            candidate_paths = set(direct_paths)
            candidate_paths.update(
                chain["source_map_path"] for chain in old.get("canonical_replacement_chains", [])
                if chain.get("source_map_path")
            )
            parent = str(PurePosixPath(archive_path).parent)
            candidate_paths.add(str(PurePosixPath(parent) / item["name"]))
            for candidate in removal_candidates:
                if candidate.get("source_sha256") != item["sha256"]:
                    continue
                source_path = candidate.get("path", "")
                if source_path not in candidate_paths and not source_path.startswith(parent + "/"):
                    continue
                replacements = [candidate.get("replacement", {})] + candidate.get("additional_replacements", [])
                for replacement in replacements:
                    path = replacement.get("path")
                    fp = repo / path if path else None
                    if not path or not fp.is_file():
                        continue
                    digest = sha256(fp)
                    declared = replacement.get("retained_sha256")
                    if digest != declared:
                        continue
                    member["canonical_replacement_chains"].append({
                        "current_mode": stat.S_IMODE(fp.stat().st_mode),
                        "current_sha256": digest,
                        "declared_retained_sha256": declared,
                        "kind": replacement.get("kind"),
                        "path": path,
                        "role": replacement.get("role"),
                        "source_map_path": source_path,
                    })

            for evidence_path, record in evidence:
                source_sha = record.get("source_sha256", record.get("source_member_sha256"))
                if record.get("archive_path") != archive_path or record.get("member") != item["name"] or source_sha != item["sha256"]:
                    continue
                relative = evidence_path.resolve().relative_to(repo.resolve()).as_posix()
                digest = sha256(evidence_path)
                member["canonical_replacement_chains"].append({
                    "current_mode": stat.S_IMODE(evidence_path.stat().st_mode),
                    "current_sha256": digest,
                    "declared_retained_sha256": digest,
                    "evidence_record_path": relative,
                    "kind": record.get("kind", record.get("dataset", "archive-member-projection")),
                    "path": relative,
                    "role": record.get("disposition", record.get("projection_parity", "member evidence")),
                })

            unique = {json.dumps(x, sort_keys=True): x for x in member["canonical_replacement_chains"]}
            member["canonical_replacement_chains"] = [unique[k] for k in sorted(unique)]
            if not member["replacement_paths"] and not member["canonical_replacement_chains"]:
                unresolved.append({"archive_path": archive_path, "member": item["name"], "source_sha256": item["sha256"]})
            archive["members"].append(member)
        archives.append(archive)

    result = dict(previous)
    result.update({
        "archive_count": len(archives),
        "archives": archives,
        "member_count": sum(len(a["members"]) for a in archives),
        "removal_map_path": removal_map_path.resolve().relative_to(repo.resolve()).as_posix(),
    })
    result.pop("removal_map_sha256", None)
    return result, unresolved


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--inventory", type=Path, help="optional frozen inventory; defaults to rebuilding from provenance and audited Git objects")
    parser.add_argument("--provenance", type=Path, required=True)
    parser.add_argument("--removal-map", type=Path, required=True)
    parser.add_argument("--evidence", type=Path, action="append", default=[])
    parser.add_argument("--write", action="store_true")
    args = parser.parse_args()
    result, unresolved = complete(args.repo, args.inventory, args.provenance, args.removal_map, args.evidence)
    if args.write:
        args.provenance.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
    if unresolved:
        print(json.dumps({"unresolved": unresolved}, indent=2))
        return 1
    print(f"archive provenance complete: {result['archive_count']} archives, {result['member_count']} regular members")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
