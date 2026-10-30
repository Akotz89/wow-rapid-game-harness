package nativeupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func inputsFixture() ScenarioInputs {
	return ScenarioInputs{AccountID: 12, CharacterGUID: 34, IdentityKey: "identity-12", SlotKey: "quest:17:choice:4", SnapshotID: "native-snapshot-17", ExporterRevision: strings.Repeat("c", 64), SourceID: "quest-source-17", StrategicGeneration: 9, Session: 1, GearInputsPath: "work/core1-native-earned-upgrade-delivery-20261021/gear-inputs.json", GearInputsSHA256: strings.Repeat("a", 64)}
}

func observationFixture() NativeObservation {
	in := inputsFixture()
	gear := nestedGear(in, 1, 111, 10, 9876, 12)
	return NativeObservation{Status: "observed", Actor: in.IdentityKey, AccountID: in.AccountID, CharacterGUID: in.CharacterGUID, Source: in.SourceID, Generation: in.StrategicGeneration, Session: in.Session, SlotKey: in.SlotKey, Gear: gear}
}

func nestedGear(in ScenarioInputs, session uint64, currentItem uint32, currentScore float64, rewardItem uint32, candidateScore float64) json.RawMessage {
	equipment := make([]map[string]any, 19)
	for slot := range equipment {
		entry := uint32(0)
		if slot == 4 {
			entry = currentItem
		}
		equipment[slot] = map[string]any{"slot": slot, "item": map[string]any{"entry": entry}}
	}
	currentValid, currentReason := true, ""
	if currentItem == 0 {
		currentValid, currentReason, currentScore = false, "empty_slot", 0
	}
	gear := map[string]any{
		"identity_key": in.IdentityKey, "account_id": in.AccountID, "character_guid": in.CharacterGUID,
		"session_incarnation": session, "strategic_generation": in.StrategicGeneration,
		"equipment": equipment,
		"quests": []any{map[string]any{"quest_id": 17, "rewards": []any{map[string]any{
			"kind": "choice", "index": 4, "item": map[string]any{"entry": rewardItem}, "slots": []any{map[string]any{
				"slot": 4, "current": map[string]any{"entry": currentItem}, "candidate_score": candidateScore,
				"candidate_score_valid": true, "current_score": currentScore, "current_score_valid": currentValid,
				"current_score_reason": currentReason,
			}},
		}}}},
	}
	encoded, _ := json.Marshal(gear)
	return encoded
}

func observedStatus(mode string, observation NativeObservation) NativeStatus {
	return NativeStatus{Mode: mode, Observation: observation, ObservationPendingKnown: true}
}

func observedStatusSession(mode string, observation NativeObservation, session uint64) NativeStatus {
	observation.Session = session
	observation.Gear = nestedGear(inputsFixture(), session, 9876, 12, 9876, 12)
	return observedStatus(mode, observation)
}

type scriptedLifecycle struct {
	calls              int
	statuses           []NativeStatus
	statusAt           int
	acquisition        NativeAcquisition
	ackRecovery        AckRecovery
	ackSettlement      AckSettlement
	settleErr          error
	prepareErr         error
	modeErr            error
	statusErrAt        int
	statusErr          error
	ackApplied         bool
	quiescent          bool
	wrongAck           bool
	queuedSlot         string
	retained           CaptureDescriptor
	drainErr           error
	drainCalls         int
	releaseCalls       int
	prepareCalls       int
	exportCalls        int
	modeCalls          int
	released           bool
	prepared           bool
	settled            bool
	settleCalls        int
	order              []string
	cancel             context.CancelFunc
	cancelAfterPrepare context.CancelFunc
	events             []string
}

func (s *scriptedLifecycle) ExportStatus(ctx context.Context) (NativeStatus, error) {
	s.calls++
	s.exportCalls++
	if err := ctx.Err(); err != nil {
		return NativeStatus{}, err
	}
	if s.statusErr != nil && s.statusAt == s.statusErrAt {
		s.statusAt++
		return NativeStatus{}, s.statusErr
	}
	if len(s.statuses) == 0 {
		return NativeStatus{}, errors.New("unexpected status call")
	}
	i := s.statusAt
	if i >= len(s.statuses) {
		i = len(s.statuses) - 1
	} else {
		s.statusAt++
	}
	if s.statuses[i].CaptureQuiescent && !s.statuses[i].CaptureRetained {
		s.events = append(s.events, "status_quiescent")
	}
	return s.statuses[i], nil
}
func (s *scriptedLifecycle) QueueObservation(ctx context.Context, slot string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.calls++
	s.order = append(s.order, "queue")
	s.queuedSlot = slot
	return nil
}
func (s *scriptedLifecycle) StageGear(ctx context.Context, _, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.calls++
	s.order = append(s.order, "stage")
	return nil
}
func (s *scriptedLifecycle) ArmCapture(ctx context.Context, _ CaptureArm) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.calls++
	if s.cancel != nil {
		s.retained = s.acquisition.Receipt
		s.cancel()
	}
	return nil
}
func (s *scriptedLifecycle) SetMode(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.calls++
	s.modeCalls++
	return s.modeErr
}
func (s *scriptedLifecycle) DrainCapture(ctx context.Context) (NativeAcquisition, error) {
	if err := ctx.Err(); err != nil {
		return NativeAcquisition{}, err
	}
	s.calls++
	s.drainCalls++
	s.retained = s.acquisition.Receipt
	s.events = append(s.events, "drain")
	if s.acquisition.Receipt.Path == "" {
		return NativeAcquisition{}, errors.New("no retained receipt")
	}
	return s.acquisition, s.drainErr
}
func (s *scriptedLifecycle) RecoverCapture(ctx context.Context, _ CaptureDescriptor) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.calls++
	s.events = append(s.events, "recover_capture")
	return nil
}
func (s *scriptedLifecycle) RecoverAck(ctx context.Context, _ CaptureDescriptor) (AckRecovery, error) {
	if err := ctx.Err(); err != nil {
		return AckRecovery{}, err
	}
	s.calls++
	s.events = append(s.events, "recover_ack")
	return s.ackRecovery, nil
}
func (s *scriptedLifecycle) ReleaseCapture(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.calls++
	s.releaseCalls++
	s.events = append(s.events, "release")
	s.quiescent = true
	s.released = true
	return nil
}
func (s *scriptedLifecycle) PrepareAck(ctx context.Context, _ CaptureDescriptor) ([]AckPair, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.calls++
	s.prepareCalls++
	s.events = append(s.events, "prepare_ack")
	s.prepared = true
	pairs := []AckPair{{RecordID: "ack-1", RecordSHA256: strings.Repeat("d", 64)}}
	if s.cancelAfterPrepare != nil {
		s.cancelAfterPrepare()
	}
	return pairs, s.prepareErr
}
func (s *scriptedLifecycle) SettleAck(ctx context.Context) (AckSettlement, error) {
	if err := ctx.Err(); err != nil {
		return AckSettlement{}, err
	}
	s.calls++
	s.settleCalls++
	s.events = append(s.events, "settle_ack")
	s.settled = true
	return s.ackSettlement, s.settleErr
}

func TestRunOptInTypedPositiveAndCleanupAfterDrainFailure(t *testing.T) {
	in := inputsFixture()
	c := completeContract()
	p := ProducerResult{Verified: true, Oracle: "sealed original raw/native export", SnapshotID: in.SnapshotID}
	trace, err := RunOptIn(context.Background(), c, in, completeScript(), p)
	if err != nil || len(trace) != 10 {
		t.Fatalf("typed opt-in fixture failed: count=%d err=%v", len(trace), err)
	}
	ops := completeScript()
	ops.drainErr = errors.New("synthetic partial drain mapping failure")
	ops.statuses = []NativeStatus{
		observedStatus("observe", observationFixture()),
		observedStatus("observe", observationFixture()),
		observedStatus("observe", observationFixture()),
		observedStatus("observe", observationFixture()),
		observedStatus("gear", observationFixture()),
		observedStatus("gear", observationFixture()),
		{CaptureQuiescent: true, RequiredACKs: fixtureACKs()},
		{CaptureQuiescent: true, RequiredACKs: fixtureACKs()},
		{ACKQueueActive: true, RequiredACKs: fixtureACKs(), ACKRecordIDs: []string{"ack-1"}},
		{ACKQueueActive: true, RequiredACKs: fixtureACKs(), AppliedACKs: fixtureACKs(), ACKRecordIDs: []string{"ack-1"}},
		{ACKQueueActive: true, RequiredACKs: fixtureACKs(), AppliedACKs: fixtureACKs(), ACKRecordIDs: []string{"ack-1"}},
	}
	_, err = RunScenario(context.Background(), ops, in, p)
	if err == nil || !ops.released || !ops.prepared || !ops.settled {
		t.Fatalf("cleanup did not preserve and settle original: err=%v released=%t prepared=%t settled=%t", err, ops.released, ops.prepared, ops.settled)
	}
}

func completeScript() *scriptedLifecycle {
	in := inputsFixture()
	desc := CaptureDescriptor{Path: "/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures/receipt-" + strings.Repeat("b", 32) + ".json", Bytes: 100, SHA256: strings.Repeat("a", 64), Generation: in.SnapshotID, Ready: false}
	base := observationFixture()
	improved := base
	improved.Gear = nestedGear(in, 1, 9876, 12, 9876, 12)
	statuses := []NativeStatus{
		observedStatus("observe", base),
		observedStatus("observe", base),
		observedStatus("observe", base),
		observedStatus("observe", base),
		observedStatus("gear", base),
		observedStatus("gear", base),
		{CaptureQuiescent: true, RequiredACKs: fixtureACKs()},
		{CaptureQuiescent: true, RequiredACKs: fixtureACKs()},
		{ACKQueueActive: true, RequiredACKs: fixtureACKs(), ACKRecordIDs: []string{"ack-1"}},
		{ACKQueueActive: true, RequiredACKs: fixtureACKs(), AppliedACKs: fixtureACKs(), ACKRecordIDs: []string{"ack-1"}},
		{ACKQueueActive: true, RequiredACKs: fixtureACKs(), AppliedACKs: fixtureACKs(), ACKRecordIDs: []string{"ack-1"}},
		observedStatus("observe", improved),
		observedStatus("observe", improved),
		{ACKQueueEmpty: true, RequiredACKs: fixtureACKs(), AppliedACKs: fixtureACKs(), RelogLifecycle: "online_new_session_observed", RelogSession: 2, Gear: nestedGear(in, 2, 9876, 12, 9876, 12)},
		{ACKQueueEmpty: true, RequiredACKs: fixtureACKs(), AppliedACKs: fixtureACKs(), RelogLifecycle: "online_new_session_observed", RelogSession: 2, Gear: nestedGear(in, 2, 9876, 12, 9876, 12)},
		{ACKQueueEmpty: true, RequiredACKs: fixtureACKs(), AppliedACKs: fixtureACKs(), ObservationPendingKnown: true, ObservationPending: true, PendingObservationSlot: in.SlotKey, PendingObservationSource: in.SourceID},
		observedStatusSession("observe", improved, 2), observedStatusSession("observe", improved, 2),
	}
	pending := NativeStatus{Mode: "observe", ObservationPendingKnown: true, ObservationPending: true,
		PendingObservationSlot: in.SlotKey, PendingObservationSource: in.SourceID}
	statuses[1] = pending
	return &scriptedLifecycle{
		statuses:      statuses,
		acquisition:   NativeAcquisition{CaptureKind: "native_execution", Actor: in.IdentityKey, Source: in.SourceID, Generation: in.StrategicGeneration, Session: in.Session, SnapshotID: in.SnapshotID, SlotKey: in.SlotKey, ItemID: 9876, Receipt: desc, ACKPairs: fixtureACKs()},
		ackRecovery:   AckRecovery{Receipt: desc, Pairs: fixtureACKs()},
		ackSettlement: AckSettlement{QueueEmpty: true, Required: fixtureACKs(), Applied: fixtureACKs()},
	}
}

func fixtureACKs() []AckPair {
	return []AckPair{{RecordID: "ack-1", RecordSHA256: strings.Repeat("d", 64)}}
}

func TestTypedLifecyclePositiveAndExactOrder(t *testing.T) {
	ops := completeScript()
	producer := ProducerResult{Verified: true, Oracle: "sealed original raw/native export", SnapshotID: inputsFixture().SnapshotID}
	trace, err := RunScenario(context.Background(), ops, inputsFixture(), producer)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySequence(trace); err != nil {
		t.Fatal(err)
	}
	if ops.queuedSlot != inputsFixture().SlotKey {
		t.Fatal("wrong exact slot queued")
	}
	if len(ops.order) < 2 || ops.order[0] != "stage" || ops.order[1] != "queue" {
		t.Fatalf("gear policy must be staged before its slot observation: %v", ops.order)
	}
}

func TestTypedLifecycleRejectsUnappliedOrUnsettledACK(t *testing.T) {
	ops := completeScript()
	ops.statuses[9] = NativeStatus{ACKQueueActive: true, RequiredACKs: fixtureACKs(), ACKRecordIDs: []string{"ack-1"}}
	ops.statuses[10] = NativeStatus{ACKQueueActive: true, RequiredACKs: fixtureACKs(), ACKRecordIDs: []string{"ack-1"}}
	oldInterval := statusPollInterval
	statusPollInterval = time.Millisecond
	defer func() { statusPollInterval = oldInterval }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := RunScenario(ctx, ops, inputsFixture(), ProducerResult{Verified: true, Oracle: "sealed original raw/native export", SnapshotID: inputsFixture().SnapshotID}); err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "tick-applied") {
		t.Fatalf("unapplied ACK accepted: %v", err)
	}
	ops = completeScript()
	ops.ackSettlement = AckSettlement{QueueEmpty: false, Required: fixtureACKs(), Applied: fixtureACKs()}
	if _, err := RunScenario(context.Background(), ops, inputsFixture(), ProducerResult{Verified: true, Oracle: "sealed original raw/native export", SnapshotID: inputsFixture().SnapshotID}); err == nil || !strings.Contains(err.Error(), "settlement incomplete") || ops.settleCalls != 1 {
		t.Fatalf("unsettled ACK accepted: %v", err)
	}
	ops = completeScript()
	ops.settleErr = errors.New("settlement response lost")
	if _, err := RunScenario(context.Background(), ops, inputsFixture(), ProducerResult{Verified: true, Oracle: "sealed original raw/native export", SnapshotID: inputsFixture().SnapshotID}); err == nil || !strings.Contains(err.Error(), "settlement response lost") || ops.settleCalls != 1 {
		t.Fatalf("ambiguous ACK settlement was retried: calls=%d err=%v", ops.settleCalls, err)
	}
}

func TestWaitStatusPollsPendingSnapshotsBeforeSuccess(t *testing.T) {
	ops := &scriptedLifecycle{statuses: []NativeStatus{{Mode: "observe"}, {Mode: "gear"}, {Mode: "gear"}}}
	status, err := waitStatus(context.Background(), ops, func(s NativeStatus) bool { return s.Mode == "gear" })
	if err != nil || status.Mode != "gear" || ops.statusAt < 3 {
		t.Fatalf("status polling did not see pending then stable success: %+v calls=%d err=%v", status, ops.statusAt, err)
	}
}

func TestWaitStatusHonorsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ops := &scriptedLifecycle{}
	if _, err := waitStatus(ctx, ops, func(NativeStatus) bool { return true }); !errors.Is(err, context.Canceled) || ops.statusAt != 0 {
		t.Fatalf("waitStatus did not honor caller cancellation: calls=%d err=%v", ops.statusAt, err)
	}
}

func TestTypedLifecyclePreservesDescriptorOnResume(t *testing.T) {
	ops := completeScript()
	first := ops.acquisition.Receipt
	trace, err := RunScenario(context.Background(), ops, inputsFixture(), ProducerResult{Verified: true, Oracle: "sealed original raw/native export", SnapshotID: inputsFixture().SnapshotID})
	if err != nil {
		t.Fatal(err)
	}
	if trace[1].ReceiptPath != first.Path || trace[1].ReceiptSHA256 != first.SHA256 {
		t.Fatal("resume replaced original receipt identity")
	}
}

func TestColdResumeReleasesAndConfirmsQuiescenceBeforeACKRecovery(t *testing.T) {
	ops := completeScript()
	in := inputsFixture()
	in.ResumeReceipt = ops.acquisition.Receipt
	ops.statuses = []NativeStatus{
		{}, // preflight sees no in-memory owner; resume descriptor is explicit
		{}, // recovery status before the fixed recovery wrappers
		{CaptureQuiescent: true}, {CaptureQuiescent: true},
		{ACKQueueActive: true, ACKRecordIDs: []string{"ack-1"}, RequiredACKs: fixtureACKs(), AppliedACKs: fixtureACKs()},
		{ACKQueueActive: true, ACKRecordIDs: []string{"ack-1"}, RequiredACKs: fixtureACKs(), AppliedACKs: fixtureACKs()},
	}
	_, err := RunScenario(context.Background(), ops, in, ProducerResult{})
	if err != nil || !ops.settled || ops.queuedSlot != "" {
		t.Fatalf("cold resume did not recover and settle exact receipt ACK: err=%v settled=%t queued=%q", err, ops.settled, ops.queuedSlot)
	}
	position := func(name string) int {
		for i, event := range ops.events {
			if event == name {
				return i
			}
		}
		return -1
	}
	if position("release") < 0 || position("status_quiescent") <= position("release") || position("recover_ack") <= position("status_quiescent") {
		t.Fatalf("cold recovery ACK preceded release and confirmed quiescence: %v", ops.events)
	}
	if len(ops.order) != 0 {
		t.Fatalf("resume unexpectedly staged/queued new gear work: %v", ops.order)
	}
}

func TestMinimalDescriptorResumePrecedesNewRunInputs(t *testing.T) {
	ops := completeScript()
	descriptor := ops.acquisition.Receipt
	ops.statuses = []NativeStatus{{}, {}, {CaptureQuiescent: true}, {CaptureQuiescent: true},
		{ACKQueueActive: true, ACKRecordIDs: []string{"ack-1"}, RequiredACKs: fixtureACKs(), AppliedACKs: fixtureACKs()},
		{ACKQueueActive: true, ACKRecordIDs: []string{"ack-1"}, RequiredACKs: fixtureACKs(), AppliedACKs: fixtureACKs()}}
	_, err := RunOptIn(context.Background(), AcceptedContract{}, ScenarioInputs{ResumeReceipt: descriptor}, ops, ProducerResult{})
	if err != nil || !ops.settled || ops.queuedSlot != "" || len(ops.order) != 0 {
		t.Fatalf("minimal descriptor did not recover before new-run inputs: err=%v settled=%t queued=%q ops=%v", err, ops.settled, ops.queuedSlot, ops.order)
	}
}

func TestColdResumeRefusesACKRecoveryWithoutDescriptorBinding(t *testing.T) {
	ops := completeScript()
	in := inputsFixture()
	in.ResumeReceipt = ops.acquisition.Receipt
	ops.ackRecovery = AckRecovery{} // A recovery result must bind the exact descriptor and ACK pairs.
	ops.statuses = []NativeStatus{{}, {}, {CaptureQuiescent: true}, {CaptureQuiescent: true}}
	_, err := RunScenario(context.Background(), ops, in, ProducerResult{})
	if err == nil || !strings.Contains(err.Error(), "not bound to the supplied original descriptor and ACK pairs") || ops.settled {
		t.Fatalf("unbound recovered ACK queue was settled: err=%v settled=%t", err, ops.settled)
	}
}

func TestColdResumeRejectsACKPairDriftBeforeSettlement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pairs []AckPair
	}{
		{"record ID mismatch", []AckPair{{RecordID: "wrong-id", RecordSHA256: strings.Repeat("d", 64)}}},
		{"record digest mismatch", []AckPair{{RecordID: "ack-1", RecordSHA256: strings.Repeat("e", 64)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := completeScript()
			in := inputsFixture()
			in.ResumeReceipt = ops.acquisition.Receipt
			ops.ackRecovery.Pairs = tc.pairs
			ops.statuses = []NativeStatus{{}, {}, {CaptureQuiescent: true}, {CaptureQuiescent: true}}
			_, err := RunScenario(context.Background(), ops, in, ProducerResult{})
			if err == nil || !strings.Contains(err.Error(), "not bound to the supplied original descriptor and ACK pairs") || ops.settled {
				t.Fatalf("mismatched recovered ACK pair was settled: err=%v settled=%t", err, ops.settled)
			}
		})
	}
}

func TestPreflightRefusesActiveACKWithoutOriginalDescriptorBinding(t *testing.T) {
	ops := &scriptedLifecycle{statuses: []NativeStatus{{ACKQueueActive: true, RequiredACKs: fixtureACKs(), ACKRecordIDs: []string{"ack-1"}}}}
	handled, err := PreflightRecovery(context.Background(), ops, ScenarioInputs{})
	if !handled || err == nil || !strings.Contains(err.Error(), "original descriptor binding") || ops.settled {
		t.Fatalf("active queue without exact original binding was settled: handled=%t err=%v", handled, err)
	}
}

func TestPreflightRefusesActiveACKBoundToDifferentDescriptor(t *testing.T) {
	expected := completeScript().acquisition.Receipt
	other := expected
	other.SHA256 = strings.Repeat("e", 64)
	status := NativeStatus{ACKQueueActive: true, ACKOriginal: &other, ACKRecordIDs: []string{"ack-1"}, RequiredACKs: fixtureACKs()}
	ops := &scriptedLifecycle{statuses: []NativeStatus{status, status}}
	handled, err := PreflightRecovery(context.Background(), ops, ScenarioInputs{ResumeReceipt: expected})
	if !handled || err == nil || !strings.Contains(err.Error(), "original descriptor differs") || ops.settled {
		t.Fatalf("active queue bound to another receipt was settled: handled=%t err=%v", handled, err)
	}
}

func TestActiveACKQueueRejectsRecordIDPairMismatch(t *testing.T) {
	descriptor := completeScript().acquisition.Receipt
	status := NativeStatus{ACKQueueActive: true, ACKOriginal: &descriptor, ACKRecordIDs: []string{"wrong-id"}, RequiredACKs: fixtureACKs()}
	ops := &scriptedLifecycle{statuses: []NativeStatus{status, status}}
	handled, err := PreflightRecovery(context.Background(), ops, ScenarioInputs{ResumeReceipt: descriptor})
	if !handled || err == nil || !strings.Contains(err.Error(), "record IDs do not exactly match") || ops.settled {
		t.Fatalf("active queue record/pair mismatch was not refused: handled=%t err=%v settled=%t", handled, err, ops.settled)
	}
}

func TestCanceledArmWithoutDeadlineReturnsRecoveryState(t *testing.T) {
	ops := completeScript()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ops.cancel = cancel
	in := inputsFixture()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("test context unexpectedly has a deadline")
	}
	done := make(chan error, 1)
	go func() {
		_, err := RunScenario(ctx, ops, in, ProducerResult{})
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled no-deadline run did not return")
	}
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "operation=arm_capture") ||
		!strings.Contains(err.Error(), "original_descriptor={unknown}") ||
		!strings.Contains(err.Error(), "ack=not_prepared") ||
		!strings.Contains(err.Error(), "actor=\"identity-12\"") ||
		!strings.Contains(err.Error(), "source=\"quest-source-17\"") {
		t.Fatalf("canceled arm did not report exact recovery boundary: %v", err)
	}
	if ops.retained.Path == "" || ops.drainCalls != 0 || ops.released || ops.prepareCalls != 0 || ops.settleCalls != 0 || ops.modeCalls != 0 {
		t.Fatalf("cancellation mutated or hid pending capture state: retained=%+v drain=%d released=%t prepare=%d settle=%d mode=%d",
			ops.retained, ops.drainCalls, ops.released, ops.prepareCalls, ops.settleCalls, ops.modeCalls)
	}
}

func TestCanceledAfterACKPreparationPreservesDescriptorAndPairs(t *testing.T) {
	ops := completeScript()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ops.cancelAfterPrepare = cancel
	in := inputsFixture()
	done := make(chan error, 1)
	go func() {
		_, err := RunScenario(ctx, ops, in, ProducerResult{})
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled pending-ACK run did not return")
	}
	desc := ops.acquisition.Receipt
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "operation=wait_ack_application") ||
		!strings.Contains(err.Error(), desc.Path) || !strings.Contains(err.Error(), desc.SHA256) ||
		!strings.Contains(err.Error(), "ack=exact_pairs=[{ack-1") {
		t.Fatalf("pending ACK recovery details were lost: %v", err)
	}
	if !ops.released || !ops.prepared || ops.prepareCalls != 1 || ops.settleCalls != 0 || ops.retained != desc {
		t.Fatalf("pending ACK was retried, settled, or discarded: released=%t prepared=%t prepareCalls=%d settleCalls=%d retained=%+v",
			ops.released, ops.prepared, ops.prepareCalls, ops.settleCalls, ops.retained)
	}
}

func TestCleanupRequiresUnretainedQuiescenceBeforePreparingACK(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    NativeStatus
		statusErr error
		timeout   bool
	}{
		{name: "status export error", statusErr: errors.New("status export failed")},
		{name: "capture still retained", status: NativeStatus{CaptureQuiescent: true, CaptureRetained: true}, timeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := completeScript()
			in := inputsFixture()
			ops.modeErr = errors.New("stop after arm")
			ops.statuses = []NativeStatus{
				observedStatus("observe", observationFixture()),
				{Mode: "observe", ObservationPendingKnown: true, ObservationPending: true, PendingObservationSlot: in.SlotKey, PendingObservationSource: in.SourceID},
				observedStatus("observe", observationFixture()),
				observedStatus("observe", observationFixture()),
				tc.status,
			}
			if tc.statusErr != nil {
				ops.statusErrAt, ops.statusErr = 4, tc.statusErr
			}
			ctx := context.Background()
			if tc.timeout {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 500*time.Millisecond)
				defer cancel()
			}
			_, err := RunScenario(ctx, ops, in, ProducerResult{})
			if err == nil || !strings.Contains(err.Error(), "native custody recovery required") ||
				!strings.Contains(err.Error(), "operation=wait_capture_quiescence") {
				t.Fatalf("incomplete quiescence was not reported for recovery: %v", err)
			}
			if !ops.released || ops.prepareCalls != 0 || ops.settled || ops.settleCalls != 0 {
				t.Fatalf("ACK preparation crossed the quiescence gate: released=%t prepare=%d settled=%t settleCalls=%d",
					ops.released, ops.prepareCalls, ops.settled, ops.settleCalls)
			}
		})
	}
}

func TestNestedGearMapsExactQuestRewardBaselineAndEquipment(t *testing.T) {
	in := inputsFixture()
	before, err := decodeGear(nestedGear(in, 1, 111, 10, 9876, 12))
	if err != nil || !nativeActorMatches(before, in, 1) {
		t.Fatalf("baseline gear did not preserve actor binding: %v", err)
	}
	quest, kind, index, item, err := selectedReward(before, in.SlotKey)
	if err != nil || quest != 17 || kind != "choice" || index != 4 || item != 9876 {
		t.Fatalf("selected quest/reward mapping failed: %d %s %d %d %v", quest, kind, index, item, err)
	}
	slot, baseline, err := selectedEquipment(before, in.SlotKey, item)
	if err != nil || slot != 4 || baseline != 10 {
		t.Fatalf("baseline equipment/score mapping failed: slot=%d score=%v err=%v", slot, baseline, err)
	}
	after, err := decodeGear(nestedGear(in, 1, 9876, 12, 9876, 12))
	if err != nil {
		t.Fatal(err)
	}
	score, err := equippedScore(after, in.SlotKey, slot, item)
	if err != nil || score <= baseline {
		t.Fatalf("exact acquired equipment improvement missing: score=%v baseline=%v err=%v", score, baseline, err)
	}
}

func TestNestedGearRejectsWrongItemAndInvalidScores(t *testing.T) {
	in := inputsFixture()
	wrongItem, err := decodeGear(nestedGear(in, 1, 111, 10, 1234, 12))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := selectedEquipment(wrongItem, in.SlotKey, 9876); err == nil {
		t.Fatal("selected reward item mismatch was accepted")
	}
	invalid := nestedGear(in, 1, 111, 10, 9876, 12)
	var value map[string]any
	if err := json.Unmarshal(invalid, &value); err != nil {
		t.Fatal(err)
	}
	quests := value["quests"].([]any)
	rewards := quests[0].(map[string]any)["rewards"].([]any)
	slots := rewards[0].(map[string]any)["slots"].([]any)
	slots[0].(map[string]any)["current_score_valid"] = false
	invalid, _ = json.Marshal(value)
	gear, err := decodeGear(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := selectedEquipment(gear, in.SlotKey, 9876); err == nil {
		t.Fatal("invalid baseline score was accepted")
	}
	if _, err := equippedScore(gear, in.SlotKey, 4, 9876); err == nil {
		t.Fatal("invalid post-acquisition current score was accepted")
	}
}

func TestNestedGearRequiresSameItemSlotAndSessionAfterRelog(t *testing.T) {
	in := inputsFixture()
	gear, err := decodeGear(nestedGear(in, 2, 9876, 12, 9876, 12))
	if err != nil || !nativeActorMatches(gear, in, 2) || gear.Session == in.Session {
		t.Fatalf("new session binding missing: gear=%+v err=%v", gear, err)
	}
	if _, err := equippedScore(gear, in.SlotKey, 4, 9876); err != nil {
		t.Fatal(err)
	}
	if _, err := equippedScore(gear, in.SlotKey, 5, 9876); err == nil {
		t.Fatal("relogged item in a different slot was accepted")
	}
	gear, err = decodeGear(nestedGear(in, 2, 111, 10, 9876, 12))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := equippedScore(gear, in.SlotKey, 4, 9876); err == nil {
		t.Fatal("missing post-relog equipped item was accepted")
	}
}

func TestNestedGearPreservesExplicitEmptySlotScoreSemantics(t *testing.T) {
	in := inputsFixture()
	gear, err := decodeGear(nestedGear(in, 1, 0, 0, 9876, 12))
	if err != nil {
		t.Fatal(err)
	}
	slot, score, err := selectedEquipment(gear, in.SlotKey, 9876)
	if err != nil || slot != 4 || score != 0 {
		t.Fatalf("valid empty-slot baseline rejected: slot=%d score=%v err=%v", slot, score, err)
	}
}

func TestNestedGearRejectsMissingScoreValidityAndWrongActor(t *testing.T) {
	in := inputsFixture()
	data := nestedGear(in, 1, 111, 10, 9876, 12)
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	quests := value["quests"].([]any)
	rewards := quests[0].(map[string]any)["rewards"].([]any)
	slots := rewards[0].(map[string]any)["slots"].([]any)
	delete(slots[0].(map[string]any), "current_score_valid")
	data, _ = json.Marshal(value)
	gear, err := decodeGear(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := selectedEquipment(gear, in.SlotKey, 9876); err == nil {
		t.Fatal("missing score-validity field was accepted")
	}
	gear, err = decodeGear(nestedGear(in, 1, 111, 10, 9876, 12))
	if err != nil {
		t.Fatal(err)
	}
	gear.IdentityKey = "different-actor"
	if nativeActorMatches(gear, in, 1) {
		t.Fatal("wrong nested actor identity was accepted")
	}
}
