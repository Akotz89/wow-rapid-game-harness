"""Exercise the verified pinned transport with local Go and in-memory peers only."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]


class TransportTests(unittest.TestCase):
    def test_actual_pinned_transport(self):
        subprocess.run([str(ROOT / 'scripts/wow-build-host-guard')], check=True,
                       capture_output=True, text=True)
        dep = HERE / '.deps/azerothghost'
        go = ROOT / 'work/rapid-game-harness-20261012/go/bin/go'
        self.assertTrue((dep / '.git').is_dir(),
                        'Offline transport tests need the local pinned dependency; run bootstrap first')
        self.assertTrue(go.is_file(), 'Offline transport tests need the local pinned Go 1.26.9 toolchain')
        env = dict(os.environ, GOTOOLCHAIN='local', GOPROXY='off', GOSUMDB='off', GOWORK='off', GOENV='off', GOFLAGS='')
        version = subprocess.check_output([str(go), 'version'], env=env, text=True).strip()
        self.assertEqual(version, 'go version go1.26.9 linux/amd64')
        # Existing checkout is required above, so bootstrap verifies/publishes without fetching.
        verified = subprocess.run([str(HERE / 'bootstrap')], capture_output=True, text=True,
                                  env=env, timeout=15)
        self.assertEqual(verified.returncode, 0, verified.stdout + verified.stderr)
        work = ROOT / 'work/rapid-game-harness-session-terminal-20261015/implementation'
        work.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix='transport-', dir=work) as folder:
            box = Path(folder)
            mod = box / 'go.mod'
            mod.write_text('module github.com/azerothcore/AzerothGhost\n\ngo 1.26.0\n')
            overlay = box / 'overlay.json'
            overlay.write_text(json.dumps({'Replace': {
                str(dep / 'client/rapid_transport_test.go'): str(HERE / 'testdata/world_transport_test.go'),
            }}))
            result = subprocess.run([str(go), 'test', '-modfile=' + str(mod),
                                     '-overlay=' + str(overlay), '-run=^TestRapidTransport',
                                     '-count=1', '-timeout=15s', '-v', './client'],
                                    cwd=dep, env=env, capture_output=True, text=True, timeout=30)
            print(result.stdout + result.stderr, end='')
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            modes = ('plain', 'encrypted', 'auth')
            failures = ('deadline_error', 'write_error', 'partial_error', 'short_nil')
            required = ['HealthyFrames/' + mode for mode in modes]
            required += ['FailureInvalidates/' + mode + '/' + failure
                         for mode in modes for failure in failures]
            required += ['BlockedWriteAndClose/' + mode for mode in modes]
            required += ['RunPreservesWriteFailure']
            required += ['AuthFailure/' + failure for failure in failures]
            required += ['LoginWait/' + case for case in ('already_stopped', 'read_loop_disconnect',
                                                         'native_receipt', 'live_without_receipt_timeout',
                                                         'stopped_transport_cause', 'receipt_and_stop_interleaving',
                                                         'receipt_and_terminal_state')]
            required += ['BufferedAuthRejectPreservesCause']
            required += ['LogoutWait/' + case for case in ('close_without_receipt', 'transport_cause',
                                                          'acknowledged_and_stopped', 'live_without_receipt_timeout')]
            required += ['StopError/' + case for case in ('unconnected', 'close', 'stopped_bit', 'closed_channel', 'retained_cause')]
            required += ['PhaseWait/' + case for case in ('stale_target', 'target_stopped_bit', 'queued_target_stop',
                                                          'queued_target_stopped_bit', 'stop_during_wait', 'timeout_with_stopped_bit',
                                                          'healthy_target', 'healthy_queued_target', 'healthy_timeout', 'auth_queue')]
            required += ['RunChallengeCause/' + case for case in ('header_read', 'payload_read', 'wrong_opcode', 'earlier_write')]
            required += ['FirstCausePublication/' + case for case in ('write', 'auth_reject')]
            required += ['LoginTimerArbitration/' + case for case in ('healthy', 'stopped_bit')]
            required += ['MalformedChallenge/' + case for case in ('zero_size', 'underflow', 'zero_payload',
                                                                  'short_payload', 'undersized', 'oversized')]
            required += ['AtomicTerminalPublication/' + case for case in ('run', 'auth_reject')]
            for name in required:
                self.assertIn('--- PASS: TestRapidTransport' + name + ' (', result.stdout,
                              'Expected actual-source transport case did not pass: ' + name)


if __name__ == '__main__':
    unittest.main()
