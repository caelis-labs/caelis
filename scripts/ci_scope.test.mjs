import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, renameSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { classifyPaths, inspectChanges } from './ci_scope.mjs';

function fixture(t) {
  const cwd = mkdtempSync(join(tmpdir(), 'caelis-ci-'));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  const git = (...args) => execFileSync('git', ['-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=/dev/null', ...args], {
    cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
  }).trim();
  const write = (path, content) => {
    mkdirSync(dirname(join(cwd, path)), { recursive: true });
    writeFileSync(join(cwd, path), content);
  };
  const commit = () => { git('add', '-A'); git('commit', '--allow-empty', '-m', 'fixture'); return git('rev-parse', 'HEAD'); };
  git('init', '-b', 'main');
  git('config', 'user.name', 'CI Test');
  git('config', 'user.email', 'ci@example.invalid');
  write('.release-please-manifest.json', '{".":"0.56.0"}\n');
  write('source.go', 'package example\n');
  const base = commit();
  const release = () => {
    write('.release-please-manifest.json', '{".":"0.56.1"}\n');
    write('CHANGELOG.md', '# Changelog\n\n## [0.56.1](https://example.invalid) (2026-09-15)\n\n### Bug Fixes\n');
  };
  return { cwd, git, write, commit, base, release };
}

test('only maintained prose and release metadata avoid full checks', () => {
  assert.deepEqual(classifyPaths([]), {full: false, docs: false, release: false});
  for (const path of ['README.md', 'README.zh-CN.md', 'AGENTS.md', 'agent-sdk/README.md', 'docs/testing.md']) {
    assert.deepEqual(classifyPaths([path]), {full: false, docs: true, release: false}, path);
  }
  assert.deepEqual(classifyPaths(['CHANGELOG.md', '.release-please-manifest.json']), {full: false, docs: false, release: true});
  for (const path of ['go.mod', 'go.sum', 'Makefile', '.github/workflows/quality.yml', 'scripts/ci_scope.mjs',
    'cmd/main.go', 'agent-sdk/model/testdata/output.json', 'app/prompts/embedded/prompt.md', 'docs/example.go',
    'api/protocol.proto', 'packages/caelis/package.json', 'unknown', 'README.md\nsource.go']) {
    assert.equal(classifyPaths([path]).full, true, path);
  }
  assert.deepEqual(classifyPaths(['source.go', 'docs/testing.md']), {full: true, docs: true, release: false});
});

test('real release diff including the first changelog uses metadata checks only', t => {
  const f = fixture(t);
  f.release(); f.commit();
  assert.deepEqual(inspectChanges(f.base, f.cwd), {full: false, docs: false, release: true});
  f.write('CHANGELOG.md', '# Changelog\n\n## 0.56.1 (2026-09-15)\nCorrected notes\n'); f.commit();
  assert.equal(inspectChanges(f.base, f.cwd).full, false);
});

for (const [name, mutate, error] of [
  ['malformed JSON', f => f.write('.release-please-manifest.json', '{broken'), /JSON/],
  ['extra component', f => f.write('.release-please-manifest.json', '{".":"0.56.1","other":"0.56.1"}'), /one root/],
  ['prerelease', f => f.write('.release-please-manifest.json', '{".":"0.56.1-rc.1"}'), /stable version/],
  ['unchanged version', f => f.write('.release-please-manifest.json', '{ ".": "0.56.0" }'), /must increase/],
  ['decreased version', f => f.write('.release-please-manifest.json', '{".":"0.55.9"}'), /must increase/],
  ['mismatched heading', f => f.write('CHANGELOG.md', '# Changelog\n\n## 0.56.2\n'), /heading must match/],
  ['missing changelog', f => rmSync(join(f.cwd, 'CHANGELOG.md')), /regular non-executable/],
  ['deleted manifest', f => rmSync(join(f.cwd, '.release-please-manifest.json')), /regular non-executable/],
  ['executable metadata', f => chmodSync(join(f.cwd, 'CHANGELOG.md'), 0o755), /regular non-executable/],
  ['symlink metadata', f => { rmSync(join(f.cwd, 'CHANGELOG.md')); symlinkSync('source.go', join(f.cwd, 'CHANGELOG.md')); }, /regular non-executable/],
]) {
  test(`reject ${name}`, t => {
    const f = fixture(t); f.release(); mutate(f); f.commit();
    assert.throws(() => inspectChanges(f.base, f.cwd), error);
  });
}

test('rename from source to docs still selects full checks', t => {
  const f = fixture(t);
  renameSync(join(f.cwd, 'source.go'), join(f.cwd, 'README.md')); f.commit();
  assert.equal(inspectChanges(f.base, f.cwd).full, true);
});

for (const mode of ['executable', 'symlink']) {
  test(`${mode} at a documentation path selects full checks`, t => {
    const f = fixture(t);
    if (mode === 'symlink') symlinkSync('source.go', join(f.cwd, 'README.md'));
    else { f.write('README.md', 'text'); chmodSync(join(f.cwd, 'README.md'), 0o755); }
    f.commit();
    assert.equal(inspectChanges(f.base, f.cwd).full, true);
  });
}

test('merge diff excludes changes already on main, including in a shallow checkout', t => {
  const f = fixture(t);
  f.git('checkout', '-b', 'pr'); f.write('docs/testing.md', '# Testing\n'); f.commit();
  f.git('checkout', 'main'); f.write('source.go', 'package renamed\n'); const currentBase = f.commit();
  f.git('merge', '--no-ff', 'pr', '-m', 'PR merge');
  const shallow = join(f.cwd, 'shallow');
  f.git('clone', '--depth=2', `file://${f.cwd}`, shallow);
  assert.deepEqual(inspectChanges(currentBase, shallow), {full: false, docs: true, release: false});
  assert.equal(inspectChanges(f.base, f.cwd).full, true, 'older base must include all intervening changes');
});

test('invalid or unavailable base fails without emitting successful outputs', t => {
  const f = fixture(t);
  for (const base of ['', 'HEAD', '0'.repeat(40)]) {
    const output = join(f.cwd, 'output');
    const result = spawnSync(process.execPath, [fileURLToPath(new URL('./ci_scope.mjs', import.meta.url))], {
      cwd: f.cwd, env: {...process.env, PR_BASE_SHA: base, GITHUB_OUTPUT: output}, encoding: 'utf8',
    });
    assert.notEqual(result.status, 0, result.stdout);
    assert.equal(existsSync(output), false);
  }
});
