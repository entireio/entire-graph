# Captured preselection diagnosis — campaign remains paused

The retained 55 unequal pairs identify different selected inputs, not merely
relation serialization order. The current bounded candidate-pool prototype
restores selection for the independent 10,020-file fixture, using only captured
content. It never authorizes a subsequent mutable Git content scan.

This prototype is not an accepted parity fix. The independent reduced-match
fixture fails because Git excludes a text file declared binary by
`.gitattributes`, while the prototype includes it. The failing test is retained.
Oversized source observations currently retain only a prefix and digest, so
complete preselection evidence for those files is also unresolved. Caller-locale
case folding and later identifier lookup behavior must be checked before the
55 retained differences can be called resolved.

The next implementation boundary is capture-time bounded matching: feed the
same source descriptor to digesting and provisional-match observation, including
oversized streams, retaining only term presence and bounded line accounting.
Attribute policy must come from captured attribute inputs with Git-compatible
classification. A temporary immutable attribute-only index is a candidate;
handwritten partial attribute parsing is not an exact replacement. Deterministic
Go case folding must not be assumed equivalent to Git caller-locale matching.
Any chosen design must preserve the ordinary cache-off selection behavior and
must not reopen mutable content for later identifier lookups.

Sources: Entire search/capture/gitutil implementation and the user-authorized
plan; fixtures are independently authored term-presence, line-budget, Unicode,
binary-attribute and large-pool regressions. No comparative measurement was run.
No default or release status changes. Campaign expansion remains blocked until
these correctness findings are fixed, followed by a newly frozen canary.
