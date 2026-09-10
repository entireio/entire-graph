#!/usr/bin/env python3
"""Project meaningful structured archive members into compact public evidence."""
from __future__ import annotations
import collections, gzip, hashlib, io, json, pathlib, subprocess, tarfile

AUDITED_HEAD = "6c1ac1d188af711e7a0e7e361bfc3e4f139ee617"
SPECS = [{'archive': 'docs/implementation/graph-advantage/evidence/diagnostic-dispatch-1f20f694/results.tar.gz', 'member': 'output/observation.ndjson', 'sha256': '53d762c8c23b9d49ff182f766f74da217be20b575437708028ff34bbdef9460d', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/evidence/diagnostic-dispatch-1f20f694/results.tar.gz', 'member': 'output/diagnostics.json', 'sha256': '79be6d6d2c2fafa495e9306fe93e4f67b8486d4b5af37f2aea2ceefe69ae038c', 'kind': 'other structured result'}, {'archive': 'docs/implementation/graph-advantage/evidence/diagnostic-graph-bench-offline-fixtures-12574522/focused-normal-git-trace2.jsonl.gz', 'member': 'focused-normal-git-trace2.jsonl', 'sha256': '2eaf3a290c2f9bb88e69d6278d59dbf41328f4af503d899161d4b34bc7a7d11a', 'kind': 'other structured result'}, {'archive': 'docs/implementation/graph-advantage/evidence/diagnostic-graph-bench-offline-fixtures-12574522/focused-race-git-trace2.jsonl.gz', 'member': 'focused-race-git-trace2.jsonl', 'sha256': 'ef10ae429d23050d9491f7be4c1bc5143fc57f9c6dc9fabd51889d59a665ad0c', 'kind': 'other structured result'}, {'archive': 'docs/implementation/graph-advantage/evidence/diagnostic-graph-bench-offline-fixtures-12574522/normal-package.jsonl.gz', 'member': 'normal-package.jsonl', 'sha256': '0d798557ee2735df2910a088718e6cafba8bd90b7c3a6f4b8812db080cd13540', 'kind': 'other structured result'}, {'archive': 'docs/implementation/graph-advantage/evidence/diagnostic-graph-bench-offline-fixtures-12574522/race-package.jsonl.gz', 'member': 'race-package.jsonl', 'sha256': '666111b1e09e2c0439807f4443dddf79f58f0f6a3d47720452e209f65aa58d9a', 'kind': 'other structured result'}, {'archive': 'docs/implementation/graph-advantage/evidence/diagnostics-linux-12574522/results.tar.gz', 'member': 'compiler-advantage.json', 'sha256': '52c7b9e7a2c3418a8ddcdb319ea490375ff0918c35e3a9dc1a963d1b17f7047f', 'kind': 'compiler result'}, {'archive': 'docs/implementation/graph-advantage/evidence/diagnostics-linux-12574522/results.tar.gz', 'member': 'compiler-review.json', 'sha256': 'eb61411e342594269be34de2136f064394959e4a5e6ff4ec8ab085fa51b9b257', 'kind': 'compiler result'}, {'archive': 'docs/implementation/graph-advantage/evidence/diagnostics-linux-450bede9/results.tar.gz', 'member': 'compiler-advantage.json', 'sha256': 'b056a786090669570251dbe280af88a829a66020cb91cb9e488f3c394bb8df88', 'kind': 'compiler result'}, {'archive': 'docs/implementation/graph-advantage/evidence/diagnostics-linux-450bede9/results.tar.gz', 'member': 'compiler-review.json', 'sha256': '95ff8d855169e00a727652e605e22817373bed3138d5781d7b75848aeeca9dfa', 'kind': 'compiler result'}, {'archive': 'docs/implementation/graph-advantage/evidence/diagnostics-linux-90ac3e16/results.tar.gz', 'member': 'compiler-advantage.json', 'sha256': 'e9b0b4d5d09e3ac34aa0424fd04bbfcb7c54c55276d06ba278510868c4dd9732', 'kind': 'compiler result'}, {'archive': 'docs/implementation/graph-advantage/evidence/diagnostics-linux-90ac3e16/results.tar.gz', 'member': 'compiler-review.json', 'sha256': '402637e55380f7419918c472428357c28eafa067a947a5c127fbb6793a3cef58', 'kind': 'compiler result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/cold-profile-d793b2be/collected.tar.gz', 'member': 'observation.ndjson', 'sha256': '5d9fc06ddd1ee76d9e9cb9f15a9502909badd825bc7f6bbad4a8c5580ada2c58', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/paused-raw/worker-1.tar.gz', 'member': 'results/campaign.ndjson', 'sha256': '4e2902a780b45d3c7e7f4ede0a421072b7c761f9e1b730de9f9158be9b1a9696', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/paused-raw/worker-2.tar.gz', 'member': 'results/campaign.ndjson', 'sha256': 'a95f81aafd943e21608557eb5030be7238f0d97195b52b81369582b77503247c', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/paused-raw/worker-3.tar.gz', 'member': 'results/campaign.ndjson', 'sha256': '008de9d8b98abe19928864cacf24315ef98c76fbb68049423bea22846ba51a78', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-linux-05ad9842/p1-retained-diagnostic-result-retained-05ad9842-20260906-a17cadb5d8e837784febe1914dfd357dd4be8a0581ee74e7ca1e575a7e908baa.tar.gz', 'member': 'raw/off.json', 'sha256': '34e8da33fbe2ad73121d998f617b5eb68f92976f9eb677d55b3bf4fd73064f91', 'kind': 'other structured result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-linux-05ad9842/p1-retained-diagnostic-result-retained-05ad9842-20260906-a17cadb5d8e837784febe1914dfd357dd4be8a0581ee74e7ca1e575a7e908baa.tar.gz', 'member': 'raw/on.json', 'sha256': '51a47793eaa0d8aa95384170df99216608e05951cb04e67ebb8bb83f58dd95e2', 'kind': 'other structured result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-linux-05ad9842/p1-retained-diagnostic-result-retained-05ad9842-20260906-a17cadb5d8e837784febe1914dfd357dd4be8a0581ee74e7ca1e575a7e908baa.tar.gz', 'member': 'raw/digests.json', 'sha256': 'c387d74b80341cb7b734e80b4e875f3925bcf5b909872d6ea2d4e21e4c352b84', 'kind': 'other structured result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-query-correctness-1c0b8e24/results-full-r2/results.tar.gz', 'member': 'retained-query-correctness-1c0b8e24/output-full-r2/observation-full-off.ndjson', 'sha256': '9904602af4e2abb935456a1b315f2a22ffe2e9707d2384b889c7ecacd57168e6', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-query-correctness-1c0b8e24/results-full-r2/results.tar.gz', 'member': 'retained-query-correctness-1c0b8e24/output-full-r2/observation-full-on.ndjson', 'sha256': '061f9cf7cbfcca32a79d42672e92ea8465c5587035840d639c658f7f3dce5ce7', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-query-correctness-1c0b8e24/results/results.tar.gz', 'member': 'retained-query-correctness-1c0b8e24/output/observation-fast-on.ndjson', 'sha256': '95e590a51f68c7d27d4e0f49a18342c6418ad16dc5893aa4b6721f17c4bdb249', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-query-correctness-1c0b8e24/results/results.tar.gz', 'member': 'retained-query-correctness-1c0b8e24/output/observation-full-off.ndjson', 'sha256': '8f6228c58a14a1018e8fe9d9d1b5ebb79bdf0ecf47fa3ce16cc06c0dcdf6a313', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-query-correctness-1c0b8e24/results/results.tar.gz', 'member': 'retained-query-correctness-1c0b8e24/output/observation-syntax-only-off.ndjson', 'sha256': '5de7931be61c10a90ca4a3c93dff9ce4f33a71ad7102a670b532e37f57e7878d', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-query-correctness-1c0b8e24/results/results.tar.gz', 'member': 'retained-query-correctness-1c0b8e24/output/observation-fast-off.ndjson', 'sha256': '45d9875e990e69ac461a7fda6766e6cf4cf063b5f87213c8a6fe52aa3642ee87', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-query-correctness-1c0b8e24/results/results.tar.gz', 'member': 'retained-query-correctness-1c0b8e24/output/observation-syntax-only-on.ndjson', 'sha256': 'b46f146bd8e312b753f27abf50aa16d7429adeda162ac67695d52a223e4d507e', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-query-correctness-1c0b8e24/results/runner-inputs.tar.gz', 'member': 'run_remote.py', 'sha256': 'eba13e825a4a28ae38a8c81af9116648693346c7f85b6f2e395b01db0ec3566d', 'kind': 'other structured result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-snapshot-05ad9842-retry/p1-snapshot-05ad9842-setup-retry.tar.gz', 'member': './observation-off.ndjson', 'sha256': '38e35f6b2b932b38b3bc8d838e86031ccae392bbf7b866266e014f10642f9057', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-snapshot-05ad9842-retry/p1-snapshot-05ad9842-setup-retry.tar.gz', 'member': './observation-on.ndjson', 'sha256': 'eea0eaa92a10209de3f3a442af4eeff34cc0bdae0d246aa4d9094d7888403d6d', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-snapshot-0c9e80f5/results.tar.gz', 'member': 'snapshot-diagnostic/observation-off.ndjson', 'sha256': 'a62263c1e1af5da40733d18bf57f5375186a8be2d65b86798df003f9d853a18b', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-snapshot-0c9e80f5/results.tar.gz', 'member': 'snapshot-diagnostic/observation-on.ndjson', 'sha256': '4e7c736ec4f6c8087c235744a605f376f77cab7d7fe7fd2bdde9eff3a9592544', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-snapshot-1c0b8e24-r2/results.tar.gz', 'member': 'snapshot-resource-diagnostic-r2/observation-off.ndjson', 'sha256': '27f7007c81f340993de6bdbe93256f17af89150d2922cce2e18782ab7f108478', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-snapshot-1c0b8e24-r2/results.tar.gz', 'member': 'snapshot-resource-diagnostic-r2/observation-on.ndjson', 'sha256': '2134ac71dcb3389de0f8513a15d542c1f361af6c4e99bd0c29393a88b225fdc5', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-snapshot-6cf92c9c/results.tar.gz', 'member': 'snapshot-resource-diagnostic-r2/observation-off.ndjson', 'sha256': '2565eb0da37af13c4a1c3278222091b9b2d22ce858271c09c084494a386a7784', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-snapshot-6cf92c9c/results.tar.gz', 'member': 'snapshot-resource-diagnostic-r2/observation-on.ndjson', 'sha256': '4a4211502d27bffe533b16a5cec32f1d95b8dab151e5dd3e3e2fd9a14b27190f', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-snapshot-d793b2be/results.tar.gz', 'member': 'snapshot-diagnostic/observation-off.ndjson', 'sha256': '52e9bb7b0c5efe9c19a5bf295dcdf69a35d89a1d425fd8fafd01a8c01b19b92d', 'kind': 'query/observation result'}, {'archive': 'docs/implementation/graph-advantage/p1-corpus-20260905/retained-snapshot-d793b2be/results.tar.gz', 'member': 'snapshot-diagnostic/observation-on.ndjson', 'sha256': '4ee07141e946093be542cc4e9c1aa1547f3acb7638a0040cdd2816c2fe8c1dee', 'kind': 'query/observation result'}]
PATH_KEYS = {"cache_path", "manifest_path", "observation_path", "repository_path", "repo_root"}
TOKENS = {"cache_path": "$CACHE_PATH", "manifest_path": "$MANIFEST_PATH", "observation_path": "$OBSERVATION_PATH", "repository_path": "$WORKLOAD_ROOT"}

def sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()

def canonical_sha(value) -> str:
    return sha(json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False).encode())

def archive_bytes(repo: pathlib.Path, path: str) -> bytes:
    live = repo / path
    if live.is_file():
        return live.read_bytes()
    return subprocess.check_output(["git", "show", f"{AUDITED_HEAD}:{path}"], cwd=repo)

def member_bytes(repo: pathlib.Path, spec: dict) -> bytes:
    raw = archive_bytes(repo, spec["archive"])
    if spec["archive"].endswith(".jsonl.gz"):
        return gzip.decompress(raw)
    with tarfile.open(fileobj=io.BytesIO(raw), mode="r:*") as bundle:
        item = bundle.extractfile(spec["member"])
        if item is None:
            raise ValueError(f"missing archive member: {spec['member']}")
        return item.read()

def sanitize(value, pointer="", redactions=None):
    if redactions is None:
        redactions = []
    if isinstance(value, dict):
        out = {}
        for key, child in value.items():
            child_pointer = f"{pointer}/{key}"
            if key in PATH_KEYS and isinstance(child, str) and child.startswith(("/opt/", "/tmp/", "/Users/")):
                replacement = "$FIXTURE_ROOT" if key == "repo_root" and child.startswith("/tmp/") else TOKENS.get(key, "$WORKLOAD_ROOT")
                out[key] = replacement
                redactions.append({"path": child_pointer, "reason": "machine-local absolute path normalized", "replacement": replacement})
            else:
                out[key] = sanitize(child, child_pointer, redactions)
        return out
    if isinstance(value, list):
        return [sanitize(child, f"{pointer}/{index}", redactions) for index, child in enumerate(value)]
    return value

def decode_rows(payload: bytes, member: str):
    if member.endswith((".ndjson", ".jsonl")):
        return [json.loads(line) for line in payload.splitlines() if line.strip()], "ordered_records"
    return json.loads(payload), "structured_object"

def aggregate_trace(rows: list[dict], kind: str) -> dict:
    if kind == "git_trace":
        return {"row_count": len(rows), "event_counts": dict(sorted(collections.Counter(str(x.get("event")) for x in rows).items())), "executables": sorted({pathlib.PurePosixPath(str(x.get("exe", ""))).name for x in rows if x.get("exe")}), "source_files": sorted({pathlib.PurePosixPath(str(x.get("file", ""))).name for x in rows if x.get("file")})}
    return {"row_count": len(rows), "action_counts": dict(sorted(collections.Counter(str(x.get("Action")) for x in rows).items())), "package_counts": dict(sorted(collections.Counter(str(x.get("Package")) for x in rows if x.get("Package")).items())), "first_time": rows[0].get("Time") if rows else None, "last_time": rows[-1].get("Time") if rows else None}

def build(repo: pathlib.Path) -> dict:
    records=[]
    retained_dir = repo / "docs/implementation/graph-advantage/cleanup/archive-retained-controls"
    retained_dir.mkdir(exist_ok=True)
    for spec in SPECS:
        payload=member_bytes(repo,spec)
        if sha(payload) != spec["sha256"]:
            raise ValueError(f"source hash mismatch: {spec['archive']}::{spec['member']}")
        common={"archive_path":spec["archive"],"member":spec["member"],"source_sha256":spec["sha256"],"source_bytes":len(payload),"kind":spec["kind"]}
        if spec["member"] == "run_remote.py":
            retained_rel=f"archive-retained-controls/{spec['sha256']}-run_remote.py"
            retained=(repo / "docs/implementation/graph-advantage/cleanup" / retained_rel)
            retained.write_bytes(payload)
            common.update({"preservation":"exact_source","retained_path":f"docs/implementation/graph-advantage/cleanup/{retained_rel}","retained_sha256":sha(payload)})
        else:
            value, shape=decode_rows(payload,spec["member"])
            if spec["archive"].endswith("git-trace2.jsonl.gz"):
                facts=aggregate_trace(value,"git_trace"); common.update({"preservation":"semantic_aggregate","facts":facts,"projection_sha256":canonical_sha(facts),"canonical_run":"diagnostic-graph-bench-offline-fixtures-12574522"})
            elif spec["archive"].endswith("package.jsonl.gz"):
                facts=aggregate_trace(value,"package"); common.update({"preservation":"semantic_aggregate","facts":facts,"projection_sha256":canonical_sha(facts),"canonical_run":"diagnostic-graph-bench-offline-fixtures-12574522"})
            else:
                redactions=[]; projected=sanitize(value,redactions=redactions)
                common.update({"preservation":"full_sanitized_"+shape,"record_count":len(projected) if isinstance(projected,list) else 1,"data":projected,"projection_sha256":canonical_sha(projected),"redactions":redactions})
        records.append(common)
    return {"schema":"graph-advantage-structured-archive-observations-v1","scope":"37 meaningful structured members; case order and all non-local fields retained; Git/package traces reduced to execution aggregates already source-bound by the canonical diagnostic run","audited_head":AUDITED_HEAD,"record_count":len(records),"source_member_bytes":sum(x["source_bytes"] for x in records),"records":records}

def main():
    repo=pathlib.Path(__file__).resolve().parents[4]
    output=pathlib.Path(__file__).with_name("structured-archive-observations.json")
    output.write_text(json.dumps(build(repo),sort_keys=True,separators=(",",":"),ensure_ascii=False,allow_nan=False)+"\n")
if __name__ == "__main__": main()
