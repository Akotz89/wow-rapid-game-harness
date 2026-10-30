"""Run production bootstrap against small isolated synthetic Git repositories."""
import os
import importlib.util
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

BASE = Path(__file__).resolve().parent
PIN = '34b756fce55ed82decb737bd9b808d7aec4e537b'


def git(root, *args):
    return subprocess.check_output(['git', '-C', str(root), *args], stderr=subprocess.DEVNULL)


def original_patch_file(patch):
    lines = []
    active = False
    for line in patch.read_text().splitlines():
        match = re.match(r'@@ -(\d+)', line)
        if match:
            while len(lines) < int(match[1]) - 1:
                lines.append('')
            active = True
        elif active and line[:1] in (' ', '-'):
            lines.append(line[1:])
    return '\n'.join(lines) + '\n'


class DependencyTests(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        self.addCleanup(self.folder.cleanup)
        self.root = Path(self.folder.name)
        self.base = self.root / 'tools/rapid-game-harness'
        self.base.mkdir(parents=True)
        source = self.root / 'work/rapid-game-harness-20261012/azerothghost'
        source.mkdir(parents=True)
        git(source, 'init', '--quiet')
        for patch, name in (('auth-os.patch', 'client/auth.go'), ('session-exact-name.patch', 'e2e/e2eharness/session.go'),
                            ('world-transport.patch', 'client/world.go'), ('client-session-phase.patch', 'client/session.go')):
            path = source / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(original_patch_file(BASE / patch))
            shutil.copyfile(BASE / patch, self.base / patch)
        with (source / 'client/world.go').open('a') as world:
            world.write('// $Format:%H$\n')
        executable = source / 'helper.sh'
        executable.write_text('#!/bin/sh\nexit 0\n')
        executable.chmod(0o755)
        (source / 'world-link').symlink_to('client/world.go')
        git(source, 'add', '.')
        git(source, '-c', 'user.name=Synthetic', '-c', 'user.email=synthetic@example.invalid', 'commit', '--quiet', '-m', 'Synthetic dependency')
        self.revision = git(source, 'rev-parse', 'HEAD').decode().strip()
        (self.base / 'bootstrap').write_text((BASE / 'bootstrap').read_text().replace(PIN, self.revision))
        (self.base / 'bootstrap').chmod(0o700)
        shutil.copyfile(BASE / 'verify_dependency.py', self.base / 'verify_dependency.py')
        scripts = self.root / 'scripts'
        scripts.mkdir()
        guard = scripts / 'wow-build-host-guard'
        guard.write_text('#!/bin/sh\nexit 0\n')
        guard.chmod(0o700)
        self.dep = self.base / '.deps/azerothghost'
        self.assertEqual(self.bootstrap().returncode, 0)

    def bootstrap(self):
        return subprocess.run([str(self.base / 'bootstrap')], capture_output=True, text=True, env=dict(os.environ, GIT_CONFIG_GLOBAL='/dev/null', GIT_CONFIG_NOSYSTEM='1'))

    def test_exact_pinned_and_patched_inputs_pass_repeatedly(self):
        self.assertEqual(self.bootstrap().returncode, 0)

    def test_world_raw_publication_and_tampered_or_partial_input(self):
        path = self.dep / 'client/world.go'
        approved = path.read_bytes()
        original = git(self.dep, 'show', self.revision + ':client/world.go')
        path.write_bytes(original)
        self.assertEqual(self.bootstrap().returncode, 0)
        self.assertEqual(path.read_bytes(), approved)
        self.assertEqual(self.bootstrap().returncode, 0)
        for content in (approved + b'// tampered world\n',
                        approved.replace(b'4 * time.Second', b'3 * time.Second', 1),
                        approved.replace(b'return w.writePacketLocked(append(header, data...))',
                                         b'_, err := w.conn.Write(append(header, data...))\n\treturn err', 1)):
            with self.subTest(content=content[-40:]):
                self.assertNotEqual(content, approved)
                path.write_bytes(content)
                self.assertNotEqual(self.bootstrap().returncode, 0)
                self.assertEqual(path.read_bytes(), content)
        path.write_bytes(approved)

    def test_session_raw_publication_and_tampered_or_partial_input(self):
        for name in ('client/session.go', 'e2e/e2eharness/session.go'):
            with self.subTest(name=name):
                path = self.dep / name
                approved = path.read_bytes()
                original = git(self.dep, 'show', self.revision + ':' + name)
                path.write_bytes(original)
                self.assertEqual(self.bootstrap().returncode, 0)
                self.assertEqual(path.read_bytes(), approved)
                for content in (approved + b'// tampered session\n',
                                approved.replace(b'w.StopError()', b'error(nil)', 1),
                                approved.replace(b'return nil', b'return error(nil)', 1)):
                    self.assertNotEqual(content, approved)
                    self.assertNotEqual(content, original)
                    path.write_bytes(content)
                    self.assertNotEqual(self.bootstrap().returncode, 0)
                    self.assertEqual(path.read_bytes(), content)
                path.write_bytes(approved)

    def test_replace_refs_cannot_change_pinned_tree(self):
        original = git(self.dep, 'show', self.revision + ':client/world.go')
        path = self.dep / 'client/world.go'
        path.write_bytes(original + b'// replacement object\n')
        blob = git(self.dep, 'hash-object', '-w', 'client/world.go').decode().strip()
        old = git(self.dep, 'rev-parse', self.revision + ':client/world.go').decode().strip()
        git(self.dep, 'replace', old, blob)
        # The expected tree must still contain the raw original object.
        self.assertEqual(git(self.dep, 'rev-parse', 'HEAD').decode().strip(), self.revision)
        self.assertNotEqual(self.bootstrap().returncode, 0)
        self.assertTrue(path.read_bytes().endswith(b'// replacement object\n'))

    def test_export_attributes_cannot_hide_or_rewrite_inputs(self):
        attributes = self.dep / '.git/info/attributes'
        path = self.dep / 'client/world.go'
        original = path.read_bytes()
        attributes.write_text('client/world.go export-ignore\n')
        path.unlink()
        self.assertNotEqual(self.bootstrap().returncode, 0)
        path.write_bytes(original)
        self.assertEqual(self.bootstrap().returncode, 0)
        attributes.write_text('client/world.go export-subst\n')
        path.write_bytes(original.replace(b'$Format:%H$', self.revision.encode()))
        self.assertNotEqual(self.bootstrap().returncode, 0)

    def test_tracked_executable_mode_and_symlink_target(self):
        executable = self.dep / 'helper.sh'
        self.assertTrue(executable.stat().st_mode & 0o111)
        self.assertEqual(os.readlink(self.dep / 'world-link'), 'client/world.go')
        executable.chmod(0o644)
        self.assertNotEqual(self.bootstrap().returncode, 0)
        executable.chmod(0o755)
        self.assertEqual(self.bootstrap().returncode, 0)
        (self.dep / 'world-link').unlink()
        (self.dep / 'world-link').symlink_to('client/auth.go')
        self.assertNotEqual(self.bootstrap().returncode, 0)
        self.assertEqual(os.readlink(self.dep / 'world-link'), 'client/auth.go')

    def test_failed_patch_publication_preserves_original_and_retries(self):
        spec = importlib.util.spec_from_file_location('verify_dependency', BASE / 'verify_dependency.py')
        verifier = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(verifier)
        for name in ('client/auth.go', 'client/world.go', 'client/session.go', 'e2e/e2eharness/session.go'):
            with self.subTest(name=name):
                expected = self.root / ('expected-' + name.replace('/', '-'))
                expected.mkdir()
                verifier.materialize(self.dep, expected, self.revision)
                git(expected, 'init', '--quiet')
                git(expected, 'apply', str(BASE / 'auth-os.patch'), str(BASE / 'session-exact-name.patch'),
                    str(BASE / 'world-transport.patch'), str(BASE / 'client-session-phase.patch'))
                path = self.dep / name
                original = git(self.dep, 'show', self.revision + ':' + name)
                path.write_bytes(original)
                with patch.object(verifier.os, 'replace', side_effect=OSError('synthetic publication')):
                    with self.assertRaises(OSError):
                        verifier.verify(self.dep, expected, self.revision)
                self.assertEqual(path.read_bytes(), original)
                verifier.verify(self.dep, expected, self.revision)
                self.assertNotEqual(path.read_bytes(), original)
                self.assertEqual(self.bootstrap().returncode, 0)

    def test_ignored_input_is_rejected_and_preserved(self):
        path = self.dep / 'client/hidden.go'
        path.write_text('package client\n')
        (self.dep / '.git/info/exclude').write_text('client/hidden.go\n')
        self.assertNotEqual(self.bootstrap().returncode, 0)
        self.assertEqual(path.read_text(), 'package client\n')

    def test_index_flags_cannot_hide_modified_bytes(self):
        path = self.dep / 'client/world.go'
        original = path.read_text()
        for flag in ('--assume-unchanged', '--skip-worktree'):
            with self.subTest(flag=flag):
                git(self.dep, 'update-index', '--no-assume-unchanged', '--no-skip-worktree', 'client/world.go')
                path.write_text(original)
                git(self.dep, 'update-index', flag, 'client/world.go')
                path.write_text(original + '// synthetic hidden modification\n')
                self.assertNotEqual(self.bootstrap().returncode, 0)
                self.assertTrue(path.read_text().endswith('// synthetic hidden modification\n'))

    def test_untracked_ignored_directory_and_symlink_rejected(self):
        path = self.dep / 'ignored/generated.go'
        path.parent.mkdir()
        path.write_text('package generated\n')
        (self.dep / '.git/info/exclude').write_text('ignored/\n')
        self.assertNotEqual(self.bootstrap().returncode, 0)
        path.unlink()
        path.parent.rmdir()
        tracked = self.dep / 'client/world.go'
        tracked.unlink()
        tracked.symlink_to('/dev/null')
        self.assertNotEqual(self.bootstrap().returncode, 0)
        self.assertTrue(tracked.is_symlink())


if __name__ == '__main__':
    unittest.main()
