# ADR: retain provider policy in the operation capture

Status: accepted, experimental and default-off
Date: 2026-09-05

P1-C and the shared capture contract require repeated consumers of a file to
observe one byte sequence. Root and nested .gitignore files were previously
parsed during enumeration outside the operation store, then could be reread as
source. Reuse the same bounded store for provider-owned policy reads and later
source, resolver and snippet reads. Existing confined policy readers retain
permission checks, size limits and error semantics. Retain missing optional
inputs as absent-policy observations, separately from unavailable source.

In worktree mode, seed the store with the initially captured explicit policy,
including overlapping .gitignore paths, before loading root/nested policy.
The HEAD view instead captures committed root/nested policy bytes at the exact
requested revision; live explicit filtering policy remains separately bound in
the operation identity. Capture the private info/exclude bytes after existing
Git indirection validation. Policy payloads share the 64 MiB retained-memory
ceiling and private spill lifetime with other source inputs. Policy storage/read
failure prevents a completed manifest. Observations remain output-bounded.

The identity now includes the provider's individual root/nested/vendor policy
observations. Git's own listing, global configuration and metadata authorization
probes remain opaque, and source enumeration is not an atomic repository view.
The manifest states this coverage limit. Capturing provider-owned bytes does not
claim to snapshot Git internals or enable persistent worktree graph reuse.

Alternative rejected: rereading policy for later source output mixes versions;
retaining unlimited policy strings outside the source store defeats its memory
bound. Replacing Git's listing machinery is outside this preservation change.

Independently authored tests cover worktree and HEAD policy/source mutation,
explicit overlapping policy paths, nested/vendor reads, private exclude input,
spill/reuse/cleanup, optional absence and fatal policy IO. Existing ignore/vendor
and source-confinement tests remain required. Rollback: disable extraction reuse,
compiler and deeper/ranking capture features to use the unchanged default path.
Comparative evaluation remains deferred; this ADR makes no performance claim.
