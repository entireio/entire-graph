# PACT live demonstration

Use a local browser at 1440 × 900 or 1440 × 1000. Keep the real Databricks H1/H2 run links from BUILDATHON.md open. Presenters must understand that the defect is seeded, the graph is static evidence, and source provenance is still incomplete until the real approval Checkpoint is captured.

## Three-minute core script

**0:00–0:20 — User and promise.** “A reviewer approves a small feature, but a shared helper can affect callers nobody edited. PACT checks whether the change preserved confirmed policies.” Show the four approved requirements. R4 is the new public-preview feature; R1 still forbids exports.

**0:20–0:55 — Run H1.** Baseline `pact-B0`, candidate `pact-H1`, Local. Click Run review. Show that guest preview passes while two guest-export assertions fail. Do not describe missing provenance as verified intent.

**0:55–1:25 — Inspect why.** Open an R1 finding. Point to `can_access ← export_document`: the export caller's file did not change. Show exact expected deny / observed allow, baseline pass / candidate fail, commit identities and original path evidence.

**1:25–1:45 — Prove the selection matters.** Compare selectors, or open the genuine saved measurement. Changed-file selects two new-feature cases and misses the defect. Graph selects ten cases and finds both violations, agreeing with the 24-case reference. There is no measured speed advantage in this tiny pilot.

**1:45–2:10 — Correction.** Click Review corrected H2. All ten applicable candidate assertions pass; the failed H1 run stays in history. Download a replay bundle. The installed CLI replays H1 with exit 1 and H2 with exit 0 from a clean directory.

**2:10–2:40 — Databricks is real.** Open the saved remote H1 result and its actual job link. Show 18 persisted observations, 20 assertion rows, and SQL grouping R1/guest into two violations. Recover remote history from Delta. Show H2's zero violations and idempotent completed-receipt replay. Label saved views Recorded run.

**2:40–3:00 — Adaptation and limits.** After noon, replace this sentence with the actual constraint, the architectural decision it changed, and its passing test. Until then say the mandatory constraint is pending. Close with the bounded 24-case domain and the next supported assertion template; never claim a guaranteed win or universal security proof.

## Optional extensions

- H3 shows a harmless refactor does not cause a demonstrated violation.
- H4 remains a violation under the existing approved policies. Only demonstrate intentional revision after a human explicitly approves the alternative policy; never silently alter the live registry for the pitch.
- Read `pact-B0` in Checkpoint sources to show real ingestion. Explain that setup context is not the later policy approval and cannot be substituted for it.
- Filter the assertion table by passed, failed, not applicable and unresolved.

## Recovery and recording

Genuine report JSON and portable replay bundles are in `pact/docs/evidence/`. H1/H2 browser screenshots are generated from the actual local review when browser verification completes. Saved cloud evidence does not become a live cloud run because it is shown in a browser.

If a remote job times out, inspect the preserved job ID instead of blindly resubmitting. Continue with a clearly labelled recorded remote result and run the local reproducer. Capture a final short recording after the real Curveball behavior is integrated; do not manufacture footage or hide current blockers.
