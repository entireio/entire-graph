#!/usr/bin/env python3
"""Build parity attestation from an explicit external pre-cleanup archive tree."""
from __future__ import annotations
import argparse,gzip,hashlib,io,json,re,sys,tarfile
from pathlib import Path
HERE=Path(__file__).resolve().parent; REPO=HERE.parents[3]; sys.path.insert(0,str(HERE))
import structured_archive_observations as structured
from audit_archive_provenance import verify as verify_archives
def digest(b): return hashlib.sha256(b).hexdigest()
def file_digest(p): return digest(p.read_bytes())
def canonical(v): return digest(json.dumps(v,sort_keys=True,separators=(",",":"),ensure_ascii=False,allow_nan=False).encode())
def sanitize(v,key=""):
 if isinstance(v,dict): return {k:sanitize(x,k) for k,x in v.items()}
 if isinstance(v,list): return [sanitize(x,key) for x in v]
 if isinstance(v,str):
  v=re.sub("/"+r"Users/[^/\s]+(?:/[^\s]*)?","<redacted-local-path>",v); v=re.sub(r"/tmp/[^\s]+","<redacted-temp-path>",v); v=re.sub(r"/opt/[^\s]+","<redacted-infra-path>",v); v=re.sub(r"https?://[^\s\"']+","<redacted-url>",v)
  return "<redacted-sensitive-value>" if re.search(r"(password|secret|token|credential|subscription|resource.?id|azure.?id)",key,re.I) else v
 return v
class External:
 def __init__(self,root,provenance): self.root=root; self.archives={a["archive_path"]:a for a in provenance["archives"]}
 def member(self,path,name,expected):
  source=self.archives[path]; raw=(self.root/path).read_bytes()
  if digest(raw)!=source["archive_sha256"]: raise ValueError(f"archive hash: {path}")
  if source.get("archive_format")=="gzip-single-member": payload=gzip.decompress(raw)
  else:
   with tarfile.open(fileobj=io.BytesIO(raw),mode="r:*") as bundle:
    item=bundle.extractfile(name)
    if item is None: raise ValueError(f"missing member: {path}:{name}")
    payload=item.read()
  if digest(payload)!=expected: raise ValueError(f"member hash: {path}:{name}")
  return payload
def validate_query_projection(decoded,public_rows):
 if [x["case_id"] for x in public_rows]!=list(decoded["responses"]): raise ValueError("query order")
 for row,response in zip(public_rows,decoded["responses"].values()):
  if (row["response"],row["sources"],row["failed"],row["fixture_origin"])!=(sanitize(response),sanitize(decoded["sources"]),decoded.get("failed",False),decoded["fixture_origin"]): raise ValueError(f"query projection: {row['case_id']}")
def build(external_root,initialize_combination=False):
 provenance=json.loads((HERE/"archive-provenance.json").read_text()); errors=verify_archives(REPO,HERE/"archive-provenance.json",external_archive_root=external_root)
 if errors: raise ValueError("\n".join(errors))
 source=External(external_root,provenance); rebuilt=structured.build(REPO,lambda s:source.member(s["archive"],s["member"],s["sha256"])); public=json.loads((HERE/"structured-archive-observations.json").read_text())
 if rebuilt!=public: raise ValueError("structured projection parity")
 mappings=json.loads((HERE/"correctness-archive-mappings.json").read_text()); rows=[json.loads(x) for x in (HERE/"correctness-query-cases.ndjson").read_text().splitlines()]; byrun={}
 for row in rows: byrun.setdefault(row["run_id"],[]).append(row)
 combination_records=[]
 for record in mappings["records"]:
  decoded=json.loads(source.member(record["archive_path"],record["member"],record["source_member_sha256"])); run=record["archive_path"].split("correctness-")[1].split("/")[0]
  if record["member"]=="queries.json":
   public_rows=byrun[run]
   validate_query_projection(decoded,public_rows)
  else:
   if canonical(decoded[record["source_field"]])!=record["source_field_sha256"]: raise ValueError(f"combination projection: {run}")
   projected={"run_id":run,"archive_path":record["archive_path"],"member":record["member"],"source_member_sha256":record["source_member_sha256"],"failed":decoded.get("failed",False),"fixture_origin":decoded["fixture_origin"],"response_order":list(decoded["responses"]),"responses":sanitize(decoded["responses"]),"sources_by_stage":sanitize(decoded["sources_by_stage"])}
   projected["projection_sha256"]=canonical({k:projected[k] for k in ("failed","fixture_origin","response_order","responses","sources_by_stage")}); combination_records.append(projected)
 combination_document={"schema":"graph-advantage-correctness-combination-records-v1","record_count":len(combination_records),"response_count":sum(len(x["response_order"]) for x in combination_records),"records":combination_records}; combination_path=HERE/"correctness-combination-records.json"
 if initialize_combination: combination_path.write_text(json.dumps(combination_document,sort_keys=True,separators=(",",":"))+"\n")
 elif not combination_path.is_file() or json.loads(combination_path.read_text())!=combination_document: raise ValueError("combination public projection parity")
 return {"schema":"graph-advantage-precleanup-parity-attestation-v1","source_commit":provenance["audited_head"],"prepared_at_head":"e440b626bea8f4e1529ada3934a088cd2e226e43","archives":{"count":len(provenance["archives"]),"member_count":sum(len(a["members"]) for a in provenance["archives"]),"records":[{"archive_path":a["archive_path"],"archive_sha256":a["archive_sha256"],"member_count":a["member_count"],"members":[{"member":m["member"],"member_mode":m["member_mode"],"sha256":m["sha256"]} for m in a["members"]]} for a in provenance["archives"]]},"structured":{"member_count":rebuilt["record_count"],"source_member_bytes":rebuilt["source_member_bytes"],"public_path":"docs/implementation/graph-advantage/cleanup/structured-archive-observations.json","public_sha256":file_digest(HERE/"structured-archive-observations.json"),"records":[{"archive_path":r["archive_path"],"member":r["member"],"source_sha256":r["source_sha256"],"preservation":r["preservation"],"record_count":r.get("record_count"),"projection_sha256":r.get("projection_sha256")} for r in rebuilt["records"]]},"correctness_queries":{"archive_count":5,"case_count":len(rows),"public_path":"docs/implementation/graph-advantage/cleanup/correctness-query-cases.ndjson","public_sha256":file_digest(HERE/"correctness-query-cases.ndjson"),"ordered_case_ids":[x["case_id"] for x in rows]},"correctness_combinations":{"archive_count":5,"response_count":25,"public_path":"docs/implementation/graph-advantage/cleanup/correctness-combination-records.json","public_sha256":file_digest(combination_path),"records":[{"run_id":x["run_id"],"source_member_sha256":x["source_member_sha256"],"response_order":x["response_order"],"projection_sha256":x["projection_sha256"]} for x in combination_records]},"derivation":"Decoded from explicit external pre-cleanup archives; archive/member identities and full normalized query and combination values, failures, sources, and order matched."}
def main():
 p=argparse.ArgumentParser(); p.add_argument("--external-archive-root",type=Path,required=True); p.add_argument("--output",type=Path,default=HERE/"precleanup-parity-attestation.json"); p.add_argument("--initialize-combination-projection",action="store_true"); a=p.parse_args(); a.output.write_text(json.dumps(build(a.external_archive_root.resolve(),a.initialize_combination_projection),sort_keys=True,separators=(",",":"))+"\n")
if __name__=="__main__": main()
