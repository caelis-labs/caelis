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


def fixture(extra=None, name='desktop-world'):
    source = {'commit': 'a' * 40, 'tree': 'b' * 40, 'cargo_lock_sha256': 'c' * 64}
    build = json.dumps({'version': '0.2.3', 'target': 'test-target', 'source': source}).encode()
    files = {'BUILD.json': build, 'plugin.json': json.dumps({'name': name, 'version': '0.2.3'}).encode(),
             'bin/dtw': b'native binary fixture', 'skills/desktop-world/SKILL.md': b'complete Skill fixture',
             'LICENSE': b'license', 'THIRD_PARTY_NOTICES.md': b'notices',
             '.mcp.json': json.dumps({'mcpServers': {'desktop-world': {'command': '${CLAUDE_PLUGIN_ROOT}/bin/dtw', 'args': ['mcp']}}}).encode()}
    files.update(extra or {})
    out = io.BytesIO()
    with zipfile.ZipFile(out, 'w') as archive:
        for key, value in files.items():
            archive.writestr('desktop-world-0.2.3-test-target/' + key, value)
    data = out.getvalue()
    platform = {'target': 'test-target', 'archive': 'desktop-world-0.2.3-test-target.zip', 'source': source,
                'archive_sha256': prepare.sha(data), 'binary_sha256': prepare.sha(files['bin/dtw']),
                'skill_sha256': prepare.sha(files['skills/desktop-world/SKILL.md']), 'build_sha256': prepare.sha(build)}
    return {'version': '0.2.3'}, platform, data


class PrepareBuiltinTests(unittest.TestCase):
    def test_complete_package_and_target_provenance(self):
        pin, target, data = fixture()
        self.assertEqual(prepare.verify(pin, target, data)['files'], 7)
        target['source'] = dict(target['source'], commit='d' * 40)
        with self.assertRaisesRegex(ValueError, 'provenance'):
            prepare.verify(pin, target, data)

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
