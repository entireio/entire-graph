#!/usr/bin/env python3
"""Verify transferred fixture bytes against the premeasurement local capture."""
import importlib.util,json,pathlib,argparse
HERE=pathlib.Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('scenario',HERE/'corpus-tools/p1_scenario.py');scenario=importlib.util.module_from_spec(spec);spec.loader.exec_module(scenario)
expected=json.loads((HERE/'expected-inputs.json').read_text());actual={}
for repo_id,wanted in expected.items():
 _,root,record=scenario.repo_path('/opt/p1/corpus/'+repo_id)
 actual[repo_id]=scenario.digest(root)
 if actual[repo_id]!=wanted:raise SystemExit('Transferred fixture identity mismatch: '+repo_id)
parser=argparse.ArgumentParser();parser.add_argument('--output',type=pathlib.Path,required=True);args=parser.parse_args()
args.output.write_text(json.dumps(actual,indent=2)+'\n')
print('All six fixture input digests verified')
