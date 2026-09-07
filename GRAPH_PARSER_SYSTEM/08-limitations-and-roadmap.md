# Chapter 8: Limitations and Roadmap

## Current Limits

GPS intentionally has strict limits:

1. Static graph relationships can be incomplete, especially for unsupported,
   dynamic, or partially parsed code. GPS reports this as incomplete evidence.
2. A reviewed anchor expresses traceability, not proof that the code still
   fulfills its requirement.
3. Declared mappings identify tests to consider; they are not measured coverage.
4. `verify` records one authorized command result and cannot prove all runtime
   environments or behavior.
5. Checkpoint trailers are bounded local provenance, not automatically imported
   conversational intent or developer approval.
6. No GPS specifications means code-only graph workflows remain available, but
   intent-aware conclusions are unavailable.

## Safe Adoption Path

1. Run `entire graph spec init --repo .`.
2. Author one small specification with requirements and acceptance criteria.
3. Add declared test selectors for its acceptance criteria.
4. Bind a high-value implementation symbol with `anchor bind`.
5. Validate intent and resolve the binding in CI or review workflows.
6. Use `context`, `check`, and `review` before expanding GPS to more areas.

This incremental approach produces useful traceability without requiring a
complete up-front model of the entire codebase.

## Future Direction

Future enhancements may add reviewed authoring assistance, richer route and
configuration anchors, multi-symbol implementation bindings, and external
client integrations. They must retain the current invariants: local operation,
explicit authority, reproducible selected inputs, no silent rebinding, and no
automatic execution of repository-supplied commands.

## Conclusion

The Graph Parser System is valuable precisely because it does not turn graph
output into unsupported certainty. It creates a repeatable path from intent to
implementation, impact, tests, evidence, and reviewer decision. That path is
the product: an inspectable workflow for safe engineering change.
