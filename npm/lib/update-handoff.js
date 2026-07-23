const { spawn } = require('node:child_process');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const handoffEnvironment = 'CAELIS_NPM_UPDATE_HANDOFF_DIR';
const handoffOwnershipName = 'ownership.json';
const handoffPlanName = 'plan.json';
const capturedOutputLimit = 64 * 1024;

class UpdateHandoffError extends Error {
  constructor(message, options = {}) {
    super(message, options);
    this.name = 'UpdateHandoffError';
  }
}

function appendCaptured(previous, chunk) {
  const next = previous + chunk.toString();
  if (next.length <= capturedOutputLimit) {
    return next;
  }
  return next.slice(next.length - capturedOutputLimit);
}

function runCapturedProcess(command, args, options = {}, signalCoordinator) {
  const pendingSignal = signalCoordinator && signalCoordinator.receivedSignal();
  if (pendingSignal) {
    return Promise.resolve({
      code: 1,
      signal: pendingSignal,
      stdout: '',
      stderr: '',
    });
  }
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      ...options,
      stdio: ['inherit', 'pipe', 'pipe'],
    });
    let releaseChild = () => {};
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (chunk) => {
      stdout = appendCaptured(stdout, chunk);
    });
    child.stderr.on('data', (chunk) => {
      stderr = appendCaptured(stderr, chunk);
    });
    child.once('error', (err) => {
      releaseChild();
      reject(err);
    });
    child.once('exit', (code, signal) => {
      releaseChild();
      resolve({ code: code === null ? 1 : code, signal, stdout, stderr });
    });
    if (signalCoordinator) {
      releaseChild = signalCoordinator.track(child);
    }
  });
}

function createStatusRenderer(stream) {
  const frames = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'];
  const interactive = Boolean(stream && stream.isTTY);
  let timer;
  let frame = 0;
  let lineWidth = 0;

  function replaceLine(text, done) {
    if (!stream) {
      return;
    }
    if (!interactive) {
      stream.write(`${text}\n`);
      return;
    }
    const padding = ' '.repeat(Math.max(lineWidth - [...text].length, 0));
    stream.write(`\r${text}${padding}${done ? '\n' : ''}`);
    lineWidth = done ? 0 : [...text].length;
  }

  function stopTimer() {
    if (timer) {
      clearInterval(timer);
      timer = undefined;
    }
  }

  return {
    start(label) {
      stopTimer();
      if (!interactive) {
        replaceLine(label, true);
        return;
      }
      replaceLine(`${frames[frame]} ${label}`, false);
      timer = setInterval(() => {
        frame = (frame + 1) % frames.length;
        replaceLine(`${frames[frame]} ${label}`, false);
      }, 80);
    },
    succeed(message) {
      stopTimer();
      replaceLine(`✓ ${message}`, true);
    },
    fail(message) {
      stopTimer();
      replaceLine(`✗ ${message}`, true);
    },
    stop() {
      stopTimer();
    },
  };
}

function validateHandoffPlan(plan) {
  if (!plan || plan.version !== 1) {
    throw new Error('invalid npm update handoff plan version');
  }
  if (!Array.isArray(plan.command) || plan.command.length === 0 ||
      plan.command.some((value) => typeof value !== 'string' || value.length === 0)) {
    throw new Error('invalid npm update command');
  }
  if (!plan.latest_version || !plan.executable) {
    throw new Error('incomplete npm update handoff plan');
  }
}

function validateHandoffOwnership(ownership) {
  if (!ownership || ownership.version !== 1) {
    throw new Error('invalid npm update handoff ownership version');
  }
  const lockPath = String(ownership.lock_path || '').trim();
  const lockToken = String(ownership.lock_token || '').trim();
  if (Boolean(lockPath) !== Boolean(lockToken)) {
    throw new Error('incomplete npm update lock ownership');
  }
}

function normalizeVersion(value) {
  return String(value || '').trim().replace(/^v/i, '');
}

async function defaultRunInstall(plan, signalCoordinator) {
  const executable = plan.command[0];
  const args = plan.command.slice(1);
  if (process.platform === 'win32' && /\.(?:cmd|bat)$/i.test(executable)) {
    const commandLine = String(plan.command_line || '').trim();
    if (!commandLine) {
      throw new Error('missing Windows npm command line');
    }
    return runCapturedProcess(
      process.env.ComSpec || 'cmd.exe',
      ['/d', '/s', '/c', commandLine],
      { env: process.env },
      signalCoordinator,
    );
  }
  return runCapturedProcess(executable, args, { env: process.env }, signalCoordinator);
}

async function defaultVerifyVersion(executable, signalCoordinator) {
  const result = await runCapturedProcess(
    executable,
    ['version', '--format', 'json'],
    { env: process.env },
    signalCoordinator,
  );
  if (result.signal || result.code !== 0) {
    const detail = String(result.stderr || result.stdout || '').trim();
    throw new Error(`updated Caelis did not start${detail ? `: ${detail}` : ''}`);
  }
  let payload;
  try {
    payload = JSON.parse(result.stdout);
  } catch (err) {
    throw new Error(`updated Caelis returned invalid version output: ${err.message}`);
  }
  return payload.version;
}

function installFailure(result) {
  if (result.signal) {
    return new Error(`npm install terminated by signal ${result.signal}`);
  }
  const detail = String(result.stderr || result.stdout || '').trim();
  return new Error(
    `npm install exited with code ${result.code}${detail ? `\n${detail}` : ''}`,
  );
}

function removeOwnedLock(lockPath, lockToken) {
  if (!lockPath || !lockToken) {
    return;
  }
  let current;
  try {
    current = fs.readFileSync(lockPath, 'utf8').trim();
  } catch (err) {
    if (err && err.code === 'ENOENT') {
      return;
    }
    throw err;
  }
  if (current !== String(lockToken).trim()) {
    return;
  }
  fs.rmSync(lockPath, { force: true });
}

async function executeHandoffPlan(plan, options = {}) {
  const stderr = options.stderr || process.stderr;
  const stdout = options.stdout || process.stdout;
  const status = options.status || createStatusRenderer(stderr);
  const signalCoordinator = options.signalCoordinator;
  const runInstall = options.runInstall ||
    ((handoffPlan) => defaultRunInstall(handoffPlan, signalCoordinator));
  const verifyVersion = options.verifyVersion ||
    ((executable) => defaultVerifyVersion(executable, signalCoordinator));
  let phase = 'install';

  try {
    validateHandoffPlan(plan);
    status.start('Installing update with npm…');
    const result = await runInstall(plan);
    if (result.signal || result.code !== 0) {
      throw installFailure(result);
    }
    status.succeed('npm install completed');

    phase = 'verify';
    status.start('Verifying updated Caelis…');
    const installedVersion = await verifyVersion(plan.executable);
    if (normalizeVersion(installedVersion) !== normalizeVersion(plan.latest_version)) {
      throw new Error(
        `expected ${plan.latest_version}, found ${installedVersion || 'unknown'}`,
      );
    }
    status.succeed(`Verified Caelis ${plan.latest_version}`);
    stdout.write(
      // Keep this completion contract aligned with formatUpdateResult in
      // internal/cli/update.go; Go is silent while a handoff is active.
      `Caelis ${plan.latest_version} is ready ` +
      `(updated from ${plan.current_version || 'unknown'} via npm).\n`,
    );
    return 0;
  } catch (err) {
    status.fail(phase === 'install' ? 'npm install failed' : 'Version verification failed');
    throw err;
  } finally {
    status.stop();
  }
}

function reserveHandoffDirectory(platform, options = {}) {
  if (platform !== 'win32') {
    return '';
  }
  const tmpdir = options.tmpdir || os.tmpdir;
  const randomUUID = options.randomUUID || crypto.randomUUID;
  return path.join(tmpdir(), `caelis-npm-update-${randomUUID()}`);
}

function readHandoffJSON(filePath, label) {
  let data;
  try {
    data = fs.readFileSync(filePath, 'utf8');
  } catch (err) {
    throw new Error(`cannot read ${label}: ${err.message}`);
  }
  try {
    return JSON.parse(data);
  } catch (err) {
    throw new Error(`invalid ${label}: ${err.message}`);
  }
}

async function completeHandoffLifecycle(handoffDir, childResult, options = {}) {
  if (!handoffDir || !fs.existsSync(handoffDir)) {
    return childResult;
  }
  const executePlan = options.executePlan ||
    ((plan) => executeHandoffPlan(plan, {
      signalCoordinator: options.signalCoordinator,
    }));
  const removeLock = options.removeLock || removeOwnedLock;
  const ownershipPath = path.join(handoffDir, handoffOwnershipName);
  const planPath = path.join(handoffDir, handoffPlanName);
  let ownership;

  try {
    if (fs.existsSync(ownershipPath)) {
      const candidate = readHandoffJSON(ownershipPath, 'npm update handoff ownership');
      validateHandoffOwnership(candidate);
      ownership = candidate;
    }
    const planExists = fs.existsSync(planPath);
    if (!planExists) {
      if (ownership && !childResult.signal && childResult.code === 0) {
        throw new Error('npm update handoff plan was not published');
      }
      return childResult;
    }
    if (!ownership) {
      throw new Error('npm update handoff ownership was not published');
    }
    if (childResult.signal || childResult.code !== 0) {
      return childResult;
    }
    const plan = readHandoffJSON(planPath, 'npm update handoff plan');
    const code = await executePlan(plan);
    return { code, signal: null };
  } finally {
    try {
      if (ownership) {
        removeLock(ownership.lock_path, ownership.lock_token);
      }
    } finally {
      fs.rmSync(handoffDir, { recursive: true, force: true });
    }
  }
}

async function completeHandoff(handoffDir, childResult, options = {}) {
  try {
    return await completeHandoffLifecycle(handoffDir, childResult, options);
  } catch (err) {
    if (err instanceof UpdateHandoffError) {
      throw err;
    }
    throw new UpdateHandoffError(err.message, { cause: err });
  }
}

module.exports = {
  UpdateHandoffError,
  completeHandoff,
  createStatusRenderer,
  executeHandoffPlan,
  handoffEnvironment,
  handoffOwnershipName,
  handoffPlanName,
  normalizeVersion,
  removeOwnedLock,
  reserveHandoffDirectory,
  validateHandoffPlan,
  validateHandoffOwnership,
};
