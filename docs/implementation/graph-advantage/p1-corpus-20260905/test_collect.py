import contextlib
import io
import json
import pathlib
import sys
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import collect


class CollectorLayoutTests(unittest.TestCase):
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

    def test_run_scoped_archive_script_and_blob_are_isolated(self):
        calls = []

        def run(vm, script):
            calls.append(script)
            return json.dumps(['status'])

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
            return json.dumps(['status'])

        with mock.patch.object(collect, 'VMS', ['worker-a']), \
                mock.patch.object(collect.cloud, 'environment', return_value='env'), \
                mock.patch.object(collect.cloud, 'run', side_effect=run), \
                mock.patch.object(collect.cloud, 'url', return_value='sas'), \
                mock.patch.object(collect.cloud, 'download'), \
                mock.patch.object(sys, 'argv', ['collect.py', '--stage', 'baseline', '--output', '/tmp/out']):
            collect.main()

        self.assertIn('tar -czf /opt/p1/baseline-results.tar.gz -C /opt/p1 results baseline.log', calls[0])


if __name__ == '__main__':
    unittest.main()
