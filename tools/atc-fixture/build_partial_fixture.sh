#!/bin/bash
# Partial-analysis fixture — "the graph is evidence, not an oracle".
#
# A repo whose dependency structure static analysis CANNOT fully resolve:
# handlers are reached through getattr() dispatch, so a signature change on
# one branch and a new dynamic caller on another produce NO graph edge, git
# merges them with zero conflicts, and the merged tree breaks at runtime.
#
# Branch pairs:
#   feat-currency  x feat-webhooks  DYNAMIC dependent -> graph sees nothing.
#                                   Must be CLEARED_PARTIAL (exit 4), never a
#                                   bare CLEARED: the getattr router is a
#                                   blind spot, not evidence of independence.
#   feat-refund-sig x feat-billing  STATIC dependent (control) -> the same
#                                   class of change through a direct call must
#                                   still be caught as a red HOLD.
#
# Usage: build_partial_fixture.sh <target-dir>
set -euo pipefail
DIR="${1:?usage: build_partial_fixture.sh <target-dir>}"
rm -rf "$DIR" && mkdir -p "$DIR" && cd "$DIR"
git init -q -b main
git config user.email atc-fixture@example.com
git config user.name "atc fixture"

cat > handlers.py << 'PY'
def handle_payment(amount):
    return {"ok": True, "amount": amount}

def handle_refund(amount):
    return {"ok": True, "amount": -amount}
PY

cat > dispatch.py << 'PY'
import handlers

def route(event, amount):
    # dynamic dispatch: static analysis cannot see which handler this calls
    fn = getattr(handlers, "handle_" + event)
    return fn(amount)
PY

cat > billing.py << 'PY'
from handlers import handle_refund

def issue_refund(amount):
    # static, direct call — the control case the graph CAN resolve
    return handle_refund(amount)
PY

git add -A
git commit -qm "seed: handlers + dynamic dispatcher + static caller"

# side A: change handle_payment's shape (breaks any caller, seen or unseen)
git checkout -qb feat-currency main
python3 - << 'PY'
s = open("handlers.py").read()
s = s.replace("def handle_payment(amount):",
              "def handle_payment(amount, currency):")
s = s.replace('return {"ok": True, "amount": amount}',
              'return {"ok": True, "amount": amount, "currency": currency}', 1)
open("handlers.py", "w").write(s)
PY
git commit -qam "require currency on handle_payment"

# side B: build a NEW call path to the old shape — through the dynamic router
git checkout -qb feat-webhooks main
cat > webhooks.py << 'PY'
from dispatch import route

def on_stripe_event(payload):
    # reaches handle_payment only through getattr dispatch
    return route("payment", payload["amount"])
PY
git add -A
git commit -qm "webhook entrypoint via dynamic router"

# control pair: the SAME shape-change class, but with a static dependent
git checkout -qb feat-refund-sig main
python3 - << 'PY'
s = open("handlers.py").read()
s = s.replace("def handle_refund(amount):",
              "def handle_refund(amount, reason):")
open("handlers.py", "w").write(s)
PY
git commit -qam "require reason on handle_refund"

git checkout -qb feat-billing main
cat >> billing.py << 'PY'

def bulk_refunds(amounts):
    return [handle_refund(a) for a in amounts]
PY
git commit -qam "bulk refunds via direct static call"

git checkout -q main
echo "partial-analysis fixture ready: $DIR"
