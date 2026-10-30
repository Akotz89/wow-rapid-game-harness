"""Run the actual observer and public cancellation path without network or credentials."""
from concurrent.futures import ThreadPoolExecutor
import fcntl
import importlib.util
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
ROOT = BASE.parents[1]
TASK = ROOT / 'work/rapid-game-harness-20261012'

SSH = '''#!/usr/bin/env python3
import json, os, pathlib, signal, sys, time
stage = os.environ['SSH_STAGE']
if sys.argv[1:] == ['-G', 'server-01'] and stage != 'config':
    print('hostname localhost')
    sys.exit()
if sys.argv[1] == '-T':
    sys.stdin.read()
if stage == 'healthy':
    print(json.dumps(dict(name='Rhfixture', guid=42, map=0, x=1, y=2, z=3,
                         level=1, health=100, mana=100, online=True)))
    sys.exit()
if os.environ['SSH_IGNORE_TERM'] == '1':
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
marker = pathlib.Path(os.environ['SSH_MARKER'])
temporary = marker.with_suffix('.tmp')
temporary.write_text(json.dumps(dict(pid=os.getpid(), parent=os.getppid(),
    group=os.getpgrp(), args=sys.argv[1:], snapshot=os.environ.get('WOW_TEST_CONFIG'))))
temporary.replace(marker)
while True:
    time.sleep(.05)
'''

GO_TEST = '''
func TestActualNativeObservation(t *testing.T) {
    o, err := nativeObservation(context.Background(), "Rhfixture")
    if err != nil { t.Fatal(err) }
    if o.Name != "Rhfixture" || o.GUID != 42 || o.Map != 0 || o.X != 1 || o.Y != 2 || o.Z != 3 ||
        o.Level != 1 || o.Health != 100 || o.Mana != 100 || !o.Online { t.Fatal("native response changed", o) }
}
'''


def alive(pid):
    try:
        return Path(f'/proc/{pid}/stat').read_text().rsplit(')', 1)[1].split()[0] != 'Z'
    except (FileNotFoundError, ProcessLookupError):
        return False


def lock_free(path):
    with path.open('a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return False
    return True


def child_groups(pid):
    groups = set()
    try:
        children = Path(f'/proc/{pid}/task/{pid}/children').read_text().split()
    except (FileNotFoundError, ProcessLookupError):
        return groups
    for child in children:
        try:
            fields = Path(f'/proc/{child}/stat').read_text().rsplit(')', 1)[1].split()
            if int(fields[1]) == pid and int(fields[2]) == int(child):
                groups.add(int(child))
        except (FileNotFoundError, ProcessLookupError):
            pass
    return groups


class NativeObservationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        subprocess.run([str(ROOT / 'scripts/wow-build-host-guard')], check=True, capture_output=True)
        work = ROOT / 'work/rapid-game-harness-next-audit-20261015/implementation'
        work.mkdir(parents=True, exist_ok=True)
        cls.temp = tempfile.TemporaryDirectory(prefix='synthetic-observer-', dir=work)
        cls.addClassCleanup(cls.temp.cleanup)
        cls.box = Path(cls.temp.name)
        source = (BASE / 'live_test.go').read_bytes()
        signature = b'func nativeObservation(ctx context.Context, name string) (observation, error) {'
        if source.count(signature) != 1 or source.count(b'\nfunc waitNative(') != 1:
            raise AssertionError('nativeObservation extraction signature changed')
        function = signature + source.split(signature, 1)[1].split(b'\nfunc waitNative(', 1)[0]
        if (not function.endswith(b'}\n') or b'context.WithTimeout(ctx, 4*time.Second)' not in function
                or b'cmd.WaitDelay = time.Second' not in function):
            raise AssertionError('nativeObservation extraction boundary or original deadline changed')
        extracted = cls.box / 'observer_test.go'
        extracted.write_bytes(b'package harness\nimport ("context"; "errors"; "os/exec"; "syscall"; "testing"; "time")\n' + function + GO_TEST.encode())
        if extracted.read_bytes().count(function) != 1:
            raise AssertionError('nativeObservation source was modified')
        shutil.copyfile(BASE / 'harness_test.go', cls.box / 'harness_test.go')
        (cls.box / 'go.mod').write_text('module synthetic-observer\n\ngo 1.26.0\n')
        cls.env = dict(os.environ, GOENV='off', GOFLAGS='', GOWORK='off', GOTOOLCHAIN='local',
                       GOPROXY='off', GOSUMDB='off', PYTHONDONTWRITEBYTECODE='1')
        cls.binary = cls.box / 'observer.test'
        build = subprocess.run([str(TASK / 'go/bin/go'), 'test', '-c', '-o', str(cls.binary), '.'],
                               cwd=cls.box, env=cls.env, capture_output=True, text=True, timeout=45)
        if build.returncode:
            raise AssertionError(build.stdout + build.stderr)

    def execute_case(self, stage, ignore, mode):
        box = Path(tempfile.mkdtemp(prefix=f'{stage}-{ignore}-{mode}-', dir=self.box))
        base = box / 'tools/rapid-game-harness'
        task = box / 'work/rapid-game-harness-20261012'
        for path in (base, box / 'scripts', box / 'bin', task / 'go/bin'):
            path.mkdir(parents=True)
        for name in ('remote.py', 'config.py', 'redact.py'):
            shutil.copyfile(BASE / name, base / name)
        for name in ('wow-test', 'wow-build-host-guard'):
            shutil.copy2(ROOT / 'scripts' / name, box / 'scripts' / name)
        transport = box / 'integration/cpi-351-api-activation-20261012/soap_transport.py'
        transport.parent.mkdir(parents=True)
        shutil.copyfile(ROOT / transport.relative_to(box), transport)
        (base / 'bootstrap').write_text('#!/bin/sh\nexit 0\n')
        (base / 'bootstrap').chmod(0o700)
        ssh = box / 'bin/ssh'
        ssh.write_text(SSH)
        ssh.chmod(0o700)
        go = task / 'go/bin/go'
        go.write_text('#!/usr/bin/env python3\nimport os, sys\n'
                      'if sys.argv[1:] == ["version"]: print("go version go1.26.9 linux/amd64")\n'
                      'else: os.execv(' + repr(str(self.binary)) + ', ["observer.test", "-test.run=^TestActualNativeObservation$", "-test.v"])\n')
        go.chmod(0o700)
        private = task / 'private'
        private.mkdir(mode=0o700)
        fixture = private / 'fixture.json'
        original = json.dumps(dict(auth_addr='localhost:3724', username='RH000000000001',
                                   password='SYNTHETIC-ONLY', character='Rhfixture')).encode()
        fixture.write_bytes(original)
        fixture.chmod(0o600)
        marker = box / 'marker.json'
        env = dict(self.env, PATH=str(box / 'bin') + os.pathsep + os.environ['PATH'],
                   SSH_STAGE=stage, SSH_IGNORE_TERM=str(int(ignore)), SSH_MARKER=str(marker),
                   WOW_TEST_CONFIG=str(fixture))
        command = [str(self.binary), '-test.run=^TestActualNativeObservation$', '-test.v']
        public = mode not in ('direct_context', 'healthy')
        if public:
            command = [str(box / 'scripts/wow-test'), 'smoke']
        if mode == 'deadline':
            runner = box / 'scripts/wow-test'
            text = runner.read_text()
            if text.count('timeout --kill-after=2s 120s') != 1:
                raise RuntimeError('public deadline extraction changed')
            runner.write_text(text.replace('timeout --kill-after=2s 120s', 'timeout --kill-after=2s 2s'))
        started = time.monotonic()
        process = subprocess.Popen(command, cwd=base, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, text=True, start_new_session=True)
        groups = {process.pid}
        data = None
        try:
            if mode != 'healthy':
                deadline = started + 3
                while not marker.exists() and process.poll() is None and time.monotonic() < deadline:
                    groups.update(child_groups(process.pid))
                    time.sleep(.01)
                groups.update(child_groups(process.pid))
                data = json.loads(marker.read_text())
                locked = not lock_free(task / 'harness.lock')
                snapshot = Path(data['snapshot'])
                snapshot_ready = snapshot.exists()
                if mode in ('TERM', 'INT', 'HUP'):
                    process.send_signal(getattr(signal, 'SIG' + mode))
            output, _ = process.communicate(timeout=7)
            elapsed = time.monotonic() - started
            if data:
                deadline = time.monotonic() + .5
                while (alive(data['pid']) or alive(data['parent'])
                       or (mode == 'direct_context' and Path(f"/proc/{data['pid']}").exists())) and time.monotonic() < deadline:
                    time.sleep(.01)
            return dict(stage=stage, ignore=ignore, mode=mode, status=process.returncode,
                        output=output, elapsed=elapsed, marker=data, locked=locked if data else None,
                        snapshot_ready=snapshot_ready if data else None,
                        observer_alive=alive(data['parent']) if data else False,
                        ssh_alive=alive(data['pid']) if data else False,
                        ssh_reaped=not Path(f"/proc/{data['pid']}").exists() if data else True,
                        lock_free=lock_free(task / 'harness.lock'),
                        snapshots=list(private.glob('.run-config-*')),
                        original=fixture.read_bytes() == original,
                        snapshot_removed=not snapshot.exists() if data and public else True)
        finally:
            # Capture the timed group even when readiness is absent or malformed, then let its owner clean up.
            groups.update(child_groups(process.pid))
            if data and data['group'] == data['parent']:
                groups.add(data['group'])
            if process.poll() is None:
                try:
                    process.terminate()
                except ProcessLookupError:
                    pass
            try:
                process.communicate(timeout=3)
            except subprocess.TimeoutExpired:
                pass
            groups.update(child_groups(process.pid))
            for group in groups - {0, 1, os.getpgrp()}:
                try:
                    os.killpg(group, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            process.communicate(timeout=3)

    def test_actual_observer_cancellation_and_healthy_response(self):
        sentinel = subprocess.Popen(['sleep', '20'], start_new_session=True)
        self.addCleanup(sentinel.wait)
        self.addCleanup(sentinel.terminate)
        # Overlap the real four-second contexts with deadlines and short public cancellations.
        cases = [(stage, ignore, mode) for mode in ('context', 'deadline', 'TERM', 'INT', 'HUP')
                 for stage in ('config', 'transport') for ignore in (False, True)]
        cases[4:4] = [(stage, True, 'direct_context') for stage in ('config', 'transport')]
        cases.append(('healthy', False, 'healthy'))
        with ThreadPoolExecutor(max_workers=8) as pool:
            futures = [pool.submit(self.execute_case, *case) for case in cases]
            for case, future in zip(cases, futures):
                with self.subTest(stage=case[0], ignore_term=case[1], mode=case[2]):
                    result = future.result()
                    mode = result['mode']
                    expected = dict(context=1, direct_context=1, TERM=143, INT=130, HUP=129, deadline=124, healthy=0)[mode]
                    self.assertEqual(result['status'], expected, result)
                    self.assertLess(result['elapsed'], 6, result)
                    self.assertFalse(result['observer_alive'], result)
                    self.assertFalse(result['ssh_alive'], result)
                    self.assertTrue(result['lock_free'], result)
                    self.assertTrue(result['original'], result)
                    self.assertFalse(result['snapshots'], result)
                    self.assertTrue(result['snapshot_removed'], result)
                    self.assertIsNone(sentinel.poll())
                    if mode in ('context', 'direct_context'):
                        self.assertGreaterEqual(result['elapsed'], 4, result)
                        self.assertIn('authenticated native observation unavailable', result['output'])
                    if mode == 'direct_context':
                        self.assertTrue(result['ssh_reaped'], result)
                    elif mode == 'healthy':
                        self.assertIn('--- PASS: TestActualNativeObservation', result['output'])
                    else:
                        self.assertTrue(result['locked'], result)
                        self.assertTrue(result['snapshot_ready'], result)
                        self.assertNotIn('Harness coverage passed:', result['output'])
                    if result['marker']:
                        args = result['marker']['args']
                        self.assertEqual(args[:1], ['-G' if result['stage'] == 'config' else '-T'])
                    self.assertNotIn('SYNTHETIC-ONLY', result['output'])
                    self.assertNotIn('RH000000000001', result['output'])

    def test_import_does_not_change_signal_handlers(self):
        before = {number: signal.getsignal(number) for number in signal.valid_signals()}
        spec = importlib.util.spec_from_file_location('observer_remote_import', BASE / 'remote.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.assertEqual({number: signal.getsignal(number) for number in before}, before)


if __name__ == '__main__':
    unittest.main()
