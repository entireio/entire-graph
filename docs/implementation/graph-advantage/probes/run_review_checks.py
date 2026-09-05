import hashlib,json,os,pathlib,subprocess,time
root=pathlib.Path.cwd(); e=root/'docs/implementation/graph-advantage/evidence'
commit=subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip()
paths=subprocess.check_output(['git','ls-files','internal','cmd','scripts','go.mod','go.sum','mise.toml'],text=True).splitlines()
def hashes():return {p:hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest() for p in paths if pathlib.Path(p).is_file()}
before=hashes();(e/'review-source-freeze.json').write_text(json.dumps({'commit':commit,'files':before},indent=2)+'\n')
env=os.environ.copy()
for key in list(env):
 if key.startswith('ENTIRE_GRAPH_RANK_') or key in ['ENTIRE_GRAPH_RELATION_PROFILE','ENTIRE_GRAPH_EXTRACTION_EVALUATION','ENTIRE_GRAPH_COMPILER_LIVE','ENTIRE_GRAPH_COMPILER_QUALITY_OUTPUT']:env.pop(key)
started=time.monotonic()
with (e/'review-mise-check.txt').open('w') as log:
 log.write('Immutable integration commit: '+commit+'\nCommand: mise run check\nComparative evaluation environment disabled.\n');log.flush()
 result=subprocess.run(['mise','run','check'],stdout=log,stderr=subprocess.STDOUT,env=env)
after=hashes()
changed=[p for p,h in before.items() if after.get(p)!=h]
record={'commit':commit,'command':'mise run check','exit_code':result.returncode,'elapsed_seconds':round(time.monotonic()-started,3),'source_unchanged':not changed,'changed_sources':changed}
(e/'review-check-result.json').write_text(json.dumps(record,indent=2)+'\n')
print(json.dumps(record),flush=True)
