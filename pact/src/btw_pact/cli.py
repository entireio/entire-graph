import argparse
import json
import shutil
from pathlib import Path

from .checkpoints import read_sources
from .contracts import ReviewRequest
from .evidence import replay
from .review import benchmark, review
from .scenarios import proposed_requirements


def main():
    parser = argparse.ArgumentParser(prog="btw-pact")
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("doctor")
    sub.add_parser("proposals")
    for name in ("review", "benchmark"):
        command = sub.add_parser(name)
        command.add_argument("--request", type=Path, required=True)
        command.add_argument("--output", type=Path, default=Path("pact/runs"))
    command = sub.add_parser("reproduce")
    command.add_argument("--bundle", type=Path, required=True)
    command = sub.add_parser("sources")
    command.add_argument("--commit", default="HEAD")
    command.add_argument("--repo", default=".")
    command = sub.add_parser("serve")
    command.add_argument("--port", type=int, default=8765)
    args = parser.parse_args()
    try:
        if args.command == "doctor":
            result = {"entire_installed": bool(shutil.which("entire")), "git_installed": bool(shutil.which("git")),
                      "registered_fixture": Path("pact/demo/workspace_app/app.py").exists(),
                      "note": "Remote readiness requires a successful authenticated Databricks run."}
        elif args.command == "proposals":
            result = [r.model_dump() for r in proposed_requirements()]
        elif args.command == "sources":
            result = read_sources(args.repo, args.commit)
        elif args.command == "reproduce":
            result = replay(json.loads(args.bundle.read_text()))
            print(json.dumps(result, indent=2))
            return result["exit_code"]
        elif args.command == "serve":
            import uvicorn
            from .web import create_app
            uvicorn.run(create_app(Path.cwd()), host="127.0.0.1", port=args.port)
            return 0
        else:
            request = ReviewRequest.model_validate_json(args.request.read_text())
            result = (review if args.command == "review" else benchmark)(request, args.output)
        print(json.dumps(result, indent=2))
        if args.command == "review":
            return 2 if result["completion_state"] != "complete" else 1 if result["counts"]["head"]["fail"] else 0
        return 0
    except (ValueError, RuntimeError, OSError) as error:
        print(json.dumps({"completion_state": "failed", "error": str(error)}))
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
