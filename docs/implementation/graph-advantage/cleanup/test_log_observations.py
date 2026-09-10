import json,sys,tempfile,unittest
from pathlib import Path
HERE=Path(__file__).resolve().parent
sys.path.insert(0,str(HERE))
import log_observations
class TestLogObservations(unittest.TestCase):
 def test_extracts_all_late_named_outcomes_and_sanitizes(self):
  with tempfile.TemporaryDirectory() as d:
   p=Path(d)/"x.log"; lines=[f"--- PASS: Early{i} (0.01s)" for i in range(30)]; lines += ["--- FAIL: LateFailure (0.01s)","--- SKIP: LateSkip (0.01s)","source=/Users/a/repo sha256="+"0"*64]; p.write_text("\n".join(lines)+"\n")
   x=log_observations.extract(p,Path(d))
   self.assertEqual(x["named_test_counts"],{"pass":30,"fail":1,"skip":1}); self.assertEqual(x["named_tests"][-1]["name"],"LateSkip"); self.assertNotIn("/Users/",json.dumps(x)); self.assertEqual(len(x["source_hashes"]),1)
 def test_build_excludes_protected_and_keeps_unrecognized_errors(self):
  with tempfile.TemporaryDirectory() as d:
   root=Path(d); ev=root/"docs/implementation/graph-advantage/evidence"; ev.mkdir(parents=True); (ev/"a.txt").write_text("panic: unusual failure detail\nok example/pkg 0.1s\nBenchmarkLookup-8 123 ns/op\n{\"status\":\"failed\",\"exit_code\":1}\nGo version go1.24 module example\n"); (ev/"check-25887f69-linux-full").mkdir(); (ev/"check-25887f69-linux-full/review").mkdir(); (ev/"check-25887f69-linux-full/review/local-prep-dir.txt").write_text("protected")
   out=log_observations.build(root); self.assertEqual(out["file_count"],1); self.assertEqual(out["files"][0]["disposition"],"retain_pending_consumer_review"); facts=out["content_records"][0]; self.assertEqual(facts["unclassified_signal_count"],1); self.assertTrue(facts["named_tests"]); self.assertGreaterEqual(facts["scalar_observation_count"],2); self.assertEqual(out["files"][0]["legacy_role"],"text evidence"); self.assertEqual(out["files"][0]["replacement_proposal"]["status"],"candidate_only")

 def test_identical_files_share_complete_facts_with_distinct_aliases(self):
  with tempfile.TemporaryDirectory() as d:
   root=Path(d); ev=root/"docs/implementation/graph-advantage/evidence"; ev.mkdir(parents=True)
   body="--- FAIL: SameCase (0.01s)\nsource_sha256="+"1"*64+"\n"
   (ev/"one.txt").write_text(body); (ev/"two.txt").write_text(body)
   out=log_observations.build(root); self.assertEqual(out["file_count"],2); self.assertEqual(out["content_count"],1); self.assertEqual({f["content_id"] for f in out["files"]},{out["content_records"][0]["sha256"]}); self.assertEqual(out["content_records"][0]["named_test_counts"],{"pass":0,"fail":1,"skip":0}); self.assertEqual({f["path"] for f in out["files"]},{"docs/implementation/graph-advantage/evidence/one.txt","docs/implementation/graph-advantage/evidence/two.txt"})

 def test_unclassified_signal_truncation_and_skip_reasons_are_explicit(self):
  with tempfile.TemporaryDirectory() as d:
   p=Path(d)/"x.log"
   p.write_text("\n".join([f"error: failure detail {i}" for i in range(90)]+["=== RUN   TestLateSkip","    late_test.go:91: requires live compiler","--- SKIP: TestLateSkip (0.00s)","SKIP: TestStatusLine platform unavailable"]) + "\n")
   x=log_observations.extract(p,Path(d))
   self.assertEqual(x["unclassified_signal_count"],90)
   self.assertEqual(x["scalar_observation_count"],90)
   self.assertFalse(x["scalar_observations_truncated"])
   self.assertEqual(len(x["scalar_observations"]),90)
   self.assertEqual(x["named_test_counts"],{"pass":0,"fail":0,"skip":2})
   self.assertEqual(x["named_tests"][-2],{"outcome":"skip","name":"TestLateSkip","details":[{"value":"requires live compiler","truncated":False}]})
   self.assertEqual(x["named_tests"][-1],{"outcome":"skip","name":"TestStatusLine","details":[{"value":"platform unavailable","truncated":False}]})

 def test_python_unittest_outcomes_and_detail_truncation_are_preserved(self):
  with tempfile.TemporaryDirectory() as d:
   p=Path(d)/"x.txt"; p.write_text("test_ok (suite.Case) ... ok\ntest_skip (suite.Case) ... skipped "+"x"*240+"\n")
   x=log_observations.extract(p,Path(d))
   self.assertEqual(x["named_test_counts"],{"pass":1,"fail":0,"skip":1})
   self.assertTrue(x["named_tests"][1]["details"][0]["truncated"])
if __name__=="__main__": unittest.main()
