import json,pathlib,sys,tempfile,unittest
sys.path.insert(0,str(pathlib.Path(__file__).resolve().parent))
import canary_admission as gate
class CanaryTests(unittest.TestCase):
 def fixture(self,d):
  p=pathlib.Path(d);identity={'binary_sha256':'frozen'}
  (p/'campaign-manifest.json').write_text(json.dumps(dict(identity,trials=1)))
  (p/'progress.json').write_text(json.dumps({'done':True}))
  rows=[{'repository':'r','profile':'fast','verb':v,'scenario':s,'reuse':a,'trial':0,'status':'ok','partial_failures_count':0,'source_unchanged':True,'elapsed_ns':1,'wall_ns':2,'peak_rss_bytes':3,'semantic_digest':'semantic','source_digest':'source'} for v in ('snapshot','search') for s in gate.SCENARIOS for a in (False,True)]
  return p,identity,rows
 def validate(self,p,identity,rows):
  (p/'campaign.ndjson').write_text('\n'.join(json.dumps(r) for r in rows))
  return gate.validate([p],[[{'repository':'r','profile':'fast'}]],identity)
 def test_complete_matching_canary_admitted(self):
  with tempfile.TemporaryDirectory() as d:
   p,i,r=self.fixture(d);self.assertEqual(self.validate(p,i,r)['observations'],32)
 def test_failure_modes_rejected(self):
  for field,value in [('status','partial'),('partial_failures_count',1),('source_unchanged',False),('semantic_digest','different'),('source_digest','different'),('peak_rss_bytes',None),('elapsed_ns',float('nan'))]:
   with self.subTest(field=field),tempfile.TemporaryDirectory() as d:
    p,i,r=self.fixture(d);r[0][field]=value
    with self.assertRaises(ValueError):self.validate(p,i,r)
 def test_missing_or_duplicate_cells_rejected(self):
  for mutation in ('missing','duplicate'):
   with self.subTest(mutation=mutation),tempfile.TemporaryDirectory() as d:
    p,i,r=self.fixture(d);r=r[:-1] if mutation=='missing' else r+[r[0]]
    with self.assertRaises(ValueError):self.validate(p,i,r)
 def test_cold_regression_pauses_expansion(self):
  with tempfile.TemporaryDirectory() as d:
   p,i,r=self.fixture(d);r[1]['elapsed_ns']=2
   with self.assertRaises(ValueError):self.validate(p,i,r)
 def test_wrong_binary_or_pause_rejected(self):
  with tempfile.TemporaryDirectory() as d:
   p,i,r=self.fixture(d)
   with self.assertRaises(ValueError):self.validate(p,{'binary_sha256':'other'},r)
   (p/'pause.json').write_text('{}')
   with self.assertRaises(ValueError):self.validate(p,i,r)
if __name__=='__main__':unittest.main()
