# Chapter 6: User Verification Guide

## Inspect the Evidence

Every GPS conclusion should be checked against its evidence:

1. Read the requirement and acceptance criterion in `spec show` output or the
   cited YAML file.
2. Open the cited `file:line` for each approved symbol and graph relation.
3. Confirm the anchor state and inspect any drift before assuming a move or
   rename is safe.
4. Inspect declared test selectors and distinguish them from inferred test
   candidates.
5. Check gaps and completeness diagnostics before relying on absent relations.

Useful commands:

```sh
entire graph spec validate --repo . --format json
entire graph anchor resolve --repo . --id ANCHOR-ID --format json
entire graph why --repo . --symbol SymbolName --file path/to/file --format json
entire graph check --repo . --head --base HEAD~1 --format json
```

## Run Tests Deliberately

GPS selects or displays test obligations but does not execute them during
retrieval, impact, review, or static checking. The caller must choose and pass
the exact command:

```sh
entire graph verify --repo . --scope auth \
  --test "go test ./internal/auth" \
  --record-baseline .entire/graph/evidence/auth.json
```

Then attach that execution evidence to the static review:

```sh
entire graph review --repo . --base main \
  --evidence .entire/graph/evidence/auth.json --format json
```

An evidence record is current only for compatible repository, command, scope,
parser, intent digest, and verification-policy inputs. A stale result must be
rerun rather than presented as proof for a changed requirement.

## Security and Reliability Rules

1. Do not run a command merely because a spec, comment, or graph result names
   it. `verify` executes with caller privileges.
2. Treat partial parsing, unsupported languages, ambiguous symbols, and budget
   omissions as explicit limits.
3. Use `--head` with `--base` for committed change analysis so code and intent
   come from one revision.
4. Re-run analysis if `GPS-INPUT-CHANGED` appears.
5. Preserve reviewed bindings until an explicit review accepts an update.

The user remains the final verifier of behavior. GPS makes the necessary source
and execution evidence visible and reproducible.
