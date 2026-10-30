package nativeupgrade

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var completeOps = []string{"witness_status", "export_status", "queue_native_gear_observation", "stage_accepted_gear", "arm_native_quest_capture", "drain_acquisition_capture", "release_acquisition_capture", "witness_command", "request_witness_session", "prepare_native_custody_ack", "settle_native_custody_ack", "recover_native_quest_capture", "recover_native_custody_ack"}

func completeContract() AcceptedContract {
	return AcceptedContract{Operations: completeOps, GearInputsPath: "work/core1-native-earned-upgrade-delivery-20261021/gear-inputs.json", GearInputsSHA256: strings.Repeat("a", 64), ManifestPath: "work/core1-native-earned-upgrade-delivery-20261021/manifest.json", ManifestSHA256: strings.Repeat("b", 64), EvidenceOutputDir: "work/core1-native-earned-upgrade-delivery-20261021/evidence"}
}

func TestMissingInputsFailBeforeLifecycleCalls(t *testing.T) {
	contract := completeContract()
	contract.Operations = contract.Operations[:8]
	contract.GearInputsPath = ""
	ops := &scriptedLifecycle{}
	_, err := RunOptIn(context.Background(), contract, ScenarioInputs{}, ops, ProducerResult{})
	if !errors.Is(err, ErrAcceptedRuntimePrerequisite) {
		t.Fatalf("unexpected result: %v", err)
	}
	if ops.calls != 0 {
		t.Fatalf("missing prerequisites caused %d native calls", ops.calls)
	}
}

func TestOptInRejectsGearAndVerifierPathsOutsideCore1Root(t *testing.T) {
	c := completeContract()
	c.GearInputsPath = "../outside/gear.json"
	_, err := RunOptIn(context.Background(), c, ScenarioInputs{}, &scriptedLifecycle{}, ProducerResult{})
	if !errors.Is(err, ErrAcceptedRuntimePrerequisite) || !strings.Contains(err.Error(), "GearInputs path") {
		t.Fatalf("unsafe path not rejected: %v", err)
	}
}

func TestTypedAdapterPollsPendingThenRefusesMissingIdentity(t *testing.T) {
	in := inputsFixture()
	base := observationFixture()
	base.AccountID = 0 // Core1 observation omits actor binding; absence stays unknown.
	ops := completeScript()
	ops.statuses = []NativeStatus{
		{Mode: "observe", ObservationPendingKnown: true, ObservationPending: true, PendingObservationSlot: in.SlotKey, PendingObservationSource: in.SourceID},
		observedStatus("observe", base), observedStatus("observe", base),
	}
	oldInterval := statusPollInterval
	statusPollInterval = time.Millisecond
	defer func() { statusPollInterval = oldInterval }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := RunScenario(ctx, ops, in, ProducerResult{Verified: true, Oracle: "sealed original raw/native export", SnapshotID: in.SnapshotID})
	if err == nil || !strings.Contains(err.Error(), "selected-slot observation") {
		t.Fatalf("missing actor binding must remain unknown and refuse: %v", err)
	}
	if ops.queuedSlot != in.SlotKey || ops.statusAt < 3 {
		t.Fatalf("did not queue exact slot and poll pending/completion snapshots: slot=%q polls=%d", ops.queuedSlot, ops.statusAt)
	}
}
