import fs from 'node:fs/promises';
import { createReadStream } from 'node:fs';
import { createHash } from 'node:crypto';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);
const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, '..');

const registry = '--registry=https://registry.npmjs.org';
const readTimeoutMs = 20_000;
const preflightTimeoutMs = 2 * 60_000;
const propagationTimeoutMs = 20 * 60_000;

function npmErrorCode(err) {
  try {
    const code = JSON.parse(err.stdout).error?.code;
    if (typeof code === 'string') return code;
  } catch { /* npm may fail before producing JSON. */ }
  return err.stderr?.match(/\b(?:code|error)\s+(E[A-Z0-9_]+)\b/)?.[1] ?? err.code;
}

function transientReadError(err) {
  const code = npmErrorCode(err);
  return err.killed || /^(E429|E5\d\d|ETIMEDOUT|ESOCKETTIMEDOUT|ECONNRESET|ECONNREFUSED|ECONNABORTED|EAI_AGAIN|ENOTFOUND|ENETUNREACH|EHOSTUNREACH|EPIPE|FETCH_ERROR|EJSONPARSE)$/.test(code);
}

async function readManifest(packageDir) {
  const manifestPath = path.join(packageDir, 'package.json');
  const raw = await fs.readFile(manifestPath, 'utf8');
  return JSON.parse(raw);
}

// Keep npm's registry/auth/proxy handling, but bound each read and let one
// deadline own retries. npm's default nested retries can take several minutes.
export async function publishedIntegrity(name, version, { run = execFileAsync, timeoutMs = readTimeoutMs } = {}) {
  let stdout;
  try {
    ({ stdout } = await run('npm', [
      'view', `${name}@${version}`, 'dist.integrity', '--json', '--prefer-online',
      '--fetch-retries=0', `--fetch-timeout=${timeoutMs}`, registry,
    ], { cwd: packageRoot, timeout: timeoutMs, killSignal: 'SIGKILL' }));
  } catch (err) {
    if (npmErrorCode(err) === 'E404') return null;
    if (transientReadError(err)) {
      throw Object.assign(new Error(`registry read ${npmErrorCode(err) ?? 'timeout'}`), { retryable: true });
    }
    throw err;
  }
  let integrity;
  try {
    integrity = JSON.parse(stdout || 'null');
  } catch {
    throw Object.assign(new Error('registry returned invalid JSON'), { retryable: true });
  }
  // A visible version with incomplete metadata must not be treated as absent.
  if (integrity === null || integrity === '') {
    throw Object.assign(new Error('registry metadata has no integrity yet'), { retryable: true });
  }
  if (typeof integrity !== 'string' || !/^sha512-[A-Za-z0-9+/]{86}==$/.test(integrity)) {
    throw new Error(`npm registry has invalid SHA512 integrity for ${name}@${version}`);
  }
  return integrity;
}

// Preflight may return a genuine 404; after submission only matching bytes are
// success. Transient reads never become proof of absence or permission to upload.
export async function waitForIntegrity(spec, expected, {
  read, allowAbsent = false, timeoutMs = propagationTimeoutMs,
  now = () => performance.now(), sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  random = Math.random, log = console.log,
} = {}) {
  const start = now();
  const deadline = start + timeoutMs;
  let attempt = 0;
  let last = 'not visible';
  while (now() < deadline) {
    try {
      const actual = await read(Math.min(readTimeoutMs, Math.max(1, Math.ceil(deadline - now()))));
      if (actual === expected) return actual;
      if (actual !== null) throw new Error(`npm ${spec} already exists with different integrity`);
      if (allowAbsent) return null;
      last = 'version not visible (404)';
    } catch (err) {
      if (!err.retryable) throw err;
      last = err.message;
    }
    const remaining = deadline - now();
    if (remaining <= 0) break;
    const delay = Math.min(remaining, Math.round(Math.min(30_000, 5_000 * 2 ** Math.min(attempt++, 3)) * (0.8 + 0.2 * random())));
    log(`[caelis] ${spec}: ${last}; elapsed ${Math.round((now() - start) / 1000)}s; retry read in ${Math.ceil(delay / 1000)}s`);
    await sleep(delay);
  }
  throw new Error(`npm ${spec}: registry verification timed out after ${Math.round(timeoutMs / 1000)}s (${last}); publication is unconfirmed. Inspect registry state before retrying; do not rebuild or re-sign artifacts`);
}

async function sha512(file) {
  const hash = createHash('sha512');
  for await (const chunk of createReadStream(file)) hash.update(chunk);
  return `sha512-${hash.digest('base64')}`;
}

async function packPackage(packageDir, manifest, tempDir, run) {
  const { stdout } = await run('npm', ['pack', '--json', '--pack-destination', tempDir], { cwd: packageDir });
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

export async function publishPackage(packageDir, { run = execFileAsync, waitOptions = {}, log = console.log } = {}) {
  const manifest = await readManifest(packageDir);
  const spec = `${manifest.name}@${manifest.version}`;
  const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), 'caelis-npm-publish-'));
  try {
    const { tarball, integrity } = await packPackage(packageDir, manifest, tempDir, run);
    const read = (timeoutMs) => publishedIntegrity(manifest.name, manifest.version, { run, timeoutMs });
    const existing = await waitForIntegrity(spec, integrity, {
      ...waitOptions, read, log, allowAbsent: true, timeoutMs: preflightTimeoutMs,
    });
    if (existing) {
      log(`[caelis] verified existing ${spec}; skip publish`);
      return;
    }
    log(`[caelis] publishing ${spec}`);
    const args = ['publish', tarball, '--access', 'public', '--json', '--fetch-retries=0', registry];
    if (process.env.CAELIS_NPM_PUBLISH_PROVENANCE === 'false') args.push('--provenance=false');
    if (manifest.version.includes('-')) args.push('--tag', 'bootstrap');
    let publishError;
    try {
      const result = await run('npm', args, { cwd: packageDir, timeout: 5 * 60_000, killSignal: 'SIGKILL', maxBuffer: 4 * 1024 * 1024 });
      log(`[caelis] ${spec}: upload command completed; verifying registry`);
      if (result.stderr) log(`[caelis] ${spec}: ${result.stderr.trim()}`);
    } catch (err) {
      publishError = err;
      // Auth/configuration failures are actionable, not propagation delays.
      // E403 may also mean a previous upload already exists, so reconcile it.
      if (['E401', 'ENEEDAUTH', 'EOTP', 'EUSAGE', 'EINVALIDPACKAGENAME'].includes(npmErrorCode(err)) || err.code === 'ENOENT') throw err;
      log(`[caelis] ${spec}: publish command failed (${npmErrorCode(err) ?? 'unknown'}); reconciling registry without another upload`);
      if (err.stderr) log(`[caelis] ${spec}: ${err.stderr.trim()}`);
    }
    try {
      await waitForIntegrity(spec, integrity, { ...waitOptions, read, log });
    } catch (err) {
      if (publishError) throw new Error(`${err.message}; publish command: ${npmErrorCode(publishError) ?? publishError.message}`, { cause: err });
      throw err;
    }
    log(`[caelis] verified published ${spec}`);
  } finally {
    await fs.rm(tempDir, { recursive: true, force: true });
  }
}

// Settle every platform even when one fails. No sibling upload is abandoned and
// the caller (the workflow's next step) proceeds only after the entire gate passes.
export async function publishPackages(inputs, { concurrency = 1, publish = publishPackage } = {}) {
  if (!Number.isInteger(concurrency) || concurrency < 1 || concurrency > 6) throw new Error('concurrency must be an integer from 1 to 6');
  if (!inputs.length) throw new Error('expected at least one package directory');
  const dirs = inputs.map((input) => path.resolve(input));
  if (new Set(dirs).size !== dirs.length) throw new Error('duplicate package directory');
  let next = 0;
  const failures = [];
  await Promise.all(Array.from({ length: Math.min(concurrency, dirs.length) }, async () => {
    while (next < dirs.length) {
      const dir = dirs[next++];
      try {
        await publish(dir);
      } catch (err) {
        failures.push(`${dir}: ${err.message}`);
      }
    }
  }));
  if (failures.length) throw new Error(`${failures.length} npm package(s) failed verification:\n${failures.join('\n')}`);
}

async function main() {
  const inputs = process.argv.slice(2);
  let concurrency = 1;
  if (inputs[0] === '--concurrency') {
    inputs.shift();
    concurrency = Number(inputs.shift());
  }
  await publishPackages(inputs, { concurrency });
}

if (process.argv[1] && path.resolve(process.argv[1]) === __filename) {
  main().catch((err) => {
    console.error('[caelis] failed to publish packages:', err.message);
    process.exitCode = 1;
  });
}
