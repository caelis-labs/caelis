#!/usr/bin/env python3
"""Generate Core's DTW pin from a published GitHub Release and its verified assets."""
import argparse
import json
from pathlib import Path
import re

from prepare_builtin_dtw import PIN, ROOT, TARGETS, inspect_package, obtain, verify_release

REPOSITORY = 'caelis-labs/desktop-world'


def stable_version(tag):
    if not re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', tag):
        raise ValueError(f'expected a stable DTW tag, got {tag!r}')
    return tuple(map(int, tag[1:].split('.')))


def published_digest(release, name):
    matches = [asset for asset in release['assets'] if asset['name'] == name]
    if len(matches) != 1 or matches[0]['state'] != 'uploaded':
        raise ValueError(f'expected one uploaded GitHub asset named {name}')
    digest = matches[0].get('digest') or ''
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', digest):
        raise ValueError(f'missing GitHub SHA-256 for {name}')
    return digest.removeprefix('sha256:')


def update_pin(current, release, cache):
    tag = release['tag_name']
    if release['draft'] or release['prerelease'] or not release['published_at']:
        raise ValueError('expected a published, stable DTW release')
    if stable_version(tag) <= stable_version(current['tag']):
        return None
    version = tag[1:]
    base_url = f'https://github.com/{REPOSITORY}/releases/download/{tag}'
    release_digest = published_digest(release, 'RELEASE.json')
    data = obtain(cache, f'RELEASE-{tag}.json', release_digest, f'{base_url}/RELEASE.json')
    formal = json.loads(data)
    if formal['schema'] != 1 or formal['tag'] != tag or formal['version'] != version:
        raise ValueError('formal RELEASE identity mismatch')

    packages = formal['platforms']
    by_target = {package['target']: package for package in packages}
    if len(by_target) != len(packages) or not set(TARGETS.values()) <= by_target.keys():
        raise ValueError('formal RELEASE must contain one package per supported target')

    pin = {'schema': 1, 'repository': REPOSITORY, 'version': version, 'tag': tag, 'platforms': []}
    for go_target, target in TARGETS.items():
        package = by_target[target]
        suffix = '.tar.gz' if go_target.startswith('linux/') else '.zip'
        name = f'desktop-world-{version}-{target}{suffix}'
        digest = published_digest(release, name)
        if package['archive'] != name or package['archive_sha256'] != digest:
            raise ValueError(f'formal RELEASE and GitHub archive identity differ for {target}')
        source = package['source']
        if source['commit'] != formal['tag_commit']:
            raise ValueError(f'formal RELEASE tag and package source differ for {target}')
        platform = {key: package[key] for key in ('target', 'archive', 'archive_sha256', 'binary_sha256', 'source')}
        archive = obtain(cache, name, digest, f'{base_url}/{name}')
        checksums, _ = inspect_package(pin, platform, archive)
        if checksums['binary_sha256'] != platform['binary_sha256']:
            raise ValueError(f'formal RELEASE binary checksum mismatch for {target}')
        platform['skill_sha256'] = checksums['skill_sha256']
        platform['build_sha256'] = checksums['build_sha256']
        pin['platforms'].append(platform)

    pin['release_sha256'] = release_digest
    verify_release(pin, data)
    return pin


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('release', type=Path, help='JSON from the official GitHub releases/latest API')
    parser.add_argument('--cache-dir', type=Path, default=ROOT / '.tmp/cache/builtin-dtw')
    args = parser.parse_args()
    current = json.loads(PIN.read_text())
    pin = update_pin(current, json.loads(args.release.read_text()), args.cache_dir)
    if pin is None:
        print(f'DTW pin {current["tag"]} is current')
        return
    PIN.write_text(json.dumps(pin, indent=2) + '\n')
    print(f'Updated DTW pin {current["tag"]} to {pin["tag"]}')


if __name__ == '__main__':
    main()
