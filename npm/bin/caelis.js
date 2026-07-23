#!/usr/bin/env node

const { spawn } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const {
  UpdateHandoffError,
  completeHandoff,
  handoffEnvironment,
  reserveHandoffDirectory,
} = require('../lib/update-handoff.js');

const packageMap = {
  'darwin:arm64': '@caelis/caelis-darwin-arm64',
  'darwin:x64': '@caelis/caelis-darwin-x64',
  'linux:arm64': '@caelis/caelis-linux-arm64',
  'linux:x64': '@caelis/caelis-linux-x64',
  'win32:arm64': '@caelis/caelis-windows-arm64',
  'win32:x64': '@caelis/caelis-windows-x64',
};

const signalForwardGraceMs = 5000;

class LauncherError extends Error {
  constructor(lines) {
    const normalized = Array.isArray(lines) ? lines : [String(lines || '')];
    super(normalized.join('\n'));
    this.name = 'LauncherError';
    this.lines = normalized;
  }
}

function resolvePackageName(platform = process.platform, arch = process.arch) {
  const key = `${platform}:${arch}`;
  const packageName = packageMap[key];
  if (!packageName) {
    throw new Error(`unsupported platform/arch: ${platform}/${arch}`);
  }
  return packageName;
}

function resolveBinaryPath(
  packageName,
  platform = process.platform,
  resolver = require.resolve,
) {
  let packageJsonPath;
  try {
    packageJsonPath = resolver(`${packageName}/package.json`);
  } catch (err) {
    throw new LauncherError([
      `platform package not installed: ${packageName}`,
      'reinstall without --omit=optional, then try again',
      `resolve error: ${err.message}`,
    ]);
  }
  const binaryName = platform === 'win32' ? 'caelis.exe' : 'caelis';
  return path.join(path.dirname(packageJsonPath), 'runtime', binaryName);
}

function handoffEligible(platform, argv, stdinIsTTY) {
  if (platform !== 'win32') {
    return false;
  }
  const first = String(argv[0] || '').trim().toLowerCase();
  if (first === 'update') {
    return !argv.some((value) => value === '--check' || value === '-check');
  }
  const nonInteractiveCommands = new Set([
    'version',
    'acp',
    'doctor',
    'serve',
    'server',
    'sandbox',
  ]);
  if (nonInteractiveCommands.has(first) ||
      argv.some((value) => value === '--doctor' || value === '-doctor')) {
    return false;
  }
  if (argv.some((value) =>
    value === '--interactive' || value === '-interactive')) {
    return true;
  }
  const hasPrompt = argv.some((value, index) => {
    if (value === '-p') {
      return index + 1 < argv.length && String(argv[index + 1]).trim() !== '';
    }
    return String(value).startsWith('-p=') &&
      String(value).slice(3).trim() !== '';
  });
  if (hasPrompt) {
    return false;
  }
  return Boolean(stdinIsTTY);
}

function formatLauncherError(err) {
  if (err instanceof UpdateHandoffError) {
    return `[caelis] update failed: ${err.message}`;
  }
  if (err instanceof LauncherError) {
    return err.lines.map((line) => `[caelis] ${line}`).join('\n');
  }
  return `[caelis] ${err && err.message ? err.message : String(err)}`;
}

function createSignalCoordinator(target = process, options = {}) {
  const forceKillAfterMs = options.forceKillAfterMs === undefined
    ? signalForwardGraceMs
    : options.forceKillAfterMs;
  const handlers = new Map();
  let activeChild;
  let forceKillTimer;
  let signal;

  function clearForceKillTimer() {
    if (forceKillTimer) {
      clearTimeout(forceKillTimer);
      forceKillTimer = undefined;
    }
  }

  function forwardSignal() {
    if (!signal || !activeChild) {
      return;
    }
    try {
      activeChild.kill(signal);
    } catch {
      // Still arm the hard-stop fallback below.
    }
    clearForceKillTimer();
    if (forceKillAfterMs < 0) {
      return;
    }
    forceKillTimer = setTimeout(() => {
      if (!activeChild) {
        return;
      }
      try {
        activeChild.kill('SIGKILL');
      } catch {
        // The child may have exited between the timer check and kill.
      }
    }, forceKillAfterMs);
    if (typeof forceKillTimer.unref === 'function') {
      forceKillTimer.unref();
    }
  }

  for (const name of ['SIGINT', 'SIGTERM']) {
    const handler = () => {
      if (!signal) {
        signal = name;
      }
      forwardSignal();
    };
    handlers.set(name, handler);
    target.on(name, handler);
  }

  return {
    receivedSignal() {
      return signal;
    },
    track(child) {
      activeChild = child;
      forwardSignal();
      let released = false;
      return () => {
        if (released) {
          return;
        }
        released = true;
        if (activeChild === child) {
          activeChild = undefined;
          clearForceKillTimer();
        }
      };
    },
    close() {
      clearForceKillTimer();
      activeChild = undefined;
      for (const [name, handler] of handlers) {
        target.removeListener(name, handler);
      }
      handlers.clear();
    },
  };
}

function runProcess(command, args, options = {}) {
  const { signalCoordinator, ...spawnOptions } = options;
  const pendingSignal = signalCoordinator && signalCoordinator.receivedSignal();
  if (pendingSignal) {
    return Promise.resolve({ code: 1, signal: pendingSignal });
  }
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, spawnOptions);
    let releaseChild = () => {};
    child.once('error', (err) => {
      releaseChild();
      reject(err);
    });
    child.once('exit', (code, signal) => {
      releaseChild();
      resolve({ code: code === null ? 1 : code, signal });
    });
    if (signalCoordinator) {
      releaseChild = signalCoordinator.track(child);
    }
  });
}

async function main() {
  const platform = process.platform;
  const packageName = resolvePackageName(platform, process.arch);
  const binPath = resolveBinaryPath(packageName, platform);
  const packageRoot = path.resolve(__dirname, '..');
  const platformPackageRoot = path.dirname(path.dirname(binPath));

  if (!fs.existsSync(binPath)) {
    throw new LauncherError([
      `binary not found at ${binPath}`,
      `reinstall ${packageName}, then try again`,
    ]);
  }

  const argv = process.argv.slice(2);
  // The TUI can request an update after startup, so eligibility cannot be
  // based only on an explicit `update` argv. Reserving a path does no
  // filesystem I/O; Go creates it lazily only for an actual handoff.
  const handoffDir = handoffEligible(platform, argv, process.stdin.isTTY)
    ? reserveHandoffDirectory(platform)
    : '';
  const signalCoordinator = handoffDir
    ? createSignalCoordinator()
    : undefined;
  const env = {
    ...process.env,
    CAELIS_INSTALL_METHOD: 'npm',
    CAELIS_NPM_PACKAGE_DIR: packageRoot,
    CAELIS_NPM_PLATFORM_PACKAGE: packageName,
    CAELIS_NPM_PLATFORM_PACKAGE_DIR: platformPackageRoot,
  };
  if (handoffDir) {
    env[handoffEnvironment] = handoffDir;
  }
  try {
    let childResult = await runProcess(binPath, argv, {
      stdio: 'inherit',
      env,
      signalCoordinator,
    });
    const receivedSignal = signalCoordinator && signalCoordinator.receivedSignal();
    if (receivedSignal && !childResult.signal) {
      childResult = { code: 1, signal: receivedSignal };
    }
    const result = await completeHandoff(handoffDir, childResult, {
      signalCoordinator,
    });
    const finalSignal = signalCoordinator && signalCoordinator.receivedSignal();
    if (finalSignal) {
      return { code: 1, signal: finalSignal };
    }
    return result;
  } catch (err) {
    const receivedSignal = signalCoordinator && signalCoordinator.receivedSignal();
    if (receivedSignal) {
      return { code: 1, signal: receivedSignal };
    }
    throw err;
  } finally {
    if (signalCoordinator) {
      signalCoordinator.close();
    }
  }
}

if (require.main === module) {
  main()
    .then((result) => {
      if (result.signal) {
        process.kill(process.pid, result.signal);
        return;
      }
      process.exit(result.code);
    })
    .catch((err) => {
      console.error(formatLauncherError(err));
      process.exit(1);
    });
}

module.exports = {
  LauncherError,
  createSignalCoordinator,
  formatLauncherError,
  handoffEligible,
  main,
  resolveBinaryPath,
  resolvePackageName,
  runProcess,
};
