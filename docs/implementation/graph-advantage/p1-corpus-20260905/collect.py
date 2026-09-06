#!/usr/bin/env python3
"""Read worker progress or retrieve immutable raw stage artifacts."""
import argparse,concurrent.futures,json,pathlib,shlex
import re
import cloud
VMS=['graph-validation-linux','graph-p1-worker-2','graph-p1-worker-3']

RUN_ID_RE=re.compile(r'[a-z0-9][a-z0-9-]{0,63}\Z')

def layout(stage,run_id=None):
 if run_id is None:
  return {'results_dir':'/opt/p1/results','unit':'p1-'+stage,'log':'/opt/p1/'+stage+'.log','token':stage}
 if not RUN_ID_RE.fullmatch(run_id):raise ValueError('Invalid isolated run id')
 root='/opt/p1/runs/'+run_id
 return {'results_dir':root,'unit':'p1-'+stage+'-'+run_id,'log':root+'/'+stage+'.log','token':stage+'-'+run_id}

def main():
 ap=argparse.ArgumentParser();ap.add_argument('--stage',choices=['baseline','campaign'],required=True);ap.add_argument('--run-id');ap.add_argument('--output',type=pathlib.Path);args=ap.parse_args()
 try:paths=layout(args.stage,args.run_id)
 except ValueError as exc:raise SystemExit(str(exc))
 env=cloud.environment() if args.output else None
 def collect(pair):
  index,vm=pair
  script='systemctl show '+shlex.quote(paths['unit'])+' --property=ActiveState --property=SubState --property=ExecMainStatus\ncat '+shlex.quote(paths['results_dir']+'/progress.json')+' 2>/dev/null || true\ntail -c 2000 '+shlex.quote(paths['log'])+' 2>/dev/null || true\n'
  if args.output:
   blob='p1-20260905-'+paths['token']+'-worker-'+str(index)+'.tar.gz';url=cloud.url(blob,'cw',env)
   # Require a completed stage before creating the retained raw archive.
   script='set -eu\npython3 -c '+shlex.quote("import json; p=json.load(open("+repr(paths['results_dir']+'/progress.json')+")); assert p.get('done') and p.get('stage')=="+repr(args.stage))+'\n'+script
   archive='/opt/p1/'+paths['token']+'-results.tar.gz'
   if args.run_id is None:
    script+='tar -czf '+shlex.quote(archive)+' -C /opt/p1 results '+shlex.quote(args.stage+'.log')+'\n'
   else:
    script+='tar -czf '+shlex.quote(archive)+' -C '+shlex.quote(paths['results_dir'])+' .\n'
   script+='curl --fail --silent --show-error -X PUT -H "x-ms-blob-type: BlockBlob" --upload-file '+shlex.quote(archive)+' '+shlex.quote(url)+'\n'
  result={'worker':index,'vm':vm,'output':json.loads(cloud.run(vm,script))}
  if args.run_id is not None:result['run_id']=args.run_id
  if args.output:
   args.output.mkdir(parents=True,exist_ok=True);cloud.download(blob,args.output/('worker-'+str(index)+'.tar.gz'),env)
  return result
 with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
  for result in pool.map(collect,enumerate(VMS,1)):print(json.dumps(result),flush=True)
if __name__=='__main__':main()
