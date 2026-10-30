// Package nativeupgrade verifies ordered evidence for an earned gear upgrade.
// It is a candidate-only verifier; it does not execute game or transport calls.
package nativeupgrade

import (
	"errors"
	"fmt"
	"math"
	"regexp"
)

type Kind string

const (
	Observed      Kind = "selected_slot_observed"
	Acquired      Kind = "native_acquisition"
	Improved      Kind = "equipped_improvement"
	Drained       Kind = "capture_drained"
	Released      Kind = "capture_released"
	AckPrepared   Kind = "custody_ack_prepared"
	AckApplied    Kind = "custody_ack_applied_in_tick"
	AckSettled    Kind = "custody_ack_settled"
	RelogSaved    Kind = "same_actor_saved"
	RelogObserved Kind = "new_session_persisted"
)

var order = []Kind{Observed, Acquired, Drained, Released, AckPrepared, AckApplied, AckSettled, Improved, RelogSaved, RelogObserved}
var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var slotRE = regexp.MustCompile(`^quest:[1-9][0-9]*:(choice|fixed):[0-9]+$`)
var receiptRE = regexp.MustCompile(`^/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures/receipt-[0-9a-f]{32}\.json$`)

type Evidence struct {
	Kind              Kind      `json:"kind"`
	Actor             string    `json:"actor"`
	Source            string    `json:"source_id"`
	Generation        string    `json:"strategic_generation"`
	SnapshotID        string    `json:"snapshot_id"`
	Session           string    `json:"session_incarnation"`
	SlotKey           string    `json:"slot_key"`
	ItemID            uint32    `json:"item_id"`
	ScoreBefore       float64   `json:"score_before,omitempty"`
	ScoreAfter        float64   `json:"score_after,omitempty"`
	ReceiptPath       string    `json:"receipt_path,omitempty"`
	ReceiptSHA256     string    `json:"receipt_sha256,omitempty"`
	AckID             string    `json:"ack_id,omitempty"`
	ACKPairs          []AckPair `json:"ack_pairs,omitempty"`
	NewSession        string    `json:"new_session,omitempty"`
	PersistedItemID   uint32    `json:"persisted_item_id,omitempty"`
	PersistedSlotKey  string    `json:"persisted_slot_key,omitempty"`
	ObservationStatus string    `json:"observation_status,omitempty"`
	CaptureKind       string    `json:"capture_kind,omitempty"`
}

// VerifySequence checks ordered evidence consistency only. It is not scenario
// acceptance and does not replace the finite Core1 producer verifier.
func VerifySequence(trace []Evidence) error {
	if len(trace) != len(order) {
		return fmt.Errorf("trace has %d events; want %d", len(trace), len(order))
	}
	first := trace[0]
	if first.Kind != Observed {
		return errors.New("selected-slot observation must start the sequence")
	}
	if first.Actor == "" || first.Source == "" || first.Generation == "" || first.SnapshotID == "" || first.Session == "" || !slotRE.MatchString(first.SlotKey) || first.ObservationStatus != "observed" {
		return errors.New("observation lacks actor/source/generation/session/exact slot")
	}
	for i, e := range trace {
		if e.Kind != order[i] {
			return fmt.Errorf("event %d is %q; want %q", i, e.Kind, order[i])
		}
		if e.Actor != first.Actor || e.Source != first.Source || e.Generation != first.Generation || e.SnapshotID != first.SnapshotID || e.SlotKey != first.SlotKey {
			return fmt.Errorf("event %q actor/source/generation/slot mismatch", e.Kind)
		}
		wantSession := first.Session
		if i == len(order)-1 {
			wantSession = trace[i-1].NewSession
		}
		if e.Session != wantSession {
			return fmt.Errorf("event %q session mismatch", e.Kind)
		}
	}
	acq, gear := trace[1], trace[7]
	if acq.ItemID == 0 || acq.CaptureKind != "native_execution" || !digestRE.MatchString(acq.ReceiptSHA256) ||
		!receiptRE.MatchString(acq.ReceiptPath) || acq.Session != first.Session {
		return errors.New("acquisition lacks current-session finite-producer receipt evidence")
	}
	if gear.ItemID == 0 || gear.ItemID != acq.ItemID || gear.ScoreAfter <= gear.ScoreBefore {
		return errors.New("equipped item does not exactly match acquisition or improve slot score")
	}
	if math.IsNaN(gear.ScoreBefore) || math.IsInf(gear.ScoreBefore, 0) ||
		math.IsNaN(gear.ScoreAfter) || math.IsInf(gear.ScoreAfter, 0) {
		return errors.New("equipped score values must be finite")
	}
	for _, index := range []int{1, 2, 3, 4, 5, 6} {
		e := trace[index]
		if e.ReceiptPath != acq.ReceiptPath || e.ReceiptSHA256 != acq.ReceiptSHA256 {
			return errors.New("receipt path/digest mismatch across acquisition and custody")
		}
	}
	for _, e := range trace[2:7] {
		if e.ItemID != acq.ItemID {
			return errors.New("custody evidence item mismatch")
		}
	}
	if trace[4].AckID == "" || trace[5].AckID != trace[4].AckID || trace[6].AckID != trace[4].AckID ||
		len(trace[4].ACKPairs) == 0 || !sameAckPairs(trace[4].ACKPairs, trace[5].ACKPairs) ||
		!sameAckPairs(trace[4].ACKPairs, trace[6].ACKPairs) {
		return errors.New("custody ACK is missing or mismatched")
	}
	for _, pair := range trace[4].ACKPairs {
		if pair.RecordID == "" || !digestRE.MatchString(pair.RecordSHA256) {
			return errors.New("custody ACK record identity pair is malformed")
		}
	}
	if trace[8].Session != first.Session || trace[8].NewSession == "" || trace[8].NewSession == first.Session {
		return errors.New("relog did not save same actor and advance session")
	}
	last := trace[9]
	if last.Session != trace[8].NewSession || last.NewSession != trace[8].NewSession ||
		last.PersistedItemID != gear.ItemID || last.PersistedSlotKey != first.SlotKey {
		return errors.New("new-session exact item/slot persistence missing")
	}
	return nil
}
