import contextlib
import io
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import collect


class CollectorLayoutTests(unittest.TestCase):
    def execute_guard(self, *, load='loaded', active='inactive', progress=None, markers=()):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory) / 'run'
            root.mkdir()
            if progress is not None:
                (root / 'progress.json').write_text(json.dumps(progress))
            for marker in markers:
                (root / marker).write_text('terminal evidence')
            fake_bin = pathlib.Path(directory) / 'bin'
            fake_bin.mkdir()
            systemctl = fake_bin / 'systemctl'
            systemctl.write_text(
                '#!/bin/sh\n'
                'printf "LoadState=%s\\nActiveState=%s\\nSubState=dead\\n" "$FAKE_LOAD" "$FAKE_ACTIVE"\n'
            )
            systemctl.chmod(0o700)
            paths = collect.layout('campaign', 'guard-run')
            paths['results_dir'] = str(root)
            env = os.environ.copy()
            env['PATH'] = str(fake_bin) + os.pathsep + env['PATH']
            env['FAKE_LOAD'] = load
            env['FAKE_ACTIVE'] = active
            return subprocess.run(
                ['/bin/sh', '-c', 'set -eu\n' + collect.terminal_guard('campaign', paths)],
                env=env, capture_output=True, text=True,
            )

    def test_legacy_layout_is_preserved(self):
        self.assertEqual(collect.layout('baseline'), {
            'results_dir': '/opt/p1/results',
            'unit': 'p1-baseline',
            'log': '/opt/p1/baseline.log',
            'token': 'baseline',
        })

    def test_run_layout_is_isolated(self):
        self.assertEqual(collect.layout('campaign', 'canary-01'), {
            'results_dir': '/opt/p1/runs/canary-01',
            'unit': 'p1-campaign-canary-01',
            'log': '/opt/p1/runs/canary-01/campaign.log',
            'token': 'campaign-canary-01',
        })

    def test_run_id_is_fail_closed(self):
        for value in ('../escape', 'UPPER', 'with space', ''):
            with self.subTest(value=value):
                with self.assertRaises(ValueError):
                    collect.layout('campaign', value)

    def test_remote_guard_accepts_paused_terminal_run(self):
        result = self.execute_guard(
            progress={'stage': 'campaign', 'done': False}, markers=('pause.json',)
        )
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_remote_guard_accepts_missing_progress_with_supervisor_stop(self):
        result = self.execute_guard(markers=('STOP', 'supervisor-stop.json'))
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_remote_guard_refuses_active_unit(self):
        result = self.execute_guard(
            active='active', progress={'stage': 'campaign', 'done': False}, markers=('pause.json',)
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('still active', result.stderr)

    def test_remote_guard_refuses_unknown_unit(self):
        result = self.execute_guard(
            load='not-found', progress={'stage': 'campaign', 'done': True}
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('unknown', result.stderr)

    def test_status_collection_uses_run_scoped_paths(self):
        calls = []

        def run(vm, script):
            calls.append((vm, script))
            return json.dumps(['status'])

        with mock.patch.object(collect, 'VMS', ['worker-a']), \
                mock.patch.object(collect.cloud, 'run', side_effect=run), \
                mock.patch.object(sys, 'argv', ['collect.py', '--stage', 'campaign', '--run-id', 'run-7']), \
                contextlib.redirect_stdout(io.StringIO()) as output:
            collect.main()

        self.assertIn('p1-campaign-run-7', calls[0][1])
        self.assertIn('/opt/p1/runs/run-7/progress.json', calls[0][1])
        self.assertIn('/opt/p1/runs/run-7/campaign.log', calls[0][1])
        self.assertIn('"run_id": "run-7"', output.getvalue())

    def test_status_collection_does_not_require_terminal_guard(self):
        calls = []

        def run(vm, script):
            calls.append(script)
            return json.dumps(['active status'])

        with mock.patch.object(collect, 'VMS', ['worker-a']), \
                mock.patch.object(collect.cloud, 'run', side_effect=run), \
                mock.patch.object(collect.cloud, 'environment', side_effect=AssertionError), \
                mock.patch.object(sys, 'argv', ['collect.py', '--stage', 'campaign']):
            collect.main()

        self.assertNotIn('P1_COLLECT_GUARD', calls[0])

    def test_archive_requires_explicit_upload_ack_before_download(self):
        downloads = []

        def run(vm, script):
            return json.dumps(['remote command failed: curl exited 22'])

        with mock.patch.object(collect, 'VMS', ['worker-a']), \
                mock.patch.object(collect.cloud, 'environment', return_value='env'), \
                mock.patch.object(collect.cloud, 'run', side_effect=run), \
                mock.patch.object(collect.cloud, 'url', return_value='sas'), \
                mock.patch.object(collect.cloud, 'download', side_effect=lambda *args: downloads.append(args)), \
                mock.patch.object(sys, 'argv', ['collect.py', '--stage', 'baseline', '--output', '/tmp/out']):
            with self.assertRaisesRegex(RuntimeError, 'upload was not acknowledged'):
                collect.main()

        self.assertEqual(downloads, [])

    def test_run_scoped_archive_script_and_blob_are_isolated(self):
        calls = []

        def run(vm, script):
            calls.append(script)
            return json.dumps(['status', 'P1_UPLOAD_OK p1-20260905-baseline-run-7-worker-1.tar.gz'])

        with mock.patch.object(collect, 'VMS', ['worker-a']), \
                mock.patch.object(collect.cloud, 'environment', return_value='env'), \
                mock.patch.object(collect.cloud, 'run', side_effect=run), \
                mock.patch.object(collect.cloud, 'url', return_value='sas'), \
                mock.patch.object(collect.cloud, 'download'), \
                mock.patch.object(sys, 'argv', ['collect.py', '--stage', 'baseline', '--run-id', 'run-7', '--output', '/tmp/out']):
            collect.main()

        script = calls[0]
        self.assertIn('/opt/p1/runs/run-7/progress.json', script)
        self.assertIn('tar -czf /opt/p1/baseline-run-7-results.tar.gz -C /opt/p1/runs/run-7 .', script)
        self.assertIn('/opt/p1/baseline-run-7-results.tar.gz', script)

    def test_legacy_archive_shape_is_preserved(self):
        calls = []

        def run(vm, script):
            calls.append(script)
            return json.dumps(['status', 'P1_UPLOAD_OK p1-20260905-baseline-worker-1.tar.gz'])

        with mock.patch.object(collect, 'VMS', ['worker-a']), \
                mock.patch.object(collect.cloud, 'environment', return_value='env'), \
                mock.patch.object(collect.cloud, 'run', side_effect=run), \
                mock.patch.object(collect.cloud, 'url', return_value='sas'), \
                mock.patch.object(collect.cloud, 'download'), \
                mock.patch.object(sys, 'argv', ['collect.py', '--stage', 'baseline', '--output', '/tmp/out']):
            collect.main()

        self.assertIn('tar -czf /opt/p1/baseline-results.tar.gz -C /opt/p1 results baseline.log', calls[0])

    def test_local_archive_is_not_overwritten_unless_identical(self):
        with tempfile.TemporaryDirectory() as directory:
            destination = pathlib.Path(directory) / 'worker-1.tar.gz'

            def download_same(blob, path, env):
                pathlib.Path(path).write_bytes(b'archive-a')

            with mock.patch.object(collect.cloud, 'download', side_effect=download_same):
                collect.download_immutable('blob', destination, 'env')
                collect.download_immutable('blob', destination, 'env')
            self.assertEqual(destination.read_bytes(), b'archive-a')

            def download_different(blob, path, env):
                pathlib.Path(path).write_bytes(b'archive-b')

            with mock.patch.object(collect.cloud, 'download', side_effect=download_different):
                with self.assertRaisesRegex(RuntimeError, 'refusing to overwrite'):
                    collect.download_immutable('blob', destination, 'env')
            self.assertEqual(destination.read_bytes(), b'archive-a')


if __name__ == '__main__':
    unittest.main()
