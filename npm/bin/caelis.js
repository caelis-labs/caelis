#!/usr/bin/env node

const { spawn } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const packageMap = {
  'darwin:arm64': '@onslaughtsnail/caelis-darwin-arm64',
  'darwin:x64': '@onslaughtsnail/caelis-darwin-x64',
  'linux:arm64': '@onslaughtsnail/caelis-linux-arm64',
  'linux:x64': '@onslaughtsnail/caelis-linux-x64',
  'win32:arm64': '@onslaughtsnail/caelis-windows-arm64',
  'win32:x64': '@onslaughtsnail/caelis-windows-x64',
};

function resolvePackageName() {
  const key = `${process.platform}:${process.arch}`;
  const packageName = packageMap[key];
  if (!packageName) {
    console.error(`[caelis] unsupported platform/arch: ${process.platform}/${process.arch}`);
    process.exit(1);
  }
  return packageName;
}

function resolveBinaryPath(packageName) {
  try {
    const packageJsonPath = require.resolve(`${packageName}/package.json`);
    const binaryName = process.platform === 'win32' ? 'caelis.exe' : 'caelis';
    return path.join(path.dirname(packageJsonPath), 'runtime', binaryName);
  } catch (err) {
    console.error(`[caelis] platform package not installed: ${packageName}`);
    console.error('[caelis] reinstall without --omit=optional, then try again.');
    console.error('[caelis] resolve error:', err.message);
    process.exit(1);
  }
}

const packageName = resolvePackageName();
const binPath = resolveBinaryPath(packageName);

if (!fs.existsSync(binPath)) {
  console.error('[caelis] binary not found at', binPath);
  console.error(`[caelis] reinstall ${packageName}, then try again.`);
  process.exit(1);
}

const child = spawn(binPath, process.argv.slice(2), {
  stdio: 'inherit',
  env: process.env,
});

child.on('error', (err) => {
  console.error('[caelis] failed to start binary:', err.message);
  process.exit(1);
});

child.on('exit', (code, signal) => {
  if (signal) {
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code === null ? 1 : code);
});
