#!/usr/bin/env python3
"""Compare file-level retrieval on pinned local corpora. Never fetches or edits a corpus."""
import argparse
import concurrent.futures
import hashlib
import json
import os
import re
import signal
import tarfile
from pathlib import Path
import subprocess
import tempfile
import time


def run(args, timeout=180):
    if os.name != "posix":
        raise OSError("this bounded benchmark runner currently requires POSIX process groups")
    process = subprocess.Popen(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               start_new_session=True,
                               env=dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull, GOMAXPROCS="4"))
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except BaseException:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=2)
        process.stdout.close()
        process.stderr.close()
        raise
    if process.returncode:
        raise RuntimeError(f"command failed: {args!r}\n{stderr}")
    return stdout



def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", required=True)
    parser.add_argument("--candidate", required=True)
    parser.add_argument("--manifest", required=True)
    parser.add_argument("--repo", action="append", default=[], metavar="NAME=PATH")
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    repos = dict(value.split("=", 1) for value in args.repo)
    manifest = json.loads(Path(args.manifest).read_text())
    # Validate every pin and label before running either binary; result-dependent labels are invalid.
    for name, commit in manifest["commits"].items():
        if not re.fullmatch(r"[A-Za-z0-9_-]+", name):
            raise ValueError("corpus names must be simple identifiers")
        repo = repos[name]
        if run(["git", "-C", repo, "rev-parse", "HEAD"]).strip() != commit:
            raise ValueError(f"{name}: checkout does not match pinned commit {commit}")
        for case in manifest["cases"]:
            if case["repo"] == name:
                for gold in case["gold"]:
                    run(["git", "-C", repo, "cat-file", "-e", f"{commit}:{gold}"])
    binaries = {label: str(Path(path).resolve(strict=True))
                for label, path in [("baseline", args.baseline), ("candidate", args.candidate)]}
    report = {"metric": "file-level first relevant rank within top 10, including normal result sections",
              "limits": "Small developer-authored local corpus cohort; not an unbiased general relevance benchmark. Scoring is isolated on filesystem exports of pinned Git trees, with working-tree mode, a fixed 96-file semantic parse limit and GOMAXPROCS=4. Git preselection is not part of this metric; a separate baseline Git probe timed out. Wall times are not a speed benchmark.",
              "manifest_sha256": hashlib.sha256(Path(args.manifest).read_bytes()).hexdigest(),
              "commits": manifest["commits"],
              "binary_sha256": {label: hashlib.sha256(Path(path).read_bytes()).hexdigest()
                                for label, path in binaries.items()}, "results": []}
    with tempfile.TemporaryDirectory(prefix="graph-ranking-comparison-") as caches:
        snapshots = {}
        for name, commit in manifest["commits"].items():
            archive = Path(caches) / (name + ".tar")
            run(["git", "-C", repos[name], "archive", "--format=tar", "--output=" + str(archive), commit])
            target = Path(caches) / "corpora" / name
            target.mkdir(parents=True)
            with tarfile.open(archive) as source:
                source.extractall(target, filter="data")
            archive.unlink()
            snapshots[name] = str(target)
            for case in manifest["cases"]:
                if case["repo"] == name:
                    for gold in case["gold"]:
                        if not (target / gold).is_file():
                            raise ValueError(f"{name}: gold file omitted by archive: {gold}")
        for case in manifest["cases"]:
            for profile in manifest["profiles"]:
                row = dict(case, profile=profile)
                def evaluate(item):
                    label, binary = item
                    before = time.monotonic()
                    data = json.loads(run([binary, "query", "--repo", snapshots[case["repo"]],
                        "--query", case["query"], "--profile", profile, "--worktree", "--no-cache",
                        "--max-indexed-files", "96", "--format", "json", "--top-k", "10", "--max-context-bytes", "24576"]))
                    paths = [r["file_path"] for r in data["results"]]
                    rank = next((i + 1 for i, path in enumerate(paths[:10]) if path in case["gold"]), None)
                    return label, {"rank":rank, "paths":paths, "seconds":round(time.monotonic()-before, 3),
                        "backend":data.get("stats", {}).get("preselection_backend"),
                        "files_indexed":data.get("stats", {}).get("files_indexed"),
                        "completeness":data.get("completeness"), "partial_failures":data.get("partial_failures", [])}
                with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
                    row.update(dict(pool.map(evaluate, binaries.items())))
                report["results"].append(row)
                print(json.dumps({"repo":case["repo"], "query":case["query"], "profile":profile,
                                  "baseline":row["baseline"]["rank"], "candidate":row["candidate"]["rank"]}), flush=True)
                Path(args.output).write_text(json.dumps(report, indent=2) + "\n")
    rows = report["results"]
    report["summary"] = {}
    for label in binaries:
        ranks = [row[label]["rank"] for row in rows]
        report["summary"][label] = {"cases":len(ranks), "mrr_at_10":sum(1/r if r else 0 for r in ranks)/len(ranks),
            **{f"recall_at_{k}":sum(r is not None and r <= k for r in ranks)/len(ranks) for k in [1,5,10]}}
    report["regressions"] = [dict(repo=r["repo"], query=r["query"], profile=r["profile"],
         baseline=r["baseline"]["rank"], candidate=r["candidate"]["rank"]) for r in rows
         if (r["candidate"]["rank"] or 11) > (r["baseline"]["rank"] or 11)]
    Path(args.output).write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report["summary"]), flush=True)


if __name__ == "__main__":
    main()
