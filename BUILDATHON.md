# Merge Seatbelt <!-- working name; rename when locked -->

## One-sentence summary
A pre-flight impact gate + merge dossier that makes every AI-agent edit carry its own structural safety evidence — compressing the "is this safe to merge?" decision from hours to minutes.

## Problem, intended user and why it matters
Reviewers of AI-generated changes get a diff with no journey: no intent, no blast radius, no record of what the agent tried and rejected. AI-agentic PRs wait ~5.3× longer for review pickup; ~46% of new production code is AI-written while ~96% of developers say they distrust it. The bottleneck has moved from writing code to deciding it is safe to merge. Intended user: any developer reviewing (or supervising) agent-written changes.

## Selected Entire track and why Entire is essential
**Track 2 — Build with Graph Intelligence.**
- Entire Graph (`impact`, `search`, co-change, semantic diff) supplies the structural evidence: blast radius before an edit, coupling anomalies after it. No graph → no gate, no dossier.
- Entire Checkpoints carry the intent: the gate writes the agent's stated plan and the graph evidence into the checkpoint at edit time, so the merge-time dossier can show *why*, not just *what*.
Entire is the data plane of the product, not a tracker bolted on.

## Architecture and main workflow
<!-- fill as built; keep modules independent for the Curveball -->
1. **Gate** (edit-time): before a risky symbol is edited, run `entire graph impact`; above threshold, require a stated plan; record plan + evidence in the checkpoint.
2. **Risk scorer**: blast radius size, co-change anomalies, untested affected callers → score with per-line evidence links.
3. **Dossier renderer** (merge-time): compiles gate events + graph findings + checkpoint intent into a verifiable safe-to-merge report.

## Entire Graph findings and verification
<!-- Record at least: one search/definition lookup, one impact analysis before a high-risk change, one final semantic-diff analysis. Every claim cites file:line and is verified against source/tests. Graph output is evidence, not oracle. -->

## Noon Curveball: what changed and how we adapted
<!-- 11:45 — commit stable state + pre-noon checkpoint recording intent, architecture, done/unresolved/risks.
12:00 — close agent session, receive constraint, start fresh session, reconstruct from checkpoint context, run graph impact on affected area, implement smallest complete response, test, checkpoint. -->

## Checkpoint links and what each checkpoint proves
<!-- Required four milestones:
1. Initial understanding and intended architecture — <link>
2. Last stable state before the Noon Curveball — <link>
3. Response to the Noon Curveball — <link>
4. Final implementation and verification — <link>
Capture decisions, rejected options, failures, assumptions, open risks. -->

## Setup, run and test instructions
<!-- exact commands from a clean checkout -->

## Databricks use, data sources and limitations (if applicable)
Not opted in. <!-- update if we opt in -->

## Known limitations and next steps
<!-- honest limits + credible next step toward production readiness -->
