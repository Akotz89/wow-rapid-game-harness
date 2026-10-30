package nativeupgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeOriginalFixture(source string) []byte {
	return []byte(`{"kind":"native_execution","snapshot_id":"snap","data":{"schema":"core1-original-native-quest-observations-v2","identity_key":"actor","binding":[12,34],"native_slot":{"key":"quest:17:fixed:0","source_entry":17,"item_entry":4455,"native_reward":{"kind":"fixed","index":0}},"selection":{"session_incarnation":2,"strategic_generation":9},"native_capture_owner":{"record_id":71},"capture_records":{"71":{"identity_key":"actor","account_id":12,"character_guid":34,"session_incarnation":2,"strategic_generation":9,"source_key":"` + source + `","quest_id":17,"item_entry":4455}},"observations":[{"attempts":[{"record_id":71,"identity_key":"actor","account_id":12,"character_guid":34,"session_incarnation":2,"strategic_generation":9,"source_key":"` + source + `","quest_id":17,"reward_kind":"guaranteed","reward_index":0,"item_entry":4455,"terminal":true,"record_sha256":"` + strings.Repeat("a", 64) + `"}]}]}}`)
}

func TestDrainAdapterMapsRealOriginalSchemaAndRefusesMissingSource(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "missing_source"}[missing], func(t *testing.T) {
			root := t.TempDir()
			receipts := filepath.Join(root, "work/core3-native-earned-upgrade-harness-20261022/receipts")
			if err := os.MkdirAll(receipts, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(receipts, 0o700); err != nil {
				t.Fatal(err)
			}
			data := nativeOriginalFixture("source-17")
			if missing {
				var original map[string]any
				if err := json.Unmarshal(data, &original); err != nil {
					t.Fatal(err)
				}
				row := original["data"].(map[string]any)["observations"].([]any)[0].(map[string]any)["attempts"].([]any)[0].(map[string]any)
				delete(row, "source_key")
				data, _ = json.Marshal(original)
			}
			digestBytes := sha256.Sum256(data)
			digest := hex.EncodeToString(digestBytes[:])
			name := "receipt-" + strings.Repeat("b", 32) + ".json"
			localPath := filepath.Join(receipts, name)
			if err := os.WriteFile(localPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			descriptor := CaptureDescriptor{Path: "/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures/" + name, Bytes: int64(len(data)), SHA256: digest, Generation: "snap", Ready: false}
			response, _ := json.Marshal(map[string]any{
				"descriptor": descriptor,
				"capture":    map[string]any{"path": localPath, "bytes": len(data), "sha256": digest, "generation": "snap", "ready": false},
			})
			ops := &RemoteLifecycle{root: root}
			acq, err := ops.mapDrainResponse(response)
			if missing {
				if err == nil {
					t.Fatalf("missing source accepted: %+v %v", acq, err)
				}
				return
			}
			if err != nil || acq.Source != "source-17" || acq.ItemID != 4455 || acq.Receipt != descriptor || len(acq.ACKPairs) != 1 {
				t.Fatalf("complete original did not map through DrainCapture adapter: %+v %v", acq, err)
			}
		})
	}
}

func TestDecodeNativeAcquisitionFromExactCore1Original(t *testing.T) {
	descriptor := CaptureDescriptor{Path: "/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures/receipt-" + strings.Repeat("b", 32) + ".json", Bytes: 512, SHA256: strings.Repeat("c", 64), Generation: "snap", Ready: false}
	acquisition, err := decodeNativeAcquisition(nativeOriginalFixture("source-17"), descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if acquisition.CaptureKind != "native_execution" || acquisition.Actor != "actor" || acquisition.Source != "source-17" ||
		acquisition.Generation != 9 || acquisition.Session != 2 || acquisition.SnapshotID != "snap" ||
		acquisition.SlotKey != "quest:17:fixed:0" || acquisition.ItemID != 4455 || len(acquisition.ACKPairs) != 1 ||
		acquisition.ACKPairs[0] != (AckPair{RecordID: "71", RecordSHA256: strings.Repeat("a", 64)}) || acquisition.Receipt != descriptor {
		t.Fatalf("accepted capture fields were not mapped exactly: %+v", acquisition)
	}
}

func TestDecodeNativeAcquisitionRefusesMissingOwnerSourceField(t *testing.T) {
	descriptor := CaptureDescriptor{Path: "/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures/receipt-" + strings.Repeat("b", 32) + ".json", Bytes: 512, SHA256: strings.Repeat("c", 64), Generation: "snap", Ready: false}
	missing := json.RawMessage(nativeOriginalFixture(""))
	var receipt map[string]any
	if err := json.Unmarshal(missing, &receipt); err != nil {
		t.Fatal(err)
	}
	data := receipt["data"].(map[string]any)
	delete(data["observations"].([]any)[0].(map[string]any)["attempts"].([]any)[0].(map[string]any), "source_key")
	encoded, _ := json.Marshal(receipt)
	if _, err := decodeNativeAcquisition(encoded, descriptor); err == nil || !strings.Contains(err.Error(), "selection/source/slot") {
		t.Fatalf("capture without accepted source field was not refused: %v", err)
	}
}
