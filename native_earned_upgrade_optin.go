// Candidate overlay for tools/rapid-game-harness; not applied to canonical files.
package harness

import (
	"context"
	"errors"
	"os"
	"strconv"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	nativeupgrade "wow/rapid-game-harness/nativeupgrade"
)

func runNativeEarnedUpgradeOptIn(ctx context.Context, _ config, session *e2eharness.Session, initial observation) error {
	if ctx == nil || ctx.Err() != nil {
		return errors.New("native earned-upgrade opt-in context unavailable")
	}
	get := os.Getenv
	resumePath, resumeBytes, resumeSHA, resumeGeneration := get("WOW_NATIVE_EARNED_UPGRADE_RESUME_RECEIPT_PATH"), get("WOW_NATIVE_EARNED_UPGRADE_RESUME_RECEIPT_BYTES"), get("WOW_NATIVE_EARNED_UPGRADE_RESUME_RECEIPT_SHA256"), get("WOW_NATIVE_EARNED_UPGRADE_RESUME_RECEIPT_GENERATION")
	resume := nativeupgrade.CaptureDescriptor{}
	if resumePath != "" || resumeBytes != "" || resumeSHA != "" || resumeGeneration != "" {
		bytes, parseErr := strconv.ParseInt(resumeBytes, 10, 64)
		if parseErr != nil || bytes <= 0 || resumePath == "" || resumeSHA == "" || resumeGeneration == "" {
			return errors.New("native earned-upgrade resume receipt descriptor is incomplete")
		}
		resume = nativeupgrade.CaptureDescriptor{Path: resumePath, Bytes: bytes, SHA256: resumeSHA, Generation: resumeGeneration, Ready: false}
		ops, err := nativeupgrade.NewRemoteLifecycle()
		if err != nil {
			return err
		}
		handled, err := nativeupgrade.PreflightRecovery(ctx, ops, nativeupgrade.ScenarioInputs{ResumeReceipt: resume, SnapshotID: resume.Generation})
		if err != nil {
			return err
		}
		if handled {
			return nil
		}
		return errors.New("exact receipt recovery was not handled")
	}
	if session == nil || session.GUID == 0 || initial.GUID != session.GUID {
		return errors.New("native earned-upgrade opt-in actor/session precondition failed")
	}
	account, err := strconv.ParseUint(get("WOW_NATIVE_EARNED_UPGRADE_ACCOUNT_ID"), 10, 64)
	if err != nil || account == 0 {
		return errors.New("native earned-upgrade account binding input unavailable")
	}
	generation, err := strconv.ParseUint(get("WOW_NATIVE_EARNED_UPGRADE_GENERATION"), 10, 64)
	if err != nil || generation == 0 {
		return errors.New("native earned-upgrade generation input unavailable")
	}
	oldSession, err := strconv.ParseUint(get("WOW_NATIVE_EARNED_UPGRADE_SESSION"), 10, 64)
	if err != nil || oldSession == 0 {
		return errors.New("native earned-upgrade session incarnation input unavailable")
	}
	contract := nativeupgrade.AcceptedContract{
		Operations:     []string{"witness_status", "export_status", "queue_native_gear_observation", "stage_accepted_gear", "arm_native_quest_capture", "drain_acquisition_capture", "release_acquisition_capture", "witness_command", "request_witness_session", "prepare_native_custody_ack", "settle_native_custody_ack", "recover_native_quest_capture", "recover_native_custody_ack"},
		GearInputsPath: get("WOW_NATIVE_EARNED_UPGRADE_GEAR_INPUTS_PATH"), GearInputsSHA256: get("WOW_NATIVE_EARNED_UPGRADE_GEAR_INPUTS_SHA256"),
		ManifestPath: get("WOW_NATIVE_EARNED_UPGRADE_MANIFEST_PATH"), ManifestSHA256: get("WOW_NATIVE_EARNED_UPGRADE_MANIFEST_SHA256"),
		EvidenceOutputDir: get("WOW_NATIVE_EARNED_UPGRADE_EVIDENCE_DIR"),
	}
	inputs := nativeupgrade.ScenarioInputs{
		AccountID: account, CharacterGUID: session.GUID, IdentityKey: get("WOW_NATIVE_EARNED_UPGRADE_IDENTITY_KEY"),
		SlotKey: get("WOW_NATIVE_EARNED_UPGRADE_SLOT_KEY"), SnapshotID: get("WOW_NATIVE_EARNED_UPGRADE_SNAPSHOT_ID"),
		ExporterRevision: get("WOW_NATIVE_EARNED_UPGRADE_EXPORTER_REVISION"), SourceID: get("WOW_NATIVE_EARNED_UPGRADE_SOURCE_ID"),
		StrategicGeneration: generation, Session: oldSession,
		GearInputsPath: contract.GearInputsPath, GearInputsSHA256: contract.GearInputsSHA256,
		ResumeReceipt: resume,
	}
	ops, err := nativeupgrade.NewRemoteLifecycle()
	if err != nil {
		return err
	}
	if handled, err := nativeupgrade.PreflightRecovery(ctx, ops, inputs); handled || err != nil {
		return err
	}
	producer, err := nativeupgrade.RunProducerVerifier(ctx, nativeupgrade.ProducerInputs{ManifestPath: contract.ManifestPath, ManifestSHA256: contract.ManifestSHA256, OutputDir: contract.EvidenceOutputDir})
	if err != nil {
		return err
	}
	_, err = nativeupgrade.RunOptIn(ctx, contract, inputs, ops, producer)
	return err
}
