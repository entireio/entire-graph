"""Task-scoped Azure transport; keys/SAS stay in memory and never enter logs."""
import datetime,json,os,pathlib,shlex,subprocess

CONFIG_ENV={
 'resource_group':'GRAPH_ADVANTAGE_AZURE_RESOURCE_GROUP',
 'storage_account':'GRAPH_ADVANTAGE_AZURE_STORAGE_ACCOUNT',
 'storage_container':'GRAPH_ADVANTAGE_AZURE_STORAGE_CONTAINER',
 'validation_vm':'GRAPH_ADVANTAGE_VALIDATION_VM',
 'worker_2_vm':'GRAPH_ADVANTAGE_WORKER_2_VM',
 'worker_3_vm':'GRAPH_ADVANTAGE_WORKER_3_VM',
}
def config():
 values={key:os.environ.get(name,'').strip() for key,name in CONFIG_ENV.items()}
 missing=[CONFIG_ENV[key] for key,value in values.items() if not value]
 if missing:raise RuntimeError('missing required completion diagnostic configuration: '+', '.join(sorted(missing)))
 return values
def az(*args,env=None):
 r=subprocess.run(['az',*args],capture_output=True,text=True,env=env)
 if r.returncode:raise RuntimeError('Azure operation failed: '+' '.join(args[:2]))
 return r.stdout.strip()
def environment():
 cfg=config();e=os.environ.copy();e['AZURE_STORAGE_ACCOUNT']=cfg['storage_account'];e['AZURE_STORAGE_KEY']=az('storage','account','keys','list','-g',cfg['resource_group'],'-n',cfg['storage_account'],'--query','[0].value','-o','tsv');return e
def url(name,permissions,env):
 cfg=config()
 expiry=(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(hours=12)).strftime('%Y-%m-%dT%H:%MZ')
 sas=az('storage','blob','generate-sas','--container-name',cfg['storage_container'],'--name',name,'--permissions',permissions,'--expiry',expiry,'--https-only','--auth-mode','key','-o','tsv',env=env)
 return 'https://'+cfg['storage_account']+'.blob.core.windows.net/'+cfg['storage_container']+'/'+name+'?'+sas
def upload(path,name,env):cfg=config();return az('storage','blob','upload','--container-name',cfg['storage_container'],'--name',name,'--file',str(path),'--overwrite','true','--auth-mode','key','--only-show-errors',env=env)
def download(name,path,env):cfg=config();return az('storage','blob','download','--container-name',cfg['storage_container'],'--name',name,'--file',str(path),'--overwrite','true','--auth-mode','key','--only-show-errors',env=env)
def run(vm,script):cfg=config();return az('vm','run-command','invoke','-g',cfg['resource_group'],'-n',vm,'--command-id','RunShellScript','--scripts',script,'--query','value[].message','-o','json')
def start(vm,env):cfg=config();return az('vm','start','-g',cfg['resource_group'],'-n',vm,'--only-show-errors',env=env)
def deallocate(vm,env):cfg=config();return az('vm','deallocate','-g',cfg['resource_group'],'-n',vm,'--only-show-errors',env=env)
def states(env):
 cfg=config();vms=(cfg['validation_vm'],cfg['worker_2_vm'],cfg['worker_3_vm'])
 return {vm:az('vm','get-instance-view','-g',cfg['resource_group'],'-n',vm,'--query',"instanceView.statuses[?starts_with(code, 'PowerState/')].displayStatus | [0]",'-o','tsv',env=env) for vm in vms}
