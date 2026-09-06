from pathlib import Path
import sys,json,hashlib,shlex,tarfile,shutil
base=Path('/Users/thomi/Projects/entire-graph-advantage/docs/implementation/graph-advantage/p1-corpus-20260905')
sys.path.insert(0,str(base));import cloud
out=base/'retained-snapshot-1c0b8e24';out.mkdir(exist_ok=False)
runner=base/'corrective-resource-runner/run_remote.py';shutil.copy2(runner,out/'run_remote.py');shutil.copy2('/tmp/graph-1c0b8e24-evaluator-build.json',out/'build-transport.json')
env=cloud.environment();blob='corrective-resource-1c0b8e24.py';cloud.upload(runner,blob,env)
source='/opt/graph-validation/correctness-1c0b8e24-20260906';remote=source+'/snapshot-resource-diagnostic';runner_remote=source+'/corrective-resource-runner.py';result_blob='p1-snapshot-resource-1c0b8e24-results.tar.gz';q=shlex.quote
args=['/usr/bin/python3',runner_remote,'--output',remote,'--binary',source+'/p1-evaluator','--binary-sha256','b51728ed2c0840081c0921e6ec29931d2b10dce749802fb4f6fb341a60852e37','--source-root',source,'--source-commit','1c0b8e24f3b6a5bb880e7b306c1c74d818614782','--scenario-script','/opt/p1/retained-diagnostics/retained-05ad9842-20260906/corpus-tools/p1_scenario.py','--corpus-root','/opt/p1/corpus','--input-sha256','d7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4']
script='set -eu\nif systemctl list-units --type=service --state=active --no-legend "p1-*" | grep -q .; then echo ACTIVE_CAMPAIGN_REFUSED; exit 1; fi\n'
script+='curl -fsS '+q(cloud.url(blob,'r',env))+' -o '+q(runner_remote)+'\n'
script+='echo '+q(hashlib.sha256(runner.read_bytes()).hexdigest()+'  '+runner_remote)+' | sha256sum -c - >/dev/null\n'
script+='set +e\n'+shlex.join(args)+'\nstatus=$?\nset -e\nprintf "%s\\n" "$status" > '+q(source+'/snapshot-resource-exit.txt')+'\n'
script+='tar --exclude="snapshot-resource-diagnostic/cache-*" -czf '+q(source+'/snapshot-resource-results.tar.gz')+' -C '+q(source)+' snapshot-resource-diagnostic snapshot-resource-exit.txt\n'
script+='curl -fsS -X PUT -H "x-ms-blob-type: BlockBlob" --upload-file '+q(source+'/snapshot-resource-results.tar.gz')+' '+q(cloud.url(result_blob,'cw',env))+' >/dev/null\necho CORRECTIVE_RESOURCE_UPLOAD_ACK\n'
(out/'invocation.json').write_text(json.dumps({'command':args,'runner_sha256':hashlib.sha256(runner.read_bytes()).hexdigest(),'script_sha256':hashlib.sha256(script.encode()).hexdigest(),'result_blob':result_blob,'purpose':'One corrective pair after immutable and pinned correctness; no campaign expansion'},indent=2)+'\n')
r=cloud.run('graph-validation-linux',script);(out/'transport.json').write_text(r+'\n')
if 'CORRECTIVE_RESOURCE_UPLOAD_ACK' not in r:raise RuntimeError('No result acknowledgement; inspect remote state, never blindly retry')
cloud.download(result_blob,out/'results.tar.gz',env)
with tarfile.open(out/'results.tar.gz') as f:f.extractall(out/'raw',filter='data')
print((out/'raw/snapshot-resource-diagnostic/outcome.json').read_text())
