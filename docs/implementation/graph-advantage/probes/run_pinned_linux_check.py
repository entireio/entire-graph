"""One pinned Linux correctness run; never starts a campaign or retries tests."""
import argparse
import hashlib
import json
import pathlib
import shlex
import subprocess
import sys

BASE = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(BASE / "p1-corpus-20260905"))
import cloud


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--commit", required=True)
    parser.add_argument("--run-id", required=True)
    args = parser.parse_args()
    if not args.run_id or any(c not in "abcdefghijklmnopqrstuvwxyz0123456789-" for c in args.run_id):
        parser.error("run-id must contain lowercase letters, digits and hyphens")
    commit = subprocess.check_output(["git", "rev-parse", "--verify", args.commit + "^{commit}"], text=True).strip()
    out = BASE / "evidence" / args.run_id
    out.mkdir(exist_ok=False)
    archive = out / "source.tar.gz"
    with archive.open("wb") as stream:
        subprocess.run(["git", "archive", "--format=tar.gz", commit,
                        "internal", "cmd", "scripts", "go.mod", "go.sum"], stdout=stream, check=True)
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    manifest = {"source_commit": commit, "archive_sha256": digest,
                "purpose": "Pinned positive Linux correctness; no comparative evaluation",
                "status": "prepared"}
    record = out / "verification.json"
    record.write_text(json.dumps(manifest, indent=2) + "\n")
    env = cloud.environment()
    blob = args.run_id + "-source.tar.gz"
    cloud.upload(archive, blob, env)
    remote = "/opt/graph-validation/" + args.run_id
    q = shlex.quote
    pattern = "Test(Compiler|LiveCompiler|LiveAdvantage|LiveReview|MapLocation|RPC|Capsule)"
    command = ("cd " + q(remote) + " && export PATH=/usr/local/go/bin:/usr/bin:/bin "
               "GOPATH=/opt/graph-validation/gopath GOCACHE=/opt/graph-validation/cache "
               "GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null "
               "GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local ENTIRE_GRAPH_COMPILER_LIVE=1 "
               "ENTIRE_GRAPH_ADVANTAGE_LIVE_OUTPUT=" + q(remote + "/combination.json") + " "
               "ENTIRE_GRAPH_REVIEW_LIVE_OUTPUT=" + q(remote + "/queries.json") + "; "
               "go test -race -v -timeout 30m ./internal/compiler ./internal/sem ./internal/cli "
               "-run " + q(pattern) + " -skip 'QualityEvaluation|ExtractionEvaluation' -count=1")
    manifest["command"] = command
    record.write_text(json.dumps(manifest, indent=2) + "\n")
    result_blob = args.run_id + "-results.tar.gz"
    script = "set -eu\n"
    script += "if systemctl list-units --type=service --state=active --no-legend 'p1-*' | grep -q .; then echo ACTIVE_CAMPAIGN_REFUSED; exit 1; fi\n"
    script += "mkdir " + q(remote) + "\n"
    script += "curl -fsS " + q(cloud.url(blob, "r", env)) + " -o " + q(remote + "/source.tar.gz") + "\n"
    script += "echo " + q(digest + "  " + remote + "/source.tar.gz") + " | sha256sum -c - >/dev/null\n"
    script += "tar xzf " + q(remote + "/source.tar.gz") + " -C " + q(remote) + "\n"
    script += "chown -R graphcheck:graphcheck " + q(remote) + "\n"
    script += "(uname -a; /usr/local/go/bin/go version; /opt/graph-tools/gopls version; /usr/bin/git --version; sha256sum /opt/graph-tools/gopls /usr/local/go/bin/go) > " + q(remote + "/environment.txt") + "\n"
    script += "set +e\ntimeout --signal=TERM --kill-after=10s 2100s runuser -u graphcheck -- sh -c " + q(command) + " > " + q(remote + "/check.txt") + " 2>&1\nstatus=$?\nset -e\n"
    script += "printf '%s\\n' \"$status\" > " + q(remote + "/exit.txt") + "\n"
    script += "cd " + q(remote) + "\n"
    script += "tar czf results.tar.gz check.txt exit.txt environment.txt $(for f in combination.json queries.json; do test ! -f \"$f\" || printf '%s ' \"$f\"; done)\n"
    script += "curl -fsS -X PUT -H 'x-ms-blob-type: BlockBlob' --upload-file results.tar.gz " + q(cloud.url(result_blob, "cw", env)) + " >/dev/null\necho PINNED_CHECK_UPLOAD_ACK\n"
    try:
        result = cloud.run("graph-validation-linux", script)
        (out / "transport.json").write_text(result + "\n")
        if "PINNED_CHECK_UPLOAD_ACK" not in result:
            raise RuntimeError("Result upload was not acknowledged; remote outcome unknown")
        cloud.download(result_blob, out / "results.tar.gz", env)
        manifest["status"] = "raw results collected; inspect exit and test outcomes"
    except Exception:
        manifest["status"] = "transport or staging failed; do not retry test automatically"
        raise
    finally:
        record.write_text(json.dumps(manifest, indent=2) + "\n")
    print(str(out))


if __name__ == "__main__":
    main()
