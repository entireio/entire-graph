#!/usr/bin/env python3
"""Validate the one-time original-to-public parity attestation and live outputs."""
from __future__ import annotations
import hashlib, json
from pathlib import Path

def sha(path: Path) -> str: return hashlib.sha256(path.read_bytes()).hexdigest()
def canonical(value: object) -> str: return hashlib.sha256(json.dumps(value,sort_keys=True,separators=(",",":"),ensure_ascii=False,allow_nan=False).encode()).hexdigest()

def verify(repo: Path, attestation_path: Path) -> list[str]:
    if not attestation_path.is_file(): return ["missing precleanup parity attestation"]
    try: data=json.loads(attestation_path.read_text())
    except (OSError,json.JSONDecodeError): return ["invalid precleanup parity attestation"]
    errors=[]
    if data.get("schema") != "graph-advantage-precleanup-parity-attestation-v1": errors.append("attestation schema")
    archives=data.get("archives",{})
    if archives.get("count") != 65 or archives.get("member_count") != 840 or len(archives.get("records",[])) != 65: errors.append("archive attestation coverage")
    for section in (data.get("structured",{}),data.get("correctness_queries",{}),data.get("platform_observations",{}),data.get("vm_statusline_observations",{})):
        path=repo/section.get("public_path","")
        if not path.is_file(): errors.append(f"missing attested public output: {section.get('public_path')}")
        elif sha(path) != section.get("public_sha256"): errors.append(f"attested public output hash: {section.get('public_path')}")
    if errors: return errors
    structured=json.loads((repo/data["structured"]["public_path"]).read_text())
    actual={(r["archive_path"],r["member"]):(r["source_sha256"],r["preservation"],r.get("record_count"),r.get("projection_sha256")) for r in structured["records"]}
    expected={(r["archive_path"],r["member"]):(r["source_sha256"],r["preservation"],r.get("record_count"),r.get("projection_sha256")) for r in data["structured"]["records"]}
    if actual != expected or len(actual) != 37: errors.append("structured source-to-projection mapping")
    rows=[json.loads(x) for x in (repo/data["correctness_queries"]["public_path"]).read_text().splitlines()]
    if [x["case_id"] for x in rows] != data["correctness_queries"]["ordered_case_ids"] or len(rows) != 25: errors.append("correctness query order")
    combos=data.get("correctness_combinations",{})
    combo_path=repo/combos.get("public_path","")
    if combos.get("archive_count") != 5 or combos.get("response_count") != 25 or len(combos.get("records",[])) != 5: errors.append("correctness combination coverage")
    if not combo_path.is_file() or sha(combo_path)!=combos.get("public_sha256"): errors.append("correctness combination public output")
    else:
        document=json.loads(combo_path.read_text()); byrun={x["run_id"]:x for x in document["records"]}
        for record in combos.get("records",[]):
            actual=byrun.get(record["run_id"])
            if actual is None or actual["response_order"]!=record["response_order"] or actual["projection_sha256"]!=record["projection_sha256"]: errors.append(f"correctness combination projection: {record['run_id']}")
    for name,count in (("platform_observations",86),("vm_statusline_observations",13)):
        section=data.get(name,{})
        try: document=json.loads((repo/section["public_path"]).read_text())
        except (KeyError,OSError,json.JSONDecodeError):
            continue
        records=section.get("records",[])
        actual=[(r["source_path"],r["source_sha256"],r["source_git_blob"],r["source_bytes"],r["kind"],canonical(r["facts"])) for r in document.get("observations",[])]
        expected=[(r.get("source_path"),r.get("source_sha256"),r.get("source_git_blob"),r.get("source_bytes"),r.get("kind"),r.get("projection_sha256")) for r in records]
        if document.get("source_revision")!=section.get("source_revision") or document.get("observation_count")!=count or section.get("observation_count")!=count or document.get("source_bytes")!=section.get("source_bytes") or actual!=expected:
            errors.append(f"{name.replace('_',' ')} source-to-projection mapping")
    return errors
