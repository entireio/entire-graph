#!/usr/bin/env python3
"""Verify archive provenance records against retained files (no extraction)."""
from __future__ import annotations
import argparse, gzip, hashlib, json, stat, tarfile
from pathlib import Path

def verify(repo: Path, provenance: Path, offline: bool = False, external_archive_root: Path | None = None) -> list[str]:
    data = json.loads(provenance.read_text())
    attestation_path = provenance.with_name("precleanup-parity-attestation.json")
    attestation = json.loads(attestation_path.read_text()) if attestation_path.is_file() else None
    attested = {a["archive_path"]: a for a in attestation["archives"]["records"]} if attestation else {}
    removal_map_path = data.get("removal_map_path")
    removal_candidates = []
    if removal_map_path:
        rp = repo / removal_map_path
        if not rp.is_file():
            return [f"missing removal map: {removal_map_path}"]
        removal_candidates = json.loads(rp.read_text()).get("candidates", [])
    errors: list[str] = []

    def live_file(path: str):
        fp = repo / path
        if not fp.is_file():
            return None
        return fp.read_bytes(), stat.S_IMODE(fp.stat().st_mode)

    def evidence_record_matches(chain, archive_path, member_name, member_sha):
        evidence_path = chain.get("evidence_record_path")
        if not evidence_path:
            return False
        candidate = live_file(evidence_path)
        if candidate is None:
            return False
        try:
            records = json.loads(candidate[0]).get("records", [])
        except (UnicodeDecodeError, json.JSONDecodeError):
            return False
        return any(
            r.get("archive_path") == archive_path
            and r.get("member") == member_name
            and r.get("source_sha256", r.get("source_member_sha256")) == member_sha
            for r in records
        )

    def removal_map_matches(chain, member_sha):
        source_path = chain.get("source_map_path")
        if not source_path:
            return False
        for candidate in removal_candidates:
            if candidate.get("disposition") != "applied" or candidate.get("path") != source_path or candidate.get("source_sha256") != member_sha:
                continue
            replacements = [candidate.get("replacement", {})] + candidate.get("additional_replacements", [])
            if any(r.get("path") == chain.get("path") and r.get("retained_sha256") == chain.get("declared_retained_sha256") for r in replacements):
                return True
        return False
    for archive in data["archives"]:
        ap = (external_archive_root / archive["archive_path"]) if external_archive_root else (repo / archive["archive_path"])
        archive_bytes = ap.read_bytes() if ap.is_file() else None
        source_attestation = attested.get(archive["archive_path"])
        expected_attestation = {"archive_path":archive["archive_path"],"archive_sha256":archive["archive_sha256"],"member_count":archive["member_count"],"members":[{"member":m["member"],"member_mode":m["member_mode"],"sha256":m["sha256"]} for m in archive["members"]]}
        if external_archive_root is not None and archive_bytes is None:
            errors.append(f"external archive missing: {archive['archive_path']}"); continue
        if archive_bytes is None and source_attestation != expected_attestation:
            errors.append(f"archive hash: {archive['archive_path']}")
            continue
        if archive_bytes is not None and hashlib.sha256(archive_bytes).hexdigest() != archive["archive_sha256"]:
            errors.append(f"archive hash: {archive['archive_path']}"); continue
        import io
        if archive_bytes is None:
            class AttestedMember:
                def __init__(self, record): self.name, self.mode = record["member"], record["member_mode"]
                def isfile(self): return True
            members = [AttestedMember(record) for record in source_attestation["members"]]
            payloads = {}
        elif archive.get("archive_format") == "gzip-single-member":
            class GzipMember:
                def __init__(self, name, mode): self.name, self.mode = name, mode
                def isfile(self): return True
            expected_member = archive["members"][0]
            members = [GzipMember(expected_member["member"], expected_member["member_mode"])]
            payloads = {expected_member["member"]: gzip.decompress(archive_bytes)}
        else:
            with tarfile.open(fileobj=io.BytesIO(archive_bytes), mode="r:gz") as tf:
                members = tf.getmembers()
                payloads = {m.name: tf.extractfile(m).read() for m in members if m.isfile()}
        by_name = {m.name: m for m in members}
        if archive_bytes is not None and len(by_name) != archive["member_count"]:
            errors.append(f"member count: {archive['archive_path']}")
        for expected in archive["members"]:
            member = by_name.get(expected["member"])
            if member is None or not member.isfile():
                errors.append(f"missing member: {archive['archive_path']}:{expected['member']}")
                continue
            payload = payloads.get(member.name)
            if payload is not None and hashlib.sha256(payload).hexdigest() != expected["sha256"]:
                errors.append(f"member hash: {archive['archive_path']}:{expected['member']}")
            if archive.get("archive_format") != "gzip-single-member" and stat.S_IMODE(member.mode) != expected["member_mode"]:
                errors.append(f"member mode: {archive['archive_path']}:{expected['member']}")
            def replacement_matches(q):
                candidate = live_file(q)
                return candidate is not None and hashlib.sha256(candidate[0]).hexdigest() == expected["sha256"] and candidate[1] == expected["replacement_modes"].get(q)
            direct = any(replacement_matches(q) for q in expected["replacement_paths"])
            chains = False
            for chain in expected.get("canonical_replacement_chains", []):
                candidate = live_file(chain.get("path", ""))
                declared = chain.get("effective_retained_sha256", chain.get("declared_retained_sha256", chain.get("retained_sha256")))
                live_matches = candidate is not None and hashlib.sha256(candidate[0]).hexdigest() == declared and candidate[1] == chain.get("current_mode")
                provenance_matches = removal_map_matches(chain, expected["sha256"]) or evidence_record_matches(chain, archive["archive_path"], expected["member"], expected["sha256"])
                if live_matches and provenance_matches:
                    chains = True
                    break
            if not direct and not chains:
                errors.append(f"replacement: {archive['archive_path']}:{expected['member']}")
    return errors

def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--provenance", type=Path, required=True)
    parser.add_argument("--offline", action="store_true", help="deprecated compatibility flag; never reads historical Git objects")
    parser.add_argument("--external-archive-root", type=Path, help="tree containing the original archives; missing input fails closed")
    args = parser.parse_args()
    errors = verify(args.repo.resolve(), args.provenance.resolve(), args.offline, args.external_archive_root.resolve() if args.external_archive_root else None)
    if errors:
        print("\n".join(errors))
        return 1
    print("archive provenance verified")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
