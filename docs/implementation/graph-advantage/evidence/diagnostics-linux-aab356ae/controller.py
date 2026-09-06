from pathlib import Path
import sys,json,hashlib,shlex,tarfile
base=Path('/Users/thomi/Projects/entire-graph-advantage/docs/implementation/graph-advantage')
sys.path.insert(0,str(base/'p1-corpus-20260905'));import cloud
out=base/'evidence/diagnostics-linux-aab356ae';manifest=json.loads((out/'manifest.json').read_text())
check=json.loads((base/'evidence/check-aab356ae/verification.json').read_text())
assert check['exit_code']==0 and check['immutable'] and check['source_commit']==manifest['source_commit']
archive=Path('/tmp/graph-aab356ae-source.tar.gz');assert hashlib.sha256(archive.read_bytes()).hexdigest()==manifest['source_archive_sha256']
env=cloud.environment();blob='diagnostics-linux-aab356ae-source.tar.gz';cloud.upload(archive,blob,env)
remote='/opt/graph-validation/diagnostics-linux-aab356ae';q=shlex.quote
command='set -eu; cd '+q(remote)+'; export PATH=/usr/local/go/bin:/usr/bin:/bin GOPATH=/opt/graph-validation/gopath GOCACHE=/opt/graph-validation/cache GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOTELEMETRY=off GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GOMAXPROCS=4; go test -race ./internal/sem -run ^TestExtractionCorpusDiagnostics -count=1 && go test -c -o p1-evaluator ./internal/sem && sha256sum p1-evaluator > binary.sha256'
script='set -eu\nif systemctl list-units --type=service --state=active --no-legend "p1-*" | grep -q .; then echo ACTIVE_CAMPAIGN_REFUSED; exit 1; fi\nmkdir '+q(remote)+'\n'
script+='curl -fsS '+q(cloud.url(blob,'r',env))+' -o '+q(remote+'/source.tar.gz')+'\necho '+q(manifest['source_archive_sha256']+'  '+remote+'/source.tar.gz')+' | sha256sum -c - >/dev/null\ntar xzf '+q(remote+'/source.tar.gz')+' -C '+q(remote)+'\nchown -R graphcheck:graphcheck '+q(remote)+'\n'
script+='(uname -a; /usr/local/go/bin/go version; /usr/bin/git --version) > '+q(remote+'/environment.txt')+'\nset +e\ntimeout --signal=TERM --kill-after=10s 600s runuser -u graphcheck -- sh -c '+q(command)+' > '+q(remote+'/check.txt')+' 2>&1\nstatus=$?\nset -e\nprintf "%s\\n" "$status" > '+q(remote+'/exit.txt')+'\n'
script+='cd '+q(remote)+'\ntar czf results.tar.gz check.txt exit.txt environment.txt $(test ! -f binary.sha256 || printf "%s" binary.sha256)\n'
result_blob='diagnostics-linux-aab356ae-results.tar.gz';script+='curl -fsS -X PUT -H "x-ms-blob-type: BlockBlob" --upload-file results.tar.gz '+q(cloud.url(result_blob,'cw',env))+' >/dev/null\necho DIAGNOSTICS_CHECK_UPLOAD_ACK\n'
manifest.update(status='dispatched once; inspect retained outcome before any retry',script_sha256=hashlib.sha256(script.encode()).hexdigest(),result_blob=result_blob,remote_source=remote)
(out/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
r=cloud.run('graph-validation-linux',script);(out/'transport.json').write_text(r+'\n')
if 'DIAGNOSTICS_CHECK_UPLOAD_ACK' not in r:raise RuntimeError('No acknowledgement: inspect remote state, do not rerun')
cloud.download(result_blob,out/'results.tar.gz',env)
with tarfile.open(out/'results.tar.gz') as t:t.extractall(out/'raw',filter='data')
manifest.update(status='collected',exit_code=int((out/'raw/exit.txt').read_text().strip()),results_sha256=hashlib.sha256((out/'results.tar.gz').read_bytes()).hexdigest())
if (out/'raw/binary.sha256').is_file():manifest['binary_sha256']=(out/'raw/binary.sha256').read_text().split()[0]
(out/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n');print(json.dumps(manifest))
