#!/usr/bin/env python3
"""Score the canonical frozen v1 contract corpus; no selection or tuning."""
import json
import pathlib
import sys

from canonical_evidence import load_run_and_rows, source_artifact_name

source = pathlib.Path(sys.argv[1])
run, all_rows = load_run_and_rows(source)
source_name = source_artifact_name(run)
source_record = run.get('source_records', {}).get(source_name, {})
rows = [row for row in all_rows if row['category'] != 'interface_candidates']
result = {'manifest': source_record['manifest'], 'label_origin': run['quality']['label_origin'],
          'compiler_contract_pass': run['quality']['compiler_contract_pass'], 'categories': all_rows}
for arm in ('static', 'compiler'):
    counts = [row[arm + '_counts'] for row in rows]
    total = {name: sum(item[name] for item in counts) for name in
             ('required', 'returned', 'true_positive', 'false_positive', 'missed')}
    for metric in ('precision', 'recall'):
        applicable = [item[metric] for item in counts if item[metric] is not None]
        total['macro_' + metric] = sum(applicable) / len(applicable) if applicable else None
        total['macro_' + metric + '_categories'] = len(applicable)
    total['micro_precision'] = total['true_positive'] / total['returned'] if total['returned'] else None
    total['micro_recall'] = total['true_positive'] / total['required'] if total['required'] else None
    result[arm + '_direct'] = total
s, c = result['static_direct'], result['compiler_direct']
result['synthetic_direct_advantage'] = (c['micro_recall'] > s['micro_recall'] and
    s['micro_precision'] is not None and c['micro_precision'] >= s['micro_precision'])
result['real_world_release_gate'] = 'not established: synthetic compiler-checked labels are not independent adjudication'
print(json.dumps(result, indent=2, sort_keys=True))
