package agentsetup

import "fmt"

// Mode is a repository-wide guidance policy, shared by Graph and Brain.
// An empty value inherits the installed mode (normal for a new repository).
type Mode string

const (
	ModeNormal Mode = "normal"
	ModeStrict Mode = "strict"
)

// SelectMode validates the mutually exclusive CLI switches.
func SelectMode(strict, normal bool) (Mode, error) {
	if strict && normal {
		return "", fmt.Errorf("--strict and --normal are mutually exclusive")
	}
	if strict {
		return ModeStrict, nil
	}
	if normal {
		return ModeNormal, nil
	}
	return "", nil
}

const strictCommonGuide = `Choose tools by question class, not convenience.
Knowing a file or symbol's location permits direct source inspection for that
location; it NEVER exempts relationship questions from Graph or historical
questions from Brain. Small edits, follow-ups, read-only reconnaissance, specs,
and reviews are subject to the same rules for the enabled products.
Do not re-read files or retrieved records that Graph or Brain already answered for.
A follow-up read MUST address a specific missing fact, stale result, heuristic
relationship, or source the payload did not carry. A result marked [complete] IS
that source, so it is not one of those cases. Repeating
an answered question in source is not verification.
` + verificationGuide + `
Do not silently substitute source inspection, text search, or recollection for
a required Graph or Brain query. Attempt the required tool first. Record missing
or unsupported capabilities once per session; record query failures and coverage
gaps when encountered. Every fallback must trace to recorded evidence relevant
to that question. State the limitation and fallback to the user.
Do not automatically install, configure, or repair tools.
`

const strictGraphWorkflow = `Use Graph for code discovery, structural understanding, and semantic change analysis.
ALWAYS check Graph availability and version once per session, at the first
structural question or code discovery task, BEFORE querying or using a fallback:

    entire graph version --json
    entire graph capabilities --json

Validate the output, not just the exit status: version must identify the
entire-graph provider, and capabilities must report the features and semantic
coverage needed for the question. Use command help to verify required commands
when the capability report does not identify them. Record the version, supported
capabilities, and an available, missing, or unsupported result for the session.
A dev version is not evidence of an outdated binary; check its capabilities.
EVERY tool-unavailability fallback MUST trace to this recorded check and be
explained to the user. A later query failure or coverage gap must be recorded
separately and only permits a fallback for the affected question. Never skip the
preflight because a file is known or a grep looks easier.

Start needed code discovery with:

    entire graph query --repo . --profile full --head --query "<task>"

Code relationships — callers, callees, dependents, implementors, type consumers,
routes, and blast radius — MUST be answered with Graph first, ALWAYS, even when
the task names the exact file or you have already read the implementation.
Use neighbors for a specific relationship and impact for blast radius.
Grep may supplement a Graph answer; it NEVER replaces the required Graph query.

ALWAYS run impact before editing exported code or shared-library symbols.
Writing a spec, scoping a refactor, reviewing a proposed change, and challenging
a design count as editing for this rule. Cover every affected exported or
shared-library symbol and cite its impact result. Do not infer that a change
is local merely because its implementation occupies one file.

Use Graph diff, commit, or checkpoint for entity-level revision comparisons.
Read the textual diff as needed to inspect the implementation.

Text occurrences the graph does not model — environment-variable names,
configuration values, string keys, and package-manifest entries — belong in
text search. One legitimate text search does not authorize using grep to answer
a relationship question.

ALWAYS use --head for interactive Graph queries by default, including the first
query. Use the working tree ONLY when the answer depends on uncommitted edits;
then omit --head. Working-tree queries rebuild the snapshot on every call and
never cache; --head permits reuse of the committed-tree cache. Never present
committed-tree results as analysis of edits they do not contain.
Before several --head queries, prewarm the matching cache variant.
Ask per-symbol commands before bulk streams. Never dump unfiltered whole-repo
NDJSON into context; redirect it to a file and extract the needed records.

An empty Graph result is NEVER proof of no consumers. Check completeness,
resolution, and confidence. Verify heuristic relationships in focused source.
For dependency injection, inspect type consumers as well as CALLS. Check
dynamic dispatch and unmodeled relationships with targeted supplementary
inspection. State unresolved coverage limits in the answer.
`

const strictBrainWorkflow = `Use Brain for task context, retained knowledge, and history.
ALWAYS check Brain availability and version once per session, at the first
substantive task or historical question, BEFORE the brief or any fallback:

    entire brain version
    entire brain capabilities --json

Validate the version output and structured capability report, not just the exit
status. Confirm the required features are supported; use command help when the
report does not identify a required command. Record the version, supported
capabilities, and an available, missing, or unsupported result for the session.
A dev version is not evidence of an outdated binary; check its capabilities.
EVERY tool-unavailability fallback MUST trace to this recorded check and be
explained to the user. Capabilities do not prove repository readiness: record
later query failures or missing-index evidence separately, and limit each
fallback to the affected question. Never defer the preflight until a query fails.

ALWAYS begin a substantive task with:

    entire brain brief "<task>" --json

Run the brief before substantive source exploration or implementation.
Familiarity with the repository, supplied file paths, and remembered context
do not waive this requirement. Specs, reviews, and refactoring plans count
as substantive tasks.

Previous decisions, rationale, prior attempts, documentation, durable project
facts, and earlier sessions MUST be investigated with Brain first, ALWAYS.
Knowing what the current code does does not establish why it was written.
Do not substitute recollection, source comments, or a plausible explanation
for retrieval of the recorded evidence.

Use Brain retrieval for recorded knowledge. Use entities history when connecting
code changes to earlier checkpoints or sessions. ALWAYS retrieve relevant prior
decisions and attempts before proposing to replace an existing approach.
ALWAYS consult relevant retained knowledge when reviewing a change.
A general brief does not replace targeted retrieval for a historical question
that the brief did not answer.

Cite the retrieved evidence for historical claims. Distinguish recorded rationale
from inference. An empty result means no evidence was found; it does not establish
that a decision or previous attempt never existed.

Durable facts are Brain's episodic memory. When you establish something durable
that the code does not already state - a decision and its rationale, an
invariant, a gotcha that cost you time - RECORD it before finishing:

    entire brain remember "<fact>" --path <category.subcategory.type> --json

Categories are architecture, constraints, preferences, project and workflow.
Omitting --path classifies the fact for you and requires a supported coding
agent on PATH; pass --path when none is guaranteed. No MCP tool writes a fact,
so this is a CLI call; do not go looking for one. Retrieve with recall and
re-check anchors with verify. A fact you author has no source anchor, so verify
reports it unverifiable-here - that is expected, not a failure.

Brain semantic answers refer to a stored index, which may differ from current
working-tree source. Verify claims about present behavior against current source
and executed tests.
`

const strictCombinedWorkflow = strictBrainWorkflow + "\n" + strictGraphWorkflow + `
Both sets of mandatory rules apply.
Brain answers recorded-context and historical questions; Graph answers structural
questions and entity-level revision comparisons.

Locations supplied by Brain may replace a Graph location query ONLY.
They NEVER replace required neighbors or impact analysis.
Graph results NEVER replace required retrieval of prior decisions or attempts.
When a task involves both relationships and history, query both tools.
`

func guideFor(active map[string]bool, mode Mode) string {
	if mode == ModeStrict {
		if active["brain"] {
			if active["graph"] {
				return "# Entire repository agent guide — Graph and Brain\n\n" + strictCombinedWorkflow + "\n" + strictCommonGuide
			}
			return "# Entire repository agent guide — Brain\n\n" + strictBrainWorkflow + "\nUse Brain's semantic inspection tools for relevant code questions.\n\n" + strictCommonGuide + "\n" + brainReference
		}
		return "# Entire repository agent guide — Graph\n\n" + strictGraphWorkflow + "\n" + strictCommonGuide
	}
	if active["brain"] {
		if active["graph"] {
			return CombinedGuide
		}
		return BrainGuide()
	}
	return GraphGuide
}
