package nativeupgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

type originalCapture struct {
	Kind       string `json:"kind"`
	SnapshotID string `json:"snapshot_id"`
	Data       struct {
		Schema      string   `json:"schema"`
		IdentityKey string   `json:"identity_key"`
		Binding     []uint64 `json:"binding"`
		NativeSlot  struct {
			Key          string `json:"key"`
			SourceEntry  uint32 `json:"source_entry"`
			ItemEntry    uint32 `json:"item_entry"`
			NativeReward struct {
				Kind  string `json:"kind"`
				Index uint32 `json:"index"`
			} `json:"native_reward"`
		} `json:"native_slot"`
		Selection struct {
			Session    uint64 `json:"session_incarnation"`
			Generation uint64 `json:"strategic_generation"`
			SourceID   string `json:"source_id"`
		} `json:"selection"`
		Owner struct {
			RecordID uint64 `json:"record_id"`
		} `json:"native_capture_owner"`
		Observations []struct {
			Attempts []nativeRecord `json:"attempts"`
		} `json:"observations"`
		CaptureRecords map[string]captureRecordBinding `json:"capture_records"`
	} `json:"data"`
}

type nativeRecord struct {
	RecordID      uint64 `json:"record_id"`
	IdentityKey   string `json:"identity_key"`
	AccountID     uint64 `json:"account_id"`
	CharacterGUID uint64 `json:"character_guid"`
	Session       uint64 `json:"session_incarnation"`
	Generation    uint64 `json:"strategic_generation"`
	SourceKey     string `json:"source_key"`
	QuestID       uint32 `json:"quest_id"`
	RewardKind    string `json:"reward_kind"`
	RewardIndex   uint32 `json:"reward_index"`
	ItemID        uint32 `json:"item_entry"`
	Terminal      bool   `json:"terminal"`
	RecordSHA256  string `json:"record_sha256"`
}

type captureRecordBinding struct {
	IdentityKey   string `json:"identity_key"`
	AccountID     uint64 `json:"account_id"`
	CharacterGUID uint64 `json:"character_guid"`
	Session       uint64 `json:"session_incarnation"`
	Generation    uint64 `json:"strategic_generation"`
	SourceKey     string `json:"source_key"`
	QuestID       uint32 `json:"quest_id"`
	ItemID        uint32 `json:"item_entry"`
}

func readExactReceipt(path, expectedPath string, descriptor CaptureDescriptor, root string) ([]byte, error) {
	if path == "" || filepath.Clean(path) != expectedPath || filepath.Dir(path) != filepath.Join(root, "work/core3-native-earned-upgrade-harness-20261022/receipts") || filepath.Base(path) != filepath.Base(descriptor.Path) {
		return nil, errors.New("native receipt transfer path is outside its fixed owned root")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("native receipt transfer cannot be opened safely")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() != descriptor.Bytes || before.Mode().Perm() != 0o600 {
		return nil, errors.New("native receipt transfer owner/mode/length mismatch")
	}
	info, ok := before.Sys().(*syscall.Stat_t)
	if !ok || info.Nlink != 1 || info.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("native receipt transfer owner/link mismatch")
	}
	data, err := io.ReadAll(file)
	if err != nil || int64(len(data)) != descriptor.Bytes {
		return nil, errors.New("native receipt transfer length mismatch")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("native receipt transfer changed while reading")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != descriptor.SHA256 {
		return nil, errors.New("native receipt transfer digest mismatch")
	}
	return data, nil
}

func decodeNativeAcquisition(data []byte, descriptor CaptureDescriptor) (NativeAcquisition, error) {
	var original originalCapture
	if err := json.Unmarshal(data, &original); err != nil {
		return NativeAcquisition{Receipt: descriptor}, errors.New("native original receipt JSON schema mismatch")
	}
	if original.Kind != "native_execution" || original.SnapshotID == "" || original.SnapshotID != descriptor.Generation ||
		original.Data.Schema != "core1-original-native-quest-observations-v2" || original.Data.IdentityKey == "" ||
		len(original.Data.Binding) != 2 || original.Data.Binding[0] == 0 || original.Data.Binding[1] == 0 ||
		!slotRE.MatchString(original.Data.NativeSlot.Key) || original.Data.NativeSlot.SourceEntry == 0 ||
		original.Data.NativeSlot.ItemEntry == 0 || original.Data.NativeSlot.NativeReward.Kind == "" ||
		original.Data.Selection.Session == 0 || original.Data.Selection.Generation == 0 || original.Data.Owner.RecordID == 0 {
		return NativeAcquisition{Receipt: descriptor}, errors.New("native original receipt lacks accepted owner/source/slot/session fields")
	}
	var matched *nativeRecord
	for _, observation := range original.Data.Observations {
		for i := range observation.Attempts {
			record := &observation.Attempts[i]
			if record.RecordID == original.Data.Owner.RecordID {
				if matched != nil {
					return NativeAcquisition{Receipt: descriptor}, errors.New("native original repeats its owner record")
				}
				matched = record
			}
		}
	}
	if matched == nil || !matched.Terminal || matched.RecordSHA256 == "" || !digestRE.MatchString(matched.RecordSHA256) {
		return NativeAcquisition{Receipt: descriptor}, errors.New("native original lacks its exact terminal owner record and digest")
	}
	recordID := strconv.FormatUint(matched.RecordID, 10)
	binding, found := original.Data.CaptureRecords[recordID]
	if !found || binding.IdentityKey != matched.IdentityKey || binding.AccountID != matched.AccountID ||
		binding.CharacterGUID != matched.CharacterGUID || binding.Session != matched.Session ||
		binding.Generation != matched.Generation || binding.SourceKey != matched.SourceKey ||
		binding.QuestID != matched.QuestID || binding.ItemID != matched.ItemID {
		return NativeAcquisition{Receipt: descriptor}, errors.New("native capture record binding differs from its original journal row")
	}
	if matched.IdentityKey != original.Data.IdentityKey || matched.AccountID != original.Data.Binding[0] ||
		matched.CharacterGUID != original.Data.Binding[1] || matched.Session != original.Data.Selection.Session ||
		matched.Generation != original.Data.Selection.Generation || matched.SourceKey == "" ||
		(matched.QuestID != original.Data.NativeSlot.SourceEntry) ||
		(matched.RewardKind != func() string {
			if original.Data.NativeSlot.NativeReward.Kind == "fixed" {
				return "guaranteed"
			}
			return original.Data.NativeSlot.NativeReward.Kind
		}()) ||
		(matched.RewardIndex != original.Data.NativeSlot.NativeReward.Index) ||
		matched.ItemID != original.Data.NativeSlot.ItemEntry ||
		(original.Data.Selection.SourceID != "" && original.Data.Selection.SourceID != matched.SourceKey) {
		return NativeAcquisition{Receipt: descriptor}, fmt.Errorf("native owner record does not match original selection/source/slot")
	}
	pair := AckPair{RecordID: recordID, RecordSHA256: matched.RecordSHA256}
	return NativeAcquisition{CaptureKind: original.Kind, Actor: original.Data.IdentityKey, Source: matched.SourceKey,
		Generation: matched.Generation, Session: matched.Session, SnapshotID: original.SnapshotID,
		SlotKey: original.Data.NativeSlot.Key, ItemID: matched.ItemID, Receipt: descriptor,
		ACKPairs: []AckPair{pair}}, nil
}
