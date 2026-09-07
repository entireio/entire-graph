# Source-bound Linux evaluator compile

This directory records one compiler-only P1 evaluator build from source commit
`950567a3c2796c99c9f46af84f7c0a8afac3351d`. It was mechanically derived from
the successful `diagnostics-linux-25887f69-evaluator-build-01` controller and
remote template.

The compiler invocation was bound to:

- source: `/opt/graph-validation/check-950567a3-linux-full-01/src`
- source archive SHA-256:
  `dfe4b484b98535d0b9c35b6bb0b524352ddb93e61f8bde391aa6663db4c2f91b`
- tracked manifest:
  `/opt/graph-validation/check-950567a3-linux-full-01/input/tracked-manifest.tsv`
- tracked manifest SHA-256:
  `7b5854a5927df8968d82df143125234538dc86be411f385f564590e49d8a9371`
- tracked files: 2,398
- fresh artifact root:
  `/opt/graph-validation/diagnostics-linux-950567a3-evaluator-build-01`

The controller refused to begin cloud work unless the local immutable full-check
evidence has identical before/after manifests and both `overall.exit.txt` and
`check.exit.txt` are zero. The remote template repeats the archive, manifest,
file-count, source-content, Go 1.26.1, gopls 0.20.0, executable-hash, and Go
realpath checks before compilation. It leaves `HOME` unchanged and retains the
no-egress Go and Git environment.

The only compiler command was:

```text
go test -c -o /opt/graph-validation/diagnostics-linux-950567a3-evaluator-build-01/p1-evaluator ./internal/sem
```

The command exited zero and produced binary SHA-256
`623fd971be1b1c9813c98b8afeb1bb5b0bf5c43e8d3135f7e13b5bca3c33ec73`.
The binary build-info SHA-256 is
`efd22811f988ce626de112057e390f236c07109c09cd18fd2f250e02580189ff`.
The source manifest was identical before and after compilation. The retained
results archive SHA-256 is
`67425de94df91d82e425f6f21c19c0d6cc467c9c375a090bc647ee44e50ef0d2`.
Build, overall, transport, and upload exits are zero.

The produced test binary was not run. The controller's cleanup trap deallocated
and verified `graph-validation-linux`, `graph-p1-worker-2`, and
`graph-p1-worker-3`; the all-VM terminal record SHA-256 is
`8211bf4115a75c70ffc43130644097c24698a55879a9a2be4c13177226678909`.
No retry or fallback occurred.

Before the accepted execution, an outer local wrapper check failed because its
expected controller hash was transcribed incorrectly. That wrapper exited
before invoking the controller and caused zero cloud, VM, or compiler actions.
The sole controller invocation was session `63903`, after both accepted file
hashes were checked exactly.

`review/preparation-provenance.json` is the historical pre-execution snapshot.
Its zero invocation counts and `execution_authorized: false` describe the held
state before session `63903`; `build-manifest.json` is the terminal execution
record.

No evaluator binary, product, corpus, diagnostic, benchmark, comparison, or
claim was invoked by this compiler-only step.
