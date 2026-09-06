# Anchors and Evidence

Status: Proposed design. See [Intent and Specifications](intent-and-specifications.md).

## Purpose

An anchor is a reviewed link between an intent record and a semantic code symbol.
It avoids binding a requirement permanently to a file offset, while preserving
enough structural and content evidence to make drift visible.

The existing `compound-v1` symbol ID remains the canonical code identity. GPS
does not change its construction or make a path-and-line location authoritative.

## Binding Format

Specifications declare intent-side anchor IDs. Separate binding documents record
the reviewed code-side target and the capture fingerprints:

```yaml
version: 1
anchors:
  - id: ANCHOR-AUTHENTICATE
    symbol_id: compound-v1:repo:go:src/auth/service.go:method:AuthService.authenticate
    selector:
      qualified_name: AuthService.authenticate
      kind: method
      file: src/auth/service.go
    baseline:
      signature_hash: sha256:...
      container_id: compound-v1:repo:go:src/auth/service.go:type:AuthService
      body_hash: sha256:...
      file_blob: sha256:...
    approved_at: 2026-09-06T12:00:00Z
```

The `id` must exist in a selected specification. The selector is a human-readable
locator and recovery aid; `symbol_id` is the primary binding. Baseline hashes use
named algorithms and a versioned canonicalization policy. A baseline must never
be overwritten merely because the target drifted.

`approved_at` is audit metadata, not a signal used for ranking. A future actor
field must identify an explicit user or local tool invocation and never invent an
agent identity.

## Resolution States

| State | Meaning | Check disposition |
| --- | --- | --- |
| `VALID` | Target ID exists and structural/content fingerprints match. | Pass for binding validity. |
| `CONTENT_DRIFT` | Target exists with compatible structure but a changed body hash. | Review required. |
| `STRUCTURAL_DRIFT` | Target exists but kind, signature, container, or path differs. | Review required. |
| `MISSING` | Target ID no longer resolves in a complete applicable graph. | Error. |
| `AMBIGUOUS` | Recovery selector finds multiple plausible targets. | Error; never select one. |
| `CANDIDATE_REBIND` | A change-analysis move or rename produces a candidate. | Warning; requires approval. |
| `UNVERIFIABLE` | Parse, language, selection, or completeness limits prevent a conclusion. | Incomplete, never pass. |
| `INVALID_BINDING` | The persisted document is malformed or conflicts with intent. | Error. |

Line movement alone does not change a semantic anchor. A body change is not
automatically wrong: it says the approved implementation changed and points
reviewers to the affected requirement. Symbol rename or movement can yield a
candidate rebinding through semantic diff, but GPS must leave the old binding
unchanged until an explicit review accepts it.

`MISSING` requires a complete enough graph for the file and language. A parser
failure, file cap, ignored file, or inventory-only language means `UNVERIFIABLE`,
not missing.

## Evidence Record

All GPS links and findings share a source-neutral evidence shape:

```json
{
  "kind": "anchor_binding",
  "authority": "developer_confirmed",
  "source": "authored_yaml",
  "confidence": 1,
  "location": {
    "path": ".entire/graph/anchors/authentication.yaml",
    "line": 4
  },
  "subject": "ANCHOR-AUTHENTICATE",
  "object": "compound-v1:...",
  "input_digest": "sha256:..."
}
```

`source` records the producing mechanism: `authored_yaml`, `parser`,
`static_analysis`, `git`, `entire`, `agent`, `developer`, `test`, or `llm`.
`authority` records whether the fact is `observed`, `inferred`,
`agent_confirmed`, or `developer_confirmed`. Some combinations are invalid, such
as a parser result declared developer-confirmed without a separate approval
record.

Confidence is meaningful only for inferred or probabilistic evidence. Authoring
does not make a relationship behaviorally true; it means a developer declared it.

## Identity and Drift Policy

- Code symbol identity is stable across ordinary body edits but can change on rename or move.
- Anchor identity is a user-owned ID and therefore survives code identity changes.
- Intent identity and code identity are separate namespaces.
- A body hash describes observed implementation text, not source intent.
- A file blob may help diagnose edits but must not be used as the only identity.
- Source locations are citations and diagnostics, never binding keys.
- An accepted rebind creates a new baseline record or revision; it preserves the prior state in Git.
- A binding can target only symbols inside the selected repository view.
- One anchor can bind one primary symbol in the MVP. Multiple implementation targets require explicit later schema support.

Existing symbol resolution supports ID and disambiguated selectors. GPS must reuse
that behavior and its source citations rather than build a second fuzzy resolver.

## Integrity and Safety

Binding files are authored inputs, so they require the same safe path handling
and bounded parsing as specs. Never follow a repository-controlled path outside
the configured root or into Git administrative data. The provider records
fingerprints and citations but does not serialize full source bodies into anchor
documents.

Intent bindings are separate from caches. Deleting cache state must make a query
slower but cannot delete author intent, baseline fingerprints, or provenance.

## Later Extensions

- Multi-symbol anchors for a requirement implemented as a deliberate collaboration.
- Endpoint, configuration, and external contract anchors when their graph records have stable identities.
- Authoring comments as supporting evidence.
- Developer review records with reason and related decision links.
- Anchor repair UI in an external client, backed by explicit `anchor bind` operations.

None of these extensions permit automatic mutation of approved bindings.
