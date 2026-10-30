package nativeupgrade

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// AcceptedContract is populated only from the pinned accepted Core1 source map.
// A caller never gets invoked until every runtime prerequisite is present.
type AcceptedContract struct {
	Operations        []string
	GearInputsPath    string
	GearInputsSHA256  string
	ManifestPath      string
	ManifestSHA256    string
	EvidenceOutputDir string
}

var ErrAcceptedRuntimePrerequisite = errors.New("accepted native earned-upgrade runtime prerequisites are incomplete")

func acceptedCore1Path(value string) bool {
	if value == "" || filepath.IsAbs(value) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	return clean != ".." && !strings.HasPrefix(clean, "../") && strings.HasPrefix(clean, captureRoot+"/")
}

// RunOptIn validates the accepted wrapper map and runtime-owned immutable inputs,
// then executes the typed lifecycle through the fixed-operation interface.
func RunOptIn(ctx context.Context, contract AcceptedContract, in ScenarioInputs, ops Lifecycle, producer ProducerResult) ([]Evidence, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrAcceptedRuntimePrerequisite
	}
	if ops == nil {
		return nil, fmt.Errorf("%w: fixed native lifecycle caller unavailable", ErrAcceptedRuntimePrerequisite)
	}
	if in.ResumeReceipt.Path != "" {
		if handled, err := PreflightRecovery(ctx, ops, in); handled || err != nil {
			return nil, err
		}
	}
	want := map[string]bool{
		"witness_status": false, "export_status": false, "queue_native_gear_observation": false,
		"stage_accepted_gear": false, "arm_native_quest_capture": false,
		"drain_acquisition_capture": false, "release_acquisition_capture": false,
		"witness_command": false, "request_witness_session": false,
		"prepare_native_custody_ack": false, "settle_native_custody_ack": false,
		"recover_native_quest_capture": false, "recover_native_custody_ack": false,
	}
	for _, op := range contract.Operations {
		if _, ok := want[op]; ok {
			want[op] = true
		}
	}
	var missing []string
	for op, present := range want {
		if !present {
			missing = append(missing, op)
		}
	}
	if !acceptedCore1Path(contract.GearInputsPath) {
		missing = append(missing, "GearInputs path")
	}
	if !digestRE.MatchString(contract.GearInputsSHA256) {
		missing = append(missing, "GearInputs SHA-256")
	}
	if !acceptedCore1Path(contract.ManifestPath) {
		missing = append(missing, "Core1 verifier manifest path")
	}
	if !digestRE.MatchString(contract.ManifestSHA256) {
		missing = append(missing, "Core1 verifier manifest SHA-256")
	}
	if !acceptedCore1Path(contract.EvidenceOutputDir) {
		missing = append(missing, "Core1 verifier evidence bundle path")
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("%w: %s", ErrAcceptedRuntimePrerequisite, strings.Join(missing, ", "))
	}
	if in.GearInputsPath != contract.GearInputsPath || in.GearInputsSHA256 != contract.GearInputsSHA256 ||
		in.SnapshotID == "" || in.SnapshotID != producer.SnapshotID {
		return nil, fmt.Errorf("%w: runtime actor/source/snapshot inputs do not match pinned producer manifest", ErrAcceptedRuntimePrerequisite)
	}
	return RunScenario(ctx, ops, in, producer)
}

// PreflightRecovery runs before verifier/gear-input checks because those inputs
// are used only by a new scenario. It never stages new gear while custody may
// be retained.
func PreflightRecovery(ctx context.Context, ops Lifecycle, in ScenarioInputs) (bool, error) {
	if ctx == nil || ctx.Err() != nil || ops == nil {
		return false, ErrAcceptedRuntimePrerequisite
	}
	if in.ResumeReceipt.Path != "" {
		if !validReceiptDescriptor(in.ResumeReceipt) {
			return true, errors.New("resume receipt descriptor is malformed")
		}
		if in.SnapshotID == "" {
			in.SnapshotID = in.ResumeReceipt.Generation
		}
		if in.SnapshotID != in.ResumeReceipt.Generation {
			return true, errors.New("resume snapshot does not match exact receipt generation")
		}
	}
	status, err := ops.ExportStatus(ctx)
	if err != nil {
		return false, err
	}
	pending := status.CaptureRetained || status.ACKQueueActive
	if !pending && in.ResumeReceipt.Path == "" {
		return false, nil
	}
	if in.ResumeReceipt.Path == "" {
		if status.ACKQueueActive {
			return true, errors.New("Core1 status lacks custody_ack original descriptor binding; preserve active ACK queue and refuse settlement")
		}
		return true, errors.New("retained native capture requires its exact original receipt descriptor for recovery")
	}
	return true, RecoverPendingCustody(ctx, ops, in)
}
