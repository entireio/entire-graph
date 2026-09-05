#!/usr/bin/env python3
"""Score the frozen v1 contract corpus; no corpus selection or tuning."""
import json
import pathlib
import sys

source = pathlib.Path(sys.argv[1])
data = json.loads(source.read_text())
rows = [row for row in data['rows'] if row['category'] != 'interface_candidates']
result = {'manifest': data['manifest'], 'label_origin': data['label_origin'],
          'compiler_contract_pass': data['compiler_contract_pass'], 'categories': data['rows']}
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
