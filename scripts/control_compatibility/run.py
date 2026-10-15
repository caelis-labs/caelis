import os, json, secrets, subprocess, time, urllib.request, threading, http.server, socket, signal, pathlib, hashlib
import argparse, tempfile
parser = argparse.ArgumentParser(description='Verify current Control restart, optionally interoperability with a compatible peer ref.')
parser.add_argument('--peer-ref', help='Optional local ref implementing the unified Session protocol and compatible Store schema')
parser.add_argument('--output', help='New evidence directory; created privately if omitted')
args = parser.parse_args()
repo = pathlib.Path(__file__).resolve().parents[2]
root = pathlib.Path(args.output).resolve() if args.output else pathlib.Path(tempfile.mkdtemp(prefix='caelis-control-compat-'))
if args.output:
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
print('Evidence:', root, flush=True)
sources = [('current', repo)]
peer_sha = None
if args.peer_ref:
    peer = root / 'peer'
    peer.mkdir(mode=0o700)
    peer_sha = subprocess.check_output(['git', 'rev-parse', '--verify', args.peer_ref + '^{commit}'], cwd=repo, text=True).strip()
    archive = root / 'peer.tar'
    with archive.open('wb') as output:
        subprocess.run(['git', 'archive', peer_sha], cwd=repo, stdout=output, check=True)
    subprocess.run(['tar', '-xf', str(archive), '-C', str(peer)], check=True)
    archive.unlink()
    schema = json.loads((peer / 'api/control/v1/openapi.json').read_text())
    if '/sessions/{session_id}/profile' not in schema['paths']:
        raise RuntimeError('Peer predates the unified Session protocol; legacy binary compatibility is outside this matrix')
    sources.append(('peer', peer))
build_env = dict(os.environ, GOWORK='off')
for name, source in sources:
    for kind, target in [('host', './cmd/caelis'), ('client', str(repo / 'scripts/control_compatibility/main.go'))]:
        subprocess.run(['go', 'build', '-o', str(root / (name + '-' + kind)), target], cwd=source, env=build_env, check=True)
files = subprocess.check_output(['git','ls-files','--cached','--others','--exclude-standard','-z'],cwd=repo).split(b'\0')
file_hashes = {os.fsdecode(name): hashlib.sha256((repo / os.fsdecode(name)).read_bytes()).hexdigest() for name in files if name and (repo / os.fsdecode(name)).is_file()}
(root / 'sources.json').write_text(json.dumps({'peer': peer_sha, 'current': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=repo, text=True).strip(), 'current_files': file_hashes}, indent=2))

class Provider(http.server.BaseHTTPRequestHandler):

    def log_message(self, *args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        with open(root / 'provider-requests.jsonl', 'a') as f:
            f.write(json.dumps(body) + '\n')
        seen = any((m.get('type') == 'function_call_output' for m in body.get('input', [])))
        if seen:
            output = {'id': 'matrix-message', 'type': 'message', 'role': 'assistant', 'content': [{'type': 'output_text', 'text': 'MATRIX_FINAL'}]}
            events = [{'type': 'response.output_text.delta', 'item_id': 'matrix-message', 'output_index': 0, 'delta': 'MATRIX_FINAL'}]
        else:
            output = {'id': 'matrix-tool', 'type': 'function_call', 'call_id': 'matrix-call', 'name': 'MatrixLookup', 'arguments': '{"value":"synthetic"}'}
            events = [{'type': 'response.output_item.added', 'output_index': 0, 'item': {k: v for k, v in output.items() if k != 'arguments'}}, {'type': 'response.function_call_arguments.delta', 'item_id': 'matrix-tool', 'output_index': 0, 'delta': output['arguments']}]
        events.append({'type': 'response.completed', 'response': {'model': body['model'], 'status': 'completed', 'output': [output]}})
        payload = ''.join(('data: ' + json.dumps(event) + '\n\n' for event in events))
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.send_header('Content-Length', str(len(payload)))
        self.end_headers()
        self.wfile.write(payload.encode())
provider = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Provider)
threading.Thread(target=provider.serve_forever, daemon=True).start()
provider_url = 'http://127.0.0.1:' + str(provider.server_port)
env = {k: v for k, v in os.environ.items() if not k.startswith('CAELIS_')}

def run_pair(name, client, host, replacements):
    case = root / name
    case.mkdir(exist_ok=False)
    (case / 'home').mkdir(mode=0o700)
    case_env = dict(env, HOME=str(case / 'home'), XDG_CONFIG_HOME=str(case / 'home' / '.config'))
    (case / 'store').mkdir(mode=0o700)
    (case / 'workspace').mkdir(mode=0o700)
    (case / 'host.token').write_text(secrets.token_urlsafe(32) + '\n')
    os.chmod(case / 'host.token', 0o600)
    for index, (binary, phase) in enumerate([(host, 'create')] + [(x, 'observe') for x in replacements]):
        with socket.socket() as s:
            s.bind(('127.0.0.1', 0))
            port = s.getsockname()[1]
        origin = 'http://127.0.0.1:' + str(port)
        log = open(case / f'host-{index}.log', 'w')
        process = subprocess.Popen([str(root / binary), 'serve', '--store-dir', str(case / 'store'), '--listen', f'127.0.0.1:{port}', '--control-token-file', str(case / 'host.token')], cwd=case / 'workspace', env=case_env, stdout=log, stderr=subprocess.STDOUT)
        try:
            ready = False
            for _ in range(150):
                if process.poll() is not None:
                    raise RuntimeError('Host exited: ' + (case / f'host-{index}.log').read_text()[-3000:])
                try:
                    request = urllib.request.Request(origin + '/api/control/v1/initialize', headers={'Authorization': 'Bearer ' + (case / 'host.token').read_text().strip()})
                    with urllib.request.urlopen(request, timeout=0.2) as r:
                        ready = r.status == 200
                    if ready:
                        break
                except Exception:
                    time.sleep(0.1)
            if not ready:
                raise RuntimeError('Host did not become ready')
            result = subprocess.run([str(root / client), origin, str(case), provider_url, phase], env=case_env, capture_output=True, text=True, timeout=60)
            (case / f'client-{index}.log').write_text(result.stdout + result.stderr)
            print(name, binary, phase, result.returncode, result.stdout, result.stderr, flush=True)
            if result.returncode:
                raise RuntimeError('client failed')
            (case / f'evidence-{phase}-{index}.json').write_text((case / f'evidence-{phase}.json').read_text())
        finally:
            process.send_signal(signal.SIGINT)
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
            log.close()
try:
    run_pair('current-restart', 'current-client', 'current-host', ['current-host'])
    if args.peer_ref:
        run_pair('peer-client-current-host', 'peer-client', 'current-host', ['current-host'])
        run_pair('current-client-peer-host', 'current-client', 'peer-host', ['current-host', 'peer-host'])
    manifest = {name + '-' + kind: hashlib.sha256((root / (name + '-' + kind)).read_bytes()).hexdigest() for name, _ in sources for kind in ['host','client']}
    (root / 'binary-hashes.json').write_text(json.dumps(manifest, indent=2))
    print('MATRIX PASS', flush=True)
finally:
    provider.shutdown()
