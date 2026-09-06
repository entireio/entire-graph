# Neutral development-fixture review packet

This packet contains all six existing synthetic development tasks and their
16 original source files. Every source byte hash matches the existing fixture
manifest. It contains no retrieval results, ranking scores, or suggested
relevance labels. `review.json` is deliberately unanswered (`null`, not an
empty list meaning no relevant sites).

For each task in `tasks.json`, inspect its query and all listed source files.
Open each file using `stored_path`; use its original `path` when recording
labels. Go inputs have a `.go.txt` storage suffix to preserve original bytes
when repository formatting runs. The suffix is packaging only, not a change
to fixture language, contents, logical paths or hashes.
Record relevant and required sites, allowed implementation candidates,
forbidden sites and any covering-test relevance in `review.json`. Identify a
site consistently by file plus qualified symbol or an explicit source range.
Record reviewer identity, review time, reasoning and unresolved disagreements.
Do not infer a label from a task category or filename alone.

These six tasks are development fixtures only. Reviewing them cannot create a
repository-disjoint held-out set or satisfy the realistic change requirement
for P3. The remaining inputs are:

- P3: realistic Go, TypeScript, Python and configuration/route changes with
  affected and unaffected edited sites, unedited affected sites, and relevant
  covering tests adjudicated independently of graph output.
- P4: pre-fix task descriptions and repository/task-family split review, with
  independently adjudicated relevance and all required sites. The held-out
  split must be populated and frozen before it is used.

No study is executed or release gate claimed by preparing this packet. Old
labels and scores remain preserved in their original artifacts and are not
presented as reviewer decisions here. `provenance.json` records origins and
copy verification. Blank answers are missing evidence, never negative labels.
