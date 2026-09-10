#!/usr/bin/env python3
"""Validate sanitized operational archive observations without reading raw archives."""
import argparse, json, re
from pathlib import Path

FORBIDDEN = re.compile(r"(?:https?://|/(?:Users|home|tmp|opt|var)/|(?:password|secret|token|credential)\s*[:=]\s*[^<])", re.I)

def validate(path: Path) -> list[str]:
    data = json.loads(path.read_text()); errors=[]
    for i, record in enumerate(data.get("records", [])):
        if not re.fullmatch(r"[0-9a-f]{64}", record.get("source_sha256", "")): errors.append(f"record {i}: source hash")
        if FORBIDDEN.search(json.dumps(record, sort_keys=True)): errors.append(f"record {i}: unsanitized value")
        if record.get("kind") == "generated_cache":
            replacement=record.get("replacement", {}); path=replacement.get("authored_control_path", "")
            if not path.endswith("batch_budget.py") or not re.fullmatch(r"[0-9a-f]{64}", replacement.get("authored_control_sha256", "")): errors.append(f"record {i}: generated cache replacement")
    return errors

def main():
    p=argparse.ArgumentParser(); p.add_argument("path",type=Path); a=p.parse_args(); errors=validate(a.path)
    if errors: print("\n".join(errors)); return 1
    print("operational observations verified"); return 0
if __name__ == "__main__": raise SystemExit(main())
