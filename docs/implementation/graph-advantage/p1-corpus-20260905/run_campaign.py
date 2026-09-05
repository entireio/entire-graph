#!/usr/bin/env python3
"""One isolated Linux worker. No corpus selection or parameter tuning from results."""
import argparse,hashlib,importlib.util,json,os,pathlib,resource,shutil,signal,subprocess,time
SCENARIOS=['cold','unchanged','one-edit','ten-edit','rename','delete','branch-switch','manifest-edit']

def sha(path): return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()
def load_module(path):
 spec=importlib.util.spec_from_file_location('p1_scenario',path);mod=importlib.util.module_from_spec(spec);spec.loader.exec_module(mod);return mod

def prime(root):
 # Fixed ordered whole-readable-file prime, not a claim of disk-cold execution.
 count=total=0
 for base,dirs,files in os.walk(root,followlinks=False):
  dirs[:]=sorted(d for d in dirs if d!='.git' and not pathlib.Path(base,d).is_symlink())
  for name in sorted(files):
   p=pathlib.Path(base,name)
   if p.is_symlink() or not p.is_file():continue
   with p.open('rb') as f:
    while True:
     b=f.read(1024*1024)
     if not b:break
     total+=len(b)
   count+=1
 return {'files':count,'bytes':total}

def disk_bytes(root):
 total=0
 for base,dirs,files in os.walk(root,followlinks=False):
  dirs[:]=[d for d in dirs if not pathlib.Path(base,d).is_symlink()]
  for name in files:
   p=pathlib.Path(base,name)
   if not p.is_symlink() and p.is_file():total+=p.stat().st_size
 return total

def child(binary,config,work,timeout):
 config_path=work/'request.json';output=work/'response.json';log=work/'process.log'
 config_path.write_text(json.dumps(config));output.unlink(missing_ok=True)
 env=os.environ.copy();env.update({'ENTIRE_GRAPH_EXTRACTION_CORPUS_CONFIG':str(config_path),'ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT':str(output),'GOMAXPROCS':'4','GIT_CONFIG_GLOBAL':'/dev/null','GIT_CONFIG_SYSTEM':'/dev/null','GOPROXY':'off','GOSUMDB':'off','GOTOOLCHAIN':'local','GOTELEMETRY':'off'})
 for key in list(env):
  if key.startswith('ENTIRE_GRAPH_RANK_') or key in ['ENTIRE_GRAPH_EXTRACTION_EVALUATION','ENTIRE_GRAPH_RELATION_PROFILE','ENTIRE_GRAPH_COMPILER_LIVE','ENTIRE_GRAPH_COMPILER_QUALITY_OUTPUT']:env.pop(key)
 start=time.monotonic_ns()
 with log.open('wb') as f:
  proc=subprocess.Popen([str(binary),'-test.run=^TestExtractionCorpusMeasurement$','-test.count=1','-test.v'],env=env,stdout=f,stderr=subprocess.STDOUT,start_new_session=True)
  timed_out=False
  while True:
   pid,status,usage=os.wait4(proc.pid,os.WNOHANG)
   if pid:break
   if (time.monotonic_ns()-start)/1e9>timeout:
    timed_out=True;os.killpg(proc.pid,signal.SIGKILL);pid,status,usage=os.wait4(proc.pid,0);break
   time.sleep(.01)
  proc.returncode=os.waitstatus_to_exitcode(status)
 row={}
 if output.exists():
  try:row=json.loads(output.read_text())
  except (ValueError,OSError):pass
 row.update({'wall_ns':time.monotonic_ns()-start,'peak_rss_bytes':int(usage.ru_maxrss)*1024,'process_exit':proc.returncode,'timed_out':timed_out})
 if timed_out:row.update(status='timeout',error='preregistered 120 second deadline')
 elif proc.returncode or not row.get('semantic_sha256'):row.update(status='error',error=row.get('error','measurement process failed or emitted no digest'))
 row['process_log']=log.read_text(errors='replace')[-12000:]
 return row

def main():
 ap=argparse.ArgumentParser();ap.add_argument('--root',type=pathlib.Path,required=True);ap.add_argument('--binary',type=pathlib.Path,required=True);ap.add_argument('--manifest',type=pathlib.Path,required=True);ap.add_argument('--scenario-script',type=pathlib.Path,required=True);ap.add_argument('--assignment',type=pathlib.Path,required=True);ap.add_argument('--output',type=pathlib.Path,required=True);ap.add_argument('--stage',choices=['baseline','campaign'],required=True);ap.add_argument('--trials',type=int,default=30);ap.add_argument('--frozen-baseline',type=pathlib.Path);args=ap.parse_args()
 args.output.mkdir(parents=True,exist_ok=True)
 manifest=json.loads(args.manifest.read_text());records={r['id']:r for r in manifest['repositories']};tasks=json.loads(args.assignment.read_text());scenario=load_module(args.scenario_script)
 outpath=args.output/(args.stage+'.ndjson')
 if outpath.exists():raise SystemExit('Exclusive output already exists; do not overwrite observations')
 progress=args.output/'progress.json';work=args.output/'request';work.mkdir(exist_ok=True)
 metadata={'binary_sha256':sha(args.binary),'manifest_sha256':sha(args.manifest),'input_manifest_sha256':sha(args.manifest),'compiler':'off','ranking':'current','runner_sha256':sha(__file__),'scenario_sha256':sha(args.scenario_script),'assignment':tasks,'stage':args.stage,'uname':list(os.uname()),'page_cache':'ordered whole-file prime before each request; no disk-cold claim','rss':'Linux wait4 whole child including verification; harness overhead included','trials':args.trials}
 (args.output/(args.stage+'-manifest.json')).write_text(json.dumps(metadata,indent=2))
 frozen=json.loads(args.frozen_baseline.read_text()) if args.frozen_baseline else {}
 if args.stage=='campaign' and args.trials==30 and not frozen:raise SystemExit('Full campaign requires frozen baseline manifest')
 if frozen and (frozen.get('binary_sha256')!=sha(args.binary) or frozen.get('input_manifest_sha256')!=sha(args.manifest)):raise SystemExit('Frozen binary/input identity mismatch')
 if args.frozen_baseline:metadata['frozen_baseline_sha256']=sha(args.frozen_baseline)
 (args.output/(args.stage+'-manifest.json')).write_text(json.dumps(metadata,indent=2))
 count=0;blocked=[]
 with outpath.open('x') as out:
  def emit(row):
   nonlocal count
   count+=1;out.write(json.dumps(row,sort_keys=True)+'\n');out.flush();progress.write_text(json.dumps({'stage':args.stage,'observations':count,'last':{k:row.get(k) for k in ['repository','profile','verb','scenario','trial','reuse','status']},'blocked':blocked}))
  for task in tasks:
   record=records[task['repository']];repo=args.root/record['id'];profile=task['profile']
   if hasattr(scenario,'repo_path'):
    verified_id,verified_root,verified_record=scenario.repo_path(str(repo))
    if verified_id!=record['id'] or verified_root.resolve()!=repo.resolve():raise RuntimeError('fixture identity mismatch')
   for verb in ['snapshot','search']:
    failures={False:0,True:0,'warm':0};stopped=False;seen=set()
    if args.stage=='campaign' and any(b.get('repository')==record['id'] and b.get('profile')==profile and b.get('verb')==verb for b in frozen.get('blocked_strata',[])):
     blocked.append({'repository':record['id'],'profile':profile,'verb':verb,'reason':'baseline hard-failure circuit breaker'})
     for missing_case in SCENARIOS:
      for missing_trial in range(args.trials):
       for missing_arm in [False,True]:emit({'repository':record['id'],'profile':profile,'verb':verb,'scenario':missing_case,'trial':missing_trial,'reuse':missing_arm,'status':'unrun','error':'baseline stratum circuit breaker; not measured'})
     continue
    scenarios=['baseline'] if args.stage=='baseline' else SCENARIOS
    for case in scenarios:
     reps=3 if args.stage=='baseline' else args.trials
     for trial in range(reps):
      scenario.reset(repo,record)
      cache=work/'cache'
      if cache.exists():shutil.rmtree(cache)
      cache.mkdir()
      config={'version':1,'repository':record['id'],'repo_path':str(repo),'operation':verb,'mode':'measure','cache':'off','cache_dir':str(cache),'profile':profile,'query':record.get('query',manifest.get('query')),'provider_version':'p1-corpus-20260905','top_k':8,'max_indexed_files':0}
      if args.stage=='campaign' and case!='cold':
       warm=child(args.binary,dict(config,mode='warm',cache='on'),work,120)
       with (args.output/'warming.ndjson').open('a') as wf:wf.write(json.dumps(dict(warm,repository=record['id'],profile=profile,verb=verb,scenario=case,trial=trial))+'\n')
       if warm.get('status')=='error' or warm.get('timed_out'):
        failures['warm']+=1
        for missing_arm in [False,True]:
         emit({'repository':record['id'],'profile':profile,'verb':verb,'scenario':case,'trial':trial,'reuse':missing_arm,'status':'unrun','error':'cache preparation failed','preparation_failure':True});seen.add((case,trial,missing_arm))
        if failures['warm']>=3:stopped=True;break
        continue
       failures['warm']=0
      if case not in ['baseline','cold','unchanged']:scenario.apply(repo,record,case)
      source=scenario.digest(repo)['effective_tracked_input_sha256']
      arms=[False] if args.stage=='baseline' else ([False,True] if trial%2==0 else [True,False])
      pair_rows=[]
      for reuse in arms:
       prep=prime(repo);row=child(args.binary,dict(config,cache='on' if reuse else 'off',source_digest=source,mutation_id=case),work,120)
       after=scenario.digest(repo)['effective_tracked_input_sha256']
       row.update(repository=record['id'],profile=profile,verb=verb,scenario=case,trial=trial,reuse=reuse,semantic_digest=row.get('semantic_sha256'),source_digest=source,source_unchanged=after==source,cache_bytes=disk_bytes(cache),prime=prep,partial_failures_count=row.get('partial_failures_count',len(row.get('partial_failures',[]))),extraction=row.get('extraction') or (row.get('stats') or {}).get('extraction'))
       if after!=source:row.update(status='error',error='source changed during measurement')
       pair_rows.append(row);seen.add((case,trial,reuse))
       if row.get('status')=='error' or row.get('timed_out'):failures[reuse]+=1
       else:failures[reuse]=0
       if failures[reuse]>=3:stopped=True;break
      equivalent=len(pair_rows)==2 and all(r.get('source_unchanged') for r in pair_rows) and pair_rows[0].get('semantic_digest') and pair_rows[0].get('semantic_digest')==pair_rows[1].get('semantic_digest')
      for finished in pair_rows:
       finished['extraction']=finished.get('extraction') or {}
       finished['extraction']['stale_source']=False if equivalent else None
       finished['paired_freshness_basis']='same fixed source bytes and semantic output as fresh cache-off reference' if equivalent else 'unverified'
       emit(finished)
      if stopped:break
     if stopped:
      blocked.append({'repository':record['id'],'profile':profile,'verb':verb,'reason':'three consecutive failures; remaining planned observations unrun'});break
    if stopped and args.stage=='campaign':
     for missing_case in SCENARIOS:
      for missing_trial in range(args.trials):
       for missing_arm in [False,True]:
        if (missing_case,missing_trial,missing_arm) not in seen:emit({'repository':record['id'],'profile':profile,'verb':verb,'scenario':missing_case,'trial':missing_trial,'reuse':missing_arm,'status':'unrun','error':'stratum circuit breaker; not measured'})
    scenario.reset(repo,record)
  progress.write_text(json.dumps({'stage':args.stage,'observations':count,'done':True,'blocked':blocked}))
 if (work/'cache').exists():shutil.rmtree(work/'cache')
if __name__=='__main__':main()
