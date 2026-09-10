import unittest
from build_history_cleanup_attestation import sanitize,validate_query_projection

class BuilderProjectionTest(unittest.TestCase):
 def source(self): return {"failed":True,"fixture_origin":"fixture","sources":{"repo":"/"+"Users/alice/private"},"responses":{"case-a":{"status":"partial","path":"/tmp/a"},"case-b":{"status":"failed"}}}
 def public(self):
  source=self.source(); return [{"case_id":key,"response":sanitize(value),"sources":sanitize(source["sources"]),"failed":True,"fixture_origin":"fixture"} for key,value in source["responses"].items()]
 def test_full_values_order_and_failure_match(self): validate_query_projection(self.source(),self.public())
 def test_order_mutation_fails(self):
  with self.assertRaisesRegex(ValueError,"query order"): validate_query_projection(self.source(),list(reversed(self.public())))
 def test_value_mutation_fails(self):
  rows=self.public(); rows[0]["response"]["status"]="passed"
  with self.assertRaisesRegex(ValueError,"query projection"): validate_query_projection(self.source(),rows)
 def test_failure_mutation_fails(self):
  rows=self.public(); rows[0]["failed"]=False
  with self.assertRaisesRegex(ValueError,"query projection"): validate_query_projection(self.source(),rows)
if __name__=="__main__": unittest.main()
