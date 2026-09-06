#!/usr/bin/env bash
# ATC demo on a real open-source repository: pallets/flask (72k stars,
# one of the 12 SWE-bench repos the industry benchmarks coding agents on).
#
# Neither side of this collision is invented:
#
#   Agent A (agent-ctx)     IS Flask's real commit 6a64969, "pass context
#                           through dispatch methods" — it added a `ctx`
#                           parameter to 12 dispatch method signatures.
#   Agent B (agent-timing)  is a plausible parallel feature: request timing
#                           instrumentation that subclasses Flask and calls
#                           those methods on their OLD shape.
#
# Both branch from adf3636, the real parent of that migration. `git merge`
# reports zero conflicts; the merged tree is broken. Flask itself shipped
# ~40 lines of runtime introspection in `Flask.__init_subclass__` to catch
# exactly this at runtime — proof the collision class is real, and that the
# only defence available today is a post-hoc shim. ATC catches it pre-merge.
#
# Usage: setup_flask_demo.sh [out-dir]        (default: /tmp/atc-flask-demo)
set -euo pipefail

OUT="${1:-${TMPDIR:-/tmp}/atc-flask-demo}"
BASE="adf3636"      # parent of the ctx migration — the shared merge-base
CTX="6a64969"       # Flask's real "pass context through dispatch methods"
CACHE="${ATC_FLASK_CACHE:-}"

rm -rf "$OUT"
if [ -n "$CACHE" ] && [ -d "$CACHE/.git" ]; then
  echo "[atc-demo] cloning from local cache $CACHE"
  git clone -q "$CACHE" "$OUT"
else
  echo "[atc-demo] cloning pallets/flask (shallow) ..."
  git clone -q --depth 120 https://github.com/pallets/flask.git "$OUT"
fi
cd "$OUT"
git config user.email agent@atc.local
git config user.name "ATC Demo Agent"

if ! git cat-file -e "${CTX}^{commit}" 2>/dev/null; then
  echo "[atc-demo] deepening history to reach $CTX ..."
  git fetch -q --unshallow 2>/dev/null || git fetch -q --depth 400
fi

# ---- the shared starting point both agents branched from ----------------
git checkout -q -B main "$BASE"

# ---- Agent A: Flask's REAL signature migration --------------------------
git branch -f agent-ctx "$CTX"

# ---- Agent B: a plausible parallel feature, built on the OLD shape ------
git checkout -q -B agent-timing main
mkdir -p src/flask
cat > src/flask/timing.py <<'EOF'
"""Request timing instrumentation.

Records how long each request spends in dispatch so slow endpoints can be
surfaced in logs. Drives the dispatch pipeline directly instead of using
before/after_request hooks, so the timer also covers error handling.
"""

from __future__ import annotations

import time

from .app import Flask
from .wrappers import Response


def time_full_dispatch(app: Flask) -> tuple[Response, float]:
    """Run one full dispatch pass and report how long it took, in ms."""
    start = time.perf_counter()
    response = app.full_dispatch_request()
    return response, (time.perf_counter() - start) * 1000


def time_view_dispatch(app: Flask) -> tuple[Response, float]:
    """Time just the view, skipping before_request handlers."""
    start = time.perf_counter()
    response = app.dispatch_request()
    return response, (time.perf_counter() - start) * 1000


def slowest_of(app: Flask, runs: int = 3) -> float:
    """Worst dispatch cost over ``runs`` passes — used by the slow-endpoint log."""
    worst = 0.0
    for _ in range(runs):
        _response, cost = time_full_dispatch(app)
        worst = max(worst, cost)
    return worst
EOF

cat > tests/test_timing.py <<'EOF'
import flask
from flask.timing import time_full_dispatch


def test_records_dispatch_duration():
    app = flask.Flask(__name__)

    @app.route("/")
    def index():
        return "ok"

    with app.test_request_context("/"):
        response, cost_ms = time_full_dispatch(app)

    assert response.get_data() == b"ok"
    assert cost_ms >= 0
EOF

git add src/flask/timing.py tests/test_timing.py
git commit -q -m "add request timing instrumentation

Wraps the dispatch pipeline so slow endpoints show up in logs, including
requests that end in an error handler."

# ---- Agent C: docs only — genuinely disjoint (precision control) --------
git checkout -q -B agent-docs main
python3 - <<'PY'
import re, pathlib
p = pathlib.Path("CHANGES.rst")
text = p.read_text(encoding="utf-8")
marker = "Unreleased\n"
idx = text.find(marker)
insert = ("\n-   Document the request dispatch pipeline and the order in which\n"
          "    before_request handlers run.\n")
if idx != -1:
    end = text.find("\n\n", idx)
    text = text[:end] + insert + text[end:]
    p.write_text(text, encoding="utf-8")
PY
git add CHANGES.rst
git commit -q -m "document the dispatch pipeline ordering"

# ---- Agent D: JSON provider — disjoint from dispatch --------------------
git checkout -q -B agent-json main
python3 - <<'PY'
import pathlib
p = pathlib.Path("src/flask/json/provider.py")
text = p.read_text(encoding="utf-8")
text += '''

def compact_dumps(obj: t.Any, **kwargs: t.Any) -> str:
    """Serialize without insignificant whitespace, for size-sensitive payloads."""
    kwargs.setdefault("separators", (",", ":"))
    kwargs.setdefault("sort_keys", False)
    return json.dumps(obj, **kwargs)
'''
p.write_text(text, encoding="utf-8")
PY
git add src/flask/json/provider.py
git commit -q -m "add compact JSON serialization helper"

git checkout -q main

# ---- prove the trap: git is happy, the merged tree is not ---------------
echo
echo "[atc-demo] verifying the trap (this is what git sees) ..."
git checkout -q -B merge-check main
if git merge -q --no-ff --no-edit agent-ctx >/dev/null 2>&1 \
   && git merge --no-ff --no-edit agent-timing >/dev/null 2>&1; then
  echo "[atc-demo]   git merge: CLEAN — zero conflicts"
else
  echo "[atc-demo]   WARNING: merge reported a conflict; the demo expects a clean merge" >&2
fi
git checkout -q main
git branch -qD merge-check

cat <<EOF

fixture ready: $OUT
  merge-base   main        $BASE  (real parent of Flask's ctx migration)
  agent A      agent-ctx   $CTX  Flask's real "pass context through dispatch methods"
  agent B      agent-timing        request timing instrumentation, built on the old shape

Run ATC over it:
  python3 tools/atc/collide.py agent-ctx agent-timing --repo $OUT

Expected: HOLD (exit 2) — READ-WRITE on the dispatch methods, with the
call sites in src/flask/timing.py as receipts.
EOF
