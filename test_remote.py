import concurrent.futures
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('remote', Path(__file__).with_name('remote.py'))
remote = importlib.util.module_from_spec(spec)
spec.loader.exec_module(remote)


class RemoteTests(unittest.TestCase):
    def test_invalid_names_rejected_before_ssh(self):
        for name in ('Yuna', 'Rhfoo;id', 'Rh../foo', 'Rhfoo\n', ''):
            with self.subTest(name=name), patch('sys.argv', ['remote', 'observe', name]), patch.object(remote.subprocess, 'run') as run:
                with self.assertRaises(ValueError):
                    remote.main()
                run.assert_not_called()

    def test_prepare_requires_private_path_before_network(self):
        with patch('sys.argv', ['remote', 'prepare', '/tmp/fixture.json']), patch.object(remote.subprocess, 'check_output') as network:
            with self.assertRaises(ValueError):
                remote.main()
            network.assert_not_called()

    def test_symlink_or_public_secret_destination_rejected(self):
        with tempfile.TemporaryDirectory() as folder, patch.object(remote, 'ROOT', Path(folder)):
            private = Path(folder) / 'work/rapid-game-harness-20261012/private'
            private.mkdir(parents=True, mode=0o700)
            target = private / 'fixture.json'
            target.write_text('{}')
            target.chmod(0o644)
            with self.assertRaises(ValueError):
                remote.private_destination(str(target))
            target.chmod(0o400)
            with self.assertRaises(ValueError):
                remote.private_destination(str(target))
            target.chmod(0o600)
            private.chmod(0o500)
            with self.assertRaises(ValueError):
                remote.private_destination(str(target))
            private.chmod(0o700)
            target.unlink()
            target.symlink_to(Path(folder) / 'other')
            with self.assertRaises(ValueError):
                remote.private_destination(str(target))

    def test_prepare_keeps_secret_off_stdout_and_mode_private(self):
        secret = 'CANARY_PASSWORD_NEVER_PRINT'
        fixture = dict(auth_addr='localhost:3724', username='RH000000000001', password=secret, character='Rhabcdefghij')
        with tempfile.TemporaryDirectory() as folder, patch.object(remote, 'ROOT', Path(folder)):
            destination = Path(folder) / 'work/rapid-game-harness-20261012/private/fixture.json'
            with patch('sys.argv', ['remote', 'prepare', str(destination)]), patch.object(remote.subprocess, 'check_output', return_value='hostname localhost\n'), patch.object(remote.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, json.dumps(fixture), '')), contextlib.redirect_stdout(io.StringIO()) as output:
                remote.main()
            self.assertNotIn(secret, output.getvalue())
            self.assertEqual(destination.stat().st_mode & 0o777, 0o600)
            self.assertEqual(destination.parent.stat().st_mode & 0o777, 0o700)
            self.assertEqual(json.loads(destination.read_text()), fixture)

    def test_reservation_failure_precedes_ssh(self):
        with tempfile.TemporaryDirectory() as folder, patch.object(remote, 'ROOT', Path(folder)):
            destination = Path(folder) / 'work/rapid-game-harness-20261012/private/fixture.json'
            with patch('sys.argv', ['remote', 'prepare', str(destination)]), patch.object(remote.tempfile, 'mkstemp', side_effect=PermissionError), patch.object(remote.subprocess, 'check_output') as network:
                with self.assertRaises(PermissionError):
                    remote.main()
                network.assert_not_called()

    def test_remote_errors_never_relay_private_body(self):
        with patch('sys.argv', ['remote', 'status']), patch.object(remote.subprocess, 'check_output', return_value='hostname localhost\n'), patch.object(remote.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, '', 'secret password')):
            with self.assertRaises(RuntimeError) as error:
                remote.main()
            self.assertNotIn('secret password', str(error.exception))

    def test_remote_program_compiles(self):
        compile(remote.TRANSPORT.read_text() + '\n' + remote.REMOTE, '<remote>', 'exec')

    def test_snapshot_mismatch_rejected_before_ssh(self):
        # The private snapshot cannot be swapped between Go validation and remote ownership verification.
        fixture = dict(username='RH000000000001', password='LOCAL-TEST', character='Rhfixture', auth_addr='localhost:3724')
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'fixture.json'
            path.write_text(json.dumps(fixture))
            path.chmod(0o600)
            with patch('sys.argv', ['remote', 'verify', str(path), '0'*64]), patch('subprocess.check_output') as network:
                with self.assertRaises(ValueError):
                    remote.main()
                network.assert_not_called()


class RemoteProgramTests(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        self.addCleanup(self.folder.cleanup)
        base = Path(self.folder.name)
        self.private = base / 'private'
        self.private.mkdir(mode=0o700)
        self.manifest = self.private / 'fixture.json'
        self.fixture = dict(username='RH000000000001', password='LOCAL-TEST', character='Rhabcdefghij', auth_addr='localhost:3724', account_id=11)
        self.account_row = '11\t0\n'
        self.character_row = '42\t11\tRhabcdefghij\n'
        self.mutations = []
        self.sql_calls = []
        self.data = dict(name='Rhabcdefghij', guid=42, account_id=11, security_level=0, map_id=0, position=dict(x=1, y=2, z=3), level=1, health=dict(current=50), power=dict(current=100), online=True)
        release = base / 'release'
        (release / 'etc/modules').mkdir(parents=True)
        (release / 'etc/modules/gamestate_api.conf').write_text('GameStateAPI.Enable = 1\nGameStateAPI.Host = "127.0.0.1"\nGameStateAPI.Port = 8080\nGameStateAPI.AuthToken = "test-only-token"\n')
        self.program = compile(remote.REMOTE.replace('/srv/wow/work/rapid-game-harness/private', str(self.private)), '<remote-test>', 'exec')
        for mock in (patch('subprocess.check_output', return_value='123'), patch('os.readlink', return_value=str(release / 'bin/worldserver')), patch('subprocess.run', side_effect=self.sql), patch('urllib.request.urlopen', side_effect=lambda *a, **k: io.StringIO(json.dumps(self.data)))):
            mock.start()
            self.addCleanup(mock.stop)

    def sql(self, args, **kwargs):
        self.sql_calls.append(args)
        row = self.character_row if args[-2] == 'fixture-character-read-only' else self.account_row
        return subprocess.CompletedProcess(args, 0, row, '')

    def soap(self, command, credentials):
        # The authoritative identity must be complete before the first mutation.
        recorded = json.loads(self.manifest.read_text())
        self.assertIn(recorded['username'], command)
        self.mutations.append(command)
        self.account_row = '11\t0\n'
        return {'ok': True}

    def write_fixture(self, **updates):
        self.fixture.update(updates)
        self.manifest.write_text(json.dumps(self.fixture))
        self.manifest.chmod(0o600)

    def execute(self, mode='verify', expected=None):
        if expected is None:
            expected = {key: self.fixture[key] for key in ('auth_addr', 'username', 'password', 'character')}
        value = self.fixture['character'] if mode == 'observe' else 'localhost'
        with patch('sys.argv', ['remote', mode, value]), contextlib.redirect_stdout(io.StringIO()) as output:
            exec(self.program, dict(expected=expected, soap_command=self.soap, read_credentials=lambda _: {}))
        return json.loads(output.getvalue())

    def test_recorded_account_loss_rejected_without_mutation(self):
        self.write_fixture(character_guid=42)
        original = self.manifest.read_bytes()
        for row in ('', '12\t0\n', '11\t3\n'):
            for mode in ('prepare', 'verify', 'observe'):
                with self.subTest(row=row, mode=mode):
                    self.account_row = row
                    with self.assertRaises(RuntimeError):
                        self.execute(mode)
                    self.assertEqual(self.mutations, [])
                    self.assertEqual(self.manifest.read_bytes(), original)

    def test_legacy_binding_and_bound_character_loss(self):
        self.write_fixture()
        self.assertTrue(self.execute()['verified'])
        self.assertEqual(json.loads(self.manifest.read_text())['character_guid'], 42)
        for row in ('', '99\t11\tRhabcdefghij\n', '42\t12\tRhabcdefghij\n', '42\t11\tRhothername\n'):
            for mode in ('prepare', 'verify', 'observe'):
                with self.subTest(row=row, mode=mode):
                    self.character_row = row
                    with self.assertRaises(RuntimeError):
                        self.execute(mode)
                    self.assertEqual(self.mutations, [])
                    self.assertEqual(json.loads(self.manifest.read_text())['character_guid'], 42)

    def test_legacy_missing_character_never_gets_creation_permission(self):
        self.write_fixture()
        self.character_row = ''
        for mode in ('prepare', 'verify'):
            with self.assertRaises(RuntimeError):
                self.execute(mode)
            self.assertNotIn('character_create_pending', json.loads(self.manifest.read_text()))
        self.assertEqual(self.mutations, [])

    def test_first_creation_and_unknown_outcomes_bind_once(self):
        self.account_row = self.character_row = ''
        self.execute('prepare')
        fixture = json.loads(self.manifest.read_text())
        self.assertTrue(fixture['character_create_pending'])
        self.assertEqual(len(self.mutations), 1)
        self.fixture = fixture
        self.assertTrue(self.execute()['verified'])
        # An unknown initial character creation is reconciled from exact native SQL.
        self.character_row = '42\t11\t' + fixture['character'] + '\n'
        self.assertTrue(self.execute()['verified'])
        bound = json.loads(self.manifest.read_text())
        self.assertEqual(bound['character_guid'], 42)
        self.assertNotIn('character_create_pending', bound)
        self.character_row = ''
        with self.assertRaises(RuntimeError):
            self.execute('prepare')
        self.assertEqual(len(self.mutations), 1)

    def test_unknown_initial_account_creation_reuses_recorded_identity(self):
        self.write_fixture(character_create_pending=True)
        del self.fixture['account_id']
        self.write_fixture()
        self.character_row = ''
        self.execute('prepare')
        self.assertEqual(self.mutations, [])
        self.assertEqual(json.loads(self.manifest.read_text())['account_id'], 11)

    def test_observer_requires_bound_sql_and_api_guid(self):
        self.write_fixture(character_guid=42)
        self.assertEqual(self.execute('observe')['guid'], 42)
        for key, value in (('guid', 99), ('account_id', 12), ('security_level', 3), ('name', 'Rhforeign')):
            with self.subTest(key=key), patch.dict(self.data, {key: value}):
                with self.assertRaises(RuntimeError):
                    self.execute('observe')

    def test_first_publication_failures_never_mutate_and_retry(self):
        self.account_row = self.character_row = ''
        def partial(value, output):
            output.write('{')
            raise OSError('synthetic short write')
        for target, replacement in (('json.dump', partial), ('os.fsync', OSError('synthetic fsync')), ('os.replace', OSError('synthetic publication'))):
            with self.subTest(target=target):
                self.manifest.unlink(missing_ok=True)
                self.account_row = ''
                with patch(target, side_effect=replacement), self.assertRaises(OSError):
                    self.execute('prepare')
                self.assertEqual(self.mutations, [])
                self.assertFalse(self.manifest.exists())
                self.assertEqual(list(self.private.glob('.fixture-*')), [])
        real_fsync = __import__('os').fsync
        calls = 0
        def directory_failure(fd):
            nonlocal calls
            calls += 1
            if calls == 3:
                raise OSError('synthetic directory fsync')
            return real_fsync(fd)
        with patch('os.fsync', side_effect=directory_failure), self.assertRaises(OSError):
            self.execute('prepare')
        recorded = json.loads(self.manifest.read_text())
        self.assertEqual(self.mutations, [])
        self.execute('prepare')
        self.assertEqual(json.loads(self.manifest.read_text())['username'], recorded['username'])
        self.assertEqual(len(self.mutations), 1)

    def test_invalid_bound_identifiers_never_verify_or_create(self):
        self.account_row = self.character_row = ''
        for key in ('account_id', 'character_guid'):
            for value in (None, False, True, 0, -1, '42'):
                for mode in ('prepare', 'verify', 'observe'):
                    with self.subTest(key=key, value=value, mode=mode):
                        self.fixture.update(account_id=11, character_guid=42)
                        self.write_fixture(**{key: value})
                        original = self.manifest.read_bytes()
                        with self.assertRaises(RuntimeError):
                            self.execute(mode)
                        self.assertEqual(self.manifest.read_bytes(), original)
                        self.assertEqual(self.mutations, [])
                        self.assertEqual(self.sql_calls, [])

    def test_bound_character_without_account_rejected_in_every_mode(self):
        self.fixture.pop('account_id')
        self.write_fixture(character_guid=42)
        self.account_row = self.character_row = ''
        original = self.manifest.read_bytes()
        for mode in ('prepare', 'verify', 'observe'):
            with self.subTest(mode=mode):
                with self.assertRaisesRegex(RuntimeError, 'account identity missing'):
                    self.execute(mode)
                self.assertEqual(self.manifest.read_bytes(), original)
                self.assertEqual(self.sql_calls, [])
                self.assertEqual(self.mutations, [])

    def test_remote_expected_config_mismatch_precedes_sql(self):
        self.write_fixture(character_guid=42)
        original = self.manifest.read_bytes()
        expected = {key: self.fixture[key] for key in ('auth_addr', 'username', 'password', 'character')}
        alternatives = dict(auth_addr='otherhost:3724', username='RH000000000002', password='OTHER-LOCAL', character='Rhothernamed')
        for key, value in alternatives.items():
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                self.execute(expected=dict(expected, **{key: value}))
            self.assertEqual(self.sql_calls, [])
            self.assertEqual(self.mutations, [])
            self.assertEqual(self.manifest.read_bytes(), original)

    def test_new_private_directory_parent_fsync_failure_precedes_mutation(self):
        self.private.rmdir()
        self.account_row = self.character_row = ''
        real_fsync = os.fsync
        parent_inode = self.private.parent.stat().st_ino
        def fail_new_entry(fd):
            if self.private.exists() and os.fstat(fd).st_ino == parent_inode:
                raise OSError('synthetic mkdir parent fsync')
            return real_fsync(fd)
        with patch('os.fsync', side_effect=fail_new_entry), self.assertRaises(OSError):
            self.execute('prepare')
        self.assertTrue(self.private.exists())
        self.assertFalse(self.manifest.exists())
        self.assertEqual(self.mutations, [])
        self.assertEqual(self.sql_calls, [])
        self.execute('prepare')
        self.assertEqual(len(self.mutations), 1)

    def test_private_objects_reject_nonregular_types_and_public_modes(self):
        self.write_fixture(character_guid=42)
        lock = self.private / '.fixture.lock'
        for path in (self.manifest, lock):
            original = self.manifest.read_bytes()
            for kind in ('fifo', 'directory', 'symlink', 'public'):
                with self.subTest(path=path.name, kind=kind):
                    path.unlink(missing_ok=True)
                    if kind == 'fifo':
                        os.mkfifo(path, 0o600)
                    elif kind == 'directory':
                        path.mkdir(mode=0o700)
                    elif kind == 'symlink':
                        path.symlink_to(self.private / 'absent')
                    else:
                        path.write_bytes(original if path == self.manifest else b'')
                        path.chmod(0o644)
                    started = __import__('time').monotonic()
                    with self.assertRaises((RuntimeError, OSError)):
                        self.execute('prepare')
                    self.assertLess(__import__('time').monotonic() - started, 1)
                    self.assertEqual(self.mutations, [])
                    self.assertEqual(self.sql_calls, [])
                    if kind == 'directory':
                        path.rmdir()
                    else:
                        path.unlink()
                    if path == self.manifest:
                        path.write_bytes(original)
                        path.chmod(0o600)

    def test_private_objects_reject_foreign_owner_and_oversize_manifest(self):
        self.write_fixture(character_guid=42)
        lock = self.private / '.fixture.lock'
        lock.write_text('')
        lock.chmod(0o600)
        real_fstat = os.fstat
        for path in (self.manifest, lock):
            inode = path.stat().st_ino
            def foreign_owner(fd):
                info = real_fstat(fd)
                if info.st_ino == inode:
                    fields = list(info)
                    fields[4] = os.geteuid() + 1
                    return os.stat_result(fields)
                return info
            with self.subTest(path=path.name), patch('os.fstat', side_effect=foreign_owner):
                with self.assertRaises(RuntimeError):
                    self.execute('prepare')
            self.assertEqual(self.sql_calls, [])
            self.assertEqual(self.mutations, [])
        self.manifest.write_bytes(b' ' * 4097)
        with self.assertRaises(RuntimeError):
            self.execute('prepare')
        self.assertEqual(self.sql_calls, [])
        self.assertEqual(self.mutations, [])

    def test_initial_flush_failure_leaves_no_manifest_or_mutation(self):
        self.account_row = self.character_row = ''
        real_fdopen = __import__('os').fdopen
        class BrokenFlush:
            def __init__(self, fd, mode):
                self.output = real_fdopen(fd, mode)
            def __enter__(self):
                return self
            def __exit__(self, *args):
                self.output.close()
            def write(self, value):
                return self.output.write(value)
            def flush(self):
                raise OSError('synthetic flush')
        with patch('os.fdopen', side_effect=BrokenFlush), self.assertRaises(OSError):
            self.execute('prepare')
        self.assertFalse(self.manifest.exists())
        self.assertEqual(self.mutations, [])
        self.execute('prepare')
        self.assertEqual(len(self.mutations), 1)

    def test_contended_remote_lock_has_bounded_rejection(self):
        self.write_fixture(character_guid=42)
        original = self.manifest.read_bytes()
        with patch('fcntl.flock', side_effect=BlockingIOError), patch('time.monotonic', side_effect=[0, 6]):
            with self.assertRaisesRegex(RuntimeError, 'busy'):
                self.execute('prepare')
        self.assertEqual(self.mutations, [])
        self.assertEqual(self.manifest.read_bytes(), original)

    def test_concurrent_initial_prepares_share_one_identity(self):
        self.account_row = self.character_row = ''
        def prepare(_):
            exec(self.program, dict(soap_command=self.soap, read_credentials=lambda _: {}))
        with patch('sys.argv', ['remote', 'prepare', 'localhost']), contextlib.redirect_stdout(io.StringIO()) as output:
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as workers:
                list(workers.map(prepare, range(2)))
        records = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual(records[0], records[1])
        self.assertEqual(len(self.mutations), 1)
        self.assertEqual(json.loads(self.manifest.read_text())['username'], records[0]['username'])


if __name__ == '__main__':
    unittest.main()
