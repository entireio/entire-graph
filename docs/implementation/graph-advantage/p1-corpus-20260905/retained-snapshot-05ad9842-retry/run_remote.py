"""One snapshot diagnostic pair using an existing verified binary. No retries."""
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys

root = Path(sys.argv[1])
source = Path('/opt/p1/retained-diagnostics/retained-05ad9842-20260906')
binary = source / 'diagnostic'
expected_binary = 'e8c11700f7b989319ecd0c42e7bdc876c71778f4161df3bf5d0f2f6c0309d8d9'
expected_input = 'd7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4'
environment = dict(PATH='/usr/local/go/bin:/usr/bin:/bin', LANG='C.UTF-8',
    LC_ALL='C.UTF-8', LC_CTYPE='C.UTF-8', GOMAXPROCS='4',
    GIT_CONFIG_GLOBAL='/dev/null', GIT_CONFIG_SYSTEM='/dev/null',
    GOPROXY='off', GOSUMDB='off', GOTOOLCHAIN='local', GOTELEMETRY='off',
    P1_CORPUS_ROOT='/opt/p1/corpus')
env = os.environ.copy()
env.update(environment)

def save(name, value):
    (root / name).write_text(json.dumps(value, indent=2) + '\n')

def fingerprint(stage):
    result = subprocess.run(['/usr/bin/python3', str(source / 'corpus-tools/p1_scenario.py'),
        'digest', 'kubernetes-kubernetes'], env=env, capture_output=True, timeout=60)
    (root / (stage + '.log')).write_bytes(result.stderr)
    (root / (stage + '.json')).write_bytes(result.stdout)
    if result.returncode:
        raise RuntimeError(stage + ' fingerprint failed')
    value = json.loads(result.stdout)
    if value.get('effective_tracked_input_sha256') != expected_input:
        raise RuntimeError(stage + ' input identity mismatch')
    return value

save('environment.json', environment)
save('identity.json', dict(source_commit='05ad98422530e3573215e2dbfb4bd2fb19fe5d55',
    binary=str(binary), expected_binary_sha256=expected_binary, expected_input=expected_input))
completed = []
issue = None
before = None
try:
    actual = hashlib.sha256(binary.read_bytes()).hexdigest()
    save('binary.json', dict(sha256=actual))
    if actual != expected_binary:
        raise RuntimeError('binary identity mismatch')
    before = fingerprint('before')
    observations = {}
    for arm in ['off', 'on']:
        config = dict(version=1, repository='kubernetes-kubernetes',
            repo_path='/opt/p1/corpus/kubernetes-kubernetes', operation='snapshot',
            mode='measure', cache=arm, cache_path=str(root / ('cache-' + arm)),
            profile='syntax-only', provider_version='p1-corpus-20260905',
            mutation_id='retained-snapshot-syntax-only', scenario='diagnostic', trial=0,
            source_digest=expected_input,
            input_manifest_sha256='d2fdce2a59befb3a0a02bcc7fc5a531eb8571a1788b0070b6fd2147e92e273e0')
        save('request-' + arm + '.json', config)
        arm_env = env.copy()
        arm_env.update(ENTIRE_GRAPH_EXTRACTION_CORPUS_CONFIG=str(root / ('request-' + arm + '.json')),
            ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT=str(root / ('observation-' + arm + '.ndjson')),
            ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT_FORMAT='ndjson')
        command = [str(binary), '-test.run=^TestExtractionCorpusMeasurement$',
            '-test.count=1', '-test.v', '-test.timeout=130s']
        with (root / ('process-' + arm + '.log')).open('wb') as log:
            process = subprocess.Popen(command, env=arm_env, cwd=root, stdout=log,
                stderr=subprocess.STDOUT, start_new_session=True)
            timed_out = False
            try:
                code = process.wait(timeout=120)
            except subprocess.TimeoutExpired:
                timed_out = True
                os.killpg(process.pid, signal.SIGKILL)
                code = process.wait()
        save('process-' + arm + '.json', dict(command=command, exit_code=code, timed_out=timed_out))
        completed.append(arm)
        if code or timed_out:
            raise RuntimeError(arm + ' process failed or timed out; stop')
        observation = json.loads((root / ('observation-' + arm + '.ndjson')).read_text())
        if (observation.get('status') not in ['ok', 'partial'] or
            not observation.get('semantic_sha256') or observation.get('source_digest') != expected_input):
            raise RuntimeError(arm + ' invalid observation; stop')
        observations[arm] = observation
    if observations['off']['semantic_sha256'] != observations['on']['semantic_sha256']:
        raise RuntimeError('semantic mismatch; stop')
except Exception as error:
    issue = str(error)
finally:
    if before is not None:
        try:
            if fingerprint('after') != before:
                issue = 'input changed during diagnostic'
        except Exception as error:
            issue = str(error)
    save('outcome.json', dict(status='issue' if issue else 'pair_semantics_equal',
        issue=issue, processes_started=completed, unrun=[a for a in ['off', 'on'] if a not in completed],
        scope='isolated diagnostic; partial status preserved; no campaign or release gate'))
