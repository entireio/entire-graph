"""Require complete, equivalent one-pair coverage before broad measurement."""
import hashlib,json,pathlib,math
SCENARIOS=('cold','unchanged','one-edit','ten-edit','rename','delete','branch-switch','manifest-edit')
def sha(p):return hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest()
def validate(directories,assignments,identities):
 expected={(r['repository'],r['profile'],v,s,a) for group in assignments for r in group for v in ('snapshot','search') for s in SCENARIOS for a in (False,True)}
 seen={};evidence=[]
 if len(directories)!=len(assignments):raise ValueError('All worker canary artifacts are required')
 for directory in directories:
  directory=pathlib.Path(directory);meta=json.loads((directory/'campaign-manifest.json').read_text())
  if meta.get('trials')!=1:raise ValueError('Canary must have one pair per cell')
  for key,value in identities.items():
   if meta.get(key)!=value:raise ValueError('Canary identity mismatch: '+key)
  progress=json.loads((directory/'progress.json').read_text())
  if not progress.get('done') or progress.get('paused') or (directory/'pause.json').exists():raise ValueError('Canary did not finish cleanly')
  raw=directory/'campaign.ndjson';evidence.append({'path':str(raw),'sha256':sha(raw)})
  for line in raw.read_text().splitlines():
   row=json.loads(line);key=tuple(row.get(k) for k in ('repository','profile','verb','scenario','reuse'))
   if key not in expected or key in seen or row.get('trial')!=0:raise ValueError('Unexpected/duplicate canary cell')
   if row.get('status')!='ok' or row.get('partial_failures_count')!=0 or not row.get('source_unchanged'):raise ValueError('Canary result is not complete and fresh')
   for k in ('elapsed_ns','wall_ns','peak_rss_bytes'):
    if not isinstance(row.get(k),(int,float)) or isinstance(row.get(k),bool) or not math.isfinite(row[k]) or row[k]<=0:raise ValueError('Missing canary resource measurement')
   if not row.get('semantic_digest') or not row.get('source_digest'):raise ValueError('Missing canary identity')
   seen[key]=row
 if set(seen)!=expected:raise ValueError('Canary coverage incomplete')
 for key,row in seen.items():
  other=seen[key[:-1]+(not key[-1],)]
  if any(row[k]!=other[k] for k in ('semantic_digest','source_digest')):raise ValueError('Canary pair differs')
  if key[-1] and row['scenario']=='cold' and any(row[k]>other[k]*1.10 for k in ('elapsed_ns','peak_rss_bytes')):raise ValueError('Cold canary exceeds 10% latency/RSS screen; diagnose before expansion (not a statistical conclusion)')
 return {'status':'pass','identities':identities,'observations':len(seen),'evidence':evidence}
