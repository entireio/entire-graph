#!/usr/bin/env python3
"""Read worker progress or retrieve immutable raw stage artifacts."""
import argparse,concurrent.futures,hashlib,json,os,pathlib,shlex,tempfile
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

def terminal_guard(stage,paths):
 return '''python3 - %s %s %s <<'P1_COLLECT_GUARD'
import json,pathlib,subprocess,sys
unit,root,stage=sys.argv[1:]
try:
    raw=subprocess.check_output(['systemctl','show',unit,'--property=LoadState','--property=ActiveState','--property=SubState'],text=True,stderr=subprocess.STDOUT)
except (OSError,subprocess.CalledProcessError) as exc:
    raise SystemExit('cannot verify worker unit: '+str(exc))
props={}
for line in raw.splitlines():
    if '=' in line:
        key,value=line.split('=',1);props[key]=value
if props.get('LoadState')!='loaded':
    raise SystemExit('worker unit is unknown or not loaded: '+props.get('LoadState',''))
if props.get('ActiveState') not in ('inactive','failed'):
    raise SystemExit('worker unit is still active: '+props.get('ActiveState',''))
directory=pathlib.Path(root)
markers=[directory/'pause.json',directory/'STOP',directory/'supervisor-stop.json']
progress=directory/'progress.json'
if progress.exists():
    try:
        payload=json.loads(progress.read_text())
    except (OSError,ValueError) as exc:
        raise SystemExit('progress evidence is unreadable: '+str(exc))
    if payload.get('stage')!=stage:
        raise SystemExit('progress evidence belongs to another stage')
    if not payload.get('done') and not any(marker.exists() for marker in markers):
        raise SystemExit('worker stopped without terminal completion or pause evidence')
elif not any(marker.exists() for marker in markers):
    raise SystemExit('worker stopped without progress or terminal stop evidence')
P1_COLLECT_GUARD
''' % (shlex.quote(paths['unit']),shlex.quote(paths['results_dir']),shlex.quote(stage))

def file_digest(path):
 digest=hashlib.sha256()
 with pathlib.Path(path).open('rb') as stream:
  for block in iter(lambda:stream.read(1024*1024),b''):digest.update(block)
 return digest.digest()

def download_immutable(blob,destination,env):
 destination=pathlib.Path(destination);destination.parent.mkdir(parents=True,exist_ok=True)
 with tempfile.NamedTemporaryFile(dir=destination.parent,prefix='.collect-',delete=False) as stream:
  temporary=pathlib.Path(stream.name)
 try:
  cloud.download(blob,temporary,env)
  if os.path.lexists(destination):
   if destination.is_symlink() or not destination.is_file():
    raise RuntimeError('refusing to overwrite non-regular archive '+str(destination))
   if file_digest(destination)!=file_digest(temporary):
    raise RuntimeError('refusing to overwrite non-identical archive '+str(destination))
  else:
   try:os.link(temporary,destination)
   except FileExistsError:
    if destination.is_symlink() or not destination.is_file():
     raise RuntimeError('refusing to overwrite non-regular archive '+str(destination))
    if file_digest(destination)!=file_digest(temporary):
     raise RuntimeError('refusing to overwrite non-identical archive '+str(destination))
 finally:
  temporary.unlink(missing_ok=True)

def main():
 ap=argparse.ArgumentParser();ap.add_argument('--stage',choices=['baseline','campaign'],required=True);ap.add_argument('--run-id');ap.add_argument('--output',type=pathlib.Path);args=ap.parse_args()
 try:paths=layout(args.stage,args.run_id)
 except ValueError as exc:raise SystemExit(str(exc))
 env=cloud.environment() if args.output else None
 def collect(pair):
  index,vm=pair
  script='set -eu\n'+terminal_guard(args.stage,paths)+'systemctl show '+shlex.quote(paths['unit'])+' --property=ActiveState --property=SubState --property=ExecMainStatus\ncat '+shlex.quote(paths['results_dir']+'/progress.json')+' 2>/dev/null || true\ntail -c 2000 '+shlex.quote(paths['log'])+' 2>/dev/null || true\n'
  if args.output:
   blob='p1-20260905-'+paths['token']+'-worker-'+str(index)+'.tar.gz';url=cloud.url(blob,'cw',env)
   archive='/opt/p1/'+paths['token']+'-results.tar.gz'
   if args.run_id is None:
    script+='tar -czf '+shlex.quote(archive)+' -C /opt/p1 results '+shlex.quote(args.stage+'.log')+'\n'
   else:
    script+='tar -czf '+shlex.quote(archive)+' -C '+shlex.quote(paths['results_dir'])+' .\n'
   script+='curl --fail --silent --show-error -X PUT -H "x-ms-blob-type: BlockBlob" --upload-file '+shlex.quote(archive)+' '+shlex.quote(url)+'\n'
  result={'worker':index,'vm':vm,'output':json.loads(cloud.run(vm,script))}
  if args.run_id is not None:result['run_id']=args.run_id
  if args.output:
   download_immutable(blob,args.output/('worker-'+str(index)+'.tar.gz'),env)
  return result
 with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
  for result in pool.map(collect,enumerate(VMS,1)):print(json.dumps(result),flush=True)
if __name__=='__main__':main()
