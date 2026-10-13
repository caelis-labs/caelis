import fs from 'node:fs/promises';
import { createReadStream } from 'node:fs';
import { createHash } from 'node:crypto';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);
const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, '..');

function runCommand(command, args, options) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      ...options,
      stdio: 'inherit',
    });
    child.on('error', reject);
    child.on('exit', (code) => {
      if (code === 0) {
        resolve();
        return;
      }
      reject(new Error(`${command} exited with code ${code}`));
    });
  });
}

async function readManifest(packageDir) {
  const manifestPath = path.join(packageDir, 'package.json');
  const raw = await fs.readFile(manifestPath, 'utf8');
  return JSON.parse(raw);
}

async function publishedIntegrity(name, version) {
  try {
    const { stdout } = await execFileAsync(
      'npm',
      ['view', `${name}@${version}`, 'dist.integrity', '--json', '--registry=https://registry.npmjs.org'],
      { cwd: packageRoot },
    );
    const integrity = JSON.parse(stdout);
    if (typeof integrity !== 'string' || !integrity.startsWith('sha512-')) {
      throw new Error(`npm registry has no SHA512 integrity for ${name}@${version}`);
    }
    return integrity;
  } catch (err) {
    if (err.stderr?.includes('E404') || err.stderr?.includes('404 Not Found')) {
      return null;
    }
    throw err;
  }
}

async function sha512(file) {
  const hash = createHash('sha512');
  for await (const chunk of createReadStream(file)) hash.update(chunk);
  return `sha512-${hash.digest('base64')}`;
}

async function packPackage(packageDir, manifest, tempDir) {
  const { stdout } = await execFileAsync('npm', ['pack', '--json', '--pack-destination', tempDir], { cwd: packageDir });
  const [packed, ...extra] = JSON.parse(stdout);
  if (extra.length || packed?.name !== manifest.name || packed?.version !== manifest.version ||
      typeof packed.filename !== 'string' || path.basename(packed.filename) !== packed.filename) {
    throw new Error(`npm pack produced unexpected package for ${manifest.name}@${manifest.version}`);
  }
  const tarball = path.join(tempDir, packed.filename);
  const integrity = await sha512(tarball);
  if (packed.integrity !== integrity) {
    throw new Error(`npm pack integrity mismatch for ${manifest.name}@${manifest.version}`);
  }
  return { tarball, integrity };
}

async function publishPackage(packageDir) {
  const manifest = await readManifest(packageDir);
  const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), 'caelis-npm-publish-'));
  try {
    const { tarball, integrity } = await packPackage(packageDir, manifest, tempDir);
    const existing = await publishedIntegrity(manifest.name, manifest.version);
    if (existing) {
      if (existing !== integrity) {
        throw new Error(`npm ${manifest.name}@${manifest.version} already exists with different integrity`);
      }
      console.log(`[caelis] verified existing ${manifest.name}@${manifest.version}; skip publish`);
      return;
    }
    console.log(`[caelis] publishing ${manifest.name}@${manifest.version}`);
    const args = ['publish', tarball, '--access', 'public'];
    if (process.env.CAELIS_NPM_PUBLISH_PROVENANCE === 'false') {
      args.push('--provenance=false');
    }
    if (manifest.version.includes('-')) {
      args.push('--tag', 'bootstrap');
    }
    await runCommand('npm', args, { cwd: packageDir });
    for (let attempt = 0; attempt < 5; attempt++) {
      const published = await publishedIntegrity(manifest.name, manifest.version);
      if (published === integrity) return;
      if (published) throw new Error(`npm ${manifest.name}@${manifest.version} published with different integrity`);
      await new Promise((resolve) => setTimeout(resolve, 2000));
    }
    throw new Error(`npm ${manifest.name}@${manifest.version} has no verifiable registry integrity after publish`);
  } finally {
    await fs.rm(tempDir, { recursive: true, force: true });
  }
}

async function main() {
  const inputs = process.argv.slice(2);
  if (inputs.length === 0) {
    throw new Error('expected at least one package directory');
  }
  for (const input of inputs) {
    const packageDir = path.resolve(input);
    await publishPackage(packageDir);
  }
}

main().catch((err) => {
  console.error('[caelis] failed to publish packages:', err.message);
  process.exit(1);
});
