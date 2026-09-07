# Source-bound Linux evaluator compile

This evidence records one compiler-only build of the P1 evaluator from source
commit `25887f6954fc06e35bc3a7c699e3c524213dada4`. The existing immutable
full-check source at `/opt/graph-validation/check-25887f69-linux-full/src` was
verified against archive SHA-256 `16f5968934aaff779eab8801e27f0183f44a8dad86e5f7ed6edc5358811fb453`
and the 2,292-entry normalized Git-mode/content manifest SHA-256
`1691ddc7be3f1e8f274779197e0b741a04a3fe717b063488effdc08a2d85fb07`
before compilation. The same manifest was reproduced after compilation.

Go 1.26.1 and gopls 0.20.0 matched their recorded versions and executable
hashes. The selected Go executable resolved to `/usr/local/go/bin/go`; `HOME`
remained unset. The no-egress Go and Git environment was retained.

The single command was `go test -c -o <fresh-remote-root>/p1-evaluator
./internal/sem`. It compiled the test binary without executing it. The binary
SHA-256 is `661629c22ab2f633a92871a4db1de5b56612b81d43c8bd91c6496d2ecc7f589a`.
Build, overall, and transport exits are zero, the result namespace was fresh,
no retry occurred, and all three VMs were deallocated after transfer.

No evaluator, product, corpus, diagnostic, benchmark, comparison, or claim was
invoked by this build step.
