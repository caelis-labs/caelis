import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);
const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, '..');

const targets = [
  { os: 'darwin', arch: 'arm64', dir: 'caelis-darwin-arm64', runtimeFiles: ['caelis'] },
  { os: 'darwin', arch: 'amd64', dir: 'caelis-darwin-x64', runtimeFiles: ['caelis'] },
  { os: 'linux', arch: 'arm64', dir: 'caelis-linux-arm64', runtimeFiles: ['caelis'] },
  { os: 'linux', arch: 'amd64', dir: 'caelis-linux-x64', runtimeFiles: ['caelis'] },
  {
    os: 'windows',
    arch: 'arm64',
    dir: 'caelis-windows-arm64',
    runtimeFiles: ['caelis.exe'],
  },
  {
    os: 'windows',
    arch: 'amd64',
    dir: 'caelis-windows-x64',
    runtimeFiles: ['caelis.exe'],
  },
];

function normalizeVersion(input) {
  const version = String(input || '').trim().replace(/^v/, '');
  if (!/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(version)) {
    throw new Error(`invalid version: ${input}`);
  }
  return version;
}

async function findFile(rootDir, expectedName) {
  const queue = [rootDir];
  while (queue.length > 0) {
    const current = queue.shift();
    const entries = await fs.readdir(current, { withFileTypes: true });
    for (const entry of entries) {
      const full = path.join(current, entry.name);
      if (entry.isDirectory()) {
        queue.push(full);
        continue;
      }
      if (entry.isFile() && entry.name === expectedName) {
        return full;
      }
    }
  }
  return '';
}

async function stageTarget(version, distDir, target) {
  const archiveName = `caelis_${version}_${target.os}_${target.arch}.tar.gz`;
  const archivePath = path.join(distDir, archiveName);
  const packageDir = path.join(packageRoot, 'packages', target.dir);
  const runtimeDir = path.join(packageDir, 'runtime');
  const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), `caelis-${target.dir}-`));
  try {
    await fs.access(archivePath);
    await execFileAsync('tar', ['-xzf', archivePath, '-C', tempDir]);
    const runtimeFiles = target.runtimeFiles || ['caelis'];
    const extractedFiles = new Map();
    for (const name of runtimeFiles) {
      const extracted = await findFile(tempDir, name);
      if (!extracted) {
        throw new Error(`${name} not found in ${archiveName}`);
      }
      extractedFiles.set(name, extracted);
    }
    await fs.rm(runtimeDir, { recursive: true, force: true });
    await fs.mkdir(runtimeDir, { recursive: true });
    for (const [name, extracted] of extractedFiles) {
      const destPath = path.join(runtimeDir, name);
      await fs.copyFile(extracted, destPath);
      await fs.chmod(destPath, 0o755);
    }
  } finally {
    await fs.rm(tempDir, { recursive: true, force: true });
  }
}

async function main() {
  const version = normalizeVersion(process.argv[2]);
  const distDir = path.resolve(process.argv[3] || path.join(packageRoot, '..', 'dist'));
  await Promise.all(targets.map((target) => stageTarget(version, distDir, target)));
}

main().catch((err) => {
  console.error('[caelis] failed to stage platform packages:', err.message);
  process.exit(1);
});
