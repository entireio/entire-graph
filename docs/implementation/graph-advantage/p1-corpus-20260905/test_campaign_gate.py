import json
import pathlib
import tempfile
import time
import unittest

import campaign_gate


def valid():
    return {'status': 'ok', 'semantic_digest': 's', 'source_digest': 'src',
            'elapsed_ns': 1, 'wall_ns': 1, 'peak_rss_bytes': 1, 'cache_bytes': 0,
            'phase_ns': {'parse': 1}, 'extraction': {}, 'partial_failures_count': 0,
            'source_unchanged': True}


class CampaignGateTests(unittest.TestCase):
    def test_atomic_pause_is_complete_json(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'pause.json'
            campaign_gate.atomic_json(path, {'reason_code': 'manual_stop_requested'})
            self.assertEqual(json.loads(path.read_text())['reason_code'], 'manual_stop_requested')
            self.assertFalse(list(pathlib.Path(directory).glob('.pause.json.*.tmp')))

    def test_lease_rejects_unbounded_or_nonfinite_values(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'lease.json'
            for expiry in (float('inf'), float('nan'), time.time() + 301):
                path.write_text(json.dumps({'expires_at': expiry}))
                self.assertFalse(campaign_gate.lease_ok(path))
            path.write_text(json.dumps({'expires_at': time.time() + 60}))
            self.assertTrue(campaign_gate.lease_ok(path))

    def test_partial_and_missing_metrics_fail_closed(self):
        partial = valid()
        partial['status'] = 'partial'
        self.assertEqual(campaign_gate.validate_observation(partial, 'b', 'm', 'src', 'src')['reason_code'],
                         'unexpected_partial')
        missing = valid()
        del missing['semantic_digest']
        self.assertEqual(campaign_gate.validate_observation(missing, 'b', 'm', 'src', 'src')['reason_code'],
                         'missing_semantic_digest')

    def test_digest_and_source_identity_fail_closed(self):
        row = valid()
        row['semantic_digest'] = 'different'
        row['binary_sha256'] = 'wrong'
        self.assertEqual(campaign_gate.validate_observation(row, 'b', 'm', 'src', 'src')['reason_code'],
                         'binary_identity_drift')
        row.pop('binary_sha256')
        self.assertEqual(campaign_gate.validate_observation(row, 'b', 'm', 'src', 'other')['reason_code'],
                         'source_identity_drift')


if __name__ == '__main__':
    unittest.main()
