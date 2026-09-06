from pathlib import Path
import sys,json,hashlib,shlex,tarfile
base=Path('/Users/thomi/Projects/entire-graph-advantage/docs/implementation/graph-advantage/p1-corpus-20260905');sys.path.insert(0,str(base));import cloud
out=base/'retained-query-correctness-1c0b8e24/results';out.mkdir(exist_ok=False)
archive=out/'runner-inputs.tar.gz'
with tarfile.open(archive,'w:gz') as t:
 t.add(base/'corrective-query-runner/run_remote.py',arcname='run_remote.py')
 for p in sorted((base/'retained-query-correctness-1c0b8e24').glob('*.json')):t.add(p,arcname='config/'+p.name)
env=cloud.environment();blob='retained-query-1c0b8e24-inputs.tar.gz';cloud.upload(archive,blob,env)
source='/opt/graph-validation/correctness-1c0b8e24-20260906';stage=source+'/query-correctness-inputs';root='/opt/graph-validation/retained-query-correctness-1c0b8e24';result_blob='retained-query-1c0b8e24-results.tar.gz';q=shlex.quote
args=['runuser','-u','graphcheck','--','/usr/bin/python3',stage+'/run_remote.py','--output',root+'/output','--config-dir',stage+'/config','--binary',source+'/p1-evaluator','--binary-sha256','b51728ed2c0840081c0921e6ec29931d2b10dce749802fb4f6fb341a60852e37','--source-root',source,'--source-commit','1c0b8e24f3b6a5bb880e7b306c1c74d818614782','--scenario-script','/opt/p1/retained-diagnostics/retained-05ad9842-20260906/corpus-tools/p1_scenario.py','--corpus-root','/opt/p1/corpus','--input-sha256','d7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4']
script='set -eu\nif systemctl list-units --type=service --state=active --no-legend "p1-*" | grep -q .; then echo ACTIVE_CAMPAIGN_REFUSED; exit 1; fi\nmkdir '+q(stage)+' '+q(root)+'\n'
script+='curl -fsS '+q(cloud.url(blob,'r',env))+' -o '+q(stage+'/inputs.tar.gz')+'\necho '+q(hashlib.sha256(archive.read_bytes()).hexdigest()+'  '+stage+'/inputs.tar.gz')+' | sha256sum -c - >/dev/null\ntar xzf '+q(stage+'/inputs.tar.gz')+' -C '+q(stage)+'\nchown -R graphcheck:graphcheck '+q(stage)+' '+q(root)+'\n'
script+='(/usr/bin/time --version; /usr/local/go/bin/go version; sha256sum /usr/bin/time; id graphcheck) > '+q(root+'/environment.txt')+'\nset +e\n'+shlex.join(args)+'\nstatus=$?\nset -e\nprintf "%s\\n" "$status" > '+q(root+'/exit.txt')+'\n'
script+='tar --exclude="retained-query-correctness-1c0b8e24/cache-*" -czf '+q(source+'/query-correctness-results.tar.gz')+' -C /opt/graph-validation retained-query-correctness-1c0b8e24\n'
script+='curl -fsS -X PUT -H "x-ms-blob-type: BlockBlob" --upload-file '+q(source+'/query-correctness-results.tar.gz')+' '+q(cloud.url(result_blob,'cw',env))+' >/dev/null\necho RETAINED_QUERY_UPLOAD_ACK\n'
(out/'invocation.json').write_text(json.dumps({'command':args,'input_archive_sha256':hashlib.sha256(archive.read_bytes()).hexdigest(),'script_sha256':hashlib.sha256(script.encode()).hexdigest(),'result_blob':result_blob,'purpose':'At most three retained profile correctness pairs; first failure halts; no performance score'},indent=2)+'\n')
r=cloud.run('graph-validation-linux',script);(out/'transport.json').write_text(r+'\n')
if 'RETAINED_QUERY_UPLOAD_ACK' not in r:raise RuntimeError('No acknowledgement: inspect existing remote outcome before any retry')
cloud.download(result_blob,out/'results.tar.gz',env)
with tarfile.open(out/'results.tar.gz') as f:f.extractall(out/'raw',filter='data')
p=out/'raw/retained-query-correctness-1c0b8e24/output/outcome.json';v=json.loads(p.read_text());print(json.dumps({k:v.get(k) for k in ('status','issue','processes_started','completed_pairs','unrun')},indent=2))
