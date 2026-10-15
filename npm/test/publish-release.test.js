const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

test('partial npm publication resumes only with identical package bytes', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'caelis-release-retry-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const bin = path.join(root, 'bin');
  const scripts = path.join(root, 'scripts');
  fs.mkdirSync(bin);
  fs.mkdirSync(scripts);
  fs.copyFileSync(path.join(__dirname, '..', 'scripts', 'publish-release.mjs'), path.join(scripts, 'publish-release.mjs'));
  const stateFile = path.join(root, 'registry.json');
  fs.writeFileSync(stateFile, JSON.stringify({ published: {}, attempts: 0, failed: false }));
  const fakeNpm = `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const { createHash } = require('node:crypto');
const args = process.argv.slice(2);
const state = JSON.parse(fs.readFileSync(process.env.FAKE_NPM_STATE, 'utf8'));
const integrity = (bytes) => 'sha512-' + createHash('sha512').update(bytes).digest('base64');
const save = () => fs.writeFileSync(process.env.FAKE_NPM_STATE, JSON.stringify(state));
if (args[0] === 'pack') {
  const manifest = JSON.parse(fs.readFileSync('package.json', 'utf8'));
  const bytes = Buffer.from(JSON.stringify({ name: manifest.name, version: manifest.version, payload: fs.readFileSync('payload', 'utf8') }));
  const filename = manifest.name.replace(/[^a-z0-9]/gi, '-') + '.tgz';
  fs.writeFileSync(path.join(args[args.indexOf('--pack-destination') + 1], filename), bytes);
  process.stdout.write(JSON.stringify([{ name: manifest.name, version: manifest.version, filename, integrity: integrity(bytes) }]));
} else if (args[0] === 'view') {
  if (!args.includes('--prefer-online')) process.exit(2);
  if (process.env.FAKE_NPM_VIEW_ERROR === '1') {
    process.stderr.write('ENETUNREACH registry unavailable\\n');
    process.exit(1);
  }
  const key = args[1];
  if (state.hiddenViews > 0 && key in state.published) {
    state.hiddenViews--;
    save();
    process.stderr.write('npm ERR! code E404\\n');
    process.exit(1);
  }
  if (!(key in state.published)) {
    process.stderr.write('npm ERR! code E404\\n');
    process.exit(1);
  }
  process.stdout.write(JSON.stringify(state.published[key]));
} else if (args[0] === 'publish') {
  const bytes = fs.readFileSync(args[1]);
  const pkg = JSON.parse(bytes);
  state.attempts++;
  if (state.attempts === 4 && !state.failed) {
    state.failed = true;
    save();
    process.stderr.write('simulated fourth package failure\\n');
    process.exit(1);
  }
  state.published[pkg.name + '@' + pkg.version] = integrity(bytes);
  if (state.attempts === 1) state.hiddenViews = 5;
  save();
} else {
  process.exit(2);
}
`;
  fs.writeFileSync(path.join(bin, 'npm'), fakeNpm, { mode: 0o700 });
  const packages = [];
  for (let n = 1; n <= 4; n++) {
    const dir = path.join(root, `p${n}`);
    fs.mkdirSync(dir);
    fs.writeFileSync(path.join(dir, 'package.json'), JSON.stringify({ name: `@caelis/test-p${n}`, version: '1.2.3' }));
    fs.writeFileSync(path.join(dir, 'payload'), `signed archive ${n}`);
    packages.push(dir);
  }
  const run = (extraEnv = {}) => spawnSync(process.execPath, [path.join(scripts, 'publish-release.mjs'), ...packages], {
    encoding: 'utf8',
    env: { ...process.env, PATH: `${bin}${path.delimiter}${process.env.PATH}`, FAKE_NPM_STATE: stateFile, ...extraEnv },
  });
  let result = run();
  assert.notEqual(result.status, 0, result.stderr);
  assert.match(result.stderr, /simulated fourth package failure/);
  let state = JSON.parse(fs.readFileSync(stateFile, 'utf8'));
  assert.equal(Object.keys(state.published).length, 3);

  result = run();
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /verified existing @caelis\/test-p1@1\.2\.3/);
  state = JSON.parse(fs.readFileSync(stateFile, 'utf8'));
  assert.equal(state.attempts, 5);
  assert.equal(Object.keys(state.published).length, 4);
  result = run();
  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(fs.readFileSync(stateFile, 'utf8')).attempts, 5);

  fs.writeFileSync(path.join(packages[1], 'payload'), 'different signed bytes');
  result = run();
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /already exists with different integrity/);
  assert.equal(JSON.parse(fs.readFileSync(stateFile, 'utf8')).attempts, 5);

  result = run({ FAKE_NPM_VIEW_ERROR: '1' });
  assert.notEqual(result.status, 0);
  assert.doesNotMatch(result.stdout, /publishing/);
  assert.equal(JSON.parse(fs.readFileSync(stateFile, 'utf8')).attempts, 5);
});
