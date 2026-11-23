#!/usr/bin/env python3
"""Prepare/verify the target's official DTW package for Core go:embed builds."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import stat
import subprocess
import tempfile
import tarfile
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]
PIN = ROOT / 'control/plugin/builtin/desktop-world.json'
TARGETS = {
    'darwin/arm64': 'aarch64-apple-darwin',
    'linux/amd64': 'x86_64-unknown-linux-gnu',
    'linux/arm64': 'aarch64-unknown-linux-gnu',
    'windows/amd64': 'x86_64-pc-windows-msvc',
}


def sha(data):
    return hashlib.sha256(data).hexdigest()


def obtain(cache, name, digest, url):
    path = cache / name
    if path.exists() and sha(path.read_bytes()) == digest:
        return path.read_bytes()
    cache.mkdir(parents=True, exist_ok=True)
    with urllib.request.urlopen(url, timeout=60) as response:
        data = response.read()
    if sha(data) != digest:
        raise ValueError(f'checksum mismatch: {name}')
    with tempfile.NamedTemporaryFile(dir=cache, delete=False) as out:
        staging = Path(out.name)
        out.write(data)
    try:
        os.replace(staging, path)
    finally:
        staging.unlink(missing_ok=True)
    return data


def verify_release(pin, data):
    if sha(data) != pin['release_sha256']:
        raise ValueError('RELEASE checksum mismatch')
    release = json.loads(data)
    if release['version'] != pin['version'] or release['tag'] != pin['tag']:
        raise ValueError('formal RELEASE identity mismatch')
    platforms = {p['target']: p for p in release['platforms']}
    for expected in pin['platforms']:
        actual = platforms[expected['target']]
        for key in ('archive', 'archive_sha256', 'binary_sha256', 'source'):
            if actual[key] != expected[key]:
                raise ValueError(f'formal RELEASE {key} mismatch')


def verify_skill_identity(data, name):
    # Official packages use a literal top-level name in fenced frontmatter;
    # this deliberately does not interpret general YAML syntax.
    lines = data.decode('utf-8').splitlines()
    if not lines or lines[0] != '---' or '---' not in lines[1:]:
        raise ValueError('missing Skill identity frontmatter')
    metadata = lines[1:lines.index('---', 1)]
    names = [line.partition(':')[2].strip() for line in metadata if line.startswith('name:')]
    if len(names) != 1 or names[0] not in (name, f'"{name}"', f"'{name}'"):
        raise ValueError('unexpected Skill identity (expected a literal name)')


def archive_suffix(name):
    for suffix in ('.zip', '.tar.gz'):
        if name.endswith(suffix):
            return suffix
    raise ValueError(f'unsupported package archive format: {name}')


def package_files(name, data):
    suffix = archive_suffix(name)
    root = name.removesuffix(suffix)
    prefix = root + '/'
    files = {}

    def check(path, is_dir, is_regular, size):
        if (not is_dir and not is_regular) or size < 0 or size > 128 << 20:
            raise ValueError(f'unsafe entry: {path}')
        if is_dir and path.rstrip('/') == root:
            return None
        if not path.startswith(prefix):
            raise ValueError(f'foreign archive root: {path}')
        rel = path[len(prefix):].removesuffix('/')
        if not rel or '\\' in rel or any(part in ('', '.', '..') for part in rel.split('/')):
            raise ValueError(f'unsafe entry: {path}')
        if rel in files:
            raise ValueError(f'duplicate entry: {rel}')
        return None if is_dir else rel

    if suffix == '.zip':
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            for entry in archive.infolist():
                is_dir = entry.is_dir()
                mode = stat.S_IFMT(entry.external_attr >> 16)
                if mode not in (0, stat.S_IFDIR if is_dir else stat.S_IFREG):
                    raise ValueError(f'unsafe entry: {entry.filename}')
                rel = check(entry.filename, is_dir, not is_dir, entry.file_size)
                if rel is not None:
                    files[rel] = archive.read(entry)
    else:
        with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as archive:
            for entry in archive:
                rel = check(entry.name, entry.isdir(), entry.type in (tarfile.REGTYPE, tarfile.AREGTYPE), entry.size)
                if rel is not None:
                    with archive.extractfile(entry) as member:
                        files[rel] = member.read()
    return files


def verify(pin, platform, data):
    if sha(data) != platform['archive_sha256']:
        raise ValueError('archive checksum mismatch')
    files = package_files(platform['archive'], data)
    build = json.loads(files['BUILD.json'])
    manifest = json.loads(files['plugin.json'])
    binary = 'bin/dtw.exe' if 'windows' in platform['target'] else 'bin/dtw'
    if manifest['name'] != 'desktop-world' or manifest['version'] != pin['version']:
        raise ValueError('package identity mismatch')
    if build['version'] != pin['version'] or build['target'] != platform['target'] or build['source'] != platform['source']:
        raise ValueError('build provenance mismatch')
    for required in ['.mcp.json', 'LICENSE', 'THIRD_PARTY_NOTICES.md']:
        if not files.get(required):
            raise ValueError(f'missing complete package content: {required}')
    servers = json.loads(files['.mcp.json'])['mcpServers']
    if set(servers) not in ({'desktop-world'}, {'dtw'}):
        raise ValueError('unexpected standard MCP server identity')
    name = next(iter(servers))
    skill = f'skills/{name}/SKILL.md'
    skills = {path for path in files if path.startswith('skills/') and path.endswith('/SKILL.md')}
    if skills != {skill}:
        raise ValueError('unexpected Skill location for MCP server identity')
    if sha(files['BUILD.json']) != platform['build_sha256'] or sha(files[binary]) != platform['binary_sha256'] or sha(files[skill]) != platform['skill_sha256']:
        raise ValueError('BUILD/binary/Skill checksum mismatch')
    verify_skill_identity(files[skill], name)
    server = servers[name]
    if server != {'command': '${CLAUDE_PLUGIN_ROOT}/' + binary, 'args': ['mcp']}:
        raise ValueError('unexpected standard MCP launch contract')
    return {'id': 'desktop-world', 'version': pin['version'], 'target': platform['target'], 'archive_sha256': sha(data), 'skill_sha256': platform['skill_sha256'], 'files': len(files)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--target', help='GOOS/GOARCH or official target (defaults to go env)')
    parser.add_argument('--all', action='store_true', help='prepare all supported targets for cross builds')
    parser.add_argument('--cache-dir', type=Path, default=ROOT / '.tmp/cache/builtin-dtw')
    parser.add_argument('--verify-archives', type=Path, help='verify actual supported Core release archives')
    parser.add_argument('--core-version', help='Core release version for archive verification')
    parser.add_argument('--verify-binary', type=Path, help='also prove the complete pinned archive is in this Core executable')
    args = parser.parse_args()
    pin = json.loads(PIN.read_text())
    pinned_targets = [p['target'] for p in pin['platforms']]
    if sorted(pinned_targets) != sorted(TARGETS.values()):
        raise ValueError('DTW pin must contain one package per supported target')
    target = args.target
    if not target and not args.all:
        env = subprocess.check_output(['go', 'env', 'GOOS', 'GOARCH'], text=True).split()
        target = '/'.join(env)
    target = TARGETS.get(target, target)
    chosen = pin['platforms'] if args.all else [p for p in pin['platforms'] if p['target'] == target]
    if not chosen:
        print(json.dumps({'computer_use': 'unsupported', 'target': target}))
        return
    release_url = f"https://github.com/{pin['repository']}/releases/download/{pin['tag']}/RELEASE.json"
    release_data = obtain(args.cache_dir, 'RELEASE-' + pin['tag'] + '.json', pin['release_sha256'], release_url)
    verify_release(pin, release_data)
    generated = ROOT / 'control/plugin/builtin/generated'
    generated.mkdir(parents=True, exist_ok=True)
    for platform in chosen:
        url = f"https://github.com/{pin['repository']}/releases/download/{pin['tag']}/{platform['archive']}"
        dest = generated / (platform['target'] + archive_suffix(platform['archive']))
        data = dest.read_bytes() if dest.exists() else b''
        if sha(data) != platform['archive_sha256']:
            data = obtain(args.cache_dir, platform['archive'], platform['archive_sha256'], url)
        evidence = verify(pin, platform, data)
        evidence['release_sha256'] = pin['release_sha256']
        if not dest.exists() or dest.read_bytes() != data:
            with tempfile.NamedTemporaryFile(dir=generated, delete=False) as out:
                staging = Path(out.name)
                out.write(data)
            os.replace(staging, dest)
        if args.verify_binary:
            if data not in args.verify_binary.read_bytes():
                raise ValueError('Core executable does not contain the complete pinned platform package')
            evidence['core_embedded_package'] = 'verified'
        if args.verify_archives:
            if not args.core_version:
                raise ValueError('--core-version is required for release archives')
            go_target = next(k for k, v in TARGETS.items() if v == platform['target'])
            goos, goarch = go_target.split('/')
            archive_path = args.verify_archives / f'caelis_{args.core_version}_{goos}_{goarch}.tar.gz'
            with tarfile.open(archive_path, 'r:gz') as bundle:
                executable = 'caelis.exe' if goos == 'windows' else 'caelis'
                candidates = [m for m in bundle.getmembers() if m.isfile() and PurePosixPath(m.name).name == executable]
                if len(candidates) != 1 or data not in bundle.extractfile(candidates[0]).read():
                    raise ValueError(f'{archive_path.name} does not contain the complete pinned DTW package')
            evidence['release_archive'] = archive_path.name
            evidence['core_embedded_package'] = 'verified'
        print(json.dumps(evidence, sort_keys=True))


if __name__ == '__main__':
    main()
