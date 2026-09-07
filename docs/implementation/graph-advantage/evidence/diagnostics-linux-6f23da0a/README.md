# Pinned Linux correctness and evaluator build

This evidence binds source commit `6f23da0ad4fa704da8c7bac53e1065030c89a4f1` to archive SHA-256 `e1a42113a94d043c79833ea9d16ae568965bdb50efdd7a5ccc5f3186c883032b`, extracted at `/opt/graph-validation/diagnostics-linux-6f23da0a/src`. The archive contained only `internal`, `cmd`, `scripts`, `go.mod`, and `go.sum`.

On the pinned Linux VM, affected route and P4 contract tests passed normally and under the race detector (32/32 each). The existing compiler correctness selection passed 28/28, including 10 live tests. The evaluator compiled without execution; its SHA-256 is `9562a0f5ee9558a0c6e7533ad7cfa6511251e52588e976cf09ee86564fa4966d`.

`stage-results.json` and `test-counts.json` record outcomes derived from the retained raw `-v` logs and exit files. The run used Go 1.26.1, gopls 0.20.0, and the enforced no-egress Go environment. Source and result blobs did not exist before this one attempt. SAS URLs and their base64 encodings were kept only in process memory; retained templates contain placeholders and the binding proof uses dummy URLs.

All stage, overall, transport, and toolchain exits are zero. `vm-terminal.json` records the validation VM deallocated after evidence transfer, and `all-vms-terminal.json` records all three resource-group VMs deallocated.

This establishes pinned Linux correctness and evaluator compilation for the named stages only. No product invocation, corpus run, retrieval study, benchmark, performance claim, release gate, or default-promotion claim is included.
