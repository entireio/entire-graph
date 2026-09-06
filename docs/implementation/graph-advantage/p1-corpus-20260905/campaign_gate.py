"""Pure fail-closed gates shared by the P1 campaign runner and tests."""
import hashlib
import json
import math
import math
import os
import pathlib
import tempfile
import time

REQUIRED_FIELDS = ('semantic_digest', 'source_digest', 'elapsed_ns', 'wall_ns',
                   'peak_rss_bytes', 'cache_bytes', 'phase_ns', 'extraction',
                   'partial_failures_count')


def sha(path):
    return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()


def atomic_json(path, value):
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=f'.{path.name}.', suffix='.tmp', dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump(value, stream, sort_keys=True, indent=2)
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        try:
            directory = os.open(path.parent, os.O_RDONLY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
        except OSError:
            pass
    finally:
        pathlib.Path(temporary).unlink(missing_ok=True)


def lease_ok(path):
    try:
        value = json.loads(pathlib.Path(path).read_text())
        expiry = value['expires_at']
        if isinstance(expiry, bool):
            return False
        expiry = float(expiry)
        now = time.time()
        return math.isfinite(expiry) and now < expiry <= now + 300
    except (OSError, ValueError, TypeError, KeyError):
        return False


def validate_observation(row, expected_binary, expected_manifest, source_before, source_after):
    """Return the first machine-readable failure, or None for a valid row."""
    if not isinstance(row, dict):
        return {'reason_code': 'malformed_output', 'message': 'observation was not an object'}
    if row.get('malformed_output'):
        return {'reason_code': 'malformed_output', 'message': 'observation output was malformed'}
    status = row.get('status')
    if status != 'ok':
        code = {'timeout': 'request_timeout', 'interrupted': row.get('interruption_reason', 'request_interrupted'),
                'partial': 'unexpected_partial', 'error': 'process_error'}.get(status, 'invalid_status')
        return {'reason_code': code, 'message': f'observation status was {status!r}'}
    if row.get('partial_failures_count') not in (0, 0.0):
        return {'reason_code': 'unexpected_partial', 'message': 'partial failures were reported'}
    for field in REQUIRED_FIELDS:
        if field not in row or row[field] is None:
            code = 'missing_semantic_digest' if field == 'semantic_digest' else 'missing_metrics'
            return {'reason_code': code, 'message': f'required field missing: {field}', 'field': field}
    for field, minimum in [('elapsed_ns', 1), ('wall_ns', 1), ('peak_rss_bytes', 1), ('cache_bytes', 0)]:
        value = row.get(field)
        if not isinstance(value, (int, float)) or isinstance(value, bool) or not math.isfinite(value) or value < minimum:
            return {'reason_code': 'missing_metrics', 'message': f'invalid resource metric: {field}', 'field': field}
    if not isinstance(row.get('phase_ns'), dict) or not isinstance(row.get('extraction'), dict):
        return {'reason_code': 'missing_metrics', 'message': 'phase_ns and extraction must be objects'}
    if not isinstance(row.get('semantic_digest'), str) or not row['semantic_digest']:
        return {'reason_code': 'missing_semantic_digest', 'message': 'semantic digest is absent'}
    if not isinstance(row.get('source_digest'), str) or row['source_digest'] != source_before:
        return {'reason_code': 'source_identity_drift', 'message': 'source digest did not match pre-request source'}
    if source_after != source_before or row.get('source_unchanged') is not True:
        return {'reason_code': 'source_identity_drift', 'message': 'source changed during measurement'}
    if row.get('binary_sha256') is not None and row['binary_sha256'] != expected_binary:
        return {'reason_code': 'binary_identity_drift', 'message': 'child binary digest changed'}
    for field in ('manifest_sha256', 'input_manifest_sha256'):
        if row.get(field) is not None and row[field] != expected_manifest:
            return {'reason_code': 'manifest_identity_drift', 'message': f'{field} changed'}
    return None
