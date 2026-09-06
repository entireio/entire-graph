#!/usr/bin/env python3
"""ATC telemetry — fleet contention memory (Databricks Delta, local fallback).

Why a data platform at all: a local run judges *this pair, right now*. Only a
shared store can answer "does this area of the codebase keep eating parallel
sessions?" across developers, machines and repos — and feed that back as a
warning BEFORE any overlap exists. That cross-fleet prior is the capability
Databricks provides and a local CLI structurally cannot.

Backends
  databricks  Delta tables via databricks-sql-connector (SQL warehouse + PAT).
              Config from env: DATABRICKS_SERVER_HOSTNAME, DATABRICKS_HTTP_PATH,
              DATABRICKS_TOKEN, optional ATC_DB_SCHEMA (default atc).
  local       SQLite at ~/.atc/telemetry.db — single-developer dev/offline mode.
              Same schema and queries, so the logic is identical and testable.

The backend in use is ALWAYS reported to the caller: a local prior must never
be presented as fleet evidence.

Usage:
  telemetry.py init                       create tables in the active backend
  telemetry.py record <verdict.json>      record one ATC run
  telemetry.py priors --paths a.py,b.py   hotspot priors for those paths
  telemetry.py hotspots [--limit 10]      contention leaderboard
"""

import argparse
import json
import os
import sqlite3
import sys
import uuid
from datetime import datetime, timezone

SCHEMA = os.environ.get("ATC_DB_SCHEMA", "atc")

DDL_RUNS = """CREATE TABLE IF NOT EXISTS {p}atc_runs (
  run_id STRING, ts STRING, repo STRING, ref_a STRING, ref_b STRING,
  merge_base STRING, verdict STRING, reds INT, advisories INT)"""
DDL_FINDINGS = """CREATE TABLE IF NOT EXISTS {p}atc_findings (
  run_id STRING, ts STRING, repo STRING, class STRING, entity STRING,
  path STRING, changed_by STRING, dependents INT, is_red INT)"""


class LocalBackend:
    name = "local"
    evidence_scope = "this machine only"

    def __init__(self):
        # ATC_LOCAL_DB lets tests/CI point at an isolated store without
        # touching HOME (which would break `entire` plugin discovery).
        path = os.environ.get("ATC_LOCAL_DB")
        if not path:
            d = os.path.expanduser("~/.atc")
            os.makedirs(d, exist_ok=True)
            path = os.path.join(d, "telemetry.db")
        else:
            os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
        self.path = path
        self.conn = sqlite3.connect(path)

    def execute(self, sql, params=()):
        # SQLite accepts arbitrary type names (STRING/INT) — no rewriting needed.
        cur = self.conn.cursor()
        cur.execute(sql, params)
        self.conn.commit()
        return cur.fetchall()

    def prefix(self):
        return ""

    def close(self):
        self.conn.close()


class DatabricksBackend:
    name = "databricks"
    evidence_scope = "fleet-wide (all developers/repos reporting to this workspace)"

    def __init__(self):
        from databricks import sql  # lazy: only needed for this backend
        host = os.environ["DATABRICKS_SERVER_HOSTNAME"]
        path = os.environ["DATABRICKS_HTTP_PATH"]
        token = os.environ["DATABRICKS_TOKEN"]
        self.conn = sql.connect(server_hostname=host, http_path=path, access_token=token)
        self.execute(f"CREATE SCHEMA IF NOT EXISTS {SCHEMA}")

    def execute(self, sql_text, params=()):
        # Databricks SQL uses %s-style params via the connector.
        cur = self.conn.cursor()
        cur.execute(sql_text, params or None)
        try:
            return cur.fetchall()
        except Exception:
            return []
        finally:
            cur.close()

    def prefix(self):
        return f"{SCHEMA}."

    def close(self):
        self.conn.close()


def get_backend(prefer=None):
    """Databricks when configured (or explicitly asked for), else local."""
    want = prefer or os.environ.get("ATC_TELEMETRY_BACKEND")
    configured = all(os.environ.get(k) for k in
                     ("DATABRICKS_SERVER_HOSTNAME", "DATABRICKS_HTTP_PATH", "DATABRICKS_TOKEN"))
    if want == "local":
        return LocalBackend()
    if want == "databricks" or configured:
        try:
            return DatabricksBackend()
        except Exception as e:
            if want == "databricks":
                raise
            print(f"[atc] databricks backend unavailable ({e}); using local store",
                  file=sys.stderr)
    return LocalBackend()


def init(be):
    p = be.prefix()
    be.execute(DDL_RUNS.format(p=p))
    be.execute(DDL_FINDINGS.format(p=p))
    return f"tables ready in backend={be.name}"


def flatten(verdict):
    """ATC verdict JSON -> (run row, finding rows)."""
    run_id = str(uuid.uuid4())
    ts = datetime.now(timezone.utc).isoformat()
    repo = os.path.basename(verdict.get("repo", "").rstrip("/"))
    run = (run_id, ts, repo, verdict["ref_a"], verdict["ref_b"],
           verdict.get("merge_base", ""), verdict["verdict"],
           int(verdict.get("reds", 0)), int(verdict.get("advisories", 0)))
    rows, f = [], verdict.get("findings", {})
    for x in f.get("write_write", []):
        rows.append((run_id, ts, repo, "WRITE_WRITE", x["entity"], x["path"], "both", 0, 1))
    for x in f.get("read_write", []):
        rows.append((run_id, ts, repo, "READ_WRITE", x["entity"], x["path"],
                     x.get("changed_by", ""), len(x.get("dependents", [])), 1))
    for x in f.get("advisory", []):
        rows.append((run_id, ts, repo, "BEHAVIOR_DRIFT", x["entity"], x["path"],
                     x.get("changed_by", ""), len(x.get("dependents", [])), 0))
    for x in f.get("proximity", []):
        rows.append((run_id, ts, repo, "PROXIMITY", "", x["path"], "", 0, 0))
    return run, rows


def record(be, verdict):
    p = be.prefix()
    run, rows = flatten(verdict)
    ph = "?" if be.name == "local" else "%s"
    be.execute(f"INSERT INTO {p}atc_runs VALUES ({','.join([ph]*9)})", run)
    for r in rows:
        be.execute(f"INSERT INTO {p}atc_findings VALUES ({','.join([ph]*9)})", r)
    return {"run_id": run[0], "findings_recorded": len(rows), "backend": be.name}


def priors(be, paths):
    """For each path: how often has parallel work here ended in a RED finding?

    This is the pre-collision warning — it fires on a path's history even when
    the current pair shows no overlap at all.
    """
    p, ph = be.prefix(), ("?" if be.name == "local" else "%s")
    out = []
    for path in paths:
        rows = be.execute(
            f"""SELECT COUNT(DISTINCT run_id), SUM(is_red) FROM {p}atc_findings
                WHERE path = {ph}""", (path,))
        runs_touching = (rows[0][0] if rows and rows[0][0] else 0)
        reds = (rows[0][1] if rows and rows[0][1] else 0)
        if runs_touching >= 2 and reds:
            out.append({"path": path, "runs_touching": int(runs_touching),
                        "red_findings": int(reds),
                        "rate": round(int(reds) / int(runs_touching), 2)})
    return sorted(out, key=lambda x: -x["rate"])


def hotspots(be, limit=10):
    p, ph = be.prefix(), ("?" if be.name == "local" else "%s")
    rows = be.execute(
        f"""SELECT path, COUNT(*) AS findings, SUM(is_red) AS reds,
                   COUNT(DISTINCT run_id) AS runs
            FROM {p}atc_findings WHERE path <> ''
            GROUP BY path ORDER BY reds DESC, findings DESC LIMIT {int(limit)}""")
    return [{"path": r[0], "findings": int(r[1] or 0), "reds": int(r[2] or 0),
             "runs": int(r[3] or 0)} for r in rows]


def main():
    ap = argparse.ArgumentParser(description="ATC fleet telemetry")
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("init")
    r = sub.add_parser("record"); r.add_argument("verdict_json")
    pr = sub.add_parser("priors"); pr.add_argument("--paths", required=True)
    hs = sub.add_parser("hotspots"); hs.add_argument("--limit", type=int, default=10)
    for s in (sub.choices.values()):
        s.add_argument("--backend", choices=["local", "databricks"], default=None)
    args = ap.parse_args()

    be = get_backend(args.backend)
    try:
        if args.cmd == "init":
            print(json.dumps({"result": init(be), "backend": be.name,
                              "evidence_scope": be.evidence_scope}, indent=2))
        elif args.cmd == "record":
            init(be)
            with open(args.verdict_json) as fh:
                verdict = json.load(fh)
            print(json.dumps(record(be, verdict), indent=2))
        elif args.cmd == "priors":
            init(be)
            print(json.dumps({"backend": be.name, "evidence_scope": be.evidence_scope,
                              "priors": priors(be, args.paths.split(","))}, indent=2))
        elif args.cmd == "hotspots":
            init(be)
            print(json.dumps({"backend": be.name, "evidence_scope": be.evidence_scope,
                              "hotspots": hotspots(be, args.limit)}, indent=2))
    finally:
        be.close()


if __name__ == "__main__":
    main()
