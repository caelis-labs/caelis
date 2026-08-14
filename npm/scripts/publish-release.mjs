import fs from 'node:fs/promises';
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

async function versionExists(name, version) {
  try {
    const { stdout } = await execFileAsync(
      'npm',
      ['view', `${name}@${version}`, 'version', '--registry=https://registry.npmjs.org'],
      { cwd: packageRoot },
    );
    return stdout.trim() === version;
  } catch {
    return false;
  }
}

async function publishPackage(packageDir) {
  const manifest = await readManifest(packageDir);
  if (await versionExists(manifest.name, manifest.version)) {
    console.log(`[caelis] skip publish for ${manifest.name}@${manifest.version}; already exists`);
    return;
  }
  console.log(`[caelis] publishing ${manifest.name}@${manifest.version}`);
  const args = ['publish', '--access', 'public'];
  if (process.env.CAELIS_NPM_PUBLISH_PROVENANCE === 'false') {
    args.push('--provenance=false');
  }
  if (manifest.version.includes('-')) {
    args.push('--tag', 'bootstrap');
  }
  await runCommand('npm', args, { cwd: packageDir });
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
