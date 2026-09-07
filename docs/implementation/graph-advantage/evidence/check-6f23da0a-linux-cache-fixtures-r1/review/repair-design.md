# Bounded follow-up repair design

The r1 gate depended on `/tmp/diagnostics-linux-6f23da0a-source.tar.gz`, the temporary download path used by the earlier pinned build. A fresh, separately authorized attempt should use a new remote and blob namespace and:

1. Upload the checked-in `diagnostics-linux-6f23da0a/source.tar.gz` without overwrite and bind its expected SHA-256 `e1a42113a94d043c79833ea9d16ae568965bdb50efdd7a5ccc5f3186c883032b`.
2. Download it to a new durable path under `/opt/graph-validation/`, then record whether both that archive and `/opt/graph-validation/diagnostics-linux-6f23da0a/src` exist.
3. Reject a missing path before toolchain or test execution; verify the durable archive hash, extract it into a fresh temporary directory, and compare that directory byte-for-byte with the existing source root.
4. Only after those gates pass, run the already reviewed toolchain checks and exact three-test command once.

This is a design only. No VM was restarted and no retry was made while producing it.
