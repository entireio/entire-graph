# P1 corpus preparation

`prepare_p1_corpus.py` materializes the approved pinned shallow repositories,
the `88dd1dc9` Entire Graph archive, and the seeded 2,000-file synthetic
fixture under `/Users/thomi/Projects/graph-advantage-p1-corpus`. It writes the
source inventory and deterministic ten-path samples to
[`corpus-manifest.json`](corpus-manifest.json).

`p1_scenario.py` is the coordinator interface:

```text
p1_scenario.py apply <repo-id> <cold|unchanged|one-edit|ten-edit|rename-delete|branch-switch|manifest-edit>
p1_scenario.py reset <repo-id>
p1_scenario.py digest <repo-id>
```

`digest` includes the effective tracked/indexed paths and bytes, `.graphignore`,
`.git/info/exclude`, and Git refs. Scenario changes are confined to the named
fixture and reset to its recorded seed commit. These scripts prepare inputs;
they do not invoke Entire Graph or run measurements.
