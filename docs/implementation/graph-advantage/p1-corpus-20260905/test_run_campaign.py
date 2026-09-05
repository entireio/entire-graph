import importlib.util,json,pathlib,subprocess,sys,tempfile,unittest
HERE=pathlib.Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('runner',HERE/'run_campaign.py');runner=importlib.util.module_from_spec(spec);spec.loader.exec_module(runner)
class RunnerContracts(unittest.TestCase):
 def test_complete_fake_matrix_and_exclusive_output(self):
  with tempfile.TemporaryDirectory() as d:
   root=pathlib.Path(d);(root/'fixture').mkdir();(root/'fixture/a.go').write_text('package p\n')
   binary=root/'fake';binary.write_text('#!'+sys.executable+'\nimport os,json\njson.dump({"semantic_sha256":"fixed","status":"ok","elapsed_ns":1},open(os.environ["ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT"],"w"))\n');binary.chmod(0o700)
   scenario=root/'scenario.py';scenario.write_text('def reset(root,record):pass\ndef apply(root,record,scenario):pass\ndef digest(root):return {"effective_tracked_input_sha256":"fixed-source"}\n')
   manifest=root/'manifest.json';manifest.write_text(json.dumps({'repositories':[{'id':'fixture','query':'a'}]}))
   assignment=root/'assignment.json';assignment.write_text(json.dumps([{'repository':'fixture','profile':'fast'}]))
   cmd=[sys.executable,str(HERE/'run_campaign.py'),'--root',d,'--binary',str(binary),'--manifest',str(manifest),'--scenario-script',str(scenario),'--assignment',str(assignment),'--output',str(root/'out'),'--stage','campaign','--trials','1']
   subprocess.run(cmd,check=True,capture_output=True)
   rows=[json.loads(s) for s in (root/'out/campaign.ndjson').read_text().splitlines()]
   self.assertEqual(len(rows),32)
   self.assertEqual({r['scenario'] for r in rows},set(runner.SCENARIOS))
   self.assertTrue(all(r['source_unchanged'] and r['status']=='ok' for r in rows))
   self.assertNotEqual(subprocess.run(cmd,capture_output=True).returncode,0)
 def test_failing_treatment_stops_without_hiding_planned_cells(self):
  with tempfile.TemporaryDirectory() as d:
   root=pathlib.Path(d);(root/'fixture').mkdir()
   binary=root/'fake';binary.write_text('#!'+sys.executable+'\nimport os,json\nc=json.load(open(os.environ["ENTIRE_GRAPH_EXTRACTION_CORPUS_CONFIG"]))\njson.dump({"semantic_sha256":"fixed","status":"error" if c["cache"]=="on" else "ok","elapsed_ns":1},open(os.environ["ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT"],"w"))\n');binary.chmod(0o700)
   scenario=root/'scenario.py';scenario.write_text('def reset(root,record):pass\ndef apply(root,record,scenario):pass\ndef digest(root):return {"effective_tracked_input_sha256":"fixed-source"}\n')
   manifest=root/'manifest.json';manifest.write_text(json.dumps({'repositories':[{'id':'fixture','query':'a'}]}))
   assignment=root/'assignment.json';assignment.write_text(json.dumps([{'repository':'fixture','profile':'fast'}]))
   subprocess.run([sys.executable,str(HERE/'run_campaign.py'),'--root',d,'--binary',str(binary),'--manifest',str(manifest),'--scenario-script',str(scenario),'--assignment',str(assignment),'--output',str(root/'out'),'--stage','campaign','--trials','4'],check=True,capture_output=True)
   rows=[json.loads(s) for s in (root/'out/campaign.ndjson').read_text().splitlines()]
   self.assertEqual(len(rows),128)
   self.assertEqual(sum(r['status']=='error' for r in rows),6)
   self.assertEqual(sum(r['status']=='unrun' for r in rows),116)
 def test_process_timeout_is_explicit(self):
  with tempfile.TemporaryDirectory() as d:
   root=pathlib.Path(d);p=root/'slow';p.write_text('#!'+sys.executable+'\nimport time\ntime.sleep(10)\n');p.chmod(0o700)
   row=runner.child(p,{},root,.05)
   self.assertEqual(row['status'],'timeout');self.assertTrue(row['timed_out']);self.assertLess(row['wall_ns'],2_000_000_000)
if __name__=='__main__':unittest.main()
