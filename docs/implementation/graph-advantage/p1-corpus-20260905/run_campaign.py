#!/usr/bin/env python3
"""One isolated Linux worker with fail-closed observation gates.

The runner records every observation it receives. The first malformed,
partial, failed, stale, or otherwise unverifiable observation pauses the
whole worker and marks the rest of the precomputed plan as ``unrun``. A
paused output directory is never resumed in place.
"""
import argparse
import hashlib
import importlib.util
import json
import os
import pathlib
import resource
import shutil
import signal
import subprocess
import time

from campaign_gate import (atomic_json as gate_atomic_json,
                           lease_ok as gate_lease_ok,
                           validate_observation as gate_validate_observation)

SCENARIOS = ['cold', 'unchanged', 'one-edit', 'ten-edit', 'rename', 'delete', 'branch-switch', 'manifest-edit']
def sha(path):
    return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()


def load_module(path):
    spec = importlib.util.spec_from_file_location('p1_scenario', path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def prime(root):
    # Fixed ordered whole-readable-file prime, not a claim of disk-cold execution.
    count = total = 0
    for base, dirs, files in os.walk(root, followlinks=False):
        dirs[:] = sorted(d for d in dirs if d != '.git' and not pathlib.Path(base, d).is_symlink())
        for name in sorted(files):
            path = pathlib.Path(base, name)
            if path.is_symlink() or not path.is_file():
                continue
            with path.open('rb') as stream:
                while True:
                    block = stream.read(1024 * 1024)
                    if not block:
                        break
                    total += len(block)
            count += 1
    return {'files': count, 'bytes': total}


def disk_bytes(root):
    total = 0
    for base, dirs, files in os.walk(root, followlinks=False):
        dirs[:] = [d for d in dirs if not pathlib.Path(base, d).is_symlink()]
        for name in files:
            path = pathlib.Path(base, name)
            if not path.is_symlink() and path.is_file():
                total += path.stat().st_size
    return total


def child(binary, config, work, timeout, stop_path=None, lease_path=None):
    """Run one child, retaining exact process/output evidence on interruption."""
    config_path = work / 'request.json'
    output = work / 'response.json'
    log = work / 'process.log'
    config_path.write_text(json.dumps(config, sort_keys=True))
    output.unlink(missing_ok=True)
    env = os.environ.copy()
    env.update({'ENTIRE_GRAPH_EXTRACTION_CORPUS_CONFIG': str(config_path),
                'ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT': str(output),
                'GOMAXPROCS': '4', 'GIT_CONFIG_GLOBAL': '/dev/null',
                'GIT_CONFIG_SYSTEM': '/dev/null', 'GOPROXY': 'off',
                'GOSUMDB': 'off', 'GOTOOLCHAIN': 'local', 'GOTELEMETRY': 'off'})
    for key in list(env):
        if key.startswith('ENTIRE_GRAPH_RANK_') or key in [
                'ENTIRE_GRAPH_EXTRACTION_EVALUATION', 'ENTIRE_GRAPH_RELATION_PROFILE',
                'ENTIRE_GRAPH_COMPILER_LIVE', 'ENTIRE_GRAPH_COMPILER_QUALITY_OUTPUT']:
            env.pop(key)
    start = time.monotonic_ns()
    timed_out = False
    interrupted = None
    with log.open('wb') as stream:
        proc = subprocess.Popen([str(binary), '-test.run=^TestExtractionCorpusMeasurement$',
                                 '-test.count=1', '-test.v'], env=env, stdout=stream,
                                stderr=subprocess.STDOUT, start_new_session=True)
        while True:
            pid, status, usage = os.wait4(proc.pid, os.WNOHANG)
            if pid:
                break
            if stop_path is not None and pathlib.Path(stop_path).exists():
                interrupted = 'manual_stop_requested'
            elif lease_path is not None and not gate_lease_ok(pathlib.Path(lease_path)):
                interrupted = 'supervisor_lease_missing_or_expired'
            elif (time.monotonic_ns() - start) / 1e9 > timeout:
                timed_out = True
            if interrupted or timed_out:
                os.killpg(proc.pid, signal.SIGKILL)
                pid, status, usage = os.wait4(proc.pid, 0)
                break
            time.sleep(.01)
        proc.returncode = os.waitstatus_to_exitcode(status)
    row = {}
    malformed = False
    raw_output = None
    if output.exists():
        try:
            row = json.loads(output.read_text())
            if not isinstance(row, dict):
                malformed = True
                row = {'raw_output': row}
        except (ValueError, OSError) as exc:
            malformed = True
            try:
                raw_output = output.read_text(errors='replace')[:16384]
            except OSError:
                raw_output = None
            row = {'malformed_output': True, 'parse_error': str(exc), 'raw_output': raw_output}
    row.update({'wall_ns': time.monotonic_ns() - start,
                'peak_rss_bytes': int(usage.ru_maxrss) * 1024,
                'process_exit': proc.returncode, 'timed_out': timed_out,
                'response_path': str(output), 'process_log_path': str(log)})
    if malformed:
        row.update(status='error', error='measurement process emitted malformed JSON')
    elif interrupted:
        row.update(status='interrupted', error=interrupted, interruption_reason=interrupted)
    elif timed_out:
        row.update(status='timeout', error='preregistered 120 second deadline')
    elif proc.returncode:
        row.update(status='error', error=row.get('error', 'measurement process failed'))
    elif not row.get('status'):
        row.update(status='error', error='measurement process emitted no observation')
    row['process_log'] = log.read_text(errors='replace')[-12000:]
    return row


def planned_cells(tasks, records, stage, trials):
    scenarios = ['baseline'] if stage == 'baseline' else SCENARIOS
    repetitions = 3 if stage == 'baseline' else trials
    for task in tasks:
        repository = records[task['repository']]['id']
        for verb in ('snapshot', 'search'):
            for scenario in scenarios:
                for trial in range(repetitions):
                    arms = [False] if stage == 'baseline' else [False, True]
                    for reuse in arms:
                        yield (repository, task['profile'], verb, scenario, trial, reuse)


def main():
    ap = argparse.ArgumentParser()
    for name in ('root', 'binary', 'manifest', 'scenario-script', 'assignment', 'output'):
        ap.add_argument(f'--{name}', type=pathlib.Path, required=True)
    ap.add_argument('--stage', choices=['baseline', 'campaign'], required=True)
    ap.add_argument('--trials', type=int, default=30)
    ap.add_argument('--frozen-baseline', type=pathlib.Path)
    ap.add_argument('--stop-file', type=pathlib.Path)
    ap.add_argument('--supervisor-lease', type=pathlib.Path)
    ap.add_argument('--require-supervisor', action='store_true')
    args = ap.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    stop_path = args.stop_file or args.output / 'STOP'
    lease_path = args.supervisor_lease or args.output / 'supervisor-lease.json'
    pause_path = args.output / 'pause.json'
    if pause_path.exists():
        raise SystemExit('Existing pause.json; no autoresume is permitted')
    manifest = json.loads(args.manifest.read_text())
    records = {record['id']: record for record in manifest['repositories']}
    tasks = json.loads(args.assignment.read_text())
    scenario = load_module(args.scenario_script)
    outpath = args.output / f'{args.stage}.ndjson'
    if outpath.exists():
        raise SystemExit('Exclusive output already exists; do not overwrite observations')
    binary_digest = sha(args.binary)
    manifest_digest = sha(args.manifest)
    metadata = {'binary_sha256': binary_digest, 'manifest_sha256': manifest_digest,
                'input_manifest_sha256': manifest_digest, 'compiler': 'off',
                'ranking': 'current', 'runner_sha256': sha(__file__),
                'gate_sha256': sha(pathlib.Path(__file__).with_name('campaign_gate.py')),
                'scenario_sha256': sha(args.scenario_script), 'assignment': tasks,
                'stage': args.stage, 'uname': list(os.uname()),
                'page_cache': 'ordered whole-file prime before each request; no disk-cold claim',
                'rss': 'Linux wait4 whole child including verification; harness overhead included',
                'trials': args.trials, 'require_supervisor': args.require_supervisor}
    frozen = json.loads(args.frozen_baseline.read_text()) if args.frozen_baseline else {}
    if args.stage == 'campaign' and args.trials == 30 and not frozen:
        raise SystemExit('Full campaign requires frozen baseline manifest')
    if frozen and (frozen.get('binary_sha256') != binary_digest or frozen.get('input_manifest_sha256') != manifest_digest):
        raise SystemExit('Frozen binary/input identity mismatch')
    if args.frozen_baseline:
        metadata['frozen_baseline_sha256'] = sha(args.frozen_baseline)
    (args.output / f'{args.stage}-manifest.json').write_text(json.dumps(metadata, indent=2))
    plan = list(planned_cells(tasks, records, args.stage, args.trials))
    count = 0
    blocked = []
    seen = set()
    paused = False

    with outpath.open('x') as out:
        progress = args.output / 'progress.json'

        def key(row):
            fields = ('repository', 'profile', 'verb', 'scenario', 'trial', 'reuse')
            if not all(field in row for field in fields):
                return None
            return tuple(row[field] for field in fields)

        def emit(row):
            nonlocal count
            row_key = key(row)
            if row_key is not None:
                seen.add(row_key)
            count += 1
            out.write(json.dumps(row, sort_keys=True) + '\n')
            out.flush()
            gate_atomic_json(progress, {'stage': args.stage, 'observations': count,
                                   'last': {field: row.get(field) for field in
                                            ('repository', 'profile', 'verb', 'scenario', 'trial', 'reuse', 'status')},
                                   'blocked': blocked})

        def unrun(cell, reason):
            repository, profile, verb, scenario_name, trial, reuse = cell
            emit({'repository': repository, 'profile': profile, 'verb': verb,
                  'scenario': scenario_name, 'trial': trial, 'reuse': reuse,
                  'status': 'unrun', 'error': 'worker paused fail-closed; not measured',
                  'unrun_reason': reason['reason_code'], 'pause_reason_code': reason['reason_code']})

        def mark_remaining(reason):
            for cell in plan:
                if cell not in seen:
                    unrun(cell, reason)

        def pause(reason, cell=None, observed=None, pair_rows=None):
            nonlocal paused
            if paused:
                return
            paused = True
            payload = {'format_version': 1, 'status': 'paused', 'stage': args.stage,
                       'reason_code': reason['reason_code'], 'reason': reason,
                       'cell': dict(zip(('repository', 'profile', 'verb', 'scenario', 'trial', 'reuse'), cell)) if cell else None,
                       'observed': observed, 'pair_rows': pair_rows,
                       'planned_unrun_count': sum(1 for item in plan if item not in seen),
                       'stop_file': str(stop_path), 'supervisor_lease': str(lease_path) if args.require_supervisor else None,
                       'created_at_unix': time.time()}
            gate_atomic_json(pause_path, payload)
            mark_remaining(reason)
            gate_atomic_json(progress, {'stage': args.stage, 'observations': count, 'done': False,
                                   'paused': True, 'pause_path': str(pause_path),
                                   'pause_reason_code': reason['reason_code'], 'blocked': blocked})

        def preflight(cell):
            if stop_path.exists():
                return {'reason_code': 'manual_stop_requested', 'message': 'STOP file exists'}
            if args.require_supervisor and not gate_lease_ok(lease_path):
                return {'reason_code': 'supervisor_lease_missing_or_expired', 'message': 'supervisor lease is absent or expired'}
            try:
                if sha(args.binary) != binary_digest:
                    return {'reason_code': 'binary_identity_drift', 'message': 'binary changed before request'}
                if sha(args.manifest) != manifest_digest:
                    return {'reason_code': 'manifest_identity_drift', 'message': 'input manifest changed before request'}
            except OSError as exc:
                return {'reason_code': 'identity_unreadable', 'message': str(exc)}
            return None

        if stop_path.exists():
            pause({'reason_code': 'manual_stop_requested', 'message': 'STOP file exists before first request'})
        else:
            for task in tasks:
                if paused:
                    break
                record = records[task['repository']]
                repo = args.root / record['id']
                profile = task['profile']
                for verb in ('snapshot', 'search'):
                    if paused:
                        break
                    if args.stage == 'campaign' and any(item.get('repository') == record['id'] and
                                                       item.get('profile') == profile and item.get('verb') == verb
                                                       for item in frozen.get('blocked_strata', [])):
                        pause({'reason_code': 'baseline_preblocked',
                               'message': 'frozen baseline contains a blocked stratum'},
                              (record['id'], profile, verb,
                               'baseline' if args.stage == 'baseline' else SCENARIOS[0], 0, False))
                        break
                    if hasattr(scenario, 'repo_path'):
                        try:
                            verified_id, verified_root, _ = scenario.repo_path(str(repo))
                            if verified_id != record['id'] or verified_root.resolve() != repo.resolve():
                                pause({'reason_code': 'source_identity_drift', 'message': 'fixture identity mismatch'},
                                      (record['id'], profile, verb, 'baseline' if args.stage == 'baseline' else SCENARIOS[0], 0, False))
                                break
                        except Exception as exc:
                            pause({'reason_code': 'source_identity_error', 'message': str(exc)})
                            break
                    scenarios = ['baseline'] if args.stage == 'baseline' else SCENARIOS
                    repetitions = 3 if args.stage == 'baseline' else args.trials
                    for case in scenarios:
                        if paused:
                            break
                        for trial in range(repetitions):
                            if paused:
                                break
                            cell = (record['id'], profile, verb, case, trial, False)
                            pair_rows = []
                            try:
                                scenario.reset(repo, record)
                                cache = args.output / 'request' / 'cache'
                                cache.parent.mkdir(exist_ok=True)
                                if cache.exists():
                                    shutil.rmtree(cache)
                                cache.mkdir()
                                config = {'version': 1, 'repository': record['id'], 'repo_path': str(repo),
                                          'operation': verb, 'mode': 'measure', 'cache': 'off',
                                          'cache_dir': str(cache), 'profile': profile,
                                          'query': record.get('query', manifest.get('query')),
                                          'provider_version': 'p1-corpus-20260905', 'top_k': 8,
                                          'max_indexed_files': 0, 'input_manifest_sha256': manifest_digest,
                                          'binary_sha256': binary_digest}
                                if args.stage == 'campaign' and case != 'cold':
                                    issue = preflight(cell)
                                    if issue:
                                        pause(issue, cell)
                                        break
                                    warm_source = scenario.digest(repo)['effective_tracked_input_sha256']
                                    warm_before_binary, warm_before_manifest = sha(args.binary), sha(args.manifest)
                                    warm = child(args.binary, dict(config, mode='warm', cache='on',
                                                                  source_digest=warm_source),
                                                  args.output / 'request', 120, stop_path,
                                                  lease_path if args.require_supervisor else None)
                                    warm_after_source = scenario.digest(repo)['effective_tracked_input_sha256']
                                    warm_after_binary, warm_after_manifest = sha(args.binary), sha(args.manifest)
                                    warm.update(semantic_digest=warm.get('semantic_sha256'),
                                                source_digest=warm.get('source_digest'),
                                                source_unchanged=warm_after_source == warm_source,
                                                cache_bytes=disk_bytes(cache),
                                                partial_failures_count=warm.get('partial_failures_count',
                                                                                 len(warm.get('partial_failures', []))),
                                                extraction=warm.get('extraction') or (warm.get('stats') or {}).get('extraction'))
                                    with (args.output / 'warming.ndjson').open('a') as stream:
                                        stream.write(json.dumps(dict(warm, repository=record['id'], profile=profile,
                                                                     verb=verb, scenario=case, trial=trial), sort_keys=True) + '\n')
                                    warm_issue = gate_validate_observation(warm, binary_digest, manifest_digest,
                                                                          warm_source, warm_after_source)
                                    if not warm_issue and (warm_before_binary != binary_digest or warm_after_binary != binary_digest):
                                        warm_issue = {'reason_code': 'binary_identity_drift', 'message': 'binary changed during warm-up'}
                                    if not warm_issue and (warm_before_manifest != manifest_digest or warm_after_manifest != manifest_digest):
                                        warm_issue = {'reason_code': 'manifest_identity_drift', 'message': 'input manifest changed during warm-up'}
                                    if warm_issue:
                                        pause(warm_issue, cell, warm)
                                        break
                                if case not in ('baseline', 'cold', 'unchanged'):
                                    scenario.apply(repo, record, case)
                                source = scenario.digest(repo)['effective_tracked_input_sha256']
                                arms = [False] if args.stage == 'baseline' else ([False, True] if trial % 2 == 0 else [True, False])
                                issue = None
                                for reuse in arms:
                                    current_cell = (record['id'], profile, verb, case, trial, reuse)
                                    issue = preflight(current_cell)
                                    if issue:
                                        break
                                    prep = prime(repo)
                                    issue = preflight(current_cell)
                                    if issue:
                                        break
                                    before_binary, before_manifest = sha(args.binary), sha(args.manifest)
                                    row = child(args.binary, dict(config, cache='on' if reuse else 'off',
                                                                  source_digest=source, mutation_id=case),
                                                 args.output / 'request', 120, stop_path,
                                                 lease_path if args.require_supervisor else None)
                                    try:
                                        after_source = scenario.digest(repo)['effective_tracked_input_sha256']
                                        after_binary, after_manifest = sha(args.binary), sha(args.manifest)
                                    except Exception as exc:
                                        after_source, after_binary, after_manifest = source, before_binary, before_manifest
                                        issue = {'reason_code': 'identity_unreadable', 'message': str(exc)}
                                    row.update(repository=record['id'], profile=profile, verb=verb, scenario=case,
                                               trial=trial, reuse=reuse, semantic_digest=row.get('semantic_sha256'),
                                               source_digest=row.get('source_digest'),
                                               source_unchanged=after_source == source, cache_bytes=disk_bytes(cache),
                                               prime=prep, partial_failures_count=row.get('partial_failures_count',
                                                                                        len(row.get('partial_failures', []))),
                                               extraction=row.get('extraction') or (row.get('stats') or {}).get('extraction'),
                                               runner_binary_sha256=before_binary, runner_manifest_sha256=before_manifest)
                                    if not issue and (before_binary != binary_digest or after_binary != binary_digest):
                                        issue = {'reason_code': 'binary_identity_drift', 'message': 'binary changed during request'}
                                    if not issue and (before_manifest != manifest_digest or after_manifest != manifest_digest):
                                        issue = {'reason_code': 'manifest_identity_drift', 'message': 'input manifest changed during request'}
                                    if not issue:
                                        issue = gate_validate_observation(row, binary_digest, manifest_digest, source, after_source)
                                    if not issue and stop_path.exists():
                                        issue = {'reason_code': 'manual_stop_requested', 'message': 'STOP file appeared during request'}
                                    pair_rows.append(row)
                                    if issue:
                                        break
                                equivalent = len(pair_rows) == 2 and not issue and all(item.get('source_unchanged') for item in pair_rows)
                                if equivalent and pair_rows[0].get('semantic_digest') != pair_rows[1].get('semantic_digest'):
                                    issue = {'reason_code': 'paired_digest_mismatch', 'message': 'paired semantic digests differ'}
                                    equivalent = False
                                for finished in pair_rows:
                                    finished['extraction'] = finished.get('extraction') or {}
                                    finished['extraction']['stale_source'] = False if equivalent else None
                                    finished['paired_freshness_basis'] = ('same fixed source bytes and semantic output as fresh cache-off reference'
                                                                           if equivalent else 'unverified')
                                    emit(finished)
                                if issue:
                                    pause(issue, cell, pair_rows[-1] if pair_rows else None, pair_rows)
                                    break
                            except Exception as exc:
                                pause({'reason_code': 'runner_process_error', 'message': str(exc)}, cell,
                                      pair_rows[-1] if pair_rows else None, pair_rows or None)
                                break
                            finally:
                                if not paused:
                                    try:
                                        scenario.reset(repo, record)
                                    except Exception as exc:
                                        pause({'reason_code': 'fixture_reset_error', 'message': str(exc)}, cell)
                            if paused:
                                break
                        if paused:
                            break
                    if paused:
                        break
                if paused:
                    break
    if not paused:
        gate_atomic_json(progress, {'stage': args.stage, 'observations': count, 'done': True, 'blocked': blocked})
    cache = args.output / 'request' / 'cache'
    if cache.exists() and not paused:
        shutil.rmtree(cache)
    return 2 if paused else 0


if __name__ == '__main__':
    raise SystemExit(main())
