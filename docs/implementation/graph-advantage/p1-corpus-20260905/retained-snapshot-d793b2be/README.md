# Retained snapshot corrective retry

This directory contains one fail-fast OFF/ON diagnostic for the Kubernetes
syntax-only snapshot at source commit `d793b2be`. It is prepared only; no VM
command, build, or measurement is part of this change.

The later operator must supply the separately verified evaluator path and its
SHA. The wrapper refuses a binary outside the pinned source root, checks the
corpus fingerprint before and after, and uses the prior retained corpus tool
from the `retained-05ad9842` staging directory when supplied as
`--scenario-script`.

Example remote invocation, with the actual evaluator SHA filled in:

```text
/usr/bin/python3 run_remote.py \
  --output /opt/graph-validation/correctness-d793b2be-20260906/snapshot-corrective \
  --binary /opt/graph-validation/correctness-d793b2be-20260906/p1-evaluator \
  --binary-sha256 0fe77d0819c4e3d5b279db68403eb8fe5d715d54847f985d61edb43b1cd4f599 \
  --source-root /opt/graph-validation/correctness-d793b2be-20260906 \
  --source-commit d793b2be \
  --scenario-script /opt/p1/retained-diagnostics/retained-05ad9842-20260906/corpus-tools/p1_scenario.py \
  --corpus-root /opt/p1/corpus \
  --input-sha256 d7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4
```

The two arms retain the prior syntax-only settings, including the 130-second
Go test timeout, with the external process deadline fixed at 120 seconds.
The expected partial-failure count, partial-failure digest, warning digest, and
semantic digest are frozen from the retained observation. Any mismatch, hard
failure, timeout, source change, or semantic mismatch stops before advancing.
RSS is recorded as unavailable because this wrapper does not claim an isolated
`wait4` measurement.

The source commit argument records the caller-supplied build archive provenance;
the wrapper checks source-root presence but does not independently verify a Git
commit.
