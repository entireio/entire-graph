#!/usr/bin/env python3
"""Build a bounded, sanitized index of status-A legacy evidence text files."""
from __future__ import annotations
import hashlib,json,re,subprocess
from pathlib import Path
EXTENSIONS={'.txt','.log','.stdout','.stderr','.exit'}
EXCLUDE=('canonical-v1/','cleanup/','diagnostic-dispatch-effa358f-completion/')
PROTECTED=('check-25887f69-linux-full/review/local-prep-dir.txt',)
TEST=re.compile(r'---\s+(PASS|FAIL|SKIP):\s+([^\s(]+)',re.I)
PLAIN_OUTCOME=re.compile(r'^\s*(ok|PASS|FAIL)\s*[: ]?\s*([^\s]+)?',re.I)
PLAIN_SKIP=re.compile(r'^\s*SKIP:\s+([^\s]+)(?:\s+(.*))?$',re.I)
GO_TEST_DETAIL=re.compile(r'^\s+[^:\s]+_test\.go:\d+:\s*(.+)$')
GO_TEST_START=re.compile(r'^\s*===\s+RUN\s+(.+)$')
PY_TEST_OUTCOME=re.compile(r'^\s*(test\S*)\s+\(([^)]+)\)\s+\.\.\.\s+(ok|FAIL|ERROR|skipped)(?:\s+(.+))?$',re.I)
HASH=re.compile(r'(?i)\b[0-9a-f]{64}\b'); COMMIT=re.compile(r'(?i)\b[0-9a-f]{40}\b')
UNCLASSIFIED=re.compile(r'\b(?:panic|fatal|assert(?:ion)?|mismatch|error|failure|failed|warning)\b',re.I)
SCALARS=(('exit',re.compile(r'["\']?\b(?:exit(?:_code)?|return code)["\']?\s*[=: ]\s*["\']?(-?\d+)',re.I)),('status',re.compile(r'["\']?\bstatus["\']?\s*[=: ]\s*["\']?([A-Za-z0-9_. -]{1,80})',re.I)),('terminal',re.compile(r'\b(?:deallocat\w*|terminal|shutdown|timed?\s*out|timeout)\b[: ]*(.{0,180})',re.I)))
def clean(s):
 return clean_detail(s)['value']
def clean_detail(s):
 s=re.sub(r'https?://\S+','<url>',s); s=re.sub(r'(?<!\w)(?:/Users|/home|/private/tmp|/tmp|/opt|/usr/local|/var)/[^\s,;]+','<local-path>',s); s=re.sub(r'(?i)(sig|se|sv|token|password|secret)=([^&\s]+)',r'\1=<redacted>',s); s=re.sub(r'\s+',' ',s).strip(); return {'value':s[:220],'truncated':len(s)>220}
def role(path):
 n=path.name.lower()
 if n.endswith('.exit'): return 'exit marker'
 if n.endswith('.stderr'): return 'stderr diagnostic'
 if n.endswith('.stdout'): return 'stdout result'
 if 'profile' in n or 'pprof' in n: return 'profile summary'
 if 'tim' in n or 'bench' in n: return 'timing or benchmark observation'
 return 'text evidence'
def git_blob(repo,p,rev='6c1ac1d188af711e7a0e7e361bfc3e4f139ee617'):
 try:return subprocess.check_output(['git','rev-parse',f'{rev}:{p}'],cwd=repo,text=True,stderr=subprocess.DEVNULL).strip()
 except subprocess.CalledProcessError:return None
def extract(path,repo,run_id=None,blob_rev='6c1ac1d188af711e7a0e7e361bfc3e4f139ee617',raw=None):
 raw=path.read_bytes() if raw is None else raw; text=raw.decode('utf-8','replace'); tests=[]; scalars=[]; scalar_total=0; unclassified=0; hashes=[]; commits=[]; pending_test_details=[]; active_test=False
 for line in text.splitlines():
  if GO_TEST_START.match(line): pending_test_details=[]; active_test=True
  detail=GO_TEST_DETAIL.match(line)
  if detail: pending_test_details.append(clean_detail(detail.group(1)))
  m=TEST.search(line)
  if m:
   item={'outcome':m.group(1).lower(),'name':clean(m.group(2))}
   if item['outcome'] in ('fail','skip') and pending_test_details: item['details']=pending_test_details[:]
   tests.append(item); pending_test_details=[]; active_test=False
  else:
   py=PY_TEST_OUTCOME.search(line)
   if py:
    marker=py.group(3).lower(); outcome='pass' if marker=='ok' else 'skip' if marker=='skipped' else 'fail'
    item={'outcome':outcome,'name':clean(py.group(1)),'container':clean(py.group(2)),'observed_marker':marker}
    if py.group(4): item['details']=[clean_detail(py.group(4))]
    if item not in tests: tests.append(item)
   else:
    skip=PLAIN_SKIP.search(line)
    if skip:
     item={'outcome':'skip','name':clean(skip.group(1))}
     if skip.group(2): item['details']=[clean_detail(skip.group(2))]
     if item not in tests: tests.append(item)
    else:
     m=PLAIN_OUTCOME.search(line)
     if m and m.group(1).lower() in ('ok','pass','fail'):
      name=clean(m.group(2) or 'package-or-suite')
      item={'outcome':'pass' if m.group(1).lower() in ('ok','pass') else 'fail','name':name}
      if item not in tests: tests.append(item)
  for kind,pat in SCALARS:
   m=pat.search(line)
   if m:
    scalar_total += 1
    scalars.append({'kind':kind,**clean_detail(' '.join(m.groups()))})
  if UNCLASSIFIED.search(line) and not TEST.search(line) and not any(p.search(line) for _,p in SCALARS):
   unclassified += 1; scalar_total += 1
   scalars.append({'kind':'unclassified_signal',**clean_detail(line)})
  if any(k in line.lower() for k in ('source','sha','hash','manifest','binary','commit')):
   hashes.extend(HASH.findall(line)); commits.extend(COMMIT.findall(line))
 counts={k:sum(t['outcome']==k for t in tests) for k in ('pass','fail','skip')}
 return {'path':path.relative_to(repo).as_posix(),'git_blob':git_blob(repo,path.relative_to(repo).as_posix(),blob_rev),'legacy_role':role(path),'bytes':len(raw),'lines':len(text.splitlines()),'sha256':hashlib.sha256(raw).hexdigest(),'run_id':run_id,'named_tests':tests,'named_test_counts':counts,'scalar_observations':scalars,'scalar_observations_truncated':False,'scalar_observation_count':scalar_total,'unclassified_signal_count':unclassified,'source_hashes':sorted(set(hashes)),'source_commits':sorted(set(commits))}
def build(repo):
 root=repo/'docs/implementation/graph-advantage/evidence'; rows=[]
 baseline='3a2a715fad1948e83dc7ebe0d307377ba29e065a'; precleanup='6c1ac1d188af711e7a0e7e361bfc3e4f139ee617'
 inventory_from_git=True
 try:
  inventory=subprocess.check_output(['git','diff','--name-status',baseline,precleanup],cwd=repo,text=True,stderr=subprocess.DEVNULL).splitlines()
  paths=[line.split('\t',1)[1] for line in inventory if line.startswith('A\t')]
 except (subprocess.CalledProcessError,FileNotFoundError):
  inventory_from_git=False
  paths=[p.relative_to(repo).as_posix() for p in root.rglob('*') if p.is_file()]
 for rel in sorted(paths):
  p=repo/rel
  if p.suffix.lower() not in EXTENSIONS or 'docs/implementation/graph-advantage/evidence/canonical-v1/' in rel or 'docs/implementation/graph-advantage/cleanup/' in rel or 'diagnostic-dispatch-effa358f-completion/' in rel or rel.endswith('check-25887f69-linux-full/review/local-prep-dir.txt'): continue
  if inventory_from_git:
   try: raw=subprocess.check_output(['git','show',f'{precleanup}:{rel}'],cwd=repo,stderr=subprocess.DEVNULL)
   except subprocess.CalledProcessError: continue
  elif p.is_file(): raw=p.read_bytes()
  else: continue
  m=re.search(r'(check-[0-9a-f]+(?:-[^/]+)?|diagnostic(?:s)?-linux-[0-9a-f]+(?:-[^/]+)?|diagnostic-dispatch-[0-9a-f]+)',rel)
  run_id=m.group(1) if m else None; x=extract(p,repo,run_id,precleanup,raw)
  meaningful=bool(raw.strip())
  if meaningful:
   x['disposition']='retain_pending_consumer_review'; x['disposition_reason']='non-empty legacy text requires per-file replacement equivalence review, including unrecognized failures or terminal detail'
   canon=f'docs/implementation/graph-advantage/evidence/canonical-v1/runs/{run_id}.json' if run_id and (repo/f'docs/implementation/graph-advantage/evidence/canonical-v1/runs/{run_id}.json').is_file() else None
   x['replacement_proposal']={'path':'docs/implementation/graph-advantage/cleanup/log-observations.json','status':'candidate_only','reason':'structured observation record is a possible replacement; verify complete text-derived detail and consumer coverage before removal'}
  else:
   x['disposition']='remove_as_empty_or_uninformative'; x['disposition_reason']='empty or whitespace-only file'; x['replacement_proposal']={'path':None,'status':'none_needed','reason':'no content'}
  rows.append(x)
 contents={}
 files=[]
 for row in rows:
  content_id=row['sha256']
  if content_id not in contents:
   contents[content_id]={k:v for k,v in row.items() if k not in ('path','git_blob','legacy_role','run_id','disposition','disposition_reason','replacement_proposal')}
  files.append({k:v for k,v in row.items() if k not in ('named_tests','named_test_counts','scalar_observations','scalar_observations_truncated','scalar_observation_count','unclassified_signal_count','source_hashes','source_commits')} | {'content_id':content_id})
 return {'schema':'graph-advantage-log-observations-v3','baseline_commit':baseline,'precleanup_head':precleanup,'scope':'status-A legacy evidence text/log/output files only; canonical-v1, cleanup, completion WIP, and protected untracked files excluded','file_count':len(files),'content_count':len(contents),'extension_counts':{e.lstrip('.'):sum(x['path'].endswith(e) for x in files) for e in sorted(EXTENSIONS)},'content_records':list(contents.values()),'files':files}

def main():
 import argparse
 a=argparse.ArgumentParser();a.add_argument('--repo',type=Path,required=True);a.add_argument('--output',type=Path,required=True);args=a.parse_args();args.output.write_text(json.dumps(build(args.repo.resolve()),indent=2)+'\n')
if __name__=='__main__':main()
