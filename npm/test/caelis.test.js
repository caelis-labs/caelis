const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { EventEmitter } = require('node:events');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const {
  UpdateHandoffError,
  completeHandoff,
  executeHandoffPlan,
  handoffOwnershipName,
  handoffPlanName,
  normalizeVersion,
  removeOwnedLock,
  reserveHandoffDirectory,
  validateHandoffPlan,
} = require('../lib/update-handoff.js');
const {
  LauncherError,
  createSignalCoordinator,
  formatLauncherError,
  handoffEligible,
  resolveBinaryPath,
  resolvePackageName,
} = require('../bin/caelis.js');

function testPlan() {
  return {
    version: 1,
    command: ['npm.cmd', 'install', '-g', '@caelis/caelis@1.2.0'],
    command_line: 'call "npm.cmd" "install" "-g" "@caelis/caelis@1.2.0"',
    current_version: 'v1.0.0',
    latest_version: 'v1.2.0',
    executable: 'C:\\caelis\\caelis.exe',
  };
}

function statusRecorder() {
  const events = [];
  return {
    events,
    start(message) {
      events.push(['start', message]);
    },
    succeed(message) {
      events.push(['succeed', message]);
    },
    fail(message) {
      events.push(['fail', message]);
    },
    stop() {
      events.push(['stop']);
    },
  };
}

test('executeHandoffPlan waits for install and verifies the target version', async () => {
  const status = statusRecorder();
  const writes = [];
  const result = await executeHandoffPlan(testPlan(), {
    status,
    stdout: { write: (value) => writes.push(value) },
    stderr: { write: () => {} },
    runInstall: async () => ({ code: 0, signal: null, stdout: '', stderr: '' }),
    verifyVersion: async () => '1.2.0',
  });

  assert.equal(result, 0);
  assert.match(writes.join(''), /Caelis v1\.2\.0 is ready/);
  assert.deepEqual(status.events, [
    ['start', 'Installing update with npm…'],
    ['succeed', 'npm install completed'],
    ['start', 'Verifying updated Caelis…'],
    ['succeed', 'Verified Caelis v1.2.0'],
    ['stop'],
  ]);
});

test('executeHandoffPlan reports npm failure', async () => {
  const status = statusRecorder();

  await assert.rejects(
    executeHandoffPlan(testPlan(), {
      status,
      stdout: { write: () => {} },
      stderr: { write: () => {} },
      runInstall: async () => ({
        code: 1,
        signal: null,
        stdout: '',
        stderr: 'registry unavailable',
      }),
      verifyVersion: async () => {
        assert.fail('version verification must not run after npm failure');
      },
    }),
    /npm install exited with code 1[\s\S]*registry unavailable/,
  );

  assert.deepEqual(status.events, [
    ['start', 'Installing update with npm…'],
    ['fail', 'npm install failed'],
    ['stop'],
  ]);
});

test('handoff plan validation rejects incomplete input', () => {
  assert.throws(
    () => validateHandoffPlan({ version: 1, command: [] }),
    /invalid npm update command/,
  );
});

test('executeHandoffPlan rejects version drift', async () => {
  const status = statusRecorder();

  await assert.rejects(
    executeHandoffPlan(testPlan(), {
      status,
      stdout: { write: () => {} },
      stderr: { write: () => {} },
      runInstall: async () => ({ code: 0, signal: null, stdout: '', stderr: '' }),
      verifyVersion: async () => 'v1.1.0',
    }),
    /expected v1\.2\.0, found v1\.1\.0/,
  );

  assert.deepEqual(status.events, [
    ['start', 'Installing update with npm…'],
    ['succeed', 'npm install completed'],
    ['start', 'Verifying updated Caelis…'],
    ['fail', 'Version verification failed'],
    ['stop'],
  ]);
});

test('normalizeVersion accepts display and npm versions', () => {
  assert.equal(normalizeVersion('v1.2.0'), '1.2.0');
  assert.equal(normalizeVersion('1.2.0'), '1.2.0');
});

test('removeOwnedLock does not remove a replacement lock', (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caelis-lock-test-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const lockPath = path.join(dir, 'update.lock');
  fs.writeFileSync(lockPath, 'new-owner\n');

  removeOwnedLock(lockPath, 'old-owner');

  assert.equal(fs.readFileSync(lockPath, 'utf8'), 'new-owner\n');
});

test('removeOwnedLock requires a matching non-empty ownership token', (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'caelis-lock-test-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const lockPath = path.join(dir, 'update.lock');
  fs.writeFileSync(lockPath, 'current-owner\n');

  removeOwnedLock(lockPath, '');

  assert.equal(fs.readFileSync(lockPath, 'utf8'), 'current-owner\n');
});

function handoffFixture(t, planData = JSON.stringify(testPlan())) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'caelis-handoff-test-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const handoffDir = path.join(root, 'reserved-handoff');
  const lockPath = path.join(root, 'update.lock');
  const lockToken = '2026-07-23T03:00:00Z';
  fs.mkdirSync(handoffDir);
  fs.writeFileSync(lockPath, `${lockToken}\n`);
  fs.writeFileSync(
    path.join(handoffDir, handoffOwnershipName),
    JSON.stringify({
      version: 1,
      lock_path: lockPath,
      lock_token: lockToken,
    }),
  );
  fs.writeFileSync(path.join(handoffDir, handoffPlanName), planData);
  return { handoffDir, lockPath };
}

test('completeHandoff executes a published plan then removes ownership state', async (t) => {
  const fixture = handoffFixture(t);
  let executed;

  const result = await completeHandoff(
    fixture.handoffDir,
    { code: 0, signal: null },
    {
      executePlan: async (plan) => {
        executed = plan;
        return 0;
      },
    },
  );

  assert.deepEqual(result, { code: 0, signal: null });
  assert.deepEqual(executed, testPlan());
  assert.equal(fs.existsSync(fixture.lockPath), false);
  assert.equal(fs.existsSync(fixture.handoffDir), false);
});

test('completeHandoff releases ownership after child signal or nonzero exit', async (t) => {
  for (const childResult of [
    { code: 1, signal: 'SIGTERM' },
    { code: 17, signal: null },
  ]) {
    const fixture = handoffFixture(t);
    const result = await completeHandoff(fixture.handoffDir, childResult, {
      executePlan: async () => assert.fail('plan must not run after child failure'),
    });

    assert.deepEqual(result, childResult);
    assert.equal(fs.existsSync(fixture.lockPath), false);
    assert.equal(fs.existsSync(fixture.handoffDir), false);
  }
});

test('completeHandoff releases ownership when plan JSON is corrupt', async (t) => {
  const fixture = handoffFixture(t, '{not-json');

  await assert.rejects(
    completeHandoff(fixture.handoffDir, { code: 0, signal: null }),
    (err) => err instanceof UpdateHandoffError &&
      /invalid npm update handoff plan/.test(err.message),
  );

  assert.equal(fs.existsSync(fixture.lockPath), false);
  assert.equal(fs.existsSync(fixture.handoffDir), false);
});

test('completeHandoff releases ownership when plan execution fails', async (t) => {
  const fixture = handoffFixture(t);

  await assert.rejects(
    completeHandoff(
      fixture.handoffDir,
      { code: 0, signal: null },
      {
        executePlan: async () => {
          throw new Error('registry unavailable');
        },
      },
    ),
    (err) => err instanceof UpdateHandoffError &&
      err.message === 'registry unavailable',
  );

  assert.equal(fs.existsSync(fixture.lockPath), false);
  assert.equal(fs.existsSync(fixture.handoffDir), false);
});

test('signal coordinator retains and forwards the first launcher signal', () => {
  const target = new EventEmitter();
  const forwarded = [];
  const coordinator = createSignalCoordinator(target, { forceKillAfterMs: -1 });
  const release = coordinator.track({
    kill(signal) {
      forwarded.push(signal);
      return true;
    },
  });

  target.emit('SIGINT');
  target.emit('SIGTERM');

  assert.equal(coordinator.receivedSignal(), 'SIGINT');
  assert.deepEqual(forwarded, ['SIGINT', 'SIGINT']);
  release();
  coordinator.close();
  assert.equal(target.listenerCount('SIGINT'), 0);
  assert.equal(target.listenerCount('SIGTERM'), 0);
});

test('launcher SIGINT waits for handoff cleanup before exiting', {
  skip: process.platform === 'win32' &&
    'subprocess.kill cannot emulate a Windows console Ctrl+C event',
}, async (t) => {
  const fixture = handoffFixture(t);
  const binModule = require.resolve('../bin/caelis.js');
  const handoffModule = require.resolve('../lib/update-handoff.js');
  const source = `
    const { createSignalCoordinator, runProcess } = require(${JSON.stringify(binModule)});
    const { completeHandoff } = require(${JSON.stringify(handoffModule)});
    const coordinator = createSignalCoordinator(process, { forceKillAfterMs: 500 });
    (async () => {
      try {
        await completeHandoff(
          ${JSON.stringify(fixture.handoffDir)},
          { code: 0, signal: null },
          {
            signalCoordinator: coordinator,
            executePlan: async () => {
              const result = await runProcess(
                process.execPath,
                ['-e', 'console.log("ACTIVE"); setTimeout(() => {}, 5000)'],
                {
                  stdio: ['ignore', 'inherit', 'inherit'],
                  signalCoordinator: coordinator,
                },
              );
              if (result.signal) {
                throw new Error('interrupted by ' + result.signal);
              }
              return result.code;
            },
          },
        );
      } catch (err) {
        if (!coordinator.receivedSignal()) {
          throw err;
        }
      } finally {
        const signal = coordinator.receivedSignal();
        coordinator.close();
        process.exitCode = signal ? 130 : 0;
      }
    })().catch((err) => {
      console.error(err);
      process.exitCode = 1;
    });
  `;
  const harness = spawn(process.execPath, ['-e', source], {
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  t.after(() => {
    if (harness.exitCode === null && harness.signalCode === null) {
      harness.kill('SIGKILL');
    }
  });
  let stdout = '';
  let stderr = '';
  let signaled = false;
  harness.stdout.on('data', (chunk) => {
    stdout += chunk;
    if (!signaled && stdout.includes('ACTIVE')) {
      signaled = true;
      harness.kill('SIGINT');
    }
  });
  harness.stderr.on('data', (chunk) => {
    stderr += chunk;
  });
  const result = await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      harness.kill('SIGKILL');
      reject(new Error('signal cleanup harness timed out'));
    }, 5000);
    harness.once('error', (err) => {
      clearTimeout(timeout);
      reject(err);
    });
    harness.once('exit', (code, signal) => {
      clearTimeout(timeout);
      resolve({ code, signal });
    });
  });

  assert.equal(signaled, true, `harness output = ${stdout}`);
  assert.deepEqual(result, { code: 130, signal: null }, stderr);
  assert.equal(fs.existsSync(fixture.lockPath), false);
  assert.equal(fs.existsSync(fixture.handoffDir), false);
});

test('reserveHandoffDirectory does not touch the normal launcher filesystem path', () => {
  const reserved = reserveHandoffDirectory('win32', {
    tmpdir: () => os.tmpdir(),
    randomUUID: () => '00000000-0000-4000-8000-000000000000',
  });

  assert.equal(
    reserved,
    path.join(os.tmpdir(), 'caelis-npm-update-00000000-0000-4000-8000-000000000000'),
  );
  assert.equal(fs.existsSync(reserved), false);
  assert.equal(reserveHandoffDirectory('linux'), '');
});

test('handoff eligibility preserves TUI updates without wrapping known commands', () => {
  assert.equal(handoffEligible('win32', ['update'], false), true);
  assert.equal(handoffEligible('win32', ['update', '--check'], true), false);
  assert.equal(handoffEligible('win32', [], true), true);
  assert.equal(handoffEligible('win32', ['--store-dir', 'C:\\caelis'], true), true);
  assert.equal(handoffEligible('win32', ['version'], true), false);
  assert.equal(handoffEligible('win32', ['-p', 'hello'], true), false);
  assert.equal(handoffEligible('win32', ['-p', ''], true), true);
  assert.equal(handoffEligible('win32', ['--interactive', '-p', 'hello'], false), true);
  assert.equal(handoffEligible('linux', ['update'], true), false);
});

test('launcher errors retain install guidance without update-failure mislabeling', () => {
  assert.throws(
    () => resolveBinaryPath(
      '@caelis/caelis-windows-x64',
      'win32',
      () => {
        throw new Error('module not found');
      },
    ),
    (err) => {
      assert.ok(err instanceof LauncherError);
      const output = formatLauncherError(err);
      assert.match(output, /\[caelis\] platform package not installed/);
      assert.match(output, /reinstall without --omit=optional/);
      assert.doesNotMatch(output, /update failed/);
      return true;
    },
  );

  let unsupported;
  try {
    resolvePackageName('plan9', 'amd64');
    assert.fail('unsupported platform must fail');
  } catch (err) {
    unsupported = err;
  }
  assert.equal(
    formatLauncherError(unsupported),
    '[caelis] unsupported platform/arch: plan9/amd64',
  );
  assert.equal(
    formatLauncherError(new UpdateHandoffError('registry unavailable')),
    '[caelis] update failed: registry unavailable',
  );
});
