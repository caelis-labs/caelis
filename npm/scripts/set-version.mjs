import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, '..');

const platformPackages = [
  '@caelis/caelis-darwin-arm64',
  '@caelis/caelis-darwin-x64',
  '@caelis/caelis-linux-arm64',
  '@caelis/caelis-linux-x64',
  '@caelis/caelis-windows-arm64',
  '@caelis/caelis-windows-x64',
];

const manifestPaths = [
  path.join(packageRoot, 'package.json'),
  path.join(packageRoot, 'packages', 'caelis-darwin-arm64', 'package.json'),
  path.join(packageRoot, 'packages', 'caelis-darwin-x64', 'package.json'),
  path.join(packageRoot, 'packages', 'caelis-linux-arm64', 'package.json'),
  path.join(packageRoot, 'packages', 'caelis-linux-x64', 'package.json'),
  path.join(packageRoot, 'packages', 'caelis-windows-arm64', 'package.json'),
  path.join(packageRoot, 'packages', 'caelis-windows-x64', 'package.json'),
];

function normalizeVersion(input) {
  const version = String(input || '').trim().replace(/^v/, '');
  if (!/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(version)) {
    throw new Error(`invalid version: ${input}`);
  }
  return version;
}

async function updateManifest(manifestPath, version) {
  const raw = await fs.readFile(manifestPath, 'utf8');
  const manifest = JSON.parse(raw);
  manifest.version = version;
  if (manifest.name === '@caelis/caelis') {
    manifest.optionalDependencies = Object.fromEntries(
      platformPackages.map((packageName) => [packageName, version]),
    );
  }
  await fs.writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
}

async function main() {
  const version = normalizeVersion(process.argv[2]);
  await Promise.all(manifestPaths.map((manifestPath) => updateManifest(manifestPath, version)));
}

main().catch((err) => {
  console.error('[caelis] failed to update package versions:', err.message);
  process.exit(1);
});
