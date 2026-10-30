"""Exercise the real runner with synthetic configs, toolchain and no network."""
from concurrent.futures import ThreadPoolExecutor
import fcntl
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import time
import unittest

BASE = Path(__file__).resolve().parent
PROFILES = ('run', 'smoke')
CHILDREN = ('Login', 'ChatEcho', 'NativeMovement', 'LearnedSelfCast')
ROOTS = ('TestLiveGameplay', 'TestPinnedExactNameCreationPolicy', '')


def finish(process, timeout):
    try:
        output, _ = process.communicate(timeout=timeout)
        return output, time.monotonic()
    except BaseException:
        process.cleanup_owned()
        raise


def receipt(case, profile, fault):
    process = case.start('success', profile, fault)
    return process, finish(process, 10)[0]


class RunnerTests(unittest.TestCase):
    def isolated_case(self):
        case = RunnerTests(self._testMethodName)
        self.addCleanup(lambda: self.assertTrue(case.doCleanups(), 'Isolated runner cleanup failed'))
        case.setUp()
        return case

    def setUp(self):
        work = BASE.parents[1] / 'work/rapid-game-harness-20261012'
        work.mkdir(parents=True, exist_ok=True)
        self.folder = tempfile.TemporaryDirectory(prefix='synthetic-runner-', dir=work)
        self.addCleanup(self.folder.cleanup)
        self.root = Path(self.folder.name)
        self.base = self.root / 'tools/rapid-game-harness'
        self.task = self.root / 'work/rapid-game-harness-20261012'
        for path in (self.base, self.root / 'scripts', self.task / 'go/bin'):
            path.mkdir(parents=True)
        shutil.copyfile(BASE.parents[1] / 'scripts/wow-test', self.root / 'scripts/wow-test')
        (self.root / 'scripts/wow-test').chmod(0o700)
        for name in ('config.py', 'redact.py'):
            shutil.copyfile(BASE / name, self.base / name)
        for name in ('scripts/wow-build-host-guard', 'tools/rapid-game-harness/bootstrap'):
            path = self.root / name
            path.write_text('#!/bin/sh\nexit 0\n')
            path.chmod(0o700)
        self.fixture = dict(auth_addr='localhost:3724', username='RH000000000001', password='SNAPSHOT-SECRET', character='Rhfixture')
        self.original = self.root / 'original.json'
        self.original.write_text(json.dumps(self.fixture))
        self.original.chmod(0o600)
        self.marker = self.root / 'consumed.json'
        go = self.task / 'go/bin/go'
        go.write_text('''#!/usr/bin/env python3
import json, os, pathlib, signal, sys, time
assert signal.getsignal(signal.SIGINT) != signal.SIG_IGN
assert os.environ.get('GOENV') == 'off'
assert os.environ.get('GOFLAGS') == ''
assert os.environ.get('GOWORK') == 'off'
assert os.environ.get('GOTOOLCHAIN') == 'local'
if sys.argv[1:] == ['version']:
 print('go version go1.26.9 linux/amd64'); sys.exit()
if os.environ.get('SYNTHETIC_CHECK') == '1':
 assert sys.argv[1:] == ['test', '-modfile=' + sys.argv[2].split('=', 1)[1], '-count=1', '-timeout=110s', '-json', '.']
 print(json.dumps({'Action':'pass','Package':'wow/rapid-game-harness'})); sys.exit()
profile=os.environ['SYNTHETIC_PROFILE']
selector='^Test(LiveGameplay|PinnedExactNameCreationPolicy)$'
if profile=='smoke': selector+='/^(Login|ChatEcho|NativeMovement|LearnedSelfCast)$'
assert sys.argv[1:]==['test','-mod=readonly','-tags=live','-run',selector,'-count=1','-timeout=110s','-json','.']
path=pathlib.Path(os.environ['WOW_TEST_CONFIG'])
data=json.loads(path.read_text())
assert data['password']==os.environ['SYNTHETIC_PASSWORD']
assert path.stat().st_mode & 0o777 == 0o600
original=pathlib.Path(os.environ['SYNTHETIC_ORIGINAL'])
replacement=dict(data,password='REPLACED-SECRET',username='RH000000000002')
original.write_text(json.dumps(replacement))
assert json.loads(path.read_text())==data
marker=pathlib.Path(os.environ['SYNTHETIC_MARKER'])
mode=os.environ.get('SYNTHETIC_MODE')
if mode=='orphan':
 if os.fork():
  while True: time.sleep(.02)
 with open(os.devnull,'w') as sink:
  os.dup2(sink.fileno(),1); os.dup2(sink.fileno(),2)
 mode='stubborn'
if mode in ('cancel','stubborn'):
 def stop(*args):
  marker.write_text(json.dumps({'path':str(path),'alive_on_term':path.exists(),'pid':os.getpid()}))
  if mode=='stubborn': return
  time.sleep(.2); sys.exit(143)
 for number in (signal.SIGTERM,signal.SIGINT,signal.SIGHUP): signal.signal(number,stop)
 marker.write_text(json.dumps({'path':str(path),'pid':os.getpid()}))
 while True: time.sleep(.02)
marker.write_text(json.dumps({'path':str(path)}))
mode=os.environ.get('SYNTHETIC_MODE')
records=[{'Action':'output','Package':'wow/rapid-game-harness','Output':data['username']+' '+data['password']}]
names=['TestLiveGameplay/'+name for name in ('Login','ChatEcho','NativeMovement','LearnedSelfCast')]
if profile=='run': names.append('TestLiveGameplay/LogoutRelogPersistence')
names+=['TestLiveGameplay','TestPinnedExactNameCreationPolicy','']
records += [dict(Action='pass',Package='wow/rapid-game-harness',**({'Test':name} if name else {})) for name in names]
fault=json.loads(os.environ.get('SYNTHETIC_RESULT','null'))
if fault:
 kind,name=fault
 if kind in ('missing','skip','fail'):
  records=[r for r in records if not (r.get('Action')=='pass' and r.get('Test','')==name)]
  if kind!='missing': records.append(dict(Action=kind,Test=name,Package='wow/rapid-game-harness'))
 elif kind=='empty': records=[]
 elif kind=='malformed': records.append('malformed')
 elif kind=='nonobject': records.append([])
 elif kind=='object': records.append(name)
 elif kind=='unexpected': records.append(dict(Action='run',Test='TestLiveGameplay/'+name,Package='wow/rapid-game-harness'))
for record in records:
 print(record if record=='malformed' else json.dumps(record),flush=True)
sys.exit(7 if os.environ.get('SYNTHETIC_MODE')=='failure' else 0)
''')
        go.chmod(0o700)
        self.env = dict(os.environ, WOW_TEST_WITHIN_BOUND='', WOW_TEST_CONFIG=str(self.original), SYNTHETIC_ORIGINAL=str(self.original), SYNTHETIC_MARKER=str(self.marker))

    def start(self, mode, profile='run', fault=None):
        self.original.write_text(json.dumps(self.fixture))
        self.original.chmod(0o600)
        self.marker.unlink(missing_ok=True)
        process = subprocess.Popen([str(self.root / 'scripts/wow-test'), profile], env=dict(self.env, SYNTHETIC_MODE=mode, SYNTHETIC_PROFILE=profile, SYNTHETIC_RESULT=json.dumps(fault), SYNTHETIC_PASSWORD=self.fixture['password']), stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, start_new_session=True)
        process.owned_groups = {process.pid}
        cleaned = False
        def cleanup():
            nonlocal cleaned
            if cleaned:
                return
            # Capture direct children before cancellation, then retain their groups after exit.
            children = Path(f'/proc/{process.pid}/task/{process.pid}/children')
            try:
                pids = children.read_text().split()
            except (FileNotFoundError, ProcessLookupError):
                pids = []
            for pid in pids:
                try:
                    if os.getpgid(int(pid)) == int(pid):
                        process.owned_groups.add(int(pid))
                except ProcessLookupError:
                    pass
            if process.poll() is None:
                try:
                    process.terminate()
                except ProcessLookupError:
                    pass
            try:
                process.communicate(timeout=10)
            finally:
                for group in process.owned_groups:
                    try:
                        os.killpg(group, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                process.communicate(timeout=5)
                cleaned = True
        process.cleanup_owned = cleanup
        self.addCleanup(cleanup)
        return process

    def assert_clean(self):
        self.assertEqual(list((self.task / 'private').glob('.run-config-*')), [])
        with (self.task / 'harness.lock').open('w') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)

    def wait_for_marker(self, process):
        deadline = time.monotonic() + 5
        while not self.marker.exists() and process.poll() is None and time.monotonic() < deadline:
            time.sleep(.01)
        self.assertTrue(self.marker.exists())
        child = json.loads(self.marker.read_text())['pid']
        group = os.getpgid(child)
        self.assertNotEqual(group, os.getpgrp())
        process.owned_groups.add(group)

    def test_snapshot_survives_original_rotation_and_is_cleaned(self):
        self.env['SHELLOPTS'] = 'monitor'
        for profile in PROFILES:
            for mode in ('success', 'failure'):
                with self.subTest(profile=profile, mode=mode):
                    process = self.start(mode, profile)
                    output, _ = process.communicate(timeout=10)
                    self.assertEqual(process.returncode, 7 if mode == 'failure' else 0, output)
                    self.assertEqual('Harness coverage passed:' in output, mode == 'success', output)
                    self.assertNotIn(self.fixture['password'], output)
                    self.assertNotIn(self.fixture['username'], output)
                    self.assertIn('[REDACTED]', output)
                    for evidence in self.task.glob('run-*/tests.jsonl'):
                        self.assertNotIn(self.fixture['password'], evidence.read_text())
                        self.assertNotIn(self.fixture['username'], evidence.read_text())
                    self.assertEqual(json.loads(self.original.read_text())['password'], 'REPLACED-SECRET')
                    self.assertFalse(Path(json.loads(self.marker.read_text())['path']).exists())
                    self.assert_clean()

    def test_signals_wait_for_children_and_clean_snapshot_lock(self):
        for profile in PROFILES:
            for number in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
                with self.subTest(profile=profile, signal=number):
                    process = self.start('cancel', profile)
                    self.wait_for_marker(process)
                    with (self.task / 'harness.lock').open('w') as lock:
                        with self.assertRaises(BlockingIOError):
                            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    process.send_signal(number)
                    output, _ = process.communicate(timeout=10)
                    self.assertEqual(process.returncode, 128 + number, output)
                    self.assertNotIn('Harness coverage passed:', output)
                    self.assertTrue(json.loads(self.marker.read_text())['alive_on_term'])
                    self.assert_child_stopped()
                    self.assert_clean()

    def assert_child_stopped(self):
        pid = json.loads(self.marker.read_text())['pid']
        state = Path(f'/proc/{pid}/stat')
        deadline = time.monotonic() + 2
        while time.monotonic() < deadline:
            try:
                # Orphan zombies are dead; only the OS/adoptive parent can reap them.
                if state.read_text().rsplit(')', 1)[1].split()[0] == 'Z':
                    return
            except FileNotFoundError:
                return
            time.sleep(.01)
        self.fail(f'Synthetic child {pid} remains live')

    def test_outer_deadline_stops_children_and_cleans_snapshot_lock(self):
        self.env['WOW_TEST_WITHIN_BOUND'] = '1'
        runner = self.root / 'scripts/wow-test'
        runner.write_text(runner.read_text().replace('120s', '2s'))
        for profile in PROFILES:
            with self.subTest(profile=profile):
                process = self.start('cancel', profile)
                self.wait_for_marker(process)
                output, _ = process.communicate(timeout=10)
                self.assertEqual(process.returncode, 124)
                self.assertNotIn('Harness coverage passed:', output)
                self.assertTrue(json.loads(self.marker.read_text())['alive_on_term'])
                self.assert_child_stopped()
                self.assert_clean()

    def test_forced_deadline_cleans_snapshot_and_lock(self):
        cases = []
        with ThreadPoolExecutor(max_workers=2) as pool:
            for profile in PROFILES:
                with self.subTest(profile=profile):
                    process = None
                    try:
                        case = self.isolated_case()
                        runner = case.root / 'scripts/wow-test'
                        runner.write_text(runner.read_text().replace('120s', '2s'))
                        started = time.monotonic()
                        process = case.start('stubborn', profile)
                        case.wait_for_marker(process)
                        future = pool.submit(finish, process, 8)
                        cases.append((profile, case, process, started, future))
                    except BaseException:
                        if process is not None:
                            process.cleanup_owned()
                        raise
            for profile, case, process, started, future in cases:
                with self.subTest(profile=profile):
                    output, completed = future.result()
                    self.assertEqual(process.returncode, 137, output)
                    self.assertLess(completed - started, 7)
                    self.assertNotIn('Harness coverage passed:', output)
                    self.assertTrue(json.loads(case.marker.read_text())['alive_on_term'])
                    case.assert_child_stopped()
                    case.assert_clean()

    def test_public_cancellation_escalates_without_waiting_for_deadline(self):
        # Keep the real 120-second worker deadline; cancellation must start its own grace.
        cases = []
        with ThreadPoolExecutor(max_workers=3) as pool:
            for number in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
                with self.subTest(signal=number):
                    process = None
                    try:
                        case = self.isolated_case()
                        process = case.start('stubborn', 'smoke')
                        case.wait_for_marker(process)
                        if number == signal.SIGTERM:
                            os.killpg(os.getpgid(json.loads(case.marker.read_text())['pid']), signal.SIGSTOP)
                        started = time.monotonic()
                        process.send_signal(number)
                        future = pool.submit(finish, process, 8)
                        cases.append((number, case, process, started, future))
                    except BaseException:
                        if process is not None:
                            process.cleanup_owned()
                        raise
            for number, case, process, started, future in cases:
                with self.subTest(signal=number):
                    output, completed = future.result()
                    self.assertEqual(process.returncode, 128 + number, output)
                    self.assertLess(completed - started, 6)
                    self.assertNotIn('Harness coverage passed:', output)
                    self.assertTrue(json.loads(case.marker.read_text())['alive_on_term'])
                    case.assert_child_stopped()
                    case.assert_clean()

    def test_abandoned_descendant_cannot_retain_snapshot_or_lock(self):
        for deadline in (False, True):
            with self.subTest(deadline=deadline):
                if deadline:
                    runner = self.root / 'scripts/wow-test'
                    runner.write_text(runner.read_text().replace('120s', '2s'))
                process = self.start('orphan', 'smoke')
                self.wait_for_marker(process)
                if not deadline:
                    process.terminate()
                output, _ = process.communicate(timeout=8)
                self.assertEqual(process.returncode, 124 if deadline else 143, output)
                self.assertNotIn('Harness coverage passed:', output)
                self.assert_child_stopped()
                self.assert_clean()

    def test_cancellation_before_worker_group_exists(self):
        runner = self.root / 'scripts/wow-test'
        runner.write_text(runner.read_text().replace('120s', '2s'))
        binary = self.root / 'bin'
        binary.mkdir()
        setsid = binary / 'setsid'
        setsid.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys, time
pathlib.Path(os.environ['SYNTHETIC_MARKER']).write_text(json.dumps({'pid':os.getpid()}))
release=pathlib.Path(os.environ['SYNTHETIC_RELEASE'])
deadline=time.monotonic()+5
while not release.exists():
 if time.monotonic()>=deadline: sys.exit(98)
 time.sleep(.01)
os.execv(''' + repr(shutil.which('setsid')) + ''', ['setsid', *sys.argv[1:]])
''')
        setsid.chmod(0o700)
        self.env['PATH'] = str(binary) + os.pathsep + self.env['PATH']
        release = self.root / 'release'
        self.env['SYNTHETIC_RELEASE'] = str(release)
        process = self.start('cancel', 'smoke')
        self.addCleanup(release.touch)
        self.wait_for_marker(process)
        self.assertEqual(os.getpgid(json.loads(self.marker.read_text())['pid']), process.pid)
        started = time.monotonic()
        process.terminate()
        output, _ = process.communicate(timeout=8)
        self.assertEqual(process.returncode, 143, output)
        self.assertLess(time.monotonic() - started, 1)
        self.assertNotIn('Harness operation=', output)
        self.assert_child_stopped()
        self.assert_clean()

    def test_term_during_snapshot_write_cleans_reserved_secret(self):
        # Pause after the real writer has written secrets, before it returns to Bash.
        config = self.base / 'config.py'
        config.rename(self.base / 'validator.py')
        config.write_text('''import pathlib, subprocess, sys, time
result=subprocess.run([sys.executable,str(pathlib.Path(__file__).with_name('validator.py')),*sys.argv[1:]])
if '--snapshot' in sys.argv:
 pathlib.Path(__file__).with_name('writing').touch()
 while True: time.sleep(.02)
sys.exit(result.returncode)
''')
        for profile in PROFILES:
            with self.subTest(profile=profile):
                (self.base / 'writing').unlink(missing_ok=True)
                process = self.start('success', profile)
                deadline = time.monotonic() + 5
                while not (self.base / 'writing').exists() and time.monotonic() < deadline:
                    time.sleep(.01)
                self.assertTrue((self.base / 'writing').exists())
                snapshots = list((self.task / 'private').glob('.run-config-*'))
                self.assertEqual(len(snapshots), 1)
                self.assertEqual(json.loads(snapshots[0].read_text()), self.fixture)
                process.terminate()
                output, _ = process.communicate(timeout=10)
                self.assertNotEqual(process.returncode, 0)
                self.assertNotIn('Harness coverage passed:', output)
                self.assertFalse(snapshots[0].exists())
                self.assertEqual(json.loads(self.original.read_text()), self.fixture)
                self.assert_clean()

    def test_result_gate_rejects_incomplete_or_bad_receipts(self):
        cases = []
        for profile in PROFILES:
            names = ['TestLiveGameplay/' + child for child in CHILDREN]
            if profile == 'run':
                names.append('TestLiveGameplay/LogoutRelogPersistence')
            names.extend(ROOTS)
            faults = [(kind, name) for kind in ('missing', 'skip', 'fail') for name in names]
            faults += [(kind, '') for kind in ('empty', 'malformed', 'nonobject')]
            if profile == 'smoke':
                faults += [('unexpected', name) for name in ('LogoutRelogPersistence', 'OtherCase')]
            for fault in faults:
                cases.append((profile, fault, self.isolated_case()))
        with ThreadPoolExecutor(max_workers=4) as pool:
            futures = [pool.submit(receipt, case, profile, fault) for profile, fault, case in cases]
            for (profile, fault, case), future in zip(cases, futures):
                with self.subTest(profile=profile, fault=fault):
                    process, output = future.result()
                    self.assertNotEqual(process.returncode, 0, output)
                    self.assertIn('Harness coverage failed:', output)
                    self.assertNotIn('Harness coverage passed:', output)
                    self.assertFalse(Path(json.loads(case.marker.read_text())['path']).exists())
                    case.assert_clean()

    def test_default_check_and_invalid_mode_do_not_read_private_input(self):
        self.original.unlink()
        (self.base / 'test_offline.py').write_text('import unittest\nclass Offline(unittest.TestCase):\n def test_check(self): pass\n')
        for arguments, expected in (([], 0), (['invalid'], 2), (['smoke', 'extra'], 2)):
            with self.subTest(arguments=arguments):
                env = dict(self.env, SYNTHETIC_CHECK='1')
                if not arguments:
                    env.pop('WOW_TEST_CONFIG')
                result = subprocess.run([str(self.root / 'scripts/wow-test'), *arguments], env=env,
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
                if not arguments:
                    self.assertIn('Harness operation=check', result.stdout)
                else:
                    self.assertIn('Usage:', result.stderr)
                self.assertFalse(self.marker.exists())
        self.original.write_text(json.dumps(self.fixture))
        self.original.chmod(0o600)
        original = self.original.read_bytes()
        result = subprocess.run([str(self.root / 'scripts/wow-test'), 'check'],
                                env=dict(self.env, SYNTHETIC_CHECK='1'),
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('Harness operation=check', result.stdout)
        self.assertEqual(self.original.read_bytes(), original)
        self.assertFalse(self.marker.exists())
        self.assert_clean()

    def test_coverage_uses_original_metadata_during_credential_collisions(self):
        def strings(value):
            if isinstance(value, str):
                yield value
            elif isinstance(value, dict):
                for child in value.values():
                    yield from strings(child)
            elif isinstance(value, list):
                for child in value:
                    yield from strings(child)

        for profile in PROFILES:
            for password in ('pass', 'Test', 'wow'):
                for fault in (None, ('missing', 'TestLiveGameplay')):
                    with self.subTest(profile=profile, password=password, fault=fault):
                        self.fixture['password'] = password
                        process = self.start('success', profile, fault)
                        output, _ = process.communicate(timeout=10)
                        self.assertEqual(process.returncode == 0, fault is None, output)
                        self.assertEqual('Harness coverage passed:' in output, fault is None, output)
                        for line in output.splitlines():
                            if line.startswith('{'):
                                for value in strings(json.loads(line)):
                                    self.assertNotIn(password, value)
                                    self.assertNotIn(self.fixture['username'], value)
                        run = next(line.split('evidence=', 1)[1] for line in output.splitlines()
                                   if line.startswith('Harness operation='))
                        for line in (Path(run) / 'tests.jsonl').read_text().splitlines():
                            for value in strings(json.loads(line)):
                                self.assertNotIn(password, value)
                        self.assert_clean()

    def test_result_gate_rejects_malformed_event_objects(self):
        package = 'wow/rapid-game-harness'
        objects = ({}, {'Action': [], 'Package': package}, {'Action': 'unknown', 'Package': package},
                   {'Action': 'output'}, {'Action': 'output', 'Package': []},
                   {'Action': 'pass', 'Package': 'other'},
                   {'Action': 'pass', 'Package': package, 'Test': []},
                   {'Action': 'run', 'Package': package},
                   {'Action': 'start', 'Package': package, 'Test': 'TestOther'})
        cases = [(profile, event, self.isolated_case()) for profile in PROFILES for event in objects]
        with ThreadPoolExecutor(max_workers=4) as pool:
            futures = [pool.submit(receipt, case, profile, ('object', event)) for profile, event, case in cases]
            for (profile, event, case), future in zip(cases, futures):
                with self.subTest(profile=profile, event=event):
                    process, output = future.result()
                    self.assertNotEqual(process.returncode, 0, output)
                    self.assertIn('Harness coverage failed: invalid or incomplete results', output)
                    self.assertNotIn('Harness coverage passed:', output)
                    self.assertNotIn('Traceback', output)
                    case.assert_clean()

    def test_standalone_redactor_preserves_unvalidated_stream_behavior(self):
        data = ('plain ' + self.fixture['password'] + '\n' +
                json.dumps({'Action': 'unknown', 'Package': self.fixture['username'],
                            'nested': [self.fixture['password'], {'value': self.fixture['username']}]}) + '\n')
        result = subprocess.run(['python3', str(self.base / 'redact.py'), str(self.original)],
                                input=data, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines()[0], 'plain [REDACTED]')
        self.assertEqual(json.loads(result.stdout.splitlines()[1]),
                         {'Action': 'unknown', 'Package': '[REDACTED]',
                          'nested': ['[REDACTED]', {'value': '[REDACTED]'}]})

    def test_actual_go_cannot_substitute_inputs_from_ambient_defaults(self):
        subprocess.run([str(BASE.parents[1] / 'scripts/wow-build-host-guard')],
                       check=True, capture_output=True)
        real_go = BASE.parents[1] / 'work/rapid-game-harness-20261012/go/bin/go'
        self.assertTrue(real_go.is_file(), 'Runner regression needs local pinned Go 1.26.9')
        tool_env = dict(os.environ, GOENV='off', GOFLAGS='', GOWORK='off', GOTOOLCHAIN='local')
        version = subprocess.check_output([str(real_go), 'version'], env=tool_env, text=True).strip()
        self.assertEqual(version, 'go version go1.26.9 linux/amd64')
        go = self.task / 'go/bin/go'
        go.unlink()
        go.symlink_to(real_go)
        canonical = self.base / 'canonical'
        replacement = self.root / 'replacement'
        for folder, value in ((canonical, 'false'), (replacement, 'true')):
            folder.mkdir()
            (folder / 'go.mod').write_text('module example.invalid/canary\n\ngo 1.26.0\n')
            (folder / 'canary.go').write_text(
                'package canary\nfunc Verified() bool { return ' + value + ' }\n')
        original = (canonical / 'canary.go').read_bytes()
        (self.base / 'go.mod').write_text(
            'module wow/rapid-game-harness\n\ngo 1.26.0\n'
            'require example.invalid/canary v0.0.0\n'
            'replace example.invalid/canary => ./canonical\n')
        (self.base / 'live_test.go').write_text('''package harness
import ("testing"; "example.invalid/canary")
func TestLiveGameplay(t *testing.T) {
 for _, name := range []string{"Login","ChatEcho","NativeMovement","LearnedSelfCast","LogoutRelogPersistence"} {
  t.Run(name, func(t *testing.T) {
   if name == "Login" && !canary.Verified() { t.Fatal("expected canonical canary failure") }
  })
 }
}
func TestPinnedExactNameCreationPolicy(t *testing.T) {}
''')
        overlay = self.base / 'overlay.json'
        overlay.write_text(json.dumps({'Replace': {
            str(canonical / 'canary.go'): str(replacement / 'canary.go')}}))
        defaults = self.root / 'goenv'
        defaults.write_text('GOFLAGS=-overlay=overlay.json\n')
        external = self.root / 'external-workspace'
        external.mkdir()
        workspace = external / 'go.work'
        workspace.write_text('go 1.26.0\nuse ../tools/rapid-game-harness\n'
                             'replace example.invalid/canary => ../replacement\n')
        workspace_defaults = self.root / 'workspace-goenv'
        workspace_defaults.write_text('GOWORK=' + str(workspace) + '\n')
        ancestor = self.root / 'go.work'
        common = dict(self.env, GOENV='off', GOFLAGS='', GOWORK='off',
                      GOPROXY='off', GOSUMDB='off')
        cases = (
            ('control', {}),
            ('overlay', {'GOFLAGS': '-overlay=overlay.json'}),
            ('stored-overlay', {'GOENV': str(defaults), 'GOFLAGS': None}),
            ('workspace', {'GOWORK': str(workspace)}),
            ('ancestor-workspace', {'GOWORK': None}),
            ('stored-workspace', {'GOENV': str(workspace_defaults), 'GOWORK': None}),
        )
        for name, poison in cases:
            with self.subTest(source=name):
                ancestor.unlink(missing_ok=True)
                if name == 'ancestor-workspace':
                    ancestor.write_text('go 1.26.0\nuse ./tools/rapid-game-harness\n'
                                        'replace example.invalid/canary => ./replacement\n')
                self.env = dict(common)
                for key, value in poison.items():
                    if value is None:
                        self.env.pop(key, None)
                    else:
                        self.env[key] = value
                # Prove each poison can actually replace this fixture without the runner.
                control = subprocess.run(
                    [str(real_go), 'test', '-mod=readonly', '-run=^TestLiveGameplay$',
                     '-count=1', '-timeout=30s', '.'],
                    cwd=self.base, env=dict(self.env, GOTOOLCHAIN='local'),
                    capture_output=True, text=True, timeout=45)
                self.assertEqual(control.returncode == 0, name != 'control',
                                 control.stdout + control.stderr)
                self.assertEqual('expected canonical canary failure' in control.stdout,
                                 name == 'control', control.stdout + control.stderr)
                process = self.start('success', 'smoke')
                output, _ = process.communicate(timeout=45)
                self.assertNotEqual(process.returncode, 0, output)
                self.assertIn('expected canonical canary failure', output)
                self.assertNotIn('Harness coverage passed:', output)
                self.assertEqual((canonical / 'canary.go').read_bytes(), original)
                self.assertEqual(defaults.read_text(), 'GOFLAGS=-overlay=overlay.json\n')
                self.assertEqual(workspace_defaults.read_text(), 'GOWORK=' + str(workspace) + '\n')
                self.assert_clean()


if __name__ == '__main__':
    unittest.main()
