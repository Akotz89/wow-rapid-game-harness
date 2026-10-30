package nativeupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const candidateRemoteScript = "tools/rapid-game-harness/remote.py"

// RemoteLifecycle routes only the fixed operation methods below through the
// candidate remote.py CLI. It never accepts a command string or shell snippet.
type RemoteLifecycle struct {
	root string
}

func NewRemoteLifecycle() (*RemoteLifecycle, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	if _, err := filepath.EvalSymlinks(filepath.Join(root, candidateRemoteScript)); err != nil {
		return nil, errors.New("fixed candidate remote transport unavailable")
	}
	return &RemoteLifecycle{root: root}, nil
}

func (r *RemoteLifecycle) invoke(ctx context.Context, operation string, args any) (json.RawMessage, error) {
	allowed := map[string]bool{"export_status": true, "queue_gear_observation": true, "stage_gear": true, "arm_capture": true, "set_mode": true, "drain_capture": true, "release_capture": true, "prepare_ack": true, "settle_ack": true, "recover_capture": true, "recover_ack": true}
	if !allowed[operation] || r == nil || r.root == "" || ctx == nil {
		return nil, errors.New("native wrapper operation is not allowlisted")
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return nil, errors.New("native wrapper arguments are not JSON encodable")
	}
	cmd := exec.CommandContext(ctx, "python3", candidateRemoteScript, "native", operation, "--native-args", string(payload))
	cmd.Dir = r.root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		} else {
			return err
		}
	}
	stdout, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("fixed native wrapper transport canceled: %w", ctx.Err())
		}
		return nil, errors.New("fixed native wrapper transport failed")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(stdout, &object) != nil || object == nil {
		return nil, errors.New("fixed native wrapper returned invalid JSON object")
	}
	return json.RawMessage(stdout), nil
}

func (r *RemoteLifecycle) ExportStatus(ctx context.Context) (NativeStatus, error) {
	var out struct {
		Status     map[string]json.RawMessage `json:"status"`
		Descriptor map[string]json.RawMessage `json:"descriptor"`
		Transfer   map[string]json.RawMessage `json:"transfer"`
	}
	raw, err := r.invoke(ctx, "export_status", map[string]any{})
	if err != nil {
		return NativeStatus{}, err
	}
	if json.Unmarshal(raw, &out) != nil || out.Status == nil || out.Descriptor == nil || out.Transfer == nil {
		return NativeStatus{}, errors.New("native status export schema mismatch")
	}
	var remoteDescriptor struct {
		Path   string `json:"path"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	var transfer struct {
		Path   string `json:"path"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	if json.Unmarshal(mustJSON(out.Descriptor), &remoteDescriptor) != nil ||
		json.Unmarshal(mustJSON(out.Transfer), &transfer) != nil ||
		!statusExportMatches(remoteDescriptor.Path, remoteDescriptor.Bytes, remoteDescriptor.SHA256, transfer.Path, transfer.Bytes, transfer.SHA256, r.root) {
		return NativeStatus{}, errors.New("native status descriptor and exact transfer do not match")
	}
	var status NativeStatus
	if err := decodeWitnessStatus(out.Status, &status); err != nil {
		return NativeStatus{}, err
	}
	return status, nil
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func statusExportMatches(remotePath string, remoteBytes int64, remoteSHA, localPath string, localBytes int64, localSHA, root string) bool {
	return remoteBytes > 0 && remoteBytes == localBytes && digestRE.MatchString(remoteSHA) && remoteSHA == localSHA &&
		strings.HasPrefix(remotePath, "/srv/wow/work/core3-native-earned-upgrade-harness-20261022/status/status-") &&
		filepath.Dir(remotePath) == "/srv/wow/work/core3-native-earned-upgrade-harness-20261022/status" &&
		filepath.Base(remotePath) == "status-"+remoteSHA+".json" &&
		filepath.Dir(localPath) == filepath.Join(root, "work/core3-native-earned-upgrade-harness-20261022/receipts") &&
		filepath.Base(localPath) == "status-"+remoteSHA+".json"
}

func decodeWitnessStatus(raw map[string]json.RawMessage, out *NativeStatus) error {
	decode := func(object map[string]json.RawMessage, key string, target any) error {
		value, exists := object[key]
		if !exists || string(value) == "null" {
			return nil // Absent source fields remain explicitly unknown zero values.
		}
		if err := json.Unmarshal(value, target); err != nil {
			return errors.New("native status field has invalid type: " + key)
		}
		return nil
	}
	if err := decode(raw, "mode", &out.Mode); err != nil {
		return err
	}
	var obs map[string]json.RawMessage
	if value := raw["gear_observation"]; len(value) == 0 || json.Unmarshal(value, &obs) != nil || obs == nil {
		return errors.New("native status lacks gear_observation object")
	}
	for key, target := range map[string]any{
		"status": &out.Observation.Status, "identity_key": &out.Observation.Actor,
		"source_id": &out.Observation.Source, "strategic_generation": &out.Observation.Generation,
		"session_incarnation": &out.Observation.Session, "slot_key": &out.Observation.SlotKey,
	} {
		if err := decode(obs, key, target); err != nil {
			return err
		}
	}
	if value := obs["gear"]; len(value) > 0 && string(value) != "null" {
		out.Observation.Gear = append(json.RawMessage(nil), value...)
	}
	var binding []uint64
	if value := obs["binding"]; len(value) > 0 && string(value) != "null" {
		if json.Unmarshal(value, &binding) != nil || len(binding) != 2 || binding[0] == 0 || binding[1] == 0 {
			return errors.New("native status actor binding schema mismatch")
		}
		out.Observation.AccountID, out.Observation.CharacterGUID = binding[0], binding[1]
	}
	if value, exists := raw["gear_observation_request"]; exists {
		out.ObservationPendingKnown = true
		if string(value) != "null" {
			var request struct {
				SlotKey  string `json:"slot_key"`
				SourceID string `json:"source_id"`
			}
			if json.Unmarshal(value, &request) != nil || request.SlotKey == "" || request.SourceID == "" {
				return errors.New("native pending gear observation request schema mismatch")
			}
			out.ObservationPending = true
			out.PendingObservationSlot = request.SlotKey
			out.PendingObservationSource = request.SourceID
		}
	}
	if value := raw["capture"]; len(value) > 0 {
		if string(value) != "null" {
			out.CaptureRetained = true
		}
		var capture struct {
			OwnerQuiescent bool `json:"owner_quiescent"`
		}
		if string(value) != "null" {
			if json.Unmarshal(value, &capture) != nil {
				return errors.New("native capture status schema mismatch")
			}
			out.CaptureQuiescent = capture.OwnerQuiescent
		}
	}
	decodeACKs := func(value json.RawMessage) ([]AckPair, error) {
		return decodeACKPairTuples(value)
	}
	if value := raw["required_acks"]; len(value) > 0 {
		var err error
		out.RequiredACKs, err = decodeACKs(value)
		if err != nil {
			return err
		}
	}
	if value := raw["applied_acks"]; len(value) > 0 {
		var err error
		out.AppliedACKs, err = decodeACKs(value)
		if err != nil {
			return err
		}
	}
	if value := raw["custody_ack"]; len(value) > 0 {
		if string(value) == "null" {
			out.ACKQueueEmpty = true
		} else {
			out.ACKQueueActive = true
			var queue struct {
				OriginalDescriptor *CaptureDescriptor `json:"original_descriptor"`
				ACKPairs           json.RawMessage    `json:"ack_pairs"`
				Records            []struct {
					RecordID json.Number `json:"record_id"`
				} `json:"records"`
			}
			if json.Unmarshal(value, &queue) != nil {
				return errors.New("native custody ACK queue schema mismatch")
			}
			if queue.OriginalDescriptor == nil || !validReceiptDescriptor(*queue.OriginalDescriptor) {
				return errors.New("native custody ACK original descriptor missing or malformed")
			}
			out.ACKOriginal = queue.OriginalDescriptor
			if len(queue.ACKPairs) == 0 || string(queue.ACKPairs) == "null" {
				return errors.New("native custody ACK queue omits exact ACK pairs")
			}
			queuePairs, err := decodeACKPairTuples(queue.ACKPairs)
			if err != nil || len(queuePairs) == 0 {
				return errors.New("native custody ACK queue ACK pairs malformed")
			}
			for _, record := range queue.Records {
				id, parseErr := strconv.ParseUint(record.RecordID.String(), 10, 64)
				if parseErr != nil || id == 0 {
					return errors.New("native ACK record id missing")
				}
				out.ACKRecordIDs = append(out.ACKRecordIDs, strconv.FormatUint(id, 10))
			}
			if !ackRecordIDsMatch(out.ACKRecordIDs, queuePairs) {
				return errors.New("native custody ACK record IDs do not match exact ACK pairs")
			}
			if !ackRecordIDsMatch(out.ACKRecordIDs, out.RequiredACKs) || !sameAckPairs(queuePairs, out.RequiredACKs) {
				return errors.New("native custody ACK association does not match required ACK pairs")
			}
		}
	}
	for key, target := range map[string]any{
		"session_lifecycle": &out.RelogLifecycle, "session_incarnation": &out.RelogSession,
	} {
		if err := decode(raw, key, target); err != nil {
			return err
		}
	}
	if value := raw["gear"]; len(value) > 0 && string(value) != "null" {
		out.Gear = append(json.RawMessage(nil), value...)
		var gear gearWire
		if json.Unmarshal(value, &gear) != nil {
			return errors.New("native gear status schema mismatch")
		}
		if out.RelogSession == 0 {
			out.RelogSession = gear.Session
		}
	}
	return nil
}

func decodeACKPairTuples(value json.RawMessage) ([]AckPair, error) {
	var rows [][]json.RawMessage
	if json.Unmarshal(value, &rows) != nil {
		return nil, errors.New("native ACK identity tuple schema mismatch")
	}
	pairs := make([]AckPair, 0, len(rows))
	for _, row := range rows {
		var pair AckPair
		var recordID uint64
		if len(row) != 2 || json.Unmarshal(row[0], &recordID) != nil || recordID == 0 || json.Unmarshal(row[1], &pair.RecordSHA256) != nil || !digestRE.MatchString(pair.RecordSHA256) {
			return nil, errors.New("native ACK identity tuple invalid")
		}
		pair.RecordID = strconv.FormatUint(recordID, 10)
		pairs = append(pairs, pair)
	}
	return pairs, nil
}

func (r *RemoteLifecycle) simple(ctx context.Context, operation string, args map[string]any) error {
	raw, err := r.invoke(ctx, operation, args)
	if err != nil {
		return err
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return errors.New("native wrapper result schema mismatch")
	}
	readString := func(key string) string {
		var value string
		_ = json.Unmarshal(result[key], &value)
		return value
	}
	readBool := func(key string) (bool, bool) {
		var value bool
		if len(result[key]) == 0 || json.Unmarshal(result[key], &value) != nil {
			return false, false
		}
		return value, true
	}
	falseValue, hasNativeEffect := readBool("native_effect")
	switch operation {
	case "queue_gear_observation":
		slot, ok := args["slot_key"].(string)
		if !ok || !queueObservationResultMatches(result, slot) {
			return errors.New("native gear observation queue result schema mismatch")
		}
	case "stage_gear":
		if readString("staged") != "gear" || !hasNativeEffect || falseValue {
			return errors.New("native gear staging result schema mismatch")
		}
	case "arm_capture":
		armed, okArmed := readBool("armed")
		ready, okReady := readBool("ready")
		if !okArmed || !armed || !okReady || ready {
			return errors.New("native capture arm result schema mismatch")
		}
	case "set_mode":
		mode, ok := args["mode"].(string)
		if !ok || readString("queued") != mode || !hasNativeEffect || falseValue {
			return errors.New("native witness mode result schema mismatch")
		}
	case "release_capture":
		released, ok := readBool("released")
		if !ok || readString("native_owner") == "" {
			return errors.New("native capture release result schema mismatch")
		}
		_ = released // A pending owner disable is valid; quiescence is checked separately.
	default:
		return errors.New("native wrapper operation has no typed result schema")
	}
	return nil
}

func queueObservationResultMatches(result map[string]json.RawMessage, slot string) bool {
	var queued, observedSlot string
	var nativeEffect *bool
	return json.Unmarshal(result["queued"], &queued) == nil && queued == "gear_observation" &&
		json.Unmarshal(result["slot_key"], &observedSlot) == nil && observedSlot == slot &&
		json.Unmarshal(result["native_effect"], &nativeEffect) == nil && nativeEffect != nil && !*nativeEffect
}

func (r *RemoteLifecycle) QueueObservation(ctx context.Context, slot string) error {
	return r.simple(ctx, "queue_gear_observation", map[string]any{"slot_key": slot})
}
func (r *RemoteLifecycle) StageGear(ctx context.Context, path, digest string) error {
	return r.simple(ctx, "stage_gear", map[string]any{"path": path, "sha256": digest})
}
func (r *RemoteLifecycle) ArmCapture(ctx context.Context, a CaptureArm) error {
	return r.simple(ctx, "arm_capture", map[string]any{"identity_key": a.IdentityKey, "binding": []uint64{a.AccountID, a.CharacterGUID}, "slot_key": a.SlotKey, "snapshot_id": a.SnapshotID, "exporter_revision": a.ExporterRevision})
}
func (r *RemoteLifecycle) SetMode(ctx context.Context, mode string) error {
	if mode != "observe" && mode != "gear" && mode != "relog" {
		return errors.New("unsupported fixed witness mode")
	}
	return r.simple(ctx, "set_mode", map[string]any{"mode": mode})
}
func (r *RemoteLifecycle) DrainCapture(ctx context.Context) (NativeAcquisition, error) {
	raw, err := r.invoke(ctx, "drain_capture", map[string]any{})
	if err != nil {
		return NativeAcquisition{}, err
	}
	return r.mapDrainResponse(raw)
}

func (r *RemoteLifecycle) mapDrainResponse(raw []byte) (NativeAcquisition, error) {
	var receipt struct {
		Descriptor CaptureDescriptor `json:"descriptor"`
		Capture    struct {
			Path       string `json:"path"`
			Bytes      int64  `json:"bytes"`
			SHA256     string `json:"sha256"`
			Generation string `json:"generation"`
			Ready      bool   `json:"ready"`
		} `json:"capture"`
	}
	if json.Unmarshal(raw, &receipt) != nil || !validReceiptDescriptor(receipt.Descriptor) ||
		receipt.Capture.Bytes != receipt.Descriptor.Bytes || receipt.Capture.SHA256 != receipt.Descriptor.SHA256 ||
		receipt.Capture.Generation != receipt.Descriptor.Generation || receipt.Capture.Ready ||
		receipt.Capture.Path != filepath.Join(r.root, "work/core3-native-earned-upgrade-harness-20261022/receipts", filepath.Base(receipt.Descriptor.Path)) {
		return NativeAcquisition{}, errors.New("capture drain response schema mismatch")
	}
	data, err := readExactReceipt(receipt.Capture.Path, receipt.Capture.Path, receipt.Descriptor, r.root)
	if err != nil {
		return NativeAcquisition{Receipt: receipt.Descriptor}, err
	}
	acquisition, err := decodeNativeAcquisition(data, receipt.Descriptor)
	if err != nil {
		return NativeAcquisition{Receipt: receipt.Descriptor}, err
	}
	return acquisition, nil
}
func (r *RemoteLifecycle) RecoverCapture(ctx context.Context, d CaptureDescriptor) error {
	raw, err := r.invoke(ctx, "recover_capture", map[string]any{"descriptor": d})
	if err != nil {
		return err
	}
	var result struct {
		Recovering bool `json:"recovering"`
		Ready      bool `json:"ready"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.Recovering || result.Ready {
		return errors.New("native capture recovery result schema mismatch")
	}
	return nil
}
func (r *RemoteLifecycle) ReleaseCapture(ctx context.Context) error {
	return r.simple(ctx, "release_capture", map[string]any{})
}
func (r *RemoteLifecycle) PrepareAck(ctx context.Context, d CaptureDescriptor) ([]AckPair, error) {
	raw, err := r.invoke(ctx, "prepare_ack", map[string]any{"descriptor": d})
	if err != nil {
		return nil, err
	}
	var value struct {
		Queued int `json:"queued"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Queued <= 0 {
		return nil, errors.New("native ACK prepare result omitted durable queued count")
	}
	status, err := waitStatus(ctx, r, func(s NativeStatus) bool {
		return s.ACKQueueActive && len(s.ACKRecordIDs) > 0 && len(s.RequiredACKs) > 0
	})
	if err != nil {
		return nil, err
	}
	if !ackRecordIDsMatch(status.ACKRecordIDs, status.RequiredACKs) {
		return nil, errors.New("native durable ACK queue identity cardinality mismatch")
	}
	return status.RequiredACKs, nil
}
func (r *RemoteLifecycle) RecoverAck(ctx context.Context, d CaptureDescriptor) (AckRecovery, error) {
	raw, err := r.invoke(ctx, "recover_ack", map[string]any{"descriptor": d})
	if err != nil {
		return AckRecovery{}, err
	}
	return decodeACKRecovery(raw, d)
}

func decodeACKRecovery(raw json.RawMessage, d CaptureDescriptor) (AckRecovery, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || envelope == nil {
		return AckRecovery{}, errors.New("native ACK recovery result schema mismatch")
	}
	queuedRaw, queuedPresent := envelope["queued"]
	nativeRaw, nativePresent := envelope["native_release"]
	if !queuedPresent || len(queuedRaw) == 0 || strings.TrimSpace(string(queuedRaw)) == "null" ||
		!nativePresent || len(nativeRaw) == 0 || strings.TrimSpace(string(nativeRaw)) == "null" {
		return AckRecovery{}, errors.New("native ACK recovery result omits required scalar fields")
	}
	var result struct {
		Queued             int                `json:"queued"`
		Native             bool               `json:"native_release"`
		OriginalDescriptor *CaptureDescriptor `json:"original_descriptor"`
		ACKPairs           json.RawMessage    `json:"ack_pairs"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Queued < 0 || result.Native {
		return AckRecovery{}, errors.New("native ACK recovery result schema mismatch")
	}
	if result.OriginalDescriptor == nil || len(result.ACKPairs) == 0 {
		return AckRecovery{}, errors.New("Core1 recover_native_custody_ack result omits original_descriptor and ACK pairs")
	}
	pairs, err := decodeACKPairTuples(result.ACKPairs)
	if err != nil {
		return AckRecovery{}, err
	}
	if *result.OriginalDescriptor != d || !validReceiptDescriptor(*result.OriginalDescriptor) || len(pairs) == 0 {
		return AckRecovery{}, errors.New("native ACK recovery response is not bound to the supplied exact original")
	}
	return AckRecovery{Receipt: *result.OriginalDescriptor, Pairs: pairs}, nil
}
func (r *RemoteLifecycle) SettleAck(ctx context.Context) (AckSettlement, error) {
	if _, err := r.invoke(ctx, "settle_ack", map[string]any{}); err != nil {
		return AckSettlement{}, err
	}
	status, err := waitStatus(ctx, r, func(s NativeStatus) bool {
		return s.ACKQueueEmpty && len(s.RequiredACKs) > 0 && sameAckPairs(s.RequiredACKs, s.AppliedACKs)
	})
	if err != nil {
		return AckSettlement{}, err
	}
	return AckSettlement{QueueEmpty: status.ACKQueueEmpty, Required: status.RequiredACKs, Applied: status.AppliedACKs}, nil
}
