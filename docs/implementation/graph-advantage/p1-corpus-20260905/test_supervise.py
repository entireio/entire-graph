import json,pathlib,sys,tempfile,unittest
sys.path.insert(0,str(pathlib.Path(__file__).resolve().parent))
import supervise
class SupervisorTests(unittest.TestCase):
 def exercise(self,states):
  calls=[]
  def run(vm,script):
   calls.append((vm,script))
   if 'P1_STATUS' in script:
    s=states[vm]
    if isinstance(s,Exception):raise s
    return json.dumps(['P1_STATUS '+json.dumps(s)])
   return json.dumps(['P1_LEASE renewed'])
  with tempfile.TemporaryDirectory() as d:
   result=supervise.supervise(list(states),'campaign',d,run=run,sleep=lambda _:None)
   paused=(pathlib.Path(d)/'supervisor-pause.json').exists()
  return result,paused,calls
 def test_remote_scripts_are_valid_python(self):
  for script in (supervise.status_script('campaign','/opt/p1/runs/new','p1-campaign-new'),supervise.lease_script('/opt/p1/runs/new'),supervise.stop_script('campaign','diagnose','/opt/p1/runs/new','p1-campaign-new')):
   code=script.split("python3 - <<'P1_SUPERVISOR'\n",1)[1].split('\nP1_SUPERVISOR',1)[0]
   compile(code,'remote-supervisor','exec')
 def test_first_worker_pause_stops_every_worker(self):
  result,paused,calls=self.exercise({'a':{'state':'active','paused':True},'b':{'state':'active'}})
  self.assertFalse(result);self.assertTrue(paused)
  self.assertEqual({vm for vm,script in calls if 'systemctl stop' in script},{'a','b'})
  self.assertFalse(any('P1_LEASE' in script for _,script in calls))
 def test_control_plane_failure_stops_all(self):
  result,paused,calls=self.exercise({'a':RuntimeError('unreachable'),'b':{'state':'active'}})
  self.assertFalse(result);self.assertTrue(paused)
 def test_completed_workers_exit_without_renewal(self):
  result,paused,calls=self.exercise({'a':{'state':'inactive','exit_code':0,'progress':{'stage':'campaign','done':True}}})
  self.assertTrue(result);self.assertFalse(paused);self.assertEqual(len(calls),1)
 def test_done_flag_cannot_hide_failed_service(self):
  for state,code in [('failed',1),('inactive',1),('inactive',None)]:
   with self.subTest(state=state,code=code):
    result,paused,calls=self.exercise({'a':{'state':state,'exit_code':code,'progress':{'stage':'campaign','done':True}}})
    self.assertFalse(result);self.assertTrue(paused)
 def test_done_flag_waits_for_successful_process_exit(self):
  self.assertFalse(supervise.completed({'state':'active','exit_code':0,'progress':{'stage':'campaign','done':True}},'campaign'))
 def test_invalid_status_is_not_health(self):
  with self.assertRaises(RuntimeError):supervise.parse_status(json.dumps(['Enable succeeded']))
 def test_renewal_requires_positive_acknowledgement(self):
  with self.assertRaises(RuntimeError):supervise.renew('a',lambda *args:'Enable failed','/opt/p1/results')
 def test_wrong_stage_completion_is_failure(self):
  result,paused,calls=self.exercise({'a':{'state':'inactive','progress':{'stage':'baseline','done':True}}})
  self.assertFalse(result);self.assertTrue(paused)
if __name__=='__main__':unittest.main()
