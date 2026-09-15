import { execFileSync } from 'node:child_process';
import { appendFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const releaseFiles = new Set(['.release-please-manifest.json', 'CHANGELOG.md']);
const rootDocs = new Set(['README.md', 'README.zh-CN.md', 'AGENTS.md', 'agent-sdk/README.md']);
const stableVersion = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;
const isDoc = path => rootDocs.has(path) || (path.startsWith('docs/') && path.endsWith('.md'));

// Unknown paths include build inputs, embedded prompts, dependencies and CI.
// Only maintained prose and the two release metadata files avoid full checks.
export function classifyPaths(paths) {
  return {
    full: paths.some(path => !isDoc(path) && !releaseFiles.has(path)),
    docs: paths.some(isDoc),
    release: paths.some(path => releaseFiles.has(path)),
  };
}

function manifestVersion(content) {
  const value = JSON.parse(content);
  if (!value || Array.isArray(value) || Object.keys(value).length !== 1 ||
      typeof value['.'] !== 'string' || !stableVersion.test(value['.'])) {
    throw new Error('release manifest must contain one root stable version');
  }
  return value['.'];
}

export function inspectChanges(base, cwd = process.cwd()) {
  if (!/^[a-f0-9]{40}$/.test(base ?? '')) throw new Error('a full PR base SHA is required');
  const git = (...args) => execFileSync('git', args, {cwd, encoding: 'utf8', maxBuffer: 8 * 1024 * 1024});
  git('merge-base', '--is-ancestor', base, 'HEAD');
  const paths = git('diff', '--no-ext-diff', '--no-textconv', '--no-renames', '--name-only', '-z', base, 'HEAD', '--')
    .split('\0').filter(Boolean);
  const scope = classifyPaths(paths);
  // A prose path becoming executable or a symlink needs the code checks too.
  const entries = new Map(git('ls-tree', '-r', '-z', 'HEAD').split('\0').filter(Boolean)
    .map(entry => [entry.slice(entry.indexOf('\t') + 1), entry.slice(0, 6)]));
  if (paths.some(path => isDoc(path) && entries.has(path) && entries.get(path) !== '100644')) {
    scope.full = true;
  }
  if (scope.release) {
    for (const path of releaseFiles) {
      if (!git('ls-tree', 'HEAD', '--', path).startsWith('100644 blob ')) {
        throw new Error(`${path} must be a regular non-executable file`);
      }
    }
    const version = manifestVersion(git('show', 'HEAD:.release-please-manifest.json'));
    if (paths.includes('.release-please-manifest.json')) {
      const previous = manifestVersion(git('show', `${base}:.release-please-manifest.json`));
      const before = previous.split('.').map(BigInt);
      const after = version.split('.').map(BigInt);
      const changed = after.findIndex((part, index) => part !== before[index]);
      if (changed < 0 || after[changed] < before[changed]) {
        throw new Error(`release version must increase from ${previous}, got ${version}`);
      }
    }
    const heading = git('show', 'HEAD:CHANGELOG.md').split(/\r?\n/).find(line => line.startsWith('## '));
    const match = heading?.match(/^## (?:\[(\d+\.\d+\.\d+)\]|(\d+\.\d+\.\d+)(?=\s|$))/);
    if ((match?.[1] ?? match?.[2]) !== version) {
      throw new Error(`first changelog release heading must match ${version}`);
    }
    console.log(`Release metadata validated: ${version}`);
  }
  return scope;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const scope = inspectChanges(process.env.PR_BASE_SHA);
    console.log(`Required checks: ${JSON.stringify(scope)}`);
    appendFileSync(process.env.GITHUB_OUTPUT, `full=${scope.full}\ndocs=${scope.docs}\n`);
  } catch (error) {
    console.error(`CI change inspection failed: ${error.message}`);
    process.exitCode = 1;
  }
}
