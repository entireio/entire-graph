#!/usr/bin/env python3
"""Summarize independently generated P1.5 component timings; no speedup claim."""
import collections
import json
import statistics
import sys

rows = [json.loads(line) for line in open(sys.argv[1])]
groups = collections.defaultdict(list)
for row in rows:
    groups[(row['language'], row['phase'])].append(row)

def percentile(values, fraction):
    values = sorted(values)
    at = (len(values)-1)*fraction
    lower = int(at)
    return values[lower] + (values[min(lower+1, len(values)-1)]-values[lower])*(at-lower)

summary = {'observations': len(rows), 'interpretation': 'Isolated component probes overlap the full relation pass. Raw/full ratios are cost indicators, not additive attribution or end-to-end savings.', 'groups': [], 'precomputed_import_pairs': []}
for (language, phase), values in sorted(groups.items()):
    assert len(values) == 30
    elapsed = [row['elapsed_ns'] for row in values]
    full = statistics.median(row['elapsed_ns'] for row in groups[(language, 'forEachRelation')])
    summary['groups'].append({'language': language, 'phase': phase, 'trials': len(values), 'median_ns': statistics.median(elapsed), 'p95_ns': percentile(elapsed, .95), 'median_relative_to_full': statistics.median(elapsed)/full, 'result_counts': sorted(set(row['result_count'] for row in values)), 'corpus_sha256': values[0]['corpus_sha256']})
for language in sorted(set(row['language'] for row in rows)):
    baseline = {row['trial']: row for row in groups[(language, 'forEachRelation')]}
    reuse = {row['trial']: row for row in groups[(language, 'forEachRelation_precomputed_imports')]}
    deltas = [reuse[trial]['elapsed_ns']/baseline[trial]['elapsed_ns']-1 for trial in sorted(baseline)]
    summary['precomputed_import_pairs'].append({'language': language, 'paired_trials': len(deltas), 'median_relative_change': statistics.median(deltas), 'min_relative_change': min(deltas), 'max_relative_change': max(deltas), 'equivalence': all(row['raw_import_equivalence'] for row in groups[(language, 'forEachRelation')])})
json.dump(summary, sys.stdout, indent=2)
print()
