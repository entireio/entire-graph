"""Summarize raw paired observations without filtering failures or tuning."""
import collections, json, math, pathlib, statistics, sys
source=pathlib.Path(sys.argv[1])
rows=[json.loads(line) for line in source.read_text().splitlines()]
groups=collections.defaultdict(list)
for row in rows: groups[row['size'],row['profile'],row['scenario']].append(row)
summary={'source':source.name,'observations':len(rows),'semantic_equal':all(row['equal'] for row in rows),'scope':'generated warm-process characterization; not a release gate','groups':[]}
for key,group in sorted(groups.items()):
    arms={reuse:[r['elapsed_ns']/1e6 for r in group if r['reuse']==reuse] for reuse in [False,True]}
    stats={str(reuse):{'n':len(values),'median_ms':statistics.median(values),'p95_ms':sorted(values)[math.ceil(.95*len(values))-1]} for reuse,values in arms.items()}
    summary['groups'].append({'size':key[0],'profile':key[1],'scenario':key[2],'arms':stats,'median_change_percent':100*(statistics.median(arms[True])/statistics.median(arms[False])-1)})
print(json.dumps(summary,indent=2))
