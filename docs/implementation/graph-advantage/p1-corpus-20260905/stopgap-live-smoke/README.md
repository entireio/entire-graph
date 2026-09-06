# Live stop propagation smoke

This helper is a correctness smoke for the existing three-worker supervisor.
It starts only isolated sleeping `systemd-run` services named
`p1-campaign-<run-id>`. It never invokes the evaluator, reads the corpus, or
runs a product query.

The operator must review the helper before running it. It requires an explicit
unique run ID and empty output directory, uses exact worker/unit paths, and
refuses unknown or malformed transport responses. Each fake service has
`RuntimeMaxSec=300`; the whole smoke has a separate default 360-second
watchdog. Setup, status, pause, and supervision calls consume the same
monotonic deadline; an expired watchdog receives a separately bounded 30-second
cleanup grace so all workers still receive exact stop commands. A timeout or
error retains every redacted raw response.

Prepared command (no command has been run by preparation):

```text
/usr/bin/python3 run_stop_smoke.py \
  --run-id stop-smoke-<unique-lowercase-id> \
  --output /path/to/stopgap-live-smoke/<unique-lowercase-id>
```

A passing result requires the three fake units to be active before the atomic
pause injection, the real supervisor to return `False`, and all three terminal
states to be inactive with their exact STOP markers present. The result is
plumbing evidence only and says nothing about product correctness or campaign
performance.
