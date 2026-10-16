import urllib.error, tempfile
import pathlib, subprocess, os, json, urllib.request, threading, http.server, time, hashlib, sys
import argparse
parser=argparse.ArgumentParser(description='Exercise released legacy/current Hosts with a newer candidate CLI and synthetic work.')
parser.add_argument('--legacy',required=True,type=pathlib.Path)
parser.add_argument('--current',required=True,type=pathlib.Path)
parser.add_argument('--candidate',required=True,type=pathlib.Path)
parser.add_argument('--output',type=pathlib.Path)
parser.add_argument('--tui', action='store_true', help='Keep real old TUI processes attached across replacement (Unix PTY)')
args=parser.parse_args()
root=args.output.resolve() if args.output else pathlib.Path(tempfile.mkdtemp(prefix='caelis-host-upgrade-'))
if args.output:root.mkdir(mode=0o700,parents=True,exist_ok=False)
new=args.candidate.resolve();old=args.legacy.resolve();modern=args.current.resolve()
print('Evidence:',root,flush=True)
entered=threading.Event();release=threading.Event();after=threading.Event();after_requests=[]
class Provider(http.server.BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def do_POST(self):
  body=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
  if 'UPGRADE_BLOCK' in json.dumps(body):
   entered.set();release.wait(90)
  text='UPGRADE_TASK_COMPLETE'
  if 'TUI_AFTER_UPGRADE' in json.dumps(body):
   after_requests.append(body);after.set();text='UPGRADE_RECONNECTED_COMPLETE'
  item={'id':'upgrade-message','type':'message','role':'assistant','content':[{'type':'output_text','text':text}]}
  events=[{'type':'response.output_text.delta','item_id':'upgrade-message','output_index':0,'delta':text},{'type':'response.completed','response':{'model':body['model'],'status':'completed','output':[item]}}]
  payload=''.join('data: '+json.dumps(e)+'\n\n' for e in events).encode()
  self.send_response(200);self.send_header('Content-Type','text/event-stream');self.send_header('Content-Length',str(len(payload)));self.end_headers();self.wfile.write(payload)
server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Provider);threading.Thread(target=server.serve_forever,daemon=True).start()
versions={str(p):json.loads(subprocess.check_output([str(p),'version','--format','json'])) for p in [old,modern,new]}
print('Binary identities',versions,flush=True)
evidence=[]
def case(binary,busy):
 name=('legacy' if binary==old else 'modern')+('-busy' if busy else '-idle')
 d=pathlib.Path(tempfile.mkdtemp(prefix=name+'-',dir=root));home=d/'home';home.mkdir();store=d/'store';store.mkdir();work=d/'work';work.mkdir()
 env={'PATH':'/usr/bin:/bin:/usr/sbin:/sbin','HOME':str(home),'XDG_CONFIG_HOME':str(home/'.config'),'TMPDIR':str(d)}
 def cli(p,*args):
  r=subprocess.run([str(p),*args,'--store-dir',str(store),'--format','json'],env=env,cwd=work,capture_output=True,text=True,timeout=45)
  if r.returncode:raise RuntimeError(name+': '+r.stderr)
  return json.loads(r.stdout)
 def api(path,body=None):
  discovery=json.loads((store/'runtime/service/discovery.json').read_text());token=(store/'runtime/service/auth.token').read_text().strip()
  req=urllib.request.Request(discovery['endpoint']+'/api/control/v1'+path,data=json.dumps(body).encode() if body is not None else None,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
  if body and 'operation_id' in body:req.add_header('Idempotency-Key',body['operation_id'])
  if body and 'expected_revision' in body:req.add_header('If-Match',json.dumps(str(body['expected_revision'])))
  try:
   with urllib.request.urlopen(req,timeout=20) as r:return json.load(r)
  except urllib.error.HTTPError as e: raise RuntimeError(path+': '+e.read().decode()) from e
 tui=None
 try:
  first=cli(binary,'service','start');info=api('/initialize')
  if busy:
   revision=api('/status')['configuration']['revision']
   cfg=api('/configuration/connect-model',{'operation_id':'connect','expected_revision':revision,'config':{'provider':'custom','model':'matrix','base_url':f'http://127.0.0.1:{server.server_port}/v1','api_key':'synthetic-only','context_window_tokens':32768,'max_output_tokens':1024}})
   assert cfg['outcome']=='committed',cfg
   created=api('/sessions',{'operation_id':'create','cwd':str(work),'workspace_key':'upgrade'})
   assert created['outcome']=='committed',created
   sid=created['session_id'];entered.clear();release.clear()
   if args.tui:
    from host_upgrade_tui import OldTUI
    tui=OldTUI(binary,store,sid,work,env,d/'old-tui.log');time.sleep(3)
   prompt=api('/sessions/'+sid+'/prompt',{'operation_id':'held-task','input':'UPGRADE_BLOCK'})
   assert entered.wait(20),'model task did not start'
   directory=api('/sessions');assert sid in directory.get('running_session_ids',[]),directory
   # The real new CLI must finish a different task against the busy old Host.
   client=subprocess.run([str(new),'-p','UPGRADE_CLIENT','--store-dir',str(store),'--format','json'],env=env,cwd=work,capture_output=True,text=True,timeout=30)
   (d/'client.log').write_text(client.stdout+client.stderr)
   assert client.returncode==0,(name,client.stderr)
   assert 'UPGRADE_TASK_COMPLETE' in client.stdout,client.stdout
   kept=cli(new,'service','status');assert kept['instance_id']==first['instance_id'],kept
   release.set()
   deadline=time.monotonic()+30
   while True:
    try:current=cli(new,'service','status')
    except RuntimeError:
     assert time.monotonic()<deadline,'Host did not become ready'
     time.sleep(.25);continue
    if current.get('distribution_version')==versions[str(new)]['version']:break
    assert time.monotonic()<deadline,'deferred upgrade did not occur'
    time.sleep(.25)
   assert current['instance_id']!=first['instance_id']
   assert api('/initialize')['store_id']==info['store_id']
   assert api('/operations/held-task')['operation_id']=='held-task'
   assert api('/sessions/'+sid+'/state')['session_id']==sid
   if tui:
    before=api('/sessions/'+sid+'/state')['run'];after.clear();after_requests.clear()
    time.sleep(3);tui.send('TUI_AFTER_UPGRADE\r')
    assert after.wait(20), 'old TUI failed to submit after replacement'
    deadline=time.monotonic()+20
    while b'UPGRADE_RECONNECTED_COMPLETE' not in tui.output:
     assert time.monotonic()<deadline,'old TUI did not render replacement result'
     time.sleep(.1)
    # The terminal text event can precede the persisted run terminal state.
    # Read the authoritative state until completion instead of treating a
    # rendered token as proof that the run has already settled.
    state=api('/sessions/'+sid+'/state')['run']
    while state.get('status')=='running':
     assert time.monotonic()<deadline,'replacement run did not complete'
     time.sleep(.1);state=api('/sessions/'+sid+'/state')['run']
    assert state.get('status')=='completed' and state.get('run_id')!=before.get('run_id'),state
    assert len(after_requests)==1,'old TUI replayed the new request'
    print(name,'persistent old TUI reconnect/render PASS',flush=True)
  else:
   current=cli(new,'service','start');assert current['distribution_version']==versions[str(new)]['version'],current
   assert current['instance_id']!=first['instance_id']
  # Older public CLI must retain the newer Host when asked to start again.
  assert cli(binary,'service','start')['instance_id']==current['instance_id']
  evidence.append({'case':name,'old':first,'new':current,'passed':True,'old_tui_reconnected':bool(tui)})
  (root/'evidence.json').write_text(json.dumps({'versions':versions,'sha256':{str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in [old,modern,new]},'cases':evidence},indent=2))
  print(name,'PASS',flush=True)
 finally:
  release.set()
  if tui:tui.close()
  cli(new,'service','stop')
try:
 for b in [old,modern]:
  for busy in [False,True]:case(b,busy)
finally:server.shutdown()
