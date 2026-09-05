#!/usr/bin/env python3
"""Read worker progress or retrieve immutable raw stage artifacts."""
import argparse,concurrent.futures,json,pathlib,shlex
import cloud
VMS=['graph-validation-linux','graph-p1-worker-2','graph-p1-worker-3']
def main():
 ap=argparse.ArgumentParser();ap.add_argument('--stage',choices=['baseline','campaign'],required=True);ap.add_argument('--output',type=pathlib.Path);args=ap.parse_args()
 env=cloud.environment() if args.output else None
 def collect(pair):
  index,vm=pair
  script='systemctl show p1-'+args.stage+' --property=ActiveState --property=SubState --property=ExecMainStatus\ncat /opt/p1/results/progress.json 2>/dev/null || true\ntail -c 2000 /opt/p1/'+args.stage+'.log 2>/dev/null || true\n'
  if args.output:
   blob='p1-20260905-'+args.stage+'-worker-'+str(index)+'.tar.gz';url=cloud.url(blob,'cw',env)
   # Require a completed stage before creating the retained raw archive.
   script='set -eu\npython3 -c '+shlex.quote("import json; p=json.load(open('/opt/p1/results/progress.json')); assert p.get('done') and p.get('stage')=="+repr(args.stage))+'\n'+script
   script+='tar -czf /opt/p1/'+args.stage+'-results.tar.gz -C /opt/p1 results '+args.stage+'.log\n'
   script+='curl --fail --silent --show-error -X PUT -H "x-ms-blob-type: BlockBlob" --upload-file /opt/p1/'+args.stage+'-results.tar.gz '+shlex.quote(url)+'\n'
  result={'worker':index,'vm':vm,'output':json.loads(cloud.run(vm,script))}
  if args.output:
   args.output.mkdir(parents=True,exist_ok=True);cloud.download(blob,args.output/('worker-'+str(index)+'.tar.gz'),env)
  return result
 with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
  for result in pool.map(collect,enumerate(VMS,1)):print(json.dumps(result),flush=True)
if __name__=='__main__':main()
