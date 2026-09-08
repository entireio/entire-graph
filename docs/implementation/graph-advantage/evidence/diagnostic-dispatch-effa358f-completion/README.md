# Paused completion-diagnostic controller WIP

Status: **incomplete, unreviewed, and not executable**. This directory preserves
the text-only work in progress after the user paused all implementation and
runtime work. It is a portability checkpoint for another checkout, not a
prepared diagnostic or runtime evidence package.

**DO NOT EXECUTE** this controller, collector, or any copied command until the
user resumes the work, the implementation and manifests are completed, the
focused synthetic tests and local preflight are run, and the resulting package
passes code review.

The intended draft direction was one detached, resource-bounded
OFF/full/snapshot completion observation with no elapsed-time product deadline.
The draft controller and collector were being changed to use one durable
transient systemd service, explicit 14 GiB and 512-task limits, OS-level network
isolation, asynchronous observation, and terminal cleanup. Those interfaces
were interrupted before integration or validation.

Known incomplete pieces:

- `controller/manifest.json` is a stale copy from the earlier retained effa358f
  diagnostic package. It does not describe this completion diagnostic.
- `batch-manifest.json`, `control-hashes.txt`, `control-files.tar.gz`, and a
  finalized runtime manifest do not exist.
- `controller/controller.py`, `collector/run_remote.py`,
  `collector/observe_remote.py`, and `collector/cloud.py` are unfinished drafts.
- `controller/test_completion_controls.py` was independently authored against
  the planned interfaces. It was syntax-checked only and was not run against
  the unfinished implementation.
- The copied `controller/test_controller.py` was not adapted or run for this
  package.
- No integrated unit tests, controller preflight, generated-script syntax
  check, package freeze, credential/archive review, or independent code review
  was completed before the pause.

Before the pause, only `controller/controller.py` was successfully checked with
`python3 -m py_compile`. The independent test author separately syntax-checked
`controller/test_completion_controls.py` and ran a whitespace diff check. These
checks do not establish behavioral correctness.

No cloud command, VM start, dispatch or worker claim, evaluator invocation,
product invocation, or corpus run was launched from this WIP. The retained
`build-manifest.json`, `full-check-gate.json`, and corpus files are copied input
bindings only; their presence does not admit this package for execution.
