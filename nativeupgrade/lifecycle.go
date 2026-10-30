package nativeupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Lifecycle is the closed set of Core1 operations used by this scenario.
// Implementations must bind these methods to the candidate's fixed wrapper
// names; there is no command-string or caller-path parameter.
type Lifecycle interface {
	ExportStatus(context.Context) (NativeStatus, error)
	QueueObservation(context.Context, string) error
	StageGear(context.Context, string, string) error
	ArmCapture(context.Context, CaptureArm) error
	SetMode(context.Context, string) error
	DrainCapture(context.Context) (NativeAcquisition, error)
	RecoverCapture(context.Context, CaptureDescriptor) error
	ReleaseCapture(context.Context) error
	PrepareAck(context.Context, CaptureDescriptor) ([]AckPair, error)
	RecoverAck(context.Context, CaptureDescriptor) (AckRecovery, error)
	SettleAck(context.Context) (AckSettlement, error)
}

type CaptureArm struct {
	IdentityKey      string
	AccountID        uint64
	CharacterGUID    uint64
	SlotKey          string
	SnapshotID       string
	ExporterRevision string
}

type CaptureDescriptor struct {
	Path       string `json:"path"`
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256"`
	Generation string `json:"generation"`
	Ready      bool   `json:"ready"`
}

type NativeObservation struct {
	Status        string          `json:"status"`
	Actor         string          `json:"identity_key"`
	AccountID     uint64          `json:"account_id"`
	CharacterGUID uint64          `json:"character_guid"`
	Source        string          `json:"source_id"`
	Generation    uint64          `json:"strategic_generation"`
	Session       uint64          `json:"session_incarnation"`
	SlotKey       string          `json:"slot_key"`
	Gear          json.RawMessage `json:"gear"`
}

type NativeStatus struct {
	Mode                     string             `json:"mode"`
	Observation              NativeObservation  `json:"gear_observation"`
	CaptureRetained          bool               `json:"capture_retained"`
	ACKQueueActive           bool               `json:"ack_queue_active"`
	CaptureQuiescent         bool               `json:"capture_quiescent"`
	RequiredACKs             []AckPair          `json:"required_acks"`
	AppliedACKs              []AckPair          `json:"applied_acks"`
	ACKRecordIDs             []string           `json:"ack_record_ids"`
	ACKQueueEmpty            bool               `json:"ack_queue_empty"`
	ACKOriginal              *CaptureDescriptor `json:"ack_original_descriptor,omitempty"`
	ObservationPendingKnown  bool               `json:"observation_pending_known"`
	ObservationPending       bool               `json:"observation_pending"`
	PendingObservationSlot   string             `json:"pending_observation_slot"`
	PendingObservationSource string             `json:"pending_observation_source"`
	RelogLifecycle           string             `json:"session_lifecycle"`
	RelogSession             uint64             `json:"session_incarnation"`
	Gear                     json.RawMessage    `json:"gear"`
}

type gearWire struct {
	IdentityKey   string `json:"identity_key"`
	AccountID     uint64 `json:"account_id"`
	CharacterGUID uint64 `json:"character_guid"`
	Session       uint64 `json:"session_incarnation"`
	Generation    uint64 `json:"strategic_generation"`
	Equipment     []struct {
		Slot uint32 `json:"slot"`
		Item struct {
			Entry uint32 `json:"entry"`
		} `json:"item"`
	} `json:"equipment"`
	Quests []struct {
		QuestID uint64 `json:"quest_id"`
		Rewards []struct {
			Kind  string `json:"kind"`
			Index uint32 `json:"index"`
			Item  struct {
				Entry uint32 `json:"entry"`
			} `json:"item"`
			Slots []struct {
				Slot    uint32 `json:"slot"`
				Current struct {
					Entry uint32 `json:"entry"`
				} `json:"current"`
				CandidateScore       float64 `json:"candidate_score"`
				CandidateScoreValid  *bool   `json:"candidate_score_valid"`
				CandidateScoreReason string  `json:"candidate_score_reason"`
				CurrentScore         float64 `json:"current_score"`
				CurrentScoreValid    *bool   `json:"current_score_valid"`
				CurrentScoreReason   string  `json:"current_score_reason"`
			} `json:"slots"`
		} `json:"rewards"`
	} `json:"quests"`
}

func decodeGear(raw json.RawMessage) (gearWire, error) {
	var gear gearWire
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &gear) != nil || gear.IdentityKey == "" ||
		gear.AccountID == 0 || gear.CharacterGUID == 0 || gear.Session == 0 || gear.Generation == 0 || len(gear.Equipment) != 19 {
		return gear, errors.New("Core1 nested gear observation is missing typed actor/equipment fields")
	}
	seen := make(map[uint32]bool, len(gear.Equipment))
	for _, row := range gear.Equipment {
		if row.Slot >= 19 || seen[row.Slot] {
			return gear, errors.New("Core1 gear equipment slot set is invalid")
		}
		seen[row.Slot] = true
	}
	if len(seen) != 19 {
		return gear, errors.New("Core1 gear equipment slots are incomplete")
	}
	return gear, nil
}

func selectedReward(gear gearWire, slotKey string) (uint64, string, uint32, uint32, error) {
	parts := strings.Split(slotKey, ":")
	if len(parts) != 4 || parts[0] != "quest" {
		return 0, "", 0, 0, errors.New("selected quest reward slot key malformed")
	}
	questID, e1 := strconv.ParseUint(parts[1], 10, 64)
	index, e2 := strconv.ParseUint(parts[3], 10, 32)
	if e1 != nil || e2 != nil || questID == 0 || (parts[2] != "choice" && parts[2] != "fixed") {
		return 0, "", 0, 0, errors.New("selected quest reward identity malformed")
	}
	var itemID uint32
	matches := 0
	for _, quest := range gear.Quests {
		if quest.QuestID != questID {
			continue
		}
		for _, reward := range quest.Rewards {
			if reward.Kind == parts[2] && reward.Index == uint32(index) && reward.Item.Entry > 0 {
				itemID = reward.Item.Entry
				matches++
			}
		}
	}
	if matches != 1 {
		return 0, "", 0, 0, errors.New("selected quest/reward key does not identify one native item")
	}
	return questID, parts[2], uint32(index), itemID, nil
}

func finiteScore(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func nativeActorMatches(gear gearWire, in ScenarioInputs, session uint64) bool {
	return gear.IdentityKey == in.IdentityKey && gear.AccountID == in.AccountID &&
		gear.CharacterGUID == in.CharacterGUID && gear.Generation == in.StrategicGeneration && gear.Session == session
}

func selectedEquipment(gear gearWire, slotKey string, itemID uint32) (uint32, float64, error) {
	questID, kind, index, rewardItem, err := selectedReward(gear, slotKey)
	if err != nil || rewardItem != itemID {
		return 0, 0, errors.New("nested quest/reward item does not match native acquisition")
	}
	var chosenSlot uint32
	var baseline float64
	matches := 0
	for _, quest := range gear.Quests {
		if quest.QuestID != questID {
			continue
		}
		for _, reward := range quest.Rewards {
			if reward.Kind != kind || reward.Index != index || reward.Item.Entry != itemID {
				continue
			}
			for _, comparison := range reward.Slots {
				if comparison.Slot >= 19 || comparison.CandidateScoreValid == nil || !*comparison.CandidateScoreValid || comparison.CandidateScoreReason != "" || !finiteScore(comparison.CandidateScore) {
					continue
				}
				before := comparison.CurrentScore
				if comparison.Current.Entry == 0 {
					if comparison.CurrentScoreValid == nil || *comparison.CurrentScoreValid || comparison.CurrentScoreReason != "empty_slot" {
						continue
					}
					before = 0
				} else if comparison.CurrentScoreValid == nil || !*comparison.CurrentScoreValid || comparison.CurrentScoreReason != "" || !finiteScore(before) {
					continue
				}
				if comparison.CandidateScore <= before {
					continue
				}
				for _, equipped := range gear.Equipment {
					if equipped.Slot == comparison.Slot && equipped.Item.Entry == comparison.Current.Entry {
						chosenSlot, baseline = comparison.Slot, before
						matches++
					}
				}
			}
		}
	}
	if matches != 1 {
		return 0, 0, errors.New("selected reward has no unique valid positive equipment-slot comparison")
	}
	return chosenSlot, baseline, nil
}

func equippedScore(gear gearWire, slotKey string, slot uint32, itemID uint32) (float64, error) {
	questID, kind, index, selectedItem, keyErr := selectedReward(gear, slotKey)
	if keyErr != nil || selectedItem != itemID {
		return 0, errors.New("equipped score is not bound to selected quest/reward")
	}
	for _, row := range gear.Equipment {
		if row.Slot == slot && row.Item.Entry == itemID {
			for _, quest := range gear.Quests {
				if quest.QuestID != questID {
					continue
				}
				for _, reward := range quest.Rewards {
					if reward.Kind != kind || reward.Index != index || reward.Item.Entry != itemID {
						continue
					}
					for _, comparison := range reward.Slots {
						if comparison.Slot == slot && comparison.Current.Entry == itemID &&
							comparison.CurrentScoreValid != nil && *comparison.CurrentScoreValid && comparison.CurrentScoreReason == "" && finiteScore(comparison.CurrentScore) {
							return comparison.CurrentScore, nil
						}
					}
				}
			}
		}
	}
	return 0, errors.New("exact equipped item lacks valid native current-score evidence")
}

type AckPair struct {
	RecordID     string `json:"record_id"`
	RecordSHA256 string `json:"record_sha256"`
}

type NativeAcquisition struct {
	CaptureKind string            `json:"kind"`
	Actor       string            `json:"actor"`
	Source      string            `json:"source_id"`
	Generation  uint64            `json:"strategic_generation"`
	Session     uint64            `json:"session_incarnation"`
	SnapshotID  string            `json:"snapshot_id"`
	SlotKey     string            `json:"slot_key"`
	ItemID      uint32            `json:"item_id"`
	Receipt     CaptureDescriptor `json:"receipt"`
	ACKPairs    []AckPair         `json:"ack_pairs"`
}

type AckSettlement struct {
	QueueEmpty bool      `json:"queue_empty"`
	Required   []AckPair `json:"required_acks"`
	Applied    []AckPair `json:"applied_acks"`
}

type AckRecovery struct {
	Receipt CaptureDescriptor `json:"original_descriptor"`
	Pairs   []AckPair         `json:"ack_pairs"`
}

type ScenarioInputs struct {
	AccountID           uint64
	CharacterGUID       uint64
	IdentityKey         string
	SlotKey             string
	SnapshotID          string
	ExporterRevision    string
	SourceID            string
	StrategicGeneration uint64
	Session             uint64
	GearInputsPath      string
	GearInputsSHA256    string
	ResumeReceipt       CaptureDescriptor
}

var statusPollInterval = 50 * time.Millisecond

func observationMatches(o NativeObservation, in ScenarioInputs, session uint64) bool {
	return o.Status == "observed" && o.Actor == in.IdentityKey && o.AccountID == in.AccountID &&
		o.CharacterGUID == in.CharacterGUID && o.Source == in.SourceID &&
		o.Generation == in.StrategicGeneration && o.Session == session &&
		o.SlotKey == in.SlotKey
}

func validReceiptDescriptor(d CaptureDescriptor) bool {
	return receiptRE.MatchString(d.Path) && d.Bytes > 0 && digestRE.MatchString(d.SHA256) && d.Generation != "" && !d.Ready
}

func custodyRecoveryError(operation string, in ScenarioInputs, original CaptureDescriptor, ackPrepared, ackSettlementAttempted bool, pairs []AckPair, cause error) error {
	descriptor := "unknown"
	if original.Path != "" {
		descriptor = fmt.Sprintf("path=%q bytes=%d sha256=%q generation=%q ready=%t", original.Path, original.Bytes, original.SHA256, original.Generation, original.Ready)
	}
	ackState := "not_prepared"
	if ackPrepared {
		ackState = "prepare_outcome_unknown"
		if len(pairs) > 0 {
			ackState = fmt.Sprintf("exact_pairs=%v; application_or_settlement_pending", pairs)
		}
	}
	if ackSettlementAttempted {
		ackState += "; settlement_outcome_unknown"
	}
	if cause == nil {
		cause = errors.New("custody transition did not complete")
	}
	return fmt.Errorf("native custody recovery required: operation=%s actor=%q source=%q slot=%q generation=%d session=%d snapshot=%q original_descriptor={%s} ack=%s; preserve state and resume with the exact descriptor before another run: %w",
		operation, in.IdentityKey, in.SourceID, in.SlotKey, in.StrategicGeneration, in.Session, in.SnapshotID, descriptor, ackState, cause)
}

func waitStatus(ctx context.Context, ops Lifecycle, accept func(NativeStatus) bool) (NativeStatus, error) {
	if ctx == nil || ops == nil || accept == nil {
		return NativeStatus{}, ErrAcceptedRuntimePrerequisite
	}
	ticker := time.NewTicker(statusPollInterval)
	defer ticker.Stop()
	consecutive := 0
	var last NativeStatus
	for {
		if err := ctx.Err(); err != nil {
			return last, err
		}
		status, err := ops.ExportStatus(ctx)
		if err != nil {
			return last, err
		}
		if err := ctx.Err(); err != nil {
			return last, err
		}
		last = status
		if accept(status) {
			consecutive++
			if consecutive >= 2 {
				return status, nil
			}
		} else {
			consecutive = 0
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-ticker.C:
		}
	}
}

// RecoverPendingCustody resumes only the exact immutable descriptor supplied by
// the interrupted run. It never stages a new GearPolicy while custody is active.
func RecoverPendingCustody(ctx context.Context, ops Lifecycle, in ScenarioInputs) error {
	if in.SnapshotID == "" && in.ResumeReceipt.Generation != "" {
		in.SnapshotID = in.ResumeReceipt.Generation
	}
	if ctx == nil || ctx.Err() != nil || ops == nil || !validReceiptDescriptor(in.ResumeReceipt) || in.ResumeReceipt.Generation != in.SnapshotID {
		return ErrAcceptedRuntimePrerequisite
	}
	status, err := ops.ExportStatus(ctx)
	if err != nil {
		return err
	}
	if !status.CaptureRetained && !status.ACKQueueActive && in.ResumeReceipt.Path == "" {
		return nil
	}
	if status.ACKQueueActive {
		if status.CaptureRetained {
			return errors.New("Core1 status reports simultaneous capture and ACK owners; preserve both")
		}
		if status.ACKOriginal == nil {
			return errors.New("Core1 witness_status custody_ack omits original_descriptor; preserve active ACK queue and refuse settlement")
		}
		if *status.ACKOriginal != in.ResumeReceipt {
			return errors.New("active native ACK queue original descriptor differs from supplied receipt; preserve queue")
		}
		if !ackRecordIDsMatch(status.ACKRecordIDs, status.RequiredACKs) {
			return errors.New("active native ACK queue record IDs do not exactly match required ACK pairs; preserve queue")
		}
		return settleRecoveredAck(ctx, ops, in.ResumeReceipt, status.RequiredACKs, status.RequiredACKs)
	}
	var acquisition NativeAcquisition
	if status.CaptureRetained {
		// A retained in-memory owner already has its original. Drain it without
		// replacing the descriptor; recovery is reserved for a cold owner.
		partial, drainErr := ops.DrainCapture(ctx)
		if drainErr != nil {
			return errors.Join(errors.New("retained original could not be fully mapped for recovery"), drainErr)
		}
		if !validReceiptDescriptor(partial.Receipt) || partial.Receipt != in.ResumeReceipt {
			return errors.Join(errors.New("recovered original receipt identity changed"), drainErr)
		}
		acquisition = partial
		if err := ops.ReleaseCapture(ctx); err != nil {
			return err
		}
		status, err = waitStatus(ctx, ops, func(s NativeStatus) bool { return s.CaptureQuiescent && !s.CaptureRetained })
		if err != nil {
			return err
		}
		pairs, err := ops.PrepareAck(ctx, in.ResumeReceipt)
		if err != nil {
			return err
		}
		if !sameAckPairs(partial.ACKPairs, pairs) {
			return errors.New("prepared native ACK pairs do not match the retained exact original; preserve queue")
		}
		return settleRecoveredAck(ctx, ops, in.ResumeReceipt, acquisition.ACKPairs, pairs)
	} else if !status.ACKQueueActive {
		if err := ops.RecoverCapture(ctx, in.ResumeReceipt); err != nil {
			return err
		}
		partial, drainErr := ops.DrainCapture(ctx)
		if drainErr != nil {
			return errors.Join(errors.New("cold recovered original could not be fully mapped"), drainErr)
		}
		if !validReceiptDescriptor(partial.Receipt) || partial.Receipt != in.ResumeReceipt {
			return errors.Join(errors.New("recovered original receipt identity changed"), drainErr)
		}
		acquisition = partial
		if err := ops.ReleaseCapture(ctx); err != nil {
			return err
		}
		if _, err := waitStatus(ctx, ops, func(s NativeStatus) bool { return s.CaptureQuiescent && !s.CaptureRetained }); err != nil {
			return errors.New("cold recovered capture did not confirm quiescence before ACK recovery")
		}
		recovery, err := ops.RecoverAck(ctx, in.ResumeReceipt)
		if err != nil {
			return err
		}
		if recovery.Receipt != in.ResumeReceipt || !sameAckPairs(recovery.Pairs, acquisition.ACKPairs) {
			return errors.New("Core1 recover_native_custody_ack result is not bound to the supplied original descriptor and ACK pairs; preserve recovered queue")
		}
		return settleRecoveredAck(ctx, ops, in.ResumeReceipt, acquisition.ACKPairs, recovery.Pairs)
	}
	return nil
}

func settleRecoveredAck(ctx context.Context, ops Lifecycle, descriptor CaptureDescriptor, receiptPairs, queuePairs []AckPair) error {
	if !validReceiptDescriptor(descriptor) || len(receiptPairs) == 0 || !sameAckPairs(receiptPairs, queuePairs) {
		return errors.New("recovered ACK queue does not match exact original receipt pairs; preserve custody")
	}
	status, err := waitStatus(ctx, ops, func(s NativeStatus) bool {
		return s.ACKQueueActive && ackRecordIDsMatch(s.ACKRecordIDs, receiptPairs) && sameAckPairs(s.RequiredACKs, receiptPairs) && sameAckPairs(s.AppliedACKs, receiptPairs)
	})
	if err != nil {
		return errors.New("recovered ACK pair was not observed applied in the native tick; preserve queue")
	}
	settled, err := ops.SettleAck(ctx)
	if err != nil {
		return err
	}
	if !settled.QueueEmpty || !sameAckPairs(settled.Required, receiptPairs) || !sameAckPairs(settled.Applied, receiptPairs) ||
		!sameAckPairs(status.RequiredACKs, settled.Required) {
		return errors.New("recovered native ACK settlement mismatch; preserve original state")
	}
	return nil
}

func sameAckPairs(a, b []AckPair) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[AckPair]int, len(a))
	for _, pair := range a {
		set[pair]++
	}
	for _, pair := range b {
		set[pair]--
		if set[pair] < 0 {
			return false
		}
	}
	for _, count := range set {
		if count != 0 {
			return false
		}
	}
	return true
}

func ackRecordIDsMatch(ids []string, pairs []AckPair) bool {
	if len(ids) == 0 || len(ids) != len(pairs) {
		return false
	}
	set := make(map[string]int, len(ids))
	for _, id := range ids {
		if id == "" || set[id] != 0 {
			return false
		}
		set[id]++
	}
	pairIDs := make(map[string]bool, len(pairs))
	for _, pair := range pairs {
		if pair.RecordID == "" || pairIDs[pair.RecordID] {
			return false
		}
		pairIDs[pair.RecordID] = true
		set[pair.RecordID]--
		if set[pair.RecordID] < 0 {
			return false
		}
	}
	for _, count := range set {
		if count != 0 {
			return false
		}
	}
	return true
}

// RunScenario executes only the fixed native lifecycle and then verifies its
// trace against independently run Core1 producer evidence. Resumption begins
// with ExportStatus and preserves any original descriptor returned by the
// existing capture owner; this function never recreates or discards it.
func RunScenario(ctx context.Context, ops Lifecycle, in ScenarioInputs, producer ProducerResult) (trace []Evidence, err error) {
	if in.ResumeReceipt.Path != "" {
		if in.SnapshotID == "" {
			in.SnapshotID = in.ResumeReceipt.Generation
		}
		return nil, RecoverPendingCustody(ctx, ops, in)
	}
	if ctx == nil || ctx.Err() != nil || ops == nil || in.AccountID == 0 || in.CharacterGUID == 0 ||
		in.IdentityKey == "" || in.SlotKey == "" || in.SnapshotID == "" || in.SourceID == "" ||
		in.StrategicGeneration == 0 || in.Session == 0 || !digestRE.MatchString(in.GearInputsSHA256) || !acceptedCore1Path(in.GearInputsPath) {
		return nil, ErrAcceptedRuntimePrerequisite
	}
	preflight, err := ops.ExportStatus(ctx)
	if err != nil {
		return nil, err
	}
	if preflight.CaptureRetained || preflight.ACKQueueActive || in.ResumeReceipt.Path != "" {
		return nil, RecoverPendingCustody(ctx, ops, in)
	}
	var armAttempted bool
	var original CaptureDescriptor
	var originalACKPairs []AckPair
	var custodyComplete bool
	var ackPreparationAttempted bool
	var ackSettlementAttempted bool
	var pendingACKPairs []AckPair
	pendingOperation := "arm_capture"
	defer func() {
		if !armAttempted || custodyComplete {
			return
		}
		recoveryError := func(cause error) {
			if cause == nil {
				cause = errors.New("custody transition did not complete")
			}
			report := custodyRecoveryError(pendingOperation, in, original, ackPreparationAttempted, ackSettlementAttempted, pendingACKPairs, cause)
			err = errors.Join(err, report)
		}
		if ctx.Err() != nil || ackPreparationAttempted || ackSettlementAttempted {
			recoveryError(ctx.Err())
			return
		}
		var cleanupErr error
		if original.Path == "" {
			pendingOperation = "drain_capture"
			partial, drainErr := ops.DrainCapture(ctx)
			original = partial.Receipt
			originalACKPairs = partial.ACKPairs
			if drainErr != nil {
				cleanupErr = drainErr
			}
		}
		if !validReceiptDescriptor(original) {
			recoveryError(errors.Join(cleanupErr, errors.New("no exact original capture descriptor is available")))
			return
		}
		pendingOperation = "release_capture"
		if e := ops.ReleaseCapture(ctx); e != nil {
			recoveryError(errors.Join(cleanupErr, e))
			return
		}
		pendingOperation = "wait_capture_quiescence"
		if _, e := waitStatus(ctx, ops, func(s NativeStatus) bool {
			return s.CaptureQuiescent && !s.CaptureRetained
		}); e != nil {
			recoveryError(errors.Join(cleanupErr, e))
			return
		}
		pendingOperation = "prepare_ack"
		ackPreparationAttempted = true
		ackPairs, e := ops.PrepareAck(ctx, original)
		if e != nil {
			recoveryError(errors.Join(cleanupErr, e))
			return
		}
		pendingACKPairs = ackPairs
		if ctx.Err() != nil || len(ackPairs) == 0 || !sameAckPairs(ackPairs, originalACKPairs) {
			recoveryError(errors.Join(cleanupErr, ctx.Err(), errors.New("prepared ACK pairs do not match the original capture")))
			return
		}
		pendingOperation = "wait_ack_application"
		st, e := waitStatus(ctx, ops, func(s NativeStatus) bool {
			return s.ACKQueueActive && ackRecordIDsMatch(s.ACKRecordIDs, ackPairs) &&
				sameAckPairs(s.RequiredACKs, ackPairs) && sameAckPairs(s.AppliedACKs, ackPairs)
		})
		if e != nil {
			recoveryError(errors.Join(cleanupErr, e))
			return
		}
		if !sameAckPairs(st.RequiredACKs, st.AppliedACKs) {
			recoveryError(errors.Join(cleanupErr, errors.New("cleanup retained ACK queue pending native tick application")))
			return
		}
		pendingOperation = "settle_ack"
		ackSettlementAttempted = true
		settled, settleErr := ops.SettleAck(ctx)
		if settleErr != nil {
			recoveryError(errors.Join(cleanupErr, settleErr))
			return
		}
		if !settled.QueueEmpty || !sameAckPairs(settled.Required, ackPairs) || !sameAckPairs(settled.Applied, ackPairs) {
			recoveryError(errors.Join(cleanupErr, errors.New("cleanup ACK settlement incomplete")))
			return
		}
		custodyComplete = true
	}()
	// GearPolicy.stage_gear resets any queued observation. Stage first, then
	// request the exact selected-slot read that will authorize capture.
	if err := ops.StageGear(ctx, in.GearInputsPath, in.GearInputsSHA256); err != nil {
		return nil, err
	}
	if err := ops.QueueObservation(ctx, in.SlotKey); err != nil {
		return nil, err
	}
	status, err := waitStatus(ctx, ops, func(s NativeStatus) bool {
		return s.ObservationPendingKnown && !s.ObservationPending && s.Mode == "observe" &&
			s.PendingObservationSlot == "" && s.PendingObservationSource == "" &&
			!s.CaptureRetained && !s.ACKQueueActive && observationMatches(s.Observation, in, in.Session)
	})
	if err != nil {
		return nil, fmt.Errorf("selected-slot observation actor/source/generation/session did not arrive: %w", err)
	}
	base := Evidence{Actor: in.IdentityKey, Source: in.SourceID, Generation: fmt.Sprint(in.StrategicGeneration), SnapshotID: in.SnapshotID, Session: fmt.Sprint(in.Session), SlotKey: in.SlotKey}
	trace = []Evidence{{Kind: Observed, Actor: base.Actor, Source: base.Source, Generation: base.Generation, SnapshotID: base.SnapshotID, Session: base.Session, SlotKey: base.SlotKey, ObservationStatus: status.Observation.Status}}
	before := status.Observation
	beforeGear, err := decodeGear(before.Gear)
	if err != nil || !nativeActorMatches(beforeGear, in, in.Session) {
		return nil, errors.New("selected-slot nested gear lacks exact actor/binding/session evidence")
	}
	_, _, _, expectedItemID, err := selectedReward(beforeGear, in.SlotKey)
	if err != nil {
		return nil, err
	}
	baselineSlot, baselineScore, err := selectedEquipment(beforeGear, in.SlotKey, expectedItemID)
	if err != nil {
		return nil, err
	}
	armAttempted = true // SOAP may have applied the arm even if its response is lost.
	pendingOperation = "arm_capture"
	if err := ops.ArmCapture(ctx, CaptureArm{in.IdentityKey, in.AccountID, in.CharacterGUID, in.SlotKey, in.SnapshotID, in.ExporterRevision}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pendingOperation = "set_gear_mode"
	if err := ops.SetMode(ctx, "gear"); err != nil {
		return nil, err
	}
	pendingOperation = "wait_gear_mode"
	status, err = waitStatus(ctx, ops, func(s NativeStatus) bool { return s.Mode == "gear" })
	if err != nil {
		return nil, fmt.Errorf("native gear mode did not reach a fresh normal-tick status: %w", err)
	}
	pendingOperation = "drain_capture"
	acq, err := ops.DrainCapture(ctx)
	original = acq.Receipt
	originalACKPairs = acq.ACKPairs
	if err != nil {
		return nil, err
	}
	if acq.CaptureKind != "native_execution" || acq.Actor != in.IdentityKey || acq.Source != in.SourceID ||
		acq.Generation != in.StrategicGeneration || acq.Session != in.Session || acq.SnapshotID != in.SnapshotID ||
		acq.SlotKey != in.SlotKey || acq.ItemID == 0 || acq.ItemID != expectedItemID || acq.Receipt.Ready || acq.Receipt.Bytes <= 0 ||
		!digestRE.MatchString(acq.Receipt.SHA256) || !receiptRE.MatchString(acq.Receipt.Path) || acq.Receipt.Generation != in.SnapshotID {
		return nil, errors.New("native acquisition receipt does not match selected actor/source/slot/session")
	}
	trace = append(trace, Evidence{Kind: Acquired, Actor: base.Actor, Source: base.Source, Generation: base.Generation, SnapshotID: base.SnapshotID, Session: base.Session, SlotKey: base.SlotKey, ItemID: acq.ItemID, CaptureKind: acq.CaptureKind, ReceiptPath: acq.Receipt.Path, ReceiptSHA256: acq.Receipt.SHA256})
	pendingOperation = "release_capture"
	if err := ops.ReleaseCapture(ctx); err != nil {
		return nil, err
	}
	pendingOperation = "wait_capture_quiescence"
	status, err = waitStatus(ctx, ops, func(s NativeStatus) bool { return s.CaptureQuiescent && !s.CaptureRetained })
	if err != nil {
		return nil, fmt.Errorf("capture owner did not confirm quiescence: %w", err)
	}
	trace = append(trace, Evidence{Kind: Drained, Actor: base.Actor, Source: base.Source, Generation: base.Generation, SnapshotID: base.SnapshotID, Session: base.Session, SlotKey: base.SlotKey, ItemID: acq.ItemID, ReceiptPath: acq.Receipt.Path, ReceiptSHA256: acq.Receipt.SHA256})
	trace = append(trace, Evidence{Kind: Released, Actor: base.Actor, Source: base.Source, Generation: base.Generation, SnapshotID: base.SnapshotID, Session: base.Session, SlotKey: base.SlotKey, ItemID: acq.ItemID, ReceiptPath: acq.Receipt.Path, ReceiptSHA256: acq.Receipt.SHA256})
	pendingOperation = "prepare_ack"
	ackPreparationAttempted = true // A lost response may follow a committed queue mutation.
	ackPairs, err := ops.PrepareAck(ctx, acq.Receipt)
	if err != nil {
		return nil, err
	}
	pendingACKPairs = ackPairs
	if len(ackPairs) == 0 || !sameAckPairs(ackPairs, acq.ACKPairs) {
		return nil, errors.New("durable native ACK queue omitted record identity pairs")
	}
	ackID := ackPairs[0].RecordID
	trace = append(trace, Evidence{Kind: AckPrepared, Actor: base.Actor, Source: base.Source, Generation: base.Generation, SnapshotID: base.SnapshotID, Session: base.Session, SlotKey: base.SlotKey, ItemID: acq.ItemID, AckID: ackID, ACKPairs: ackPairs, ReceiptPath: acq.Receipt.Path, ReceiptSHA256: acq.Receipt.SHA256})
	pendingOperation = "wait_ack_application"
	status, err = waitStatus(ctx, ops, func(s NativeStatus) bool {
		return s.ACKQueueActive && ackRecordIDsMatch(s.ACKRecordIDs, ackPairs) && sameAckPairs(s.RequiredACKs, ackPairs) && sameAckPairs(ackPairs, s.AppliedACKs)
	})
	if err != nil {
		return nil, fmt.Errorf("native exact ACK pairs were not observed as tick-applied: %w", err)
	}
	trace = append(trace, Evidence{Kind: AckApplied, Actor: base.Actor, Source: base.Source, Generation: base.Generation, SnapshotID: base.SnapshotID, Session: base.Session, SlotKey: base.SlotKey, ItemID: acq.ItemID, AckID: ackID, ACKPairs: ackPairs, ReceiptPath: acq.Receipt.Path, ReceiptSHA256: acq.Receipt.SHA256})
	pendingOperation = "settle_ack"
	ackSettlementAttempted = true // A lost response may follow a committed queue mutation.
	settled, err := ops.SettleAck(ctx)
	if err != nil {
		return nil, err
	}
	if !settled.QueueEmpty || !sameAckPairs(settled.Required, ackPairs) || !sameAckPairs(settled.Applied, ackPairs) {
		return nil, errors.New("native ACK settlement incomplete; preserve queue and earned state")
	}
	custodyComplete = true
	trace = append(trace, Evidence{Kind: AckSettled, Actor: base.Actor, Source: base.Source, Generation: base.Generation, SnapshotID: base.SnapshotID, Session: base.Session, SlotKey: base.SlotKey, ItemID: acq.ItemID, AckID: ackID, ACKPairs: ackPairs, ReceiptPath: acq.Receipt.Path, ReceiptSHA256: acq.Receipt.SHA256})
	if err := ops.SetMode(ctx, "observe"); err != nil {
		return nil, err
	}
	if err := ops.QueueObservation(ctx, in.SlotKey); err != nil {
		return nil, err
	}
	status, err = waitStatus(ctx, ops, func(s NativeStatus) bool {
		return s.ObservationPendingKnown && !s.ObservationPending && s.Mode == "observe" &&
			s.PendingObservationSlot == "" && s.PendingObservationSource == "" &&
			!s.CaptureRetained && !s.ACKQueueActive && observationMatches(s.Observation, in, in.Session)
	})
	if err != nil {
		return nil, fmt.Errorf("exact selected-slot post-acquisition observation did not arrive: %w", err)
	}
	postGear, err := decodeGear(status.Observation.Gear)
	if err != nil || !nativeActorMatches(postGear, in, in.Session) {
		return nil, errors.New("post-acquisition nested gear lacks exact actor/binding/session evidence")
	}
	_, _, _, postRewardItem, err := selectedReward(postGear, in.SlotKey)
	if err != nil || postRewardItem != acq.ItemID {
		return nil, errors.New("post-acquisition selected reward item mismatches native receipt")
	}
	postSlot, err := equippedScore(postGear, in.SlotKey, baselineSlot, acq.ItemID)
	if err != nil || postSlot <= baselineScore {
		return nil, errors.New("acquired item is not an improved exact-slot equipment observation")
	}
	trace = append(trace, Evidence{Kind: Improved, Actor: base.Actor, Source: base.Source, Generation: base.Generation, SnapshotID: base.SnapshotID, Session: base.Session, SlotKey: base.SlotKey, ItemID: acq.ItemID, ScoreBefore: baselineScore, ScoreAfter: postSlot})
	if err := ops.SetMode(ctx, "relog"); err != nil {
		return nil, err
	}
	status, err = waitStatus(ctx, ops, func(s NativeStatus) bool {
		if s.RelogLifecycle != "online_new_session_observed" {
			return false
		}
		gear, decodeErr := decodeGear(s.Gear)
		return decodeErr == nil && nativeActorMatches(gear, in, gear.Session) && gear.Session != in.Session
	})
	if err != nil {
		return nil, fmt.Errorf("same-actor new-session relog observation missing: %w", err)
	}
	newGear, err := decodeGear(status.Gear)
	if err != nil || !nativeActorMatches(newGear, in, newGear.Session) || newGear.Session == in.Session {
		return nil, errors.New("relog status lacks exact new-session actor gear")
	}
	newSession := newGear.Session
	if err := ops.SetMode(ctx, "observe"); err != nil {
		return nil, err
	}
	if err := ops.QueueObservation(ctx, in.SlotKey); err != nil {
		return nil, err
	}
	status, err = waitStatus(ctx, ops, func(s NativeStatus) bool {
		return s.ObservationPendingKnown && !s.ObservationPending && s.Mode == "observe" &&
			s.PendingObservationSlot == "" && s.PendingObservationSource == "" &&
			observationMatches(s.Observation, in, newSession)
	})
	if err != nil {
		return nil, fmt.Errorf("post-relog exact-slot observation did not arrive: %w", err)
	}
	finalGear, err := decodeGear(status.Observation.Gear)
	if err != nil || !nativeActorMatches(finalGear, in, newSession) {
		return nil, errors.New("post-relog nested gear actor/session mismatch")
	}
	_, _, _, finalRewardItem, err := selectedReward(finalGear, in.SlotKey)
	if err != nil || finalRewardItem != acq.ItemID {
		return nil, errors.New("post-relog selected reward item differs from native receipt")
	}
	finalScore, err := equippedScore(finalGear, in.SlotKey, baselineSlot, acq.ItemID)
	if err != nil || finalScore <= baselineScore || finalScore != postSlot {
		return nil, errors.New("post-relog exact item/slot score persistence missing")
	}
	trace = append(trace, Evidence{Kind: RelogSaved, Actor: base.Actor, Source: base.Source, Generation: base.Generation, SnapshotID: base.SnapshotID, Session: base.Session, SlotKey: base.SlotKey, NewSession: fmt.Sprint(newSession)})
	trace = append(trace, Evidence{Kind: RelogObserved, Actor: base.Actor, Source: base.Source, Generation: base.Generation, SnapshotID: base.SnapshotID, Session: fmt.Sprint(newSession), SlotKey: base.SlotKey, NewSession: fmt.Sprint(newSession), PersistedItemID: acq.ItemID, PersistedSlotKey: base.SlotKey})
	if err := verifyScenarioWithProducer(trace, producer); err != nil {
		return nil, err
	}
	return trace, nil
}
