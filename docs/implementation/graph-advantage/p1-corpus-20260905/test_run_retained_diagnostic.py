import argparse
import contextlib
import io
import json
import pathlib
import sys
import tempfile
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import run_retained_diagnostic as diagnostic


class FakeTransport:
    def __init__(self, response='P1_UPLOAD_OK artifact.tar.gz', error=None):
        self.response = response
        self.error = error
        self.calls = []

    def environment(self):
        self.calls.append(('environment',))
        return 'env'

    def upload(self, path, blob, env):
        self.calls.append(('upload', pathlib.Path(path), blob, env))

    def url(self, name, permissions, env):
        self.calls.append(('url', name, permissions, env))
        return f'https://storage/{name}?{permissions}'

    def run(self, vm, script):
        self.calls.append(('run', vm, script))
        if self.error:
            raise self.error
        artifact = next((item[1] for item in reversed(self.calls)
                         if item[0] == 'url' and item[2] == 'cw'), 'artifact.tar.gz')
        return json.dumps([self.response.replace('artifact.tar.gz', artifact)])


class RetainedDiagnosticTests(unittest.TestCase):
    def test_remote_script_is_single_fail_fast_run_and_refuses_campaign(self):
        script = diagnostic.remote_script(
            source_url='https://source', archive_sha256='a' * 64,
            expected_digest='b' * 64, artifact_url='https://artifact',
            artifact_blob='artifact.tar.gz',
            remote_root='/opt/p1/retained-diagnostics/run-1', run_id='run-1',
        )
        self.assertIn("systemctl list-units --state=active", script)
        self.assertIn('retained diagnostic run directory already exists', script)
        self.assertIn("-test.run=^TestRetainedSearchDiagnostic$", script)
        self.assertIn('"-test.count=1"', script)
        self.assertIn('"-test.timeout=5m"', script)
        self.assertIn('start_new_session=True', script)
        self.assertIn('os.killpg', script)
        self.assertIn('P1_EXPECTED', script)
        self.assertIn('P1_AFTER_EXPECTED', script)
        self.assertIn('GOPROXY=off', script)
        self.assertIn('GOSUMDB=off', script)
        self.assertIn('GOTOOLCHAIN=local', script)
        self.assertIn('timeout --signal=TERM --kill-after=15s 300s', script)
        self.assertIn('> "$root/build.log" 2>&1', script)
        self.assertIn('archive_out="/opt/p1/retained-diagnostics/retained-diagnostic-run-1.tar.gz"', script)
        self.assertIn('tar -czf "$archive_out" -C "$root" -T "$archive_list"', script)
        self.assertIn('runtime-environment.json', script)
        self.assertIn('LANG=C.UTF-8', script)
        self.assertIn('P1_CORPUS_ROOT=/opt/p1/corpus', script)
        self.assertIn('corpus-tools/p1_scenario.py', script)
        self.assertIn('raw/off.json', script)
        self.assertIn('raw/on.json', script)
        self.assertIn('raw/digests.json', script)
        self.assertNotIn("printf '%s\\n' raw >>", script)
        self.assertNotIn('raw/cache-off', script)
        self.assertNotIn('raw/cache-on', script)
        self.assertIn('set +e\n    runuser -u graphcheck -- /usr/bin/python3 - "$root"', script)
        self.assertNotIn('tar -czf "$archive_out" -C "$root" .', script)
        self.assertNotIn('run_campaign.py', script)
        self.assertNotIn('launch.py', script)
        self.assertNotIn('az vm start', script)

    def test_persisted_script_evidence_redacts_signed_urls(self):
        script = diagnostic.remote_script(
            source_url='https://storage/source?sig=secret-source',
            archive_sha256='a' * 64, expected_digest='b' * 64,
            artifact_url='https://storage/artifact?sig=secret-artifact',
            artifact_blob='artifact.tar.gz',
            remote_root='/opt/p1/retained-diagnostics/run-1', run_id='run-1',
        )
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            diagnostic.persist_script_evidence(
                output, script, source_blob='source.tgz', artifact_blob='artifact.tar.gz',
                remote_root='/opt/p1/retained-diagnostics/run-1')
            record = (output / 'remote-script.json').read_text()
            self.assertNotIn('https://', record)
            self.assertNotIn('secret-source', record)
            self.assertNotIn('secret-artifact', record)
            self.assertNotIn('remote-script.sh', [path.name for path in output.iterdir()])

    def test_azure_json_string_envelope_is_decoded(self):
        self.assertEqual(
            diagnostic.decode_run_command_response(json.dumps(['P1_UPLOAD_OK blob'])),
            ['P1_UPLOAD_OK blob'],
        )
        self.assertEqual(diagnostic.decode_run_command_response('plain output'), 'plain output')

    def test_run_requires_unique_run_id_and_output(self):
        args = argparse.Namespace(run_id='../escape', output=pathlib.Path('/tmp/nope'),
                                  source_commit='HEAD', source_root=diagnostic.REPO_ROOT,
                                  vm=diagnostic.DEFAULT_VM)
        with self.assertRaisesRegex(ValueError, 'invalid run id'):
            diagnostic.run(args)

        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / 'existing'
            output.mkdir()
            args.run_id = 'run-1'
            args.output = output
            with self.assertRaises(FileExistsError):
                diagnostic.run(args)

    def _run_with_transport(self, transport, downloader=None):
        if downloader is None:
            downloader = lambda blob, path, env: pathlib.Path(path).write_bytes(b'archive')
        with tempfile.TemporaryDirectory() as directory:
            args = argparse.Namespace(
                run_id='run-1', output=pathlib.Path(directory) / 'out',
                source_commit='frozen', source_root=pathlib.Path(directory), vm='worker-a',
            )
            with mock.patch.object(diagnostic, 'resolve_commit', return_value='1' * 40), \
                    mock.patch.object(diagnostic, 'expected_corpus_digest', return_value='b' * 64), \
                    mock.patch.object(diagnostic, 'sha256_file', side_effect=['harness', 'a' * 64]), \
                    mock.patch.object(diagnostic, 'build_source_archive',
                                      side_effect=lambda root, commit, destination, metadata:
                                      destination.write_bytes(b'source')):
                result = diagnostic.run(args, transport=transport, downloader=downloader)
            return pathlib.Path(directory), result

    def test_upload_ack_is_required_before_download(self):
        transport = FakeTransport(response='remote output without acknowledgement')
        downloaded = []
        with self.assertRaisesRegex(RuntimeError, 'upload was not acknowledged'):
            self._run_with_transport(
                transport, downloader=lambda *args: downloaded.append(args))
        self.assertEqual(downloaded, [])

    def test_acknowledged_upload_is_downloaded_once(self):
        transport = FakeTransport()
        downloaded = []

        def download(blob, path, env):
            downloaded.append((blob, pathlib.Path(path), env))
            pathlib.Path(path).write_bytes(b'archive')

        _, result = self._run_with_transport(transport, download)
        self.assertEqual(result['outcome'], 'artifact_collected')
        self.assertEqual(len(downloaded), 1)
        self.assertEqual(downloaded[0][2], 'env')

    def test_transport_failure_records_unknown_outcome(self):
        transport = FakeTransport(error=RuntimeError('run-command unavailable'))
        with tempfile.TemporaryDirectory() as directory:
            args = argparse.Namespace(
                run_id='run-1', output=pathlib.Path(directory) / 'out',
                source_commit='frozen', source_root=pathlib.Path(directory), vm='worker-a',
            )
            with mock.patch.object(diagnostic, 'resolve_commit', return_value='1' * 40), \
                    mock.patch.object(diagnostic, 'expected_corpus_digest', return_value='b' * 64), \
                    mock.patch.object(diagnostic, 'sha256_file', side_effect=['harness', 'a' * 64]), \
                    mock.patch.object(diagnostic, 'build_source_archive',
                                      side_effect=lambda root, commit, destination, metadata:
                                      destination.write_bytes(b'source')):
                with self.assertRaisesRegex(RuntimeError, 'run-command unavailable'):
                    diagnostic.run(args, transport=transport)
            unknown = json.loads((args.output / 'unknown.json').read_text())
            self.assertEqual(unknown['outcome'], 'unknown')
            self.assertIn('run-command unavailable', unknown['reason'])

    def test_parser_requires_frozen_source_commit_and_run_id(self):
        with contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit):
                diagnostic.parser().parse_args(['--run-id', 'r', '--output', '/tmp/o'])
            with self.assertRaises(SystemExit):
                diagnostic.parser().parse_args(['--source-commit', 'HEAD', '--output', '/tmp/o'])


if __name__ == '__main__':
    unittest.main()
