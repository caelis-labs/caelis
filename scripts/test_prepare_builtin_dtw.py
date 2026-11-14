"""Package integrity and release-executable checks without network or native input."""
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
import zipfile

spec = importlib.util.spec_from_file_location('prepare_builtin_dtw', Path(__file__).with_name('prepare_builtin_dtw.py'))
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)


def fixture(extra=None, name='desktop-world', identity='desktop-world', target='test-target', skill_name=None, skill_identity=None):
    source = {'commit': 'a' * 40, 'tree': 'b' * 40, 'cargo_lock_sha256': 'c' * 64}
    build = json.dumps({'version': '0.2.3', 'target': target, 'source': source}).encode()
    binary = 'bin/dtw.exe' if 'windows' in target else 'bin/dtw'
    skill = f'skills/{skill_name or identity}/SKILL.md'
    skill_data = f'---\nname: {skill_identity or identity}\ndescription: Complete Skill fixture\n---\nSkill body\n'.encode()
    files = {'BUILD.json': build, 'plugin.json': json.dumps({'name': name, 'version': '0.2.3'}).encode(),
             binary: b'native binary fixture', skill: skill_data,
             'LICENSE': b'license', 'THIRD_PARTY_NOTICES.md': b'notices',
             '.mcp.json': json.dumps({'mcpServers': {identity: {'command': '${CLAUDE_PLUGIN_ROOT}/' + binary, 'args': ['mcp']}}}).encode()}
    files.update(extra or {})
    out = io.BytesIO()
    with zipfile.ZipFile(out, 'w') as archive:
        for key, value in files.items():
            archive.writestr(f'desktop-world-0.2.3-{target}/' + key, value)
    data = out.getvalue()
    platform = {'target': target, 'archive': f'desktop-world-0.2.3-{target}.zip', 'source': source,
                'archive_sha256': prepare.sha(data), 'binary_sha256': prepare.sha(files[binary]),
                'skill_sha256': prepare.sha(files[skill]), 'build_sha256': prepare.sha(build)}
    return {'version': '0.2.3'}, platform, data


class PrepareBuiltinTests(unittest.TestCase):
    def test_complete_package_and_target_provenance(self):
        pin, target, data = fixture()
        self.assertEqual(prepare.verify(pin, target, data)['files'], 7)
        target['source'] = dict(target['source'], commit='d' * 40)
        with self.assertRaisesRegex(ValueError, 'provenance'):
            prepare.verify(pin, target, data)

    def test_coherent_identities_on_both_platforms_without_version_threshold(self):
        for identity in ('desktop-world', 'dtw'):
            for target in ('aarch64-apple-darwin', 'x86_64-pc-windows-msvc'):
                with self.subTest(identity=identity, target=target):
                    pin, platform, data = fixture(identity=identity, target=target)
                    evidence = prepare.verify(pin, platform, data)
                    self.assertEqual(evidence['id'], 'desktop-world')
                    self.assertEqual(evidence['target'], target)

    def test_mixed_server_and_skill_locations_are_rejected(self):
        for identity, skill in (('dtw', 'desktop-world'), ('desktop-world', 'dtw')):
            with self.subTest(identity=identity):
                pin, platform, data = fixture(identity=identity, skill_name=skill)
                with self.assertRaisesRegex(ValueError, 'Skill location'):
                    prepare.verify(pin, platform, data)

    def test_two_server_or_skill_identities_are_rejected(self):
        launch = {'command': '${CLAUDE_PLUGIN_ROOT}/bin/dtw', 'args': ['mcp']}
        extras = [
            {'.mcp.json': json.dumps({'mcpServers': {name: launch for name in ('dtw', 'desktop-world')}}).encode()},
            {'skills/desktop-world/SKILL.md': b'---\nname: desktop-world\n---\n'},
        ]
        for extra in extras:
            with self.subTest(extra=extra):
                pin, platform, data = fixture(extra, identity='dtw')
                with self.assertRaisesRegex(ValueError, 'identity|location'):
                    prepare.verify(pin, platform, data)

    def test_unknown_server_identity_is_rejected(self):
        pin, platform, data = fixture(identity='other')
        with self.assertRaisesRegex(ValueError, 'server identity'):
            prepare.verify(pin, platform, data)

    def test_matching_checksum_cannot_hide_wrong_skill_metadata(self):
        for identity, metadata in (('dtw', 'desktop-world'), ('desktop-world', 'dtw'), ('dtw', 'desktop-world:dtw')):
            with self.subTest(identity=identity, metadata=metadata):
                pin, platform, data = fixture(identity=identity, skill_identity=metadata)
                with self.assertRaisesRegex(ValueError, 'Skill identity'):
                    prepare.verify(pin, platform, data)

    def test_skill_requires_one_literal_frontmatter_name(self):
        for content in (b'name: dtw\n', b'---\nname: dtw\n', b'---\ndescription: dtw\n---\n',
                        b'---\nname: dtw\nname: other\n---\n', b'---\nname: >\n  dtw\n---\n'):
            with self.subTest(content=content):
                pin, platform, data = fixture({'skills/dtw/SKILL.md': content}, identity='dtw')
                with self.assertRaisesRegex(ValueError, 'Skill identity'):
                    prepare.verify(pin, platform, data)
        for name in ('dtw', "'dtw'", '"dtw"'):
            with self.subTest(name=name):
                pin, platform, data = fixture(identity='dtw', skill_identity=name)
                prepare.verify(pin, platform, data)

    def test_windows_launch_must_use_exe_and_mcp_argument(self):
        for launch in ({'command': '${CLAUDE_PLUGIN_ROOT}/bin/dtw', 'args': ['mcp']},
                       {'command': '${CLAUDE_PLUGIN_ROOT}/bin/dtw.exe', 'args': []}):
            with self.subTest(launch=launch):
                pin, platform, data = fixture({'.mcp.json': json.dumps({'mcpServers': {'dtw': launch}}).encode()},
                                              identity='dtw', target='x86_64-pc-windows-msvc')
                with self.assertRaisesRegex(ValueError, 'launch contract'):
                    prepare.verify(pin, platform, data)

    def test_tamper_is_not_accepted_as_a_valid_cached_asset(self):
        pin, target, data = fixture()
        with self.assertRaisesRegex(ValueError, 'archive checksum'):
            prepare.verify(pin, target, data + b'altered')

    def test_correct_zip_checksum_does_not_hide_wrong_identity(self):
        pin, target, data = fixture(name='other')
        with self.assertRaisesRegex(ValueError, 'identity'):
            prepare.verify(pin, target, data)

    def test_parent_traversal_cannot_escape_a_pinned_package(self):
        pin, target, data = fixture({'../outside': b'bad'})
        with self.assertRaisesRegex(ValueError, 'unsafe'):
            prepare.verify(pin, target, data)

    def test_binary_and_skill_are_independently_checked(self):
        pin, target, data = fixture()
        for key in ('skill_sha256', 'binary_sha256', 'build_sha256'):
            bad = dict(target, **{key: '0' * 64})
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, 'checksum'):
                prepare.verify(pin, bad, data)

    def test_formal_promotion_must_match_every_platform_source_and_hash(self):
        pin, platform, _ = fixture()
        release = {'version':pin['version'], 'tag':'v'+pin['version'], 'platforms':[platform]}
        data = json.dumps(release).encode()
        pin.update(tag=release['tag'], platforms=[dict(platform)], release_sha256=prepare.sha(data))
        prepare.verify_release(pin, data)
        pin['platforms'][0]['binary_sha256'] = '0'*64
        with self.assertRaisesRegex(ValueError, 'formal RELEASE binary_sha256'):
            prepare.verify_release(pin, data)
        with self.assertRaisesRegex(ValueError, 'RELEASE checksum'):
            prepare.verify_release(pin, data+b'changed')

    def test_verified_cache_requires_no_network(self):
        _, target, data = fixture()
        with tempfile.TemporaryDirectory() as root:
            cache = Path(root)
            (cache / target['archive']).write_bytes(data)
            self.assertEqual(prepare.obtain(cache, target['archive'], target['archive_sha256'], 'invalid://network-must-not-be-used'), data)


if __name__ == '__main__':
    unittest.main()
