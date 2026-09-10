import json,unittest,re
from pathlib import Path
from validate_precleanup_attestation import verify as verify_attestation
HERE=Path(__file__).resolve().parent
class TestCorrectnessArchiveMappings(unittest.TestCase):
 def test_five_runs_have_combination_and_query_mappings(self):
  d=json.loads((HERE/'correctness-archive-mappings.json').read_text()); self.assertEqual(len(d['records']),10)
  combo=[x for x in d['records'] if x['member']=='combination.json']
  queries=[x for x in d['records'] if x['member']=='queries.json']
  self.assertEqual(len(combo),5); self.assertEqual(len(queries),5)
  self.assertEqual({x['dataset'] for x in d['records']},{'combination','queries'})
  self.assertEqual({x['replacement_path'] for x in combo},{'docs/implementation/graph-advantage/cleanup/correctness-combination-records.json'})
  for x in combo:
   self.assertEqual(x['case_count'],5); self.assertTrue(x['source_member_sha256']); self.assertTrue(x['projection_sha256'])
   self.assertEqual(x['fixture_origin'],'hand-authored plan section 8 combination contract')
   self.assertIn('full sanitized',x['projection_parity'])
  combo_records=json.loads((HERE/'correctness-combination-records.json').read_text())
  self.assertEqual(combo_records['record_count'],5); self.assertEqual(combo_records['response_count'],25)
  self.assertTrue(all(len(x['response_order'])==5 and list(x['responses'])==x['response_order'] for x in combo_records['records']))
  qmeta=json.loads((HERE/'correctness-query-metadata.json').read_text())
  self.assertEqual(len(qmeta['records']),5)
  self.assertEqual({x['replacement_path'] for x in queries},{'docs/implementation/graph-advantage/cleanup/correctness-query-cases.ndjson'})
  self.assertEqual(sum(x['case_count'] for x in queries),25)
  self.assertEqual({x['fixture_origin'] for x in queries},{'hand-authored interim review F2 promoted-method and alias correctness fixture'})
  self.assertEqual(len((HERE/'correctness-query-cases.ndjson').read_text().splitlines()),25)

 def test_query_stream_is_ordered_sanitized_archive_projection(self):
  """Check the full retained query stream against the original-parity attestation."""
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
  self.assertEqual(verify_attestation(HERE.parents[3],HERE/'precleanup-parity-attestation.json'),[])
  stream=[json.loads(x) for x in (HERE/'correctness-query-cases.ndjson').read_text().splitlines()]
  byrun={}
  for x in stream: byrun.setdefault(x['run_id'],[]).append(x)
  self.assertEqual(len(byrun),5)
  self.assertTrue(all(len(rows)==5 for rows in byrun.values()))
  for rows in byrun.values():
   self.assertTrue(all(x['dataset']=='queries' for x in rows))
   self.assertTrue(all(x['fixture_origin']=='hand-authored interim review F2 promoted-method and alias correctness fixture' for x in rows))
if __name__=='__main__':unittest.main()
