"""Portable tests for the published native-upgrade binding and stream path."""

import hashlib
import json
import os
from pathlib import Path
import stat
import tempfile
import unittest
from unittest import mock

import native_upgrade
import remote


def parse_frames(response):
    lines = response.splitlines()
    if not lines or lines[0] != "Output:" or any("lines truncated" in line for line in lines):
        raise ValueError("invalid NativeCaptureFrames output")
    return json.loads("".join(lines[1:]))


class NativeUpgradeBindingTests(unittest.TestCase):
    def test_fixed_wrappers_and_validated_arguments(self):
        status = native_upgrade.command("export_status", {})
        self.assertTrue(status.startswith("python exec exec("))
        self.assertIn("_value=witness_status()", status)
        self.assertIn("NativeCaptureFrames.emit(", status)
        self.assertIn("O_EXCL", status)
        self.assertIn("os.link(", status)
        self.assertNotIn("sys.argv", status)
        self.assertNotIn("__import__('os').system", status)
        with self.assertRaises(ValueError):
            native_upgrade.command("select_seed_slot", {"slot_key": "quest:17:choice:4"})
        self.assertIn(
            "queue_native_gear_observation('quest:17:choice:4')",
            native_upgrade.command("queue_gear_observation", {"slot_key": "quest:17:choice:4"}),
        )
        command = native_upgrade.command("arm_capture", {
            "identity_key": "a" * 64, "binding": [7, 12], "slot_key": "quest:17:choice:4",
            "snapshot_id": "snapshot-1", "exporter_revision": "b" * 64,
        })
        self.assertIn("arm_native_quest_capture(", command)
        self.assertNotIn("exec(", command.removeprefix("python exec "))

    def test_stage_gear_maps_only_the_accepted_repository_path(self):
        relative = "work/core1-native-earned-upgrade-delivery-20261021/gear-inputs.json"
        command = native_upgrade.command("stage_gear", {"path": relative, "sha256": "a" * 64})
        self.assertIn("stage_accepted_gear('/srv/wow/" + relative + "',", command)
        for path in ("../gear-inputs.json", "work/other/gear-inputs.json",
                     "work/core1-native-earned-upgrade-delivery-20261021/../../etc/passwd"):
            with self.subTest(path=path), self.assertRaises(ValueError):
                native_upgrade.command("stage_gear", {"path": path, "sha256": "a" * 64})

    def test_rejects_arbitrary_commands_and_argument_injection(self):
        cases = (
            ("python", {"command": "__import__('os').system('id')"}),
            ("status", {"command": "something"}),
            ("queue_gear_observation", {"slot_key": "x'); arbitrary() #"}),
            ("set_mode", {"mode": "shell"}),
            ("arm_capture", {"identity_key": "a" * 64, "binding": [True, 12],
             "slot_key": "x", "snapshot_id": "s", "exporter_revision": "b" * 64}),
        )
        for operation, args in cases:
            with self.subTest(operation=operation, args=args), self.assertRaises(ValueError):
                native_upgrade.command(operation, args)
        with self.assertRaises(ValueError):
            native_upgrade.command("drain_capture", {"path": "/tmp/other-run.json"})

    def test_ack_and_recovery_calls_are_fixed_and_receipt_bound(self):
        descriptor = {
            "path": "/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures/receipt-" + "c" * 32 + ".json",
            "bytes": 123, "sha256": "a" * 64, "generation": "snapshot-1", "ready": False,
        }
        calls = {
            "prepare_ack": native_upgrade.command("prepare_ack", {"descriptor": descriptor}),
            "recover_ack": native_upgrade.command("recover_ack", {"descriptor": descriptor}),
            "recover_capture": native_upgrade.command("recover_capture", {"descriptor": descriptor}),
            "settle_ack": native_upgrade.command("settle_ack", {}),
        }
        self.assertIn("prepare_native_custody_ack(", calls["prepare_ack"])
        self.assertIn("/acks/ack-" + "c" * 32 + ".json", calls["prepare_ack"])
        self.assertIn("recover_native_custody_ack(", calls["recover_ack"])
        self.assertIn("recover_native_quest_capture(", calls["recover_capture"])
        self.assertIn("settle_native_custody_ack()", calls["settle_ack"])
        invalid = (
            ("prepare_ack", {"descriptor": dict(descriptor, path="/tmp/x")}),
            ("recover_ack", {"descriptor": dict(descriptor, sha256="bad")}),
            ("recover_capture", {"descriptor": dict(descriptor, ready=True)}),
            ("settle_ack", {"path": "/tmp/x"}),
        )
        for operation, args in invalid:
            with self.subTest(operation=operation), self.assertRaises(ValueError):
                native_upgrade.command(operation, args)

    def test_capture_and_status_descriptors_validate_exact_fields(self):
        capture = {
            "path": "/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures/receipt-" + "e" * 32 + ".json",
            "bytes": 17, "sha256": "f" * 64, "generation": "snapshot-7", "ready": False,
        }
        self.assertEqual(native_upgrade.capture_descriptor(capture), capture)
        for bad in (dict(capture, ready=True), dict(capture, generation=""),
                    dict(capture, path=capture["path"].replace("captures/receipt", "receipts/receipt")),
                    {key: value for key, value in capture.items() if key != "generation"}):
            with self.subTest(descriptor=bad), self.assertRaises(ValueError):
                native_upgrade.capture_descriptor(bad)

        digest = "a" * 64
        status = {"path": native_upgrade.STATUS_ROOT + "/status-" + digest + ".json", "bytes": 17, "sha256": digest}
        self.assertEqual(native_upgrade.status_descriptor(status), status)
        for bad in (dict(status, bytes=True), dict(status, path="/tmp/status.json"),
                    dict(status, sha256="b" * 64), dict(status, extra=True)):
            with self.subTest(descriptor=bad), self.assertRaises(ValueError):
                native_upgrade.status_descriptor(bad)

    def test_soap_failures_faults_and_truncation_are_rejected(self):
        base = {"http_status": 200, "ok": True, "response": "Output:\n{}", "soap_fault": None,
                "receipt_truncated": False}
        for change in ({"http_status": 503}, {"ok": False},
                       {"soap_fault": {"code": "Server", "string": "fault"}},
                       {"receipt_truncated": True}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                native_upgrade.result_value(dict(base, **change), parse_frames)
        with self.assertRaises(ValueError):
            native_upgrade.result_value({"http_status": 200, "ok": True, "response": "Output:\n{}"}, parse_frames)
        with self.assertRaises(ValueError):
            native_upgrade.result_value(dict(base, response="Output:\n{}\nlines truncated"), parse_frames)

    def test_execute_uses_only_the_bound_command_and_passes_credentials_opaquely(self):
        calls = []

        def soap(command, credentials):
            calls.append((command, credentials))
            return {"http_status": 200, "ok": True, "response": 'Output:\n{"queued":"request"}',
                    "soap_fault": None, "receipt_truncated": False}

        credentials = object()
        result = native_upgrade.execute("request_session", {}, credentials, soap, parse_frames)
        self.assertEqual(result, {"queued": "request"})
        self.assertEqual(len(calls), 1)
        self.assertIn("request_witness_session()", calls[0][0])
        self.assertIs(calls[0][1], credentials)


class FakeSSHProcess:
    def __init__(self, output, returncode=0):
        self.stdin = _MemoryStdin()
        self.stdout = tempfile.TemporaryFile()
        self.stdout.write(output)
        self.stdout.seek(0)
        self.returncode = returncode
        self.terminated = False
        self.killed = False
        self.wait_calls = 0

    def poll(self):
        return self.returncode if self.wait_calls else None

    def wait(self, timeout=None):
        self.wait_calls += 1
        return self.returncode

    def terminate(self):
        self.terminated = True
        self.returncode = -15

    def kill(self):
        self.killed = True
        self.returncode = -9

    def close(self):
        self.stdout.close()


class _MemoryStdin:
    def __init__(self):
        self.data = bytearray()
        self.closed = False

    def write(self, data):
        self.data.extend(data)
        return len(data)

    def close(self):
        self.closed = True


class NativeRemoteStreamTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        os.chmod(self.root, 0o700)
        self.receipt_dir = self.root / "receipts"
        self.receipt_dir.mkdir(mode=0o700)
        self.process_identity = {"pid": 101, "invocation_id": "1" * 32, "uid": os.geteuid(), "exe": "/usr/bin/worldserver"}
        self.payload = b'{"schema":"offline-stream-test","status":{}}'
        self.digest = hashlib.sha256(self.payload).hexdigest()
        self.descriptor = {
            "path": native_upgrade.STATUS_ROOT + "/status-" + self.digest + ".json",
            "bytes": len(self.payload), "sha256": self.digest,
        }
        self.root_patcher = mock.patch.object(remote, "_private_receipts", return_value=self.receipt_dir)
        self.root_patcher.start()
        self.addCleanup(self.root_patcher.stop)
        self.processes = []

    def fake_popen(self, output, returncode=0):
        process = FakeSSHProcess(output, returncode)
        self.processes.append(process)
        self.addCleanup(process.close)
        return process

    def test_stream_file_publishes_exact_content_with_fixed_mocked_ssh(self):
        process = self.fake_popen(self.payload)
        with mock.patch.object(remote.subprocess, "Popen", return_value=process) as popen:
            result = remote.stream_file(self.descriptor, self.process_identity)
        target = Path(result["path"])
        self.assertEqual(target.read_bytes(), self.payload)
        self.assertEqual(result, {"path": str(target), "bytes": len(self.payload), "sha256": self.digest})
        self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o600)
        self.assertEqual(popen.call_args.args[0], remote.STREAM_SSH)
        self.assertEqual(popen.call_args.kwargs["stdin"], remote.subprocess.PIPE)
        self.assertEqual(popen.call_args.kwargs["stdout"], remote.subprocess.PIPE)
        self.assertTrue(process.stdin.closed)
        self.assertIn(self.descriptor["path"].encode(), process.stdin.data)
        self.assertIn(self.digest.encode(), process.stdin.data)

    def test_stream_file_preserves_exact_unready_capture_descriptor(self):
        descriptor = {
            "path": "/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures/receipt-" + "c" * 32 + ".json",
            "bytes": len(self.payload), "sha256": self.digest,
            "generation": "snapshot-17", "ready": False,
        }
        process = self.fake_popen(self.payload)
        with mock.patch.object(remote.subprocess, "Popen", return_value=process):
            result = remote.stream_file(descriptor, self.process_identity)
        target = Path(result["path"])
        self.assertEqual(target.read_bytes(), self.payload)
        self.assertEqual(result, {
            "path": str(target), "bytes": len(self.payload), "sha256": self.digest,
            "generation": "snapshot-17",
        })

    def test_stream_file_rejects_bad_length_digest_and_child_status_without_publish(self):
        failures = (
            ("early EOF", self.payload[:-1], 0, ValueError),
            ("excess bytes", self.payload + b"x", 0, ValueError),
            ("child failure", self.payload, 1, RuntimeError),
            ("digest mismatch", self.payload, 0, ValueError),
        )
        for name, output, returncode, error_type in failures:
            with self.subTest(name=name):
                digest = "0" * 64 if name == "digest mismatch" else self.digest
                descriptor = dict(self.descriptor, sha256=digest)
                process = self.fake_popen(output, returncode)
                with mock.patch.object(remote.subprocess, "Popen", return_value=process):
                    with self.assertRaises(error_type):
                        remote.stream_file(descriptor, self.process_identity)
                self.assertFalse((self.receipt_dir / Path(descriptor["path"]).name).exists())
                self.assertEqual(list(self.receipt_dir.glob(".native-transfer-*")), [])

    def test_stream_file_reuses_only_an_exact_existing_status_transfer(self):
        target = self.receipt_dir / Path(self.descriptor["path"]).name
        target.write_bytes(self.payload)
        os.chmod(target, 0o600)
        with mock.patch.object(remote.subprocess, "Popen") as popen:
            result = remote.stream_file(self.descriptor, self.process_identity)
        self.assertEqual(result["path"], str(target))
        popen.assert_not_called()

        target.write_bytes(b"conflict")
        with mock.patch.object(remote.subprocess, "Popen") as popen:
            with self.assertRaises(FileExistsError):
                remote.stream_file(self.descriptor, self.process_identity)
        popen.assert_not_called()

    def test_stream_file_validates_descriptors_and_process_before_ssh(self):
        bad_descriptors = (
            dict(self.descriptor, bytes=True),
            dict(self.descriptor, sha256="invalid"),
            dict(self.descriptor, extra="not allowed"),
            {key: value for key, value in self.descriptor.items() if key != "sha256"},
        )
        with mock.patch.object(remote.subprocess, "Popen") as popen:
            for descriptor in bad_descriptors:
                with self.subTest(descriptor=descriptor), self.assertRaises(ValueError):
                    remote.stream_file(descriptor, self.process_identity)
            with self.assertRaises(ValueError):
                remote.stream_file(self.descriptor, dict(self.process_identity, uid=True))
        popen.assert_not_called()


if __name__ == "__main__":
    unittest.main()
