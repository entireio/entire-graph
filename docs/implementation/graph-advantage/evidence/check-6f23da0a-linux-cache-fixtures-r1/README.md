# Linux cache-fixture diagnosis, r1

This directory preserves the single authorized r1 attempt against archived source commit `6f23da0ad4fa704da8c7bac53e1065030c89a4f1`. The reviewed remote template SHA-256 was `286841617db7bcb675f5a9f142e2542afbb77db73624ac2b834b22e592c14515` before execution.

The Azure transport completed and uploaded `results.tar.gz`, but the remote script recorded overall exit `74`. Under the reviewed template, `74` means either `/opt/graph-validation/diagnostics-linux-6f23da0a/src` was absent or `/tmp/diagnostics-linux-6f23da0a-source.tar.gz` was absent. No source-identity log was produced, so this evidence cannot distinguish those two conditions and does not establish a hash mismatch. The archive contains only `raw/overall.exit.txt`; toolchain and test stages did not run. Observed named test count is zero, and there were no product, corpus, benchmark, or retrieval-study invocations.

The original pinned build controller downloaded the source archive to `/tmp/diagnostics-linux-6f23da0a-source.tar.gz` before extracting the durable source tree. That temporary archive path was reused by this check, while the checked-in identical archive remains available at `../diagnostics-linux-6f23da0a/source.tar.gz` with SHA-256 `e1a42113a94d043c79833ea9d16ae568965bdb50efdd7a5ccc5f3186c883032b`. A subsequent attempt would need separate authorization and a fresh namespace. Its bounded repair should stage that checked archive to a durable remote path, verify its SHA and the extracted source against it, and report archive/source presence before deciding whether tests may start.

Controller cleanup independently recorded all three validation VMs as `VM deallocated`. No retry was performed.
