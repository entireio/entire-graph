"""Task-scoped Azure transport; keys/SAS stay in memory and never enter logs."""
import datetime,json,os,pathlib,shlex,subprocess
RG='rg-entire-graph-advantage-20260905';ACCOUNT='entiregraphadv20260905';CONTAINER='validation'
VMS=('graph-validation-linux','graph-p1-worker-2','graph-p1-worker-3')
def az(*args,env=None):
 r=subprocess.run(['az',*args],capture_output=True,text=True,env=env)
 if r.returncode:raise RuntimeError('Azure operation failed: '+' '.join(args[:2]))
 return r.stdout.strip()
def environment():
 e=os.environ.copy();e['AZURE_STORAGE_ACCOUNT']=ACCOUNT;e['AZURE_STORAGE_KEY']=az('storage','account','keys','list','-g',RG,'-n',ACCOUNT,'--query','[0].value','-o','tsv');return e
def url(name,permissions,env):
 expiry=(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(hours=12)).strftime('%Y-%m-%dT%H:%MZ')
 sas=az('storage','blob','generate-sas','--container-name',CONTAINER,'--name',name,'--permissions',permissions,'--expiry',expiry,'--https-only','--auth-mode','key','-o','tsv',env=env)
 return 'https://'+ACCOUNT+'.blob.core.windows.net/'+CONTAINER+'/'+name+'?'+sas
def upload(path,name,env):return az('storage','blob','upload','--container-name',CONTAINER,'--name',name,'--file',str(path),'--overwrite','true','--auth-mode','key','--only-show-errors',env=env)
def download(name,path,env):return az('storage','blob','download','--container-name',CONTAINER,'--name',name,'--file',str(path),'--overwrite','true','--auth-mode','key','--only-show-errors',env=env)
def run(vm,script):return az('vm','run-command','invoke','-g',RG,'-n',vm,'--command-id','RunShellScript','--scripts',script,'--query','value[].message','-o','json')
def start(vm,env):return az('vm','start','-g',RG,'-n',vm,'--only-show-errors',env=env)
def deallocate(vm,env):return az('vm','deallocate','-g',RG,'-n',vm,'--only-show-errors',env=env)
def states(env):
 return {vm:az('vm','get-instance-view','-g',RG,'-n',vm,'--query',"instanceView.statuses[?starts_with(code, 'PowerState/')].displayStatus | [0]",'-o','tsv',env=env) for vm in VMS}
