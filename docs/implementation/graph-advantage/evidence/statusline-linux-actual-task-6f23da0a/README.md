# Linux statusline actual-task attempt

This preserves the single authorized attempt. It did not reach source identity, toolchain, or `mise run test:statusline`: the remote script recorded overall exit `73`. The configured `/opt/graph-validation/check-6f23da0a-linux-full-r1` base matches the successful r1 runner, but the diagnosis script checked for `input/tracked-manifest.tsv`; r1 stored and consumed `input/tracked-manifest-r1.tsv`. `raw/after-identity.txt` confirms the former path was missing. The uploaded result archive SHA-256 is `dbdd826a3fe6fdf32b3d07ed8753787766c6f9bf0e5a3872771e307333a24c32`.

No statusline task, product binary, corpus, or traced fallback ran. The result is not correctness evidence and does not reproduce or diagnose the earlier failure. A corrected attempt would require separate authorization and the manifest filename correction. Controller cleanup recorded all three validation VMs deallocated.

The attempted diagnostic runner also drifted from the successful r1 runner: it overrode `HOME`, compared filesystem permission digits rather than r1's Git executable-mode normalization, and required a literal selected-Go path rather than comparing resolved paths. It is retained unchanged as failed-attempt provenance and must not be reused as the basis of a corrected runner.
