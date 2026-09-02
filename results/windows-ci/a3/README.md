# A3 raw evidence

This directory holds compact, credential-reviewed summaries and reproduction
pointers for the Linux-to-Windows CGO cross-compile experiment. Raw Azure
responses, full logs, ZIPs, and executables live only in the ignored local raw
directory long enough to verify and summarize them; the tagged Azure resource
group is deleted after verification. Retained SHA-256 values identify each raw
artifact without committing it.

The accepted evidence set consists of the infrastructure, native baseline,
cross compile, transfer, weighted execution, native fidelity, Linux comparator,
cost, bounded sequential diagnostic, and cleanup summaries. The Linux summary
also preserves the rejected Git 2.43.0 diagnostic beside the accepted Git
2.55.0 comparator. Run-specific Azure staging drivers remain ignored; reusable
logic is under `tools/ci-bench/a3/` and in the manual workflow prototype.
