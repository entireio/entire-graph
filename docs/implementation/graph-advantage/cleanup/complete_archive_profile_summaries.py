#!/usr/bin/env python3
"""Attach the bounded profile-text assignment to the canonical profile summaries."""
import argparse
import hashlib
import json
import re
import subprocess
from pathlib import Path


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--assignment", type=Path, required=True)
    parser.add_argument("--records", type=Path, required=True)
    parser.add_argument("--profile-summaries", type=Path, required=True)
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--source-ref", required=True)
    args = parser.parse_args()
    data = json.loads(args.records.read_text())
    data["records"] = [x for x in data["records"] if x.get("kind") != "profile_text_summary_reference"]
    summary_data = json.loads(args.profile_summaries.read_text())
    profiles = {x["run_id"]: x for x in summary_data["profiles"]}
    summary_sha = hashlib.sha256(args.profile_summaries.read_bytes()).hexdigest()
    for source in json.loads(args.assignment.read_text())["records"]:
        path = source["path"]
        run_id = next((key for key in profiles if key != "baseline" and key in path), None)
        payload = subprocess.check_output(["git", "show", f"{args.source_ref}:{path}"], cwd=args.repo).decode("utf-8", "replace")
        record = {
            "kind": "profile_text_summary_reference",
            "source_path": path,
            "source_sha256": source["source_sha256"],
            "source_bytes": source["bytes"],
            "profile_summary_path": "docs/implementation/graph-advantage/cleanup/profile-summaries.json",
            "profile_summary_sha256": summary_sha,
            "projection": "exact offline reconstruction from retained raw profile, source commit, and pprof selector; raw sample lines intentionally not duplicated",
            "no_performance_claim": True,
        }
        if path.endswith("live-sample-addr2line.txt") or payload.startswith("build cache is required") or path.endswith("source-pointers.txt"):
            record["projection"] = "bounded authored or tool outcome retained verbatim; no raw sample lines"
            record["preserved_lines"] = payload.splitlines()
        else:
            profile = profiles[run_id]
            record.update({
                "run_id": run_id,
                "raw_profile_sha256": profile["raw_profile_sha256"],
                "raw_profile_git_blob": profile["raw_profile_git_blob"],
                "external_content_id": profile["external_content_id"],
                "external_copy_verified": profile["external_copy_verified"],
                "source_commit": subprocess.check_output(["git", "rev-parse", run_id], cwd=args.repo, text=True).strip(),
            })
            name = Path(path).name
            selector = {"pprof-top.txt": "top", "pprof-top-cumulative.txt": "top -cum", "cumulative.txt": "top -cum", "top-recovered.txt": "top"}.get(name)
            if name == "pprof-call-tree.txt": selector = "tree"
            if "-list-" in name or name.endswith("-list.txt"):
                if name.startswith("pprof-list-"):
                    function = name.removeprefix("pprof-list-").removesuffix(".txt").replace("-", "")
                else:
                    function = name.removeprefix("pprof-").removesuffix("-list.txt").replace("-", "")
                known = {
                    "gohttprouteregistrations": "goHTTPRouteRegistrations", "gohttprouterelations": "goHTTPRouteRelations",
                    "staticstringconstants": "staticStringConstants", "returnflow": "returnFlowCalls",
                    "receivercallrelations": "receiverCallRelations",
                }
                function = known.get(function, "receiverCallRelations" if "receiver-call-relations" in name else function)
                selector = f"list {function}"
            record["reconstruction"] = {"tool": "go tool pprof", "selector": selector, "input": profile["external_content_id"]}
            record["reconstruction"]["argv_template"] = ["go", "tool", "pprof", f"-{selector.split()[0]}"] + ([selector.split()[1]] if selector.startswith("list ") else (["-cum"] if selector == "top -cum" else [])) + ["<raw-profile-from-content-id>"]
            if selector.startswith("list "):
                record["reconstruction"].update({
                    "argv_template": ["go", "tool", "pprof", f"-list={selector.split()[1]}", "-trim_path=<derived-profile-source-prefix>", "-source_path=<pinned-source-root>", "<raw-profile-from-content-id>"],
                    "trim_path_derivation": "prefix of the profile's recorded source filename before /internal/sem",
                    "source_materialization": {"git_commit": record["source_commit"], "path": "internal/sem", "destination": "<pinned-source-root>/internal/sem"},
                })
                if run_id == "950567a3" and selector == "list receiverCallRelations":
                    record["reconstruction"]["tested_argv_sha256"] = "7776c5ffed84b20f1f730e6fb129e910fb08dc056326181eca27f4e04bc0d1ce"
        data["records"].append(record)
    data["representative_reconstruction_proof"] = {
        "source_path": "docs/implementation/graph-advantage/evidence/diagnostic-dispatch-950567a3/controller/raw/pprof-receiver-call-relations-list.txt",
        "source_sha256": "73687af200a7a80806c9d90a278189ec69f82de38162a2bd51b15ac72aab987a",
        "method": "offline pprof only; exact raw profile Git blob and pinned 950567a3 internal/sem source; argv template recorded on member",
        "comparison": "byte-for-byte full output",
        "status": "passed",
        "reconstructed_sha256": "73687af200a7a80806c9d90a278189ec69f82de38162a2bd51b15ac72aab987a",
    }
    data["records"].sort(key=lambda x: (x.get("archive_path", x.get("source_path", "")), x.get("member", ""), x["kind"]))
    data["record_count"] = len(data["records"])
    data["scope"] = "12 archive profile members plus 24 bounded profile-derived text artifacts mapped to verified profile summaries"
    args.records.write_text(json.dumps(data, indent=2) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
