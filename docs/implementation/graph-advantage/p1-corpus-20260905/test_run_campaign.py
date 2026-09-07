import importlib.util
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
spec = importlib.util.spec_from_file_location('runner', HERE / 'run_campaign.py')
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)
import batch_budget


class RunnerContracts(unittest.TestCase):
    def case(self, mode='ok', dynamic_source=False):
        root = pathlib.Path(tempfile.mkdtemp())
        (root / 'fixture').mkdir()
        (root / 'fixture' / 'a.go').write_text('package p\n')
        counter = root / 'counter'
        fake = root / 'fake'
        fake.write_text(f'''#!{sys.executable}
import json, os, pathlib, time
c = json.load(open(os.environ["ENTIRE_GRAPH_EXTRACTION_CORPUS_CONFIG"]))
counter = pathlib.Path(os.environ["FAKE_COUNTER"])
counter.write_text(str(int(counter.read_text() or "0") + 1) if counter.exists() else "1")
mode = os.environ.get("FAKE_MODE", "{mode}")
if mode == "slow": time.sleep(10)
if mode == "source": pathlib.Path(c["repo_path"], "a.go").write_text("package changed\\n")
if mode == "manifest": pathlib.Path(os.environ["FAKE_MANIFEST_PATH"]).write_text("{{\\"repositories\\":[]}}")
row = {{"status":"ok", "semantic_sha256":"fixed", "elapsed_ns":1,
       "source_digest":c.get("source_digest"),
       "phase_ns":{{"parse":1}}, "extraction":{{"files_parsed":0}},
       "partial_failures_count":0}}
if mode == "error" and c.get("cache") == "on": row = {{"status":"error", "error":"fake failure"}}
if mode == "partial": row.update(status="partial", partial_failures_count=1)
if mode == "mismatch" and c.get("cache") == "on": row["semantic_sha256"] = "different"
json.dump(row, open(os.environ["ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT"], "w"))
''')
        fake.chmod(0o700)
        scenario = root / 'scenario.py'
        if dynamic_source:
            scenario.write_text('''import hashlib, pathlib
def reset(root, record): pass
def apply(root, record, case): pass
def digest(root): return {"effective_tracked_input_sha256": hashlib.sha256(pathlib.Path(root, "a.go").read_bytes()).hexdigest()}
''')
        else:
            scenario.write_text('''def reset(root, record): pass
def apply(root, record, case): pass
def digest(root): return {"effective_tracked_input_sha256": "fixed-source"}
''')
        manifest = root / 'manifest.json'
        manifest.write_text(json.dumps({'repositories': [{'id': 'fixture', 'query': 'a'}]}))
        assignment = root / 'assignment.json'
        assignment.write_text(json.dumps([{'repository': 'fixture', 'profile': 'fast'}]))
        return root, fake, manifest, assignment, counter, scenario

    def command(self, root, fake, manifest, assignment, scenario, output, stage='campaign', trials='1', extra=()):
        selected_build = root / 'build-auto.json'
        selected_build.write_text(json.dumps({'binary_sha256': runner.sha(fake),
                                              'source_file_hash_manifest_sha256': 'a' * 64}))
        extra = tuple(extra)
        if '--build-manifest' in extra:
            selected_build = pathlib.Path(extra[extra.index('--build-manifest') + 1])
        batch_path = root / 'batch.json'
        selected = []
        tasks = json.loads(assignment.read_text())
        scenarios = ['baseline'] if stage == 'baseline' else runner.SCENARIOS
        repetitions = 3 if stage == 'baseline' else int(trials)
        for task in tasks:
            for verb in ('snapshot', 'search'):
                for case in scenarios:
                    for trial in range(repetitions):
                        arms = [False] if stage == 'baseline' else ([False, True] if trial % 2 == 0 else [True, False])
                        cell = {'worker': 1, 'repository': task['repository'], 'profile': task['profile'],
                                'verb': verb, 'scenario': case, 'trial': trial, 'arms': arms,
                                'preparatory_invocations': int(stage == 'campaign' and case != 'cold')}
                        cell['cell_id'] = batch_budget.cell_id(cell)
                        selected.append(cell)
        allocation = {'1': sum(cell['preparatory_invocations'] + len(cell['arms']) for cell in selected)}
        batch_path.write_text(json.dumps({
            'version': 1, 'batch_id': 'synthetic-test-batch', 'scope': 'bounded-diagnostic', 'cap': 100,
            'identity': {'binary_sha256': runner.sha(fake), 'input_manifest_sha256': runner.sha(manifest),
                         'runner_sha256': runner.sha(HERE / 'run_campaign.py'),
                         'scenario_sha256': runner.sha(scenario),
                         'gate_sha256': runner.sha(HERE / 'campaign_gate.py'),
                         'budget_sha256': runner.sha(HERE / 'batch_budget.py'),
                         'build_manifest_sha256': runner.sha(selected_build)},
            'cells': selected, 'workers': allocation,
        }))
        return [sys.executable, str(HERE / 'run_campaign.py'), '--root', str(root), '--binary', str(fake),
                '--manifest', str(manifest), '--scenario-script', str(scenario), '--assignment', str(assignment),
                '--output', str(output), '--stage', stage, '--trials', trials,
                '--batch-manifest', str(batch_path), '--batch-worker', '1',
                '--build-manifest', str(selected_build), *extra]

    def execute(self, case, mode=None, extra=(), check=False):
        root, fake, manifest, assignment, counter, scenario = case
        output = root / 'out'
        env = os.environ.copy()
        env['FAKE_COUNTER'] = str(counter)
        env['FAKE_MANIFEST_PATH'] = str(manifest)
        if mode:
            env['FAKE_MODE'] = mode
        result = subprocess.run(self.command(root, fake, manifest, assignment, scenario, output, extra=extra),
                                env=env, capture_output=True, text=True, check=check)
        rows = [json.loads(line) for line in (output / 'campaign.ndjson').read_text().splitlines()]
        return result, output, rows

    def test_complete_canary_matrix_and_exclusive_output(self):
        case = self.case()
        result, output, rows = self.execute(case)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(rows), 32)
        self.assertTrue(all(row['status'] == 'ok' and row['source_unchanged'] for row in rows))
        self.assertEqual(json.loads((output / 'progress.json').read_text())['done'], True)
        self.assertNotEqual(self.execute(case)[0].returncode, 0)

    def test_build_manifest_identity_is_recorded_and_binary_is_pinned(self):
        case = self.case()
        root, fake, manifest, assignment, counter, scenario = case
        build = root / 'build.json'
        build.write_text(json.dumps({
            'binary_sha256': runner.sha(fake),
            'source_file_hash_manifest_sha256': 'a' * 64,
        }))
        result, output, _ = self.execute(
            case,
            extra=('--build-manifest', str(build)),
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        metadata = json.loads((output / 'campaign-manifest.json').read_text())
        self.assertEqual(metadata['build_manifest_sha256'], runner.sha(build))
        self.assertEqual(metadata['source_file_hash_manifest_sha256'], 'a' * 64)

    def test_first_process_error_pauses_without_starting_another_child(self):
        case = self.case(mode='error')
        result, output, rows = self.execute(case, mode='error')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(int(case[4].read_text()), 2)
        pause = json.loads((output / 'pause.json').read_text())
        self.assertEqual(pause['reason_code'], 'process_error')
        self.assertEqual(sum(row['status'] == 'unrun' for row in rows), 30)

    def test_unexpected_partial_pauses_on_first_observation(self):
        result, output, rows = self.execute(self.case(mode='partial'), mode='partial')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(json.loads((output / 'pause.json').read_text())['reason_code'], 'unexpected_partial')
        self.assertEqual(int(rows[0]['trial']), 0)
        self.assertEqual(len(rows), 32)

    def test_paired_digest_mismatch_pauses_after_preserving_both_rows(self):
        result, output, rows = self.execute(self.case(mode='mismatch'), mode='mismatch')
        self.assertNotEqual(result.returncode, 0)
        pause = json.loads((output / 'pause.json').read_text())
        self.assertEqual(pause['reason_code'], 'paired_digest_mismatch')
        self.assertEqual(len(pause['pair_rows']), 2)
        self.assertEqual(len(rows), 32)

    def test_source_identity_drift_pauses(self):
        result, output, rows = self.execute(self.case(mode='source', dynamic_source=True), mode='source')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(json.loads((output / 'pause.json').read_text())['reason_code'], 'source_identity_drift')
        self.assertEqual(rows[0]['status'], 'ok')

    def test_manifest_identity_drift_pauses(self):
        result, output, _ = self.execute(self.case(mode='manifest'), mode='manifest')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(json.loads((output / 'pause.json').read_text())['reason_code'], 'manifest_identity_drift')

    def test_missing_semantic_or_resource_metrics_pauses(self):
        case = self.case()
        pathlib.Path(case[1]).write_text(f'''#!{sys.executable}
import json, os
json.dump({{"status":"ok", "elapsed_ns":1}}, open(os.environ["ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT"], "w"))
''')
        pathlib.Path(case[1]).chmod(0o700)
        result, output, _ = self.execute(case)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(json.loads((output / 'pause.json').read_text())['reason_code'], 'missing_semantic_digest')

    def test_malformed_output_pauses_before_another_child(self):
        case = self.case()
        pathlib.Path(case[1]).write_text(f'''#!{sys.executable}
import os
open(os.environ["ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT"], "w").write("{{")
''')
        pathlib.Path(case[1]).chmod(0o700)
        result, output, rows = self.execute(case)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(json.loads((output / 'pause.json').read_text())['reason_code'], 'malformed_output')
        self.assertEqual(len(rows), 32)

    def test_stop_file_pauses_before_any_child_and_is_not_resumable(self):
        case = self.case()
        root, fake, manifest, assignment, counter, scenario = case
        output = root / 'out'
        output.mkdir()
        (output / 'STOP').write_text('operator stop\n')
        result = subprocess.run(self.command(root, fake, manifest, assignment, scenario, output),
                                env={**os.environ, 'FAKE_COUNTER': str(counter)}, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(counter.exists())
        self.assertEqual(json.loads((output / 'pause.json').read_text())['reason_code'], 'manual_stop_requested')
        self.assertNotEqual(subprocess.run(self.command(root, fake, manifest, assignment, scenario, output),
                                           capture_output=True).returncode, 0)

    def test_missing_batch_manifest_refuses_without_starting_child(self):
        case = self.case()
        root, fake, manifest, assignment, counter, scenario = case
        output = root / 'out-no-batch'
        command = [sys.executable, str(HERE / 'run_campaign.py'), '--root', str(root),
                   '--binary', str(fake), '--manifest', str(manifest), '--scenario-script', str(scenario),
                   '--assignment', str(assignment), '--output', str(output), '--stage', 'campaign', '--trials', '1']
        result = subprocess.run(command, env={**os.environ, 'FAKE_COUNTER': str(counter)},
                                capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('batch manifest', result.stderr.lower())
        self.assertFalse(counter.exists())

    def test_selected_batch_runs_only_selected_pair(self):
        case = self.case()
        root, fake, manifest, assignment, counter, scenario = case
        output = root / 'selected-out'
        argv = self.command(root, fake, manifest, assignment, scenario, output)
        batch = json.loads((root / 'batch.json').read_text())
        batch['cells'] = [cell for cell in batch['cells']
                         if cell['verb'] == 'snapshot' and cell['scenario'] == 'cold']
        batch['workers'] = {'1': 2}
        (root / 'batch.json').write_text(json.dumps(batch))
        result = subprocess.run(argv,
                                env={**os.environ, 'FAKE_COUNTER': str(counter)}, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        rows = [json.loads(line) for line in (output / 'campaign.ndjson').read_text().splitlines()]
        self.assertEqual(len(rows), 2)
        self.assertEqual({row['verb'] for row in rows}, {'snapshot'})
        self.assertEqual({row['scenario'] for row in rows}, {'cold'})

    def test_batch_order_must_match_frozen_traversal(self):
        case = self.case()
        root, fake, manifest, assignment, counter, scenario = case
        output = root / 'order-out'
        argv = self.command(root, fake, manifest, assignment, scenario, output)
        batch = json.loads((root / 'batch.json').read_text())
        batch['cells'].reverse()
        (root / 'batch.json').write_text(json.dumps(batch))
        result = subprocess.run(argv,
                                env={**os.environ, 'FAKE_COUNTER': str(counter)}, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('order', result.stderr.lower())
        self.assertFalse(counter.exists())

    def test_supervisor_lease_is_required_when_requested(self):
        case = self.case()
        result, output, _ = self.execute(case, extra=('--require-supervisor',))
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(json.loads((output / 'pause.json').read_text())['reason_code'],
                         'supervisor_lease_missing_or_expired')

    def test_final_lease_loss_pauses_before_marking_done(self):
        case = self.case()
        root, fake, manifest, assignment, counter, scenario = case
        output = root / 'out'
        output.mkdir()
        lease = output / 'supervisor-lease.json'
        lease.write_text(json.dumps({'expires_at': runner.time.time() + 180}))
        scenario.write_text('''import os, pathlib
def reset(root, record):
    if pathlib.Path(os.environ["FAKE_COUNTER"]).exists() and pathlib.Path(os.environ["FAKE_COUNTER"]).read_text() == "6":
        pathlib.Path(os.environ["FAKE_LEASE"]).unlink(missing_ok=True)
def apply(root, record, case): pass
def digest(root): return {"effective_tracked_input_sha256": "fixed-source"}
''')
        env = os.environ.copy()
        env.update({'FAKE_COUNTER': str(counter),
                    'FAKE_MANIFEST_PATH': str(manifest), 'FAKE_LEASE': str(lease)})
        result = subprocess.run(self.command(root, fake, manifest, assignment, scenario, output,
                                             stage='baseline', extra=('--require-supervisor',)),
                                env=env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        pause = json.loads((output / 'pause.json').read_text())
        self.assertEqual(pause['reason_code'], 'supervisor_lease_missing_or_expired')
        self.assertFalse(json.loads((output / 'progress.json').read_text())['done'])

    def test_post_request_accounting_failure_retains_observation_row(self):
        case = self.case()
        root, fake, manifest, assignment, counter, scenario = case
        output = root / 'out'
        argv = self.command(root, fake, manifest, assignment, scenario, output, stage='baseline')
        previous_argv = sys.argv
        previous_env = os.environ.copy()
        try:
            sys.argv = [str(HERE / 'run_campaign.py'), *argv[2:]]
            os.environ.update({'FAKE_COUNTER': str(counter), 'FAKE_MANIFEST_PATH': str(manifest)})
            with mock.patch.object(runner, 'disk_bytes', side_effect=OSError('cache disappeared')):
                result = runner.main()
        finally:
            sys.argv = previous_argv
            os.environ.clear()
            os.environ.update(previous_env)
        self.assertEqual(result, 2)
        rows = [json.loads(line) for line in (output / 'baseline.ndjson').read_text().splitlines()]
        self.assertEqual(rows[0]['status'], 'ok')
        self.assertIsNone(rows[0]['cache_bytes'])
        self.assertEqual(json.loads((output / 'pause.json').read_text())['reason_code'],
                         'resource_accounting_error')

    def test_child_stop_interruption_is_distinct_from_timeout(self):
        case = self.case(mode='slow')
        root, fake, _, _, _, _ = case
        work = root / 'work'
        work.mkdir()
        stop = root / 'STOP'
        thread = threading.Timer(.1, stop.write_text, args=('stop',))
        thread.start()
        row = runner.child(fake, {}, work, 2, stop_path=stop)
        thread.join()
        self.assertEqual(row['status'], 'interrupted')
        self.assertEqual(row['interruption_reason'], 'manual_stop_requested')

    def test_process_timeout_is_explicit(self):
        case = self.case(mode='slow')
        root, fake, _, _, _, _ = case
        work = root / 'work'
        work.mkdir()
        row = runner.child(fake, {}, work, .05)
        self.assertEqual(row['status'], 'timeout')
        self.assertTrue(row['timed_out'])
        self.assertLess(row['wall_ns'], 2_000_000_000)


if __name__ == '__main__':
    unittest.main()
