import subprocess,json,time,hashlib,sys
from pathlib import Path
source=sys.argv[1]
checkout=Path('/var/folders/7g/r0pg1n495tb1snh2zvk9y0_r0000gn/T/graph-integration-0c9e80f5-61zo1a1q/source')
root=Path('/Users/thomi/Projects/entire-graph-advantage')
def git(*args):
 return subprocess.check_output(['git',*args],cwd=checkout,text=True).strip()
assert not git('status','--porcelain'), 'verification checkout is dirty'
subprocess.run(['git','checkout','--detach',source],cwd=checkout,check=True)
assert git('rev-parse','HEAD')==source
out=root/'docs/implementation/graph-advantage/evidence'/('check-'+source[:8]);out.mkdir(exist_ok=False)
start=time.monotonic()
with (out/'check.log').open('wb') as log:
 r=subprocess.run(['mise','run','check'],cwd=checkout,stdout=log,stderr=subprocess.STDOUT)
status=git('status','--porcelain');after=git('rev-parse','HEAD')
v={'source_commit':source,'command':'mise run check','exit_code':r.returncode,'elapsed_seconds':time.monotonic()-start,'after_commit':after,'status_after':status,'immutable':after==source and not status,'log_sha256':hashlib.sha256((out/'check.log').read_bytes()).hexdigest(),'checkout':str(checkout)}
(out/'verification.json').write_text(json.dumps(v,indent=2)+'\n');print(json.dumps(v))
raise SystemExit(r.returncode)
