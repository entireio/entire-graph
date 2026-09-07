# PACT for Entire - three-minute presenter script

## 0:00-0:30 - State the problem

"A new feature can work while an unchanged caller breaks. PACT connects a confirmed policy, a dependency graph and an executed check so a reviewer can inspect why a change is acceptable. This is a deliberately seeded 24-case permission pilot."

## 0:30-1:10 - Reproduce D1

In the local workbench, show the four confirmed requirements. Select **D1 - Dynamic dispatch regression**, keep **Local**, and click **Run review**. The baseline automatically becomes D0.

"Guests may preview public content, but must never export it. The helper change accidentally permits public exports. The unchanged export caller uses dynamic lookup, so we cannot assert a resolved Graph path. PACT falls back to all four confirmed requirements and finds these two actual violations."

Point to both guest-export findings and their baseline-pass/candidate-fail evidence. Point to **Behavioral evidence - no Graph relationship asserted** and the visible source diagnostics.

## 1:10-1:40 - Show correction

Click **Review corrected version**. D2 should show ten candidate passes.

"The correction narrows the exception to preview. All ten approved candidate assertions pass, but the analysis stays partial. Passing execution does not repair missing Graph relationships or the missing original policy-source Checkpoint."

## 1:40-2:15 - Show the comparison and cloud proof

Open the public cloud-verification manifest or run history. Show D1 changed-file: two observations and zero failures; Graph fallback: ten and two; all registered: ten and two; full matrix:24 and two. Show the corrected D2 Databricks report: ten passes.

"All five real Databricks jobs succeeded and match local assertions. We verify payload and evidence hashes, repeat receipt execution idempotently, and recover the same evidence from Delta history. Workspace access is permissioned; the published reports and offline replay let you inspect the result without an account."

Do not submit a new cloud job during this short demonstration unless the judge explicitly wants a fresh run and there is time for startup.

## 2:15-3:00 - Explain trust and limits

Show Checkpoint90d4a736f01e and the replay download.

"A fresh session independently reviewed the implementation and all five completed cloud runs. Its authentic transcript is captured in this Checkpoint. Original policy/pre-noon capture gaps are disclosed; this later review cannot backdate them. A clean installed reproducer detects D1's two failures and D2's correction without the original checkout or cloud credentials. Next, we would evaluate larger labelled applications and add more scenario adapters."

Fallback: play PACT-Demo.mp4, a conversion of the real sampled browser recording. It is a short silent workflow clip, not a narrated three-minute presentation. Keep BUILDATHON.md and the pitch deck available beside it.

Do not claim universal coverage, faster execution, autonomous repair, native continuous capture, or broad evaluation of every language in the upstream repository.
