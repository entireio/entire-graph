import datetime,hashlib,json,os,pathlib,subprocess,tarfile,shlex
ROOT=pathlib.Path.cwd(); evidence=ROOT/'docs/implementation/graph-advantage/evidence'
def az(*args,env=None):
 r=subprocess.run(['az',*args],capture_output=True,text=True,env=env)
 if r.returncode: raise RuntimeError('Azure operation failed: '+args[0]+' '+args[1])
 return r.stdout.strip()
commit=subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip()
paths=subprocess.check_output(['git','ls-files','internal','cmd','scripts','go.mod','go.sum','mise.toml'],text=True).splitlines()
manifest={p:hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest() for p in paths if pathlib.Path(p).is_file() and not pathlib.Path(p).is_symlink()}
archive=pathlib.Path('/tmp/graph-review-'+commit[:12]+'.tgz')
with tarfile.open(archive,'w:gz') as tar:
 for p in manifest:tar.add(p,arcname=p)
(evidence/'review-linux-source.json').write_text(json.dumps({'commit':commit,'files':manifest,'archive_sha256':hashlib.sha256(archive.read_bytes()).hexdigest()},indent=2)+'\n')
environment=os.environ.copy();environment['AZURE_STORAGE_ACCOUNT']='entiregraphadv20260905'
environment['AZURE_STORAGE_KEY']=az('storage','account','keys','list','-g','rg-entire-graph-advantage-20260905','-n','entiregraphadv20260905','--query','[0].value','-o','tsv')
az('storage','blob','upload','--container-name','validation','--name',archive.name,'--file',str(archive),'--overwrite','true','--auth-mode','key','--only-show-errors',env=environment)
expiry=(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(hours=2)).strftime('%Y-%m-%dT%H:%MZ')
def sasurl(name,permissions):
 sas=az('storage','blob','generate-sas','--container-name','validation','--name',name,'--permissions',permissions,'--expiry',expiry,'--https-only','--auth-mode','key','-o','tsv',env=environment)
 return 'https://entiregraphadv20260905.blob.core.windows.net/validation/'+name+'?'+sas
url=sasurl(archive.name,'r'); remote='/opt/graph-validation/review-'+commit[:12]
outputs={'review-linux-correctness.txt':'/tmp/graph-review-correctness.txt','review-linux-combination.json':'/tmp/graph-review-combination.json','review-linux-queries.json':'/tmp/graph-review-queries.json'}
script='set -eu\nmkdir -p '+shlex.quote(remote)+'\ncurl -fsSL '+shlex.quote(url)+' -o /tmp/graph-review-code.tgz\ntar xzf /tmp/graph-review-code.tgz -C '+shlex.quote(remote)+'\nchown -R graphcheck:graphcheck '+shlex.quote(remote)+'\n'
command='cd '+shlex.quote(remote)+' && export PATH=/usr/local/go/bin:/usr/bin:/bin GOPATH=/opt/graph-validation/gopath GOCACHE=/opt/graph-validation/cache GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null ENTIRE_GRAPH_COMPILER_LIVE=1 ENTIRE_GRAPH_ADVANTAGE_LIVE_OUTPUT=/tmp/graph-review-combination.json ENTIRE_GRAPH_REVIEW_LIVE_OUTPUT=/tmp/graph-review-queries.json; go test -race -v -timeout 30m ./internal/compiler ./internal/sem ./internal/cli -run '+shlex.quote('Test(Compiler|LiveCompiler|LiveAdvantage|LiveReview|MapLocation|RPC|Capsule|Extraction|Captured|Operation|Linux|Sandbox)')+' -skip '+shlex.quote('QualityEvaluation|ExtractionEvaluation')
script+='set +e\nrunuser -u graphcheck -- sh -c '+shlex.quote(command)+' > /tmp/graph-review-correctness.txt 2>&1\nstatus=$?\nset -e\n'
for local,rpath in outputs.items():
 outurl=sasurl(commit[:12]+'-'+local,'cw')
 script+='if [ -f '+shlex.quote(rpath)+' ]; then curl -fsS -X PUT -H "x-ms-blob-type: BlockBlob" --upload-file '+shlex.quote(rpath)+' '+shlex.quote(outurl)+' >/dev/null; fi\n'
script+='tail -n 12 /tmp/graph-review-correctness.txt\nprintf "CORRECTNESS_EXIT=%s\\n" "$status"\n'
print('Executing Linux correctness at '+commit,flush=True)
result=az('vm','run-command','invoke','-g','rg-entire-graph-advantage-20260905','-n','graph-validation-linux','--command-id','RunShellScript','--scripts',script,'--query','value[].message','-o','json')
(evidence/'review-linux-run-command.json').write_text(result+'\n')
print(result,flush=True)
for local in outputs:
 try:az('storage','blob','download','--container-name','validation','--name',commit[:12]+'-'+local,'--file',str(evidence/local),'--overwrite','true','--auth-mode','key','--only-show-errors',env=environment)
 except RuntimeError:print('Artifact not available: '+local,flush=True)
