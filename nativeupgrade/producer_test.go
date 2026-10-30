package nativeupgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidSequenceFixtureCannotPassScenarioWithoutPinnedProducerInputs(t *testing.T) {
	if err := VerifySequence(valid()); err != nil {
		t.Fatalf("fixture should cover sequence validator: %v", err)
	}
	if err := VerifyScenario(context.Background(), valid(), ProducerInputs{}); err == nil {
		t.Fatal("sequence-only fixture passed scenario acceptance without running the pinned Core1 verifier")
	}
}

func TestProducerManifestIdentityJoinsNativeTraceWithoutInventedReceiptDigest(t *testing.T) {
	producer := ProducerResult{Verified: true, Oracle: "sealed original raw/native export", SnapshotID: "native-snapshot-17"}
	if err := verifyScenarioWithProducer(valid(), producer); err != nil {
		t.Fatalf("matching actual shared snapshot identity rejected: %v", err)
	}
	trace := valid()
	trace[1].ReceiptSHA256 = strings.Repeat("a", 64) // native receipt digest remains independently verified
	producer.SnapshotID = "other-snapshot"
	if err := verifyScenarioWithProducer(trace, producer); err == nil {
		t.Fatal("producer/native snapshot mismatch accepted")
	}
	producer.SnapshotID = ""
	if err := verifyScenarioWithProducer(valid(), producer); err == nil {
		t.Fatal("producer result without manifest snapshot identity accepted")
	}
}

func TestProducerVerifierRequiresExactManifestDigestAndCore1Paths(t *testing.T) {
	cases := []ProducerInputs{
		{ManifestPath: "../elsewhere/manifest.json", ManifestSHA256: strings.Repeat("a", 64), OutputDir: "work/core1-native-earned-upgrade-delivery-20261021/output"},
		{ManifestPath: "work/core1-native-earned-upgrade-delivery-20261021/manifest.json", ManifestSHA256: "bad", OutputDir: "work/core1-native-earned-upgrade-delivery-20261021/output"},
	}
	for i, in := range cases {
		if _, err := RunProducerVerifier(context.Background(), in); err == nil {
			t.Fatalf("case %d accepted invalid verifier inputs", i)
		}
	}
}

func TestPinnedProducerSchemaHasNoReceiptBindingAndScenarioFailsClosed(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(root, producerVerifier))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "receipt_sha256") {
		t.Fatal("producer verifier schema changed; review its actual receipt association")
	}
	err = VerifyScenario(context.Background(), valid(), ProducerInputs{})
	if err == nil {
		t.Fatal("sequence-only fixture passed without pinned verifier inputs")
	}
}
