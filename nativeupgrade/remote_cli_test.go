package nativeupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDecodeWitnessStatusPreservesDeclaredFieldsAndUnknowns(t *testing.T) {
	raw := map[string]json.RawMessage{
		"gear_observation":         json.RawMessage(`{"status":"observed","identity_key":"id","source_id":"src","strategic_generation":7,"session_incarnation":2,"slot_key":"quest:17:choice:4","item_id":91,"slot_index":4,"score":3.5,"binding":[12,34]}`),
		"gear_observation_request": json.RawMessage(`null`),
		"gear":                     json.RawMessage(`{"session_incarnation":3}`),
	}
	var status NativeStatus
	if err := decodeWitnessStatus(raw, &status); err != nil {
		t.Fatal(err)
	}
	if status.Observation.AccountID != 12 || status.Observation.CharacterGUID != 34 || status.Observation.Session != 2 || status.RelogSession != 3 || len(status.Gear) == 0 || !status.ObservationPendingKnown || status.ObservationPending {
		t.Fatalf("declared fields or unknown fields mapped incorrectly: %+v", status)
	}
}

func TestDecodeWitnessStatusPreservesPendingObservationState(t *testing.T) {
	raw := map[string]json.RawMessage{
		"gear_observation":         json.RawMessage(`{"status":"refused"}`),
		"gear_observation_request": json.RawMessage(`{"slot_key":"quest:17:choice:4","source_id":"quest-source-17"}`),
	}
	var status NativeStatus
	if err := decodeWitnessStatus(raw, &status); err != nil {
		t.Fatal(err)
	}
	if !status.ObservationPendingKnown || !status.ObservationPending || status.PendingObservationSlot != "quest:17:choice:4" || status.PendingObservationSource != "quest-source-17" {
		t.Fatalf("pending selected-slot request was not preserved: %+v", status)
	}
}

func TestDecodeWitnessStatusRejectsMalformedDeclaredTypes(t *testing.T) {
	raw := map[string]json.RawMessage{"gear_observation": json.RawMessage(`{"status":4}`)}
	if err := decodeWitnessStatus(raw, &NativeStatus{}); err == nil || !strings.Contains(err.Error(), "invalid type") {
		t.Fatalf("malformed declared field accepted: %v", err)
	}
}

func activeACKStatusFixture() map[string]json.RawMessage {
	digest := strings.Repeat("a", 64)
	status := map[string]any{
		"gear_observation": map[string]any{"status": "unknown"},
		"required_acks":    [][]any{{uint64(41), digest}},
		"applied_acks":     [][]any{},
		"custody_ack": map[string]any{
			"original_descriptor": map[string]any{
				"path":       "/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures/receipt-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json",
				"bytes":      19,
				"sha256":     strings.Repeat("b", 64),
				"generation": "native-snapshot-17",
				"ready":      false,
			},
			"ack_pairs": [][]any{{uint64(41), digest}},
			"records":   []any{map[string]any{"record_id": uint64(41), "status": "pending", "reason": ""}},
			"results":   []any{},
		},
	}
	encoded, _ := json.Marshal(status)
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &raw)
	return raw
}

func TestDecodeWitnessStatusAcceptsExactActiveACKAssociation(t *testing.T) {
	var status NativeStatus
	if err := decodeWitnessStatus(activeACKStatusFixture(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.ACKQueueActive || status.ACKOriginal == nil || len(status.ACKRecordIDs) != 1 || status.ACKRecordIDs[0] != "41" ||
		len(status.RequiredACKs) != 1 || status.RequiredACKs[0].RecordID != "41" || status.RequiredACKs[0].RecordSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("accepted active ACK response was not preserved: %+v", status)
	}
}

func TestDecodeWitnessStatusRejectsActiveACKAssociationDrift(t *testing.T) {
	tests := map[string]func(map[string]json.RawMessage){
		"missing descriptor": func(raw map[string]json.RawMessage) {
			var queue map[string]json.RawMessage
			_ = json.Unmarshal(raw["custody_ack"], &queue)
			delete(queue, "original_descriptor")
			raw["custody_ack"], _ = json.Marshal(queue)
		},
		"missing queue pairs": func(raw map[string]json.RawMessage) {
			var queue map[string]json.RawMessage
			_ = json.Unmarshal(raw["custody_ack"], &queue)
			delete(queue, "ack_pairs")
			raw["custody_ack"], _ = json.Marshal(queue)
		},
		"record ID mismatch": func(raw map[string]json.RawMessage) {
			var queue map[string]json.RawMessage
			_ = json.Unmarshal(raw["custody_ack"], &queue)
			queue["records"] = json.RawMessage(`[{"record_id":42,"status":"pending","reason":""}]`)
			raw["custody_ack"], _ = json.Marshal(queue)
		},
		"queue digest mismatch": func(raw map[string]json.RawMessage) {
			var queue map[string]json.RawMessage
			_ = json.Unmarshal(raw["custody_ack"], &queue)
			queue["ack_pairs"] = json.RawMessage(`[[41,"` + strings.Repeat("c", 64) + `"]]`)
			raw["custody_ack"], _ = json.Marshal(queue)
		},
		"required digest mismatch": func(raw map[string]json.RawMessage) {
			raw["required_acks"] = json.RawMessage(`[[41,"` + strings.Repeat("d", 64) + `"]]`)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			raw := activeACKStatusFixture()
			mutate(raw)
			if err := decodeWitnessStatus(raw, &NativeStatus{}); err == nil {
				t.Fatal("mismatched active ACK association was accepted")
			}
		})
	}
}

func TestDecodeACKRecoveryAcceptedCore1ShapeAndRejectsAssociationDrift(t *testing.T) {
	descriptor := CaptureDescriptor{
		Path:       "/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures/receipt-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json",
		Bytes:      19,
		SHA256:     strings.Repeat("b", 64),
		Generation: "native-snapshot-17",
		Ready:      false,
	}
	digest := strings.Repeat("a", 64)
	valid, _ := json.Marshal(map[string]any{
		"queued": 1, "native_release": false, "original_descriptor": descriptor,
		"ack_pairs": [][]any{{uint64(41), digest}},
	})
	recovered, err := decodeACKRecovery(valid, descriptor)
	if err != nil || recovered.Receipt != descriptor || len(recovered.Pairs) != 1 || recovered.Pairs[0] != (AckPair{RecordID: "41", RecordSHA256: digest}) {
		t.Fatalf("accepted cold recovery shape was not preserved: result=%+v err=%v", recovered, err)
	}
	wrongDescriptor := descriptor
	wrongDescriptor.SHA256 = strings.Repeat("e", 64)
	missingDescriptor, _ := json.Marshal(map[string]any{"queued": 1, "native_release": false, "ack_pairs": [][]any{{uint64(41), digest}}})
	missingPairs, _ := json.Marshal(map[string]any{"queued": 1, "native_release": false, "original_descriptor": descriptor})
	for name, tc := range map[string]struct {
		data []byte
		desc CaptureDescriptor
	}{
		"wrong original descriptor": {valid, wrongDescriptor},
		"missing original":          {missingDescriptor, descriptor},
		"missing pairs":             {missingPairs, descriptor},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeACKRecovery(tc.data, tc.desc); err == nil {
				t.Fatal("unbound cold ACK recovery was accepted")
			}
		})
	}
}

func TestStatusExportBindsExactDescriptorAndTransfer(t *testing.T) {
	digest := strings.Repeat("a", 64)
	remote := "/srv/wow/work/core3-native-earned-upgrade-harness-20261022/status/status-" + digest + ".json"
	root := "/checkout"
	local := root + "/work/core3-native-earned-upgrade-harness-20261022/receipts/status-" + digest + ".json"
	if !statusExportMatches(remote, 23, digest, local, 23, digest, root) {
		t.Fatal("matching immutable status export rejected")
	}
	if statusExportMatches(remote, 23, digest, local, 24, digest, root) || statusExportMatches(remote, 23, digest, "/tmp/status-"+digest+".json", 23, digest, root) {
		t.Fatal("mismatched status transfer accepted")
	}
}

func TestQueueObservationRequiresExactNoEffectAcknowledgement(t *testing.T) {
	const slot = "quest:17:choice:4"
	cases := []struct {
		name string
		json string
		want bool
	}{
		{"Core1 acknowledgement", `{"queued":"gear_observation","slot_key":"quest:17:choice:4","native_effect":false}`, true},
		{"unexpected native effect", `{"queued":"gear_observation","slot_key":"quest:17:choice:4","native_effect":true}`, false},
		{"missing native effect", `{"queued":"gear_observation","slot_key":"quest:17:choice:4"}`, false},
		{"null native effect", `{"queued":"gear_observation","slot_key":"quest:17:choice:4","native_effect":null}`, false},
		{"wrong slot", `{"queued":"gear_observation","slot_key":"quest:17:fixed:2","native_effect":false}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var result map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.json), &result); err != nil {
				t.Fatal(err)
			}
			if got := queueObservationResultMatches(result, slot); got != tc.want {
				t.Fatalf("queue acknowledgement match = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestDecodeACKRecoveryRequiresTypedScalarEnvelopeFields(t *testing.T) {
	descriptor := CaptureDescriptor{
		Path:       "/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures/receipt-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json",
		Bytes:      19,
		SHA256:     strings.Repeat("b", 64),
		Generation: "native-snapshot-17",
		Ready:      false,
	}
	valid, _ := json.Marshal(map[string]any{
		"queued": 1, "native_release": false, "original_descriptor": descriptor,
		"ack_pairs": [][]any{{uint64(41), strings.Repeat("a", 64)}},
	})
	tests := map[string]func(map[string]json.RawMessage){
		"missing queued": func(fields map[string]json.RawMessage) { delete(fields, "queued") },
		"null queued":    func(fields map[string]json.RawMessage) { fields["queued"] = json.RawMessage("null") },
		"string queued":  func(fields map[string]json.RawMessage) { fields["queued"] = json.RawMessage(`"1"`) },
		"fractional queued": func(fields map[string]json.RawMessage) {
			fields["queued"] = json.RawMessage("1.5")
		},
		"missing native_release": func(fields map[string]json.RawMessage) { delete(fields, "native_release") },
		"null native_release":    func(fields map[string]json.RawMessage) { fields["native_release"] = json.RawMessage("null") },
		"string native_release":  func(fields map[string]json.RawMessage) { fields["native_release"] = json.RawMessage(`"false"`) },
		"numeric native_release": func(fields map[string]json.RawMessage) { fields["native_release"] = json.RawMessage("0") },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(valid, &fields); err != nil {
				t.Fatal(err)
			}
			mutate(fields)
			malformed, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeACKRecovery(malformed, descriptor); err == nil {
				t.Fatal("missing, null, or wrongly typed recovery scalar was accepted")
			}
		})
	}
}

func TestCanceledTransportReapsOwnedProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	scriptPath := filepath.Join(root, filepath.FromSlash(candidateRemoteScript))
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(root, "child.pid")
	script := "import os, signal, subprocess, sys\n" +
		"child = subprocess.Popen([sys.executable, \"-c\", \"import time; time.sleep(60)\"])\n" +
		"def stop(_signal, _frame): raise SystemExit(143)\n" +
		"signal.signal(signal.SIGTERM, stop)\n" +
		"pid_tmp = os.environ[\"NATIVEUPGRADE_CHILD_PID\"] + \".tmp\"\n" +
		"with open(pid_tmp, \"w\") as pid_file:\n" +
		"    pid_file.write(str(child.pid))\n" +
		"os.replace(pid_tmp, os.environ[\"NATIVEUPGRADE_CHILD_PID\"])\n" +
		"try:\n" +
		"    child.wait()\n" +
		"except BaseException:\n" +
		"    if child.poll() is None:\n" +
		"        child.terminate()\n" +
		"        try: child.wait(timeout=2)\n" +
		"        except subprocess.TimeoutExpired:\n" +
		"            child.kill()\n" +
		"            child.wait()\n" +
		"    raise\n" +
		"print(\"{}\")\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NATIVEUPGRADE_CHILD_PID", pidPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})
	var childPID int
	readChildPID := func() int {
		data, err := os.ReadFile(pidPath)
		if err != nil {
			return 0
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 0 {
			return 0
		}
		return pid
	}
	processState := func(pid int) (string, error) {
		stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			return "", err
		}
		fields := strings.Fields(string(stat))
		if len(fields) < 3 {
			return "?", errors.New("malformed proc stat")
		}
		return fields[2], nil
	}
	waitGone := func(pid int, duration time.Duration) bool {
		deadline := time.Now().Add(duration)
		for time.Now().Before(deadline) {
			_, err := processState(pid)
			if errors.Is(err, os.ErrNotExist) {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	go func() {
		_, err := (&RemoteLifecycle{root: root}).invoke(ctx, "export_status", map[string]any{})
		done <- err
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Errorf("owned transport invocation did not finish during cleanup")
		}
		if childPID == 0 {
			childPID = readChildPID()
		}
		if childPID == 0 {
			return
		}
		if waitGone(childPID, time.Second) {
			return
		}
		state, stateErr := processState(childPID)
		if stateErr == nil && state != "Z" {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
		if !waitGone(childPID, time.Second) {
			t.Errorf("owned child %d survived cleanup (state=%q)", childPID, state)
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		childPID = readChildPID()
		if childPID > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("transport did not start its child process")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("transport cancellation lost its cause: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled transport did not reap its direct child")
	}
	if !waitGone(childPID, 2*time.Second) {
		state, _ := processState(childPID)
		t.Fatalf("transport child %d survived cancellation (state=%q)", childPID, state)
	}

}
