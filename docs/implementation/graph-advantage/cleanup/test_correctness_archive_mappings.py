import io,json,unittest,tarfile,re,hashlib,subprocess
from pathlib import Path
HERE=Path(__file__).resolve().parent
class TestCorrectnessArchiveMappings(unittest.TestCase):
 def test_five_runs_have_combination_and_query_mappings(self):
  d=json.loads((HERE/'correctness-archive-mappings.json').read_text()); self.assertEqual(len(d['records']),10)
  combo=[x for x in d['records'] if x['member']=='combination.json']
  queries=[x for x in d['records'] if x['member']=='queries.json']
  self.assertEqual(len(combo),5); self.assertEqual(len(queries),5)
  self.assertEqual({x['dataset'] for x in d['records']},{'combination','queries'})
  self.assertEqual(len({x['canonical_run_path'] for x in combo}),5)
  for x in combo:
   self.assertEqual(x['case_count'],5); self.assertTrue(x['canonical_case_sha256'])
   self.assertEqual(x['fixture_origin'],'hand-authored plan section 8 combination contract')
   self.assertIn('combination dataset only',x['projection_parity'])
  qmeta=json.loads((HERE/'correctness-query-metadata.json').read_text())
  self.assertEqual(len(qmeta['records']),5)
  self.assertEqual({x['replacement_path'] for x in queries},{'docs/implementation/graph-advantage/cleanup/correctness-query-cases.ndjson'})
  self.assertEqual(sum(x['case_count'] for x in queries),25)
  self.assertEqual({x['fixture_origin'] for x in queries},{'hand-authored interim review F2 promoted-method and alias correctness fixture'})
  self.assertEqual(len((HERE/'correctness-query-cases.ndjson').read_text().splitlines()),25)

 def test_query_stream_is_ordered_sanitized_archive_projection(self):
  """Re-decode each retained archive and compare every query response field."""
  def sanitize(x,key=''):
   if isinstance(x,dict): return {k:sanitize(v,k) for k,v in x.items()}
   if isinstance(x,list): return [sanitize(v,key) for v in x]
   if isinstance(x,str):
    y=re.sub(r'/Users/[^/\s]+(?:/[^\s]*)?', '<redacted-local-path>', x)
    y=re.sub(r'/tmp/[^\s]+', '<redacted-temp-path>', y)
    y=re.sub(r'/opt/[^\s]+', '<redacted-infra-path>', y)
    y=re.sub(r'https?://[^\s"\']+', '<redacted-url>', y)
    return '<redacted-sensitive-value>' if re.search(r'(password|secret|token|credential|subscription|resource.?id|azure.?id)',key,re.I) else y
   return x
  stream=[json.loads(x) for x in (HERE/'correctness-query-cases.ndjson').read_text().splitlines()]
  byrun={}
  for x in stream: byrun.setdefault(x['run_id'],[]).append(x)
  provenance=json.loads((HERE/'archive-provenance.json').read_text()); ref=provenance['audited_head']; repo=HERE.parents[3]
  paths=[a['archive_path'] for a in provenance['archives'] if '/correctness-' in a['archive_path']]
  self.assertEqual(len(paths),5)
  for ap in sorted(paths):
   run=ap.split('correctness-')[1].split('/')[0]
   payload=subprocess.check_output(['git','show',f'{ref}:{ap}'],cwd=repo)
   with tarfile.open(fileobj=io.BytesIO(payload),mode='r:gz') as t: d=json.load(t.extractfile('queries.json'))
   self.assertEqual([x['case_id'] for x in byrun[run]],list(d['responses']))
   for x,(case,response) in zip(byrun[run],d['responses'].items()):
    self.assertEqual(x['dataset'],'queries'); self.assertEqual(x['fixture_origin'],d['fixture_origin'])
    self.assertEqual(x['failed'],d.get('failed',False)); self.assertEqual(x['response'],sanitize(response)); self.assertEqual(x['sources'],sanitize(d['sources']))
if __name__=='__main__':unittest.main()
