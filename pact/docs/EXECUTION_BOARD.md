# PACT execution board

Event: 6 September 2026, E2 Graph Intelligence. All times IST. Full scope is retained in the root PACT_IMPLEMENTATION_GUIDE.md.

## Verified setup

- Repository: https://github.com/Shaurya002800/PACT
- Origin: entire://aws-ap-south-1.entire.io/gh/shaurya002800/pact (India; ready; fetch succeeded).
- GitHub push remote: github. Initial upstream history pushed successfully.
- Upstream base: 3a2a715fad1948e83dc7ebe0d307377ba29e065a from entireio/entire-graph.
- This was an empty standalone GitHub repository, populated with upstream history. It is not represented as a GitHub-created fork; confirm the organiser accepts this repository arrangement.
- Entire CLI 0.10.5; Graph 0.4.0; Python 3.12.12.
- Codex hooks installed. A captured implementation session must be verified before marking CP00 complete.
- Active implementation directory: /Users/shaurya/Desktop/BTW/PACT-event, created with `entire repo clone` through the India mirror. The earlier PACT directory is preserved as bootstrap material.
- A real fresh Codex session (01a074ee-4456-7fc3-a160-9f669a03611c) was captured, then hit the account limit before application code. The coordinator is continuing implementation and will explicitly attach its authentic session at milestones; this is manual attachment, not claimed automatic capture.
- No application implementation or application acceptance tests completed yet.
- Databricks final workspace supplied: https://dbc-c3d496ed-7dbd.cloud.databricks.com (organisation 7474653723260152); API authentication still to be established.

## Work

| Task | State | Owner | Next evidence |
| --- | --- | --- | --- |
| T01 repository/capture | in_progress | coordinator | CP00 and captured coding session |
| T02 baseline/contracts | not_started | implementation | 24 scenarios; 8 baseline assertions |
| T03 Checkpoints/Graph | not_started | implementation | real source and path |
| T04 local review | not_started | implementation | failing H1; passing H2 |
| T05 Databricks | blocked | coordinator/implementation | workspace URL and authenticated job |
| T06 interface | not_started | implementation | real review interaction |
| T07 reproducer | not_started | implementation | clean replay |
| T08 noon adaptation | not_started | team | actual 12:00 constraint |
| T09 evaluation | not_started | implementation | focused acceptance evidence |
| T10 demo/submission | not_started | team | rehearsed demo and receipt |

The new organiser participant guide supersedes the original schedule: submission is 15:00 IST, stable pre-noon checkpoint is 11:45, and sessions restart at noon. Full product scope remains unchanged. The setup took longer than the original 09:20 gate. Prioritise a real local review by 10:30; record observed progress and never backdate milestones. The PDF's E2 scope and rubric otherwise agree with the plan.

## Commit policy

Commit coherent, checked milestones and push them to the user-authorised PACT repository. Preserve authentic Checkpoints. Never force-push. Report any failed push; a local commit is not a successful remote milestone.
