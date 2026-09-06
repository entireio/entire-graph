#!/usr/bin/env bash
# ATC seeded fixture — simulates two parallel agent sessions whose work
# merges CLEAN textually but breaks at runtime (READ–WRITE collision),
# plus WW / PROX seeds and a clean control pair. See ATC_PLAN.md §7.
#
# Usage: build_fixture.sh [out-dir]   (default: ${TMPDIR:-/tmp}/atc-fixture)
set -euo pipefail

OUT="${1:-${TMPDIR:-/tmp}/atc-fixture}"
rm -rf "$OUT" "${OUT}-wt-auth" "${OUT}-wt-checkout"
mkdir -p "$OUT"
cd "$OUT"
git init -q -b main
git config user.email fixture@atc.local
git config user.name "ATC Fixture"

# ---------------- main (merge-base) ----------------
cat > auth.py <<'EOF'
"""Token validation for the demo shop."""


def validate_token(token):
    """Validate an auth token. Returns the user id."""
    if not token or "." not in token:
        raise ValueError("malformed token")
    user, sig = token.split(".", 1)
    if sig != "sig":
        raise ValueError("bad signature")
    return user


def login(user):
    token = f"{user}.sig"
    validate_token(token)
    return token
EOF

cat > config.py <<'EOF'
"""Config loading."""


def parse_config(text):
    """Parse key=value lines into a dict."""
    cfg = {}
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        key, _, value = line.partition("=")
        cfg[key.strip()] = value.strip()
    return cfg
EOF

cat > payments.py <<'EOF'
"""Payment operations."""


def charge(user, amount):
    if amount <= 0:
        raise ValueError("amount must be positive")
    return {"user": user, "amount": amount, "status": "charged"}


def refund(user, amount):
    if amount <= 0:
        raise ValueError("amount must be positive")
    return {"user": user, "amount": amount, "status": "refunded"}
EOF

cat > checkout.py <<'EOF'
"""Checkout flow."""

from auth import validate_token
from payments import charge


def checkout(user, token, amount):
    uid = validate_token(token)
    return charge(uid, amount)
EOF

cat > run_tests.py <<'EOF'
"""Fixture smoke tests."""

from auth import login, validate_token
from checkout import checkout
from config import parse_config


def main():
    token = login("alice")
    assert validate_token(token) == "alice"
    assert checkout("alice", token, 10)["status"] == "charged"
    cfg = parse_config("retries=3\n# comment\nhost=localhost")
    assert cfg["retries"] == "3"
    print("OK: fixture tests passed")


if __name__ == "__main__":
    main()
EOF

git add -A && git commit -qm "base: demo shop (auth, config, payments, checkout)"

# ---------------- feat-auth (session A) ----------------
# RW seed:   validate_token gains a REQUIRED expiry arg (signature change)
# WW seed:   parse_config edited near the TOP (normalize newlines)
# PROX seed: charge() body edited
git checkout -qb feat-auth
cat > auth.py <<'EOF'
"""Token validation for the demo shop."""


def validate_token(token, expiry):
    """Validate an auth token with a required expiry (seconds)."""
    if not token or "." not in token:
        raise ValueError("malformed token")
    if not isinstance(expiry, int) or expiry <= 0:
        raise ValueError("expiry must be a positive int")
    user, sig = token.split(".", 1)
    if sig != "sig":
        raise ValueError("bad signature")
    return user


def login(user):
    token = f"{user}.sig"
    validate_token(token, 3600)
    return token
EOF

cat > config.py <<'EOF'
"""Config loading."""


def parse_config(text):
    """Parse key=value lines into a dict."""
    text = text.replace("\r\n", "\n")
    cfg = {}
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        key, _, value = line.partition("=")
        cfg[key.strip()] = value.strip()
    return cfg
EOF

cat > payments.py <<'EOF'
"""Payment operations."""


def charge(user, amount):
    amount = round(amount, 2)
    if amount <= 0:
        raise ValueError("amount must be positive")
    return {"user": user, "amount": amount, "status": "charged"}


def refund(user, amount):
    if amount <= 0:
        raise ValueError("amount must be positive")
    return {"user": user, "amount": amount, "status": "refunded"}
EOF

cat > checkout.py <<'EOF'
"""Checkout flow."""

from auth import validate_token
from payments import charge


def checkout(user, token, amount):
    uid = validate_token(token, 3600)
    return charge(uid, amount)
EOF

cat > run_tests.py <<'EOF'
"""Fixture smoke tests (session A keeps its own tree self-consistent)."""

from auth import login, validate_token
from checkout import checkout
from config import parse_config


def main():
    token = login("alice")
    assert validate_token(token, 3600) == "alice"
    assert checkout("alice", token, 10)["status"] == "charged"
    cfg = parse_config("retries=3\r\n# comment\nhost=localhost")
    assert cfg["retries"] == "3"
    print("OK: fixture tests passed")


if __name__ == "__main__":
    main()
EOF
git add -A && git commit -qm "feat-auth: require token expiry; normalize config newlines; round charge amounts"

# ---------------- feat-checkout (session B) ----------------
# RW seed:   NEW call sites to validate_token(token) — OLD 1-arg shape
# WW seed:   parse_config edited near the BOTTOM (host validation)
# PROX seed: refund() body edited
git checkout -q main
git checkout -qb feat-checkout
cat > checkout.py <<'EOF'
"""Checkout flow."""

from auth import validate_token
from payments import charge


def checkout(user, token, amount):
    uid = validate_token(token)
    return charge(uid, amount)


def quick_pay(token):
    """One-tap purchase for a signed-in user."""
    uid = validate_token(token)
    return charge(uid, 1)
EOF

cat > subscriptions.py <<'EOF'
"""Subscription renewals (new in session B)."""

from auth import validate_token
from payments import charge


def renew(token, plan_amount):
    uid = validate_token(token)
    return charge(uid, plan_amount)
EOF

cat > config.py <<'EOF'
"""Config loading."""


def parse_config(text):
    """Parse key=value lines into a dict."""
    cfg = {}
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        key, _, value = line.partition("=")
        cfg[key.strip()] = value.strip()
    if cfg.get("host") == "":
        raise ValueError("host must not be empty")
    return cfg
EOF

cat > payments.py <<'EOF'
"""Payment operations."""


def charge(user, amount):
    if amount <= 0:
        raise ValueError("amount must be positive")
    return {"user": user, "amount": amount, "status": "charged"}


def refund(user, amount):
    if amount <= 0:
        raise ValueError("amount must be positive")
    return {"user": user, "amount": amount, "status": "refunded", "method": "original"}
EOF

cat > tests_checkout.py <<'EOF'
"""Session B tests: quick_pay and renewals."""

from auth import login
from checkout import quick_pay
from subscriptions import renew


def main():
    token = login("bob")
    assert quick_pay(token)["status"] == "charged"
    assert renew(token, 99)["status"] == "charged"
    print("OK: checkout tests passed")


if __name__ == "__main__":
    main()
EOF
git add -A && git commit -qm "feat-checkout: quick_pay + subscriptions renew; validate empty host; refund method"

# ---------------- clean control pair ----------------
git checkout -q main
git checkout -qb feat-logging
cat > logutil.py <<'EOF'
"""Tiny logging helper (independent of everything)."""


def log(msg):
    print(f"[shop] {msg}")
EOF
git add -A && git commit -qm "feat-logging: add logutil"

git checkout -q main
git checkout -qb feat-docs
cat > NOTES.md <<'EOF'
# Demo shop notes

Operational notes only. No code changes.
EOF
git add -A && git commit -qm "feat-docs: add notes"

# ---------------- hotspot-prior session (no current collision) -------
# Touches auth.py — historically contended — but collides with nobody today.
# Used to demonstrate that fleet history warns BEFORE any overlap exists.
git checkout -q main
git checkout -qb feat-audit
python3 - <<'PY'
s = open("auth.py").read()
s = s.replace(
    'def login(user):\n    token = f"{user}.sig"',
    'def login(user):\n    """Issue a token (audit session adds trace logging)."""\n'
    '    print(f"[audit] login {user}")\n    token = f"{user}.sig"', 1)
open("auth.py", "w").write(s)
PY
git add -A && git commit -qm "feat-audit: add audit trace to login"

git checkout -q main

# ---------------- worktrees = live parallel sessions ----------------
git worktree add -q "${OUT}-wt-auth" feat-auth
git worktree add -q "${OUT}-wt-checkout" feat-checkout

echo "fixture ready: $OUT"
echo "worktrees:     ${OUT}-wt-auth  ${OUT}-wt-checkout"
echo
echo "Prove the trap (git says clean, runtime breaks):"
echo "  cd $OUT && git merge --no-ff --no-edit feat-auth && git merge --no-ff --no-edit feat-checkout"
echo "  python3 run_tests.py        # passes (A self-consistent)"
echo "  python3 tests_checkout.py   # TypeError: validate_token missing 'expiry' — the RW collision"
