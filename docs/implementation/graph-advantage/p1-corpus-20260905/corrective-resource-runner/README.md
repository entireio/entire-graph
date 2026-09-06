# Corrective resource runner

This versioned runner prepares one corrective Kubernetes syntax-only snapshot
pair using the retained `0c9e80f5` identity contract. It performs one OFF arm
and advances to one ON arm only when the OFF observation, semantic/input
identity, and external resource measurement all validate. It never retries and
never starts a campaign.

Each request runs under Linux `/usr/bin/time -v`. The raw time output is kept in
`time-off.txt` or `time-on.txt`; the exact `Maximum resident set size (kbytes)`
line is parsed once and converted to bytes. Missing, duplicated, malformed, or
nonpositive RSS data fails the OFF arm before ON can start. The 120-second
process-group deadline, known 194 partial-failure identity, semantic identity,
and source fingerprint checks remain unchanged. RSS here is measurement
completeness evidence; it is not a performance claim.

Prepared invocation, with the separately verified binary and source arguments
filled by the operator:

```text
/usr/bin/python3 run_remote.py \
  --output /opt/graph-validation/corrective-resource-runner/<unique-id> \
  --binary <verified-binary-path> \
  --binary-sha256 <verified-binary-sha256> \
  --source-root <verified-source-root> \
  --source-commit <verified-source-commit> \
  --scenario-script /opt/p1/retained-diagnostics/retained-05ad9842-20260906/corpus-tools/p1_scenario.py \
  --corpus-root /opt/p1/corpus \
  --input-sha256 d7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4
```

The final input digest argument must be the frozen corpus digest from the
retained evaluation; the command above intentionally leaves the binary SHA
operator-supplied. No VM or product measurement was run while preparing this
runner.
