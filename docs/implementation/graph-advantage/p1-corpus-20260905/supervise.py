#!/usr/bin/env python3
"""Fail-closed external campaign supervisor; never runs inside the provider."""
import concurrent.futures,json,pathlib,shlex,time
import cloud
LEASE_SECONDS=180
POLL_SECONDS=30

def remote_python(code):
 return 'set -eu\npython3 - <<\'P1_SUPERVISOR\'\n'+code+'\nP1_SUPERVISOR\n'

def status_script(stage,results_dir="/opt/p1/results",unit_name=None):
 unit_name=unit_name or "p1-"+stage
 return remote_python("""import pathlib,json,subprocess
p=pathlib.Path("""+repr(results_dir)+""")
progress=json.loads((p/'progress.json').read_text()) if (p/'progress.json').exists() else {}
state=subprocess.check_output(['systemctl','show',"""+repr(unit_name)+""",'--property=ActiveState','--value'],text=True).strip()
exit_code=int(subprocess.check_output(['systemctl','show',"""+repr(unit_name)+""",'--property=ExecMainStatus','--value'],text=True).strip())
print('P1_STATUS '+json.dumps({'state':state,'exit_code':exit_code,'progress':progress,'paused':(p/'pause.json').exists(),'stop':(p/'STOP').exists()}))
""")

def parse_status(raw):
 messages=json.loads(raw)
 records=[json.loads(line[len('P1_STATUS '):]) for msg in messages for line in msg.splitlines() if line.startswith('P1_STATUS ')]
 if len(records)!=1:raise RuntimeError('Missing or ambiguous supervisor status')
 return records[0]

def lease_script(results_dir="/opt/p1/results"):
 return remote_python("""import pathlib,json,time,os
p=pathlib.Path("""+repr(results_dir)+""")
assert not (p/'STOP').exists() and not (p/'pause.json').exists(), 'paused campaign cannot renew'
t=p/'supervisor-lease.tmp';t.write_text(json.dumps({'expires_at':time.time()+180}));os.replace(t,p/'supervisor-lease.json')
print('P1_LEASE renewed')
""")

def stop_script(stage,reason,results_dir="/opt/p1/results",unit_name=None):
 unit_name=unit_name or "p1-"+stage
 code="import pathlib,json,time,os\np=pathlib.Path("+repr(results_dir)+");p.mkdir(parents=True,exist_ok=True)\nt=p/'STOP.tmp';t.write_text("+repr(reason)+");os.replace(t,p/'STOP')\n(p/'supervisor-stop.json').write_text(json.dumps({'reason':"+repr(reason)+",'time':time.time()}))\n"
 # Give the worker bounded time to persist pause and remaining-cell evidence.
 return remote_python(code)+"for attempt in $(seq 1 20); do\n  if ! systemctl is-active --quiet "+shlex.quote(unit_name)+"; then break; fi\n  sleep 1\ndone\nsystemctl stop "+shlex.quote(unit_name)+"\n"

def renew(vm,run,results_dir):
 raw=run(vm,lease_script(results_dir))
 if 'P1_LEASE renewed' not in raw:raise RuntimeError('Lease was not acknowledged')
 return True

def run_parallel(vms,fn):
 with concurrent.futures.ThreadPoolExecutor(max_workers=len(vms)) as pool:
  futures={pool.submit(fn,vm):vm for vm in vms};results={};errors={}
  for future,vm in futures.items():
   try:results[vm]=future.result()
   except Exception as exc:errors[vm]=type(exc).__name__
 return results,errors

def completed(state,stage):
 return state.get('state')=='inactive' and type(state.get('exit_code')) is int and state['exit_code']==0 and state.get('progress',{}).get('done') is True and state.get('progress',{}).get('stage')==stage

def supervise(vms,stage,output,run=cloud.run,sleep=time.sleep,results_dir="/opt/p1/results",unit_name=None):
 output=pathlib.Path(output);output.mkdir(parents=True,exist_ok=True)
 try:
  while True:
   states,errors=run_parallel(vms,lambda vm:parse_status(run(vm,status_script(stage,results_dir,unit_name))))
   bad={vm:s for vm,s in states.items() if s.get('paused') or s.get('stop') or (s.get('state')!='active' and not completed(s,stage))}
   with (output/'supervisor.ndjson').open('a') as f:f.write(json.dumps({'time':time.time(),'states':states,'errors':errors})+'\n')
   if errors or bad:raise RuntimeError('Worker pause, failure, or control-plane status unavailable')
   if all(completed(s,stage) for s in states.values()):return True
   active=[vm for vm,s in states.items() if not completed(s,stage)]
   _,errors=run_parallel(active,lambda vm:renew(vm,run,results_dir))
   if errors:raise RuntimeError('Supervisor lease renewal failed')
   sleep(POLL_SECONDS)
 except BaseException as exc:
  reason=type(exc).__name__+': campaign supervision stopped; diagnose before a new run'
  stopped,errors=run_parallel(vms,lambda vm:run(vm,stop_script(stage,reason,results_dir,unit_name)))
  (output/'supervisor-pause.json').write_text(json.dumps({'reason':reason,'stop_errors':errors,'lease_expiry_seconds':LEASE_SECONDS},indent=2)+'\n')
  if isinstance(exc,(KeyboardInterrupt,SystemExit)):raise
  return False
