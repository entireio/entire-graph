#!/usr/bin/env python3
"""Launch one preregistered stage on the three task-owned Linux workers.

Transport uses private blobs; SAS credentials remain in memory. Requires a
prepared corpus on each worker and the shared evaluator blob. Baselines must
finish and be frozen locally before launching the campaign stage.
"""
import argparse,concurrent.futures,hashlib,json,pathlib,shlex,tarfile,tempfile
import cloud
HERE=pathlib.Path(__file__).resolve().parent
VMS=['graph-validation-linux','graph-p1-worker-2','graph-p1-worker-3']
def main():
 ap=argparse.ArgumentParser();ap.add_argument('stage',choices=['baseline','campaign']);ap.add_argument('--frozen-baseline',type=pathlib.Path);args=ap.parse_args()
 if args.stage=='campaign' and not args.frozen_baseline:raise SystemExit('Campaign requires frozen baseline manifest')
 expected=json.loads((HERE/'build.json').read_text())['binary_sha256']
 env=cloud.environment()
 with tempfile.TemporaryDirectory() as d:
  archive=pathlib.Path(d)/'scripts.tar.gz'
  with tarfile.open(archive,'w:gz') as tar:
   for name in ['run_campaign.py','worker-1.json','worker-2.json','worker-3.json']:
    tar.add(HERE/name,arcname=name)
   for name in ['p1_scenario.py','corpus-manifest.json']:
    tar.add(HERE.parent/'corpus'/name,arcname='corpus-tools/'+name)
   if args.frozen_baseline:tar.add(args.frozen_baseline,arcname='frozen-baseline.json')
  digest=hashlib.sha256(archive.read_bytes()).hexdigest();blob='p1-20260905-scripts-'+digest+'.tar.gz';cloud.upload(archive,blob,env)
 source=cloud.url(blob,'r',env);binary=cloud.url('p1-20260905-evaluator','r',env)
 def start(pair):
  index,vm=pair
  command=['/usr/bin/python3','/opt/p1/scripts/run_campaign.py','--root','/opt/p1/corpus','--binary','/opt/p1/p1-evaluator','--manifest','/opt/p1/scripts/corpus-tools/corpus-manifest.json','--scenario-script','/opt/p1/scripts/corpus-tools/p1_scenario.py','--assignment',f'/opt/p1/scripts/worker-{index}.json','--output','/opt/p1/results','--stage',args.stage]
  if args.stage=='campaign':command+=['--frozen-baseline','/opt/p1/scripts/frozen-baseline.json']
  script='set -eu\n'
  script+='test ! -e /opt/p1/results/'+args.stage+'.ndjson\n'
  script+='mkdir -p /opt/p1/scripts /opt/p1/results\n'
  script+='curl --fail --silent --show-error '+shlex.quote(source)+' -o /opt/p1/scripts.tar.gz\n'
  script+='tar -xzf /opt/p1/scripts.tar.gz -C /opt/p1/scripts\n'
  script+='curl --fail --silent --show-error '+shlex.quote(binary)+' -o /opt/p1/p1-evaluator\n'
  script+='chmod 755 /opt/p1/p1-evaluator\nchown -R graphcheck:graphcheck /opt/p1/scripts /opt/p1/results\n'
  script+='echo '+shlex.quote(expected+'  /opt/p1/p1-evaluator')+' | sha256sum -c -\n'
  script+='systemd-run --unit=p1-'+args.stage+' --uid=graphcheck --property=WorkingDirectory=/opt/p1 --property=MemoryMax=14G --property=TasksMax=512 --property=StandardOutput=append:/opt/p1/'+args.stage+'.log --property=StandardError=append:/opt/p1/'+args.stage+'.log '+shlex.join(command)+'\n'
  return {'worker':index,'vm':vm,'result':json.loads(cloud.run(vm,script))}
 with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
  for result in pool.map(start,enumerate(VMS,1)):print(json.dumps(result),flush=True)
if __name__=='__main__':main()
