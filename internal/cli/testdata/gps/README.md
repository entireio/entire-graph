# GPS CLI Fixture Contracts

`token-auth/` is the committed baseline for focused GPS CLI tests. Each test
copies it into a temporary Git repository and creates only the scenario under
test, so its result does not depend on this checkout's history or worktree.

The JSON files in `golden/` pin stable public fields only. They deliberately
exclude commit IDs, tree IDs, symbol IDs, body hashes, and intent digests.

The Git-backed scenarios cover deleted reviewed anchors and declared test
mappings, dirty working-tree versus `--head` selection, ignored untracked
paths, malformed binding validation, ambiguous rebind candidates, partial
graphs, and an unborn `HEAD` failure. The unborn-HEAD case has no JSON response
because committed-view input cannot be selected.
