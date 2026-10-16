const assert = require('node:assert/strict');
const { createHash } = require('node:crypto');
const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const publisher = import('../scripts/publish-release.mjs');
const hash = (bytes) => 'sha512-' + createHash('sha512').update(bytes).digest('base64');
const expected = hash('signed archive');
const npmError = (code) => Object.assign(new Error(code), {
  stdout: JSON.stringify({ error: { code } }), stderr: `npm error code ${code}\n`,
});
function clock() {
  let elapsed = 0;
  const delays = [];
  return { now: () => elapsed, sleep: async (ms) => { delays.push(ms); elapsed += ms; }, random: () => 0.5, log: () => {}, delays };
}

// Real files/packing digest and command arguments, with a deterministic registry
// and virtual clock: no credentials, real publication or 20-minute test sleeps.
function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'caelis-release-retry-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const packages = Array.from({ length: 6 }, (_, i) => {
    const dir = path.join(root, `p${i}`);
    fs.mkdirSync(dir);
    fs.writeFileSync(path.join(dir, 'package.json'), JSON.stringify({ name: `@caelis/test-p${i}`, version: '1.2.3' }));
    fs.writeFileSync(path.join(dir, 'payload'), `signed archive ${i}`);
    return dir;
  });
  const state = { published: new Map(), attempts: [], packed: [], views: [], fail: null, afterUpload: null };
  async function run(command, args, options) {
    assert.equal(command, 'npm');
    if (args[0] === 'pack') {
      const manifest = JSON.parse(fs.readFileSync(path.join(options.cwd, 'package.json')));
      const bytes = Buffer.from(JSON.stringify({ ...manifest, payload: fs.readFileSync(path.join(options.cwd, 'payload'), 'utf8') }));
      const filename = 'packed.tgz';
      fs.writeFileSync(path.join(args[args.indexOf('--pack-destination') + 1], filename), bytes);
      state.packed.push(manifest.name);
      return { stdout: JSON.stringify([{ ...manifest, filename, integrity: hash(bytes) }]) };
    }
    if (args[0] === 'view') {
      assert.ok(args.includes('--prefer-online'));
      assert.ok(args.includes('--fetch-retries=0'));
      assert.ok(args.includes('--registry=https://registry.npmjs.org'));
      assert.ok(options.timeout > 0 && options.timeout <= 20_000);
      state.views.push(args[1]);
      if (state.readError) throw state.readError;
      const integrity = state.published.get(args[1]);
      if (!integrity) throw npmError('E404');
      return { stdout: JSON.stringify(integrity) };
    }
    assert.equal(args[0], 'publish');
    assert.ok(args.includes('--fetch-retries=0'));
    assert.ok(args.includes('--registry=https://registry.npmjs.org'));
    assert.equal(options.timeout, 300_000);
    const bytes = fs.readFileSync(args[1]);
    const manifest = JSON.parse(bytes);
    const spec = `${manifest.name}@${manifest.version}`;
    state.attempts.push(spec);
    if (state.fail === spec) throw npmError('E401');
    state.published.set(spec, hash(bytes));
    if (state.afterUpload) throw state.afterUpload;
    return { stdout: '{}', stderr: '' };
  }
  return { ...state, state, packages, run };
}

test('partial six-platform publication resumes only with identical bytes', async (t) => {
  const { publishPackage, publishPackages } = await publisher;
  const { packages, state, run } = fixture(t);
  const publish = (dir) => publishPackage(dir, { run, waitOptions: clock(), log: () => {} });
  state.fail = '@caelis/test-p3@1.2.3';
  await assert.rejects(publishPackages(packages, { concurrency: 6, publish }), /1 npm package.*failed verification/);
  assert.equal(state.published.size, 5); // Failure did not abandon siblings.
  assert.equal(state.attempts.length, 6);
  state.fail = null;
  await publishPackages(packages, { concurrency: 6, publish });
  assert.equal(state.published.size, 6);
  assert.equal(state.attempts.length, 7);
  await publishPackages(packages, { concurrency: 6, publish });
  assert.equal(state.attempts.length, 7);
  fs.writeFileSync(path.join(packages[1], 'payload'), 'different signed bytes');
  await assert.rejects(publish(packages[1]), /different integrity/);
  assert.equal(state.attempts.length, 7);
  state.readError = npmError('ENETUNREACH');
  await assert.rejects(publish(packages[0]), /timed out after 120s/);
  assert.equal(state.attempts.length, 7); // An outage cannot mean absent.
});

test('uncertain publish is reconciled without a second upload', async (t) => {
  const { publishPackage } = await publisher;
  const { packages, state, run } = fixture(t);
  state.afterUpload = npmError('ECONNRESET');
  await publishPackage(packages[0], { run, waitOptions: clock(), log: () => {} });
  assert.equal(state.attempts.length, 1);
  assert.equal(state.packed.length, 1);
});

test('unknown publish failure that never appears times out without redispatch', async (t) => {
  const { publishPackage } = await publisher;
  const { packages, state, run } = fixture(t);
  let uploads = 0;
  const fail = async (command, args, opts) => {
    if (args[0] === 'publish') { uploads++; throw npmError('ETIMEDOUT'); }
    return run(command, args, opts);
  };
  await assert.rejects(publishPackage(packages[0], { run: fail, waitOptions: clock(), log: () => {} }), /timed out after 1200s.*publish command: ETIMEDOUT/);
  assert.equal(uploads, 1);
  assert.equal(state.published.size, 0);
});

test('propagation beyond old two-minute window survives 404, rate limit and network failures', async () => {
  const { publishedIntegrity, waitForIntegrity } = await publisher;
  const time = clock();
  let calls = 0;
  const run = async () => {
    calls++;
    if (time.now() > 9 * 60_000) return { stdout: JSON.stringify(expected) };
    throw npmError(['E404', 'E429', 'E503', 'ECONNRESET'][calls % 4]);
  };
  await waitForIntegrity('p@1', expected, { ...time, read: (timeoutMs) => publishedIntegrity('p', '1', { run, timeoutMs }) });
  assert.ok(time.now() > 9 * 60_000 && time.now() < 20 * 60_000);
  assert.ok(time.delays.every((delay) => delay <= 30_000));
});

test('preflight retries incomplete metadata without treating it as absent', async () => {
  const { publishedIntegrity, waitForIntegrity } = await publisher;
  let calls = 0;
  const run = async () => ({ stdout: [undefined, '{bad json', JSON.stringify(expected)][Math.min(calls++, 2)] });
  const actual = await waitForIntegrity('p@1', expected, {
    ...clock(), allowAbsent: true,
    read: (timeoutMs) => publishedIntegrity('p', '1', { run, timeoutMs }),
  });
  assert.equal(actual, expected);
  assert.equal(calls, 3);
});

test('read classification retries only transient failures and validates metadata', async () => {
  const { publishedIntegrity } = await publisher;
  for (const code of ['E429', 'E500', 'E502', 'E503', 'E504', 'ETIMEDOUT', 'ESOCKETTIMEDOUT', 'ECONNRESET', 'EAI_AGAIN', 'ENOTFOUND', 'ENETUNREACH', 'EJSONPARSE']) {
    await assert.rejects(publishedIntegrity('p', '1', { run: async () => { throw npmError(code); } }), (err) => err.retryable === true);
  }
  for (const code of ['E401', 'E403', 'ENOENT']) {
    await assert.rejects(publishedIntegrity('p', '1', { run: async () => { throw npmError(code); } }), (err) => !err.retryable);
  }
  await assert.rejects(publishedIntegrity('p', '1', { run: async () => { throw { killed: true }; } }), (err) => err.retryable === true);
  assert.equal(await publishedIntegrity('p', '1', { run: async () => { throw npmError('E404'); } }), null);
  for (const bad of ['sha512-bad', {}, ['sha512-bad']]) {
    await assert.rejects(publishedIntegrity('p', '1', { run: async () => ({ stdout: JSON.stringify(bad) }) }), /invalid SHA512/);
  }
});

test('mismatched integrity fails immediately and polling has a hard deadline', async () => {
  const { waitForIntegrity } = await publisher;
  const time = clock();
  await assert.rejects(waitForIntegrity('p@1', expected, { ...time, read: async () => hash('wrong bytes') }), /different integrity/);
  assert.equal(time.delays.length, 0);
  const timeouts = [];
  await assert.rejects(waitForIntegrity('p@1', expected, {
    ...time, timeoutMs: 50_000,
    read: async (timeoutMs) => { timeouts.push(timeoutMs); return null; },
  }), /timed out after 50s/);
  assert.equal(time.now(), 50_000);
  assert.ok(timeouts.every((ms) => ms > 0 && ms <= 20_000));
});

test('six publishers overlap and gate waits for every settlement before main', async () => {
  const { publishPackages } = await publisher;
  const active = new Map();
  let started = 0;
  const packages = Array.from({ length: 6 }, (_, n) => `platform-${n}`);
  let settled = false;
  let mainPublished = false;
  const gate = publishPackages(packages, { concurrency: 6, publish: (dir) => {
    started++;
    return new Promise((resolve, reject) => active.set(path.basename(dir), { resolve, reject }));
  } }).then(() => { mainPublished = true; }, (err) => { assert.match(err.message, /platform-0: failure/); }).finally(() => { settled = true; });
  assert.equal(started, 6);
  active.get('platform-0').reject(new Error('failure'));
  await new Promise(setImmediate);
  assert.equal(settled, false);
  for (let n = 1; n < 6; n++) active.get(`platform-${n}`).resolve();
  await gate;
  assert.equal(mainPublished, false);
});

test('main starts only after all six platform verifications succeed', async () => {
  const { publishPackages } = await publisher;
  const resolvers = [];
  let mainPublished = false;
  const gate = publishPackages(Array.from({ length: 6 }, (_, n) => `p${n}`), {
    concurrency: 6, publish: () => new Promise((resolve) => resolvers.push(resolve)),
  }).then(() => { mainPublished = true; });
  for (let n = 0; n < 5; n++) resolvers[n]();
  await new Promise(setImmediate);
  assert.equal(mainPublished, false);
  resolvers[5]();
  await gate;
  assert.equal(mainPublished, true);
});

test('concurrency limits and CLI validation reject unsafe arguments', async () => {
  const { publishPackages } = await publisher;
  for (const concurrency of [0, 7, NaN, 1.5]) {
    await assert.rejects(publishPackages(['p'], { concurrency }), /concurrency/);
  }
  await assert.rejects(publishPackages(['p', './p']), /duplicate/);
  const result = spawnSync(process.execPath, [path.join(__dirname, '../scripts/publish-release.mjs'), '--concurrency', 'bogus', 'p'], { encoding: 'utf8' });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /concurrency/);
});
