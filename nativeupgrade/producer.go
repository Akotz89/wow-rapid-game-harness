package nativeupgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const producerRoot = "work/core1-native-earned-upgrade-delivery-20261021/finite-custody-integration/candidate/producer"
const producerVerifier = producerRoot + "/verify_generation.py"
const captureRoot = "work/core1-native-earned-upgrade-delivery-20261021"

type ProducerInputs struct {
	ManifestPath   string
	ManifestSHA256 string
	OutputDir      string
}

var ErrProducerSnapshotBindingMissing = errors.New("pinned producer manifest has no snapshot identity shared with the native receipt")

type ProducerResult struct {
	Verified       bool   `json:"verified"`
	Oracle         string `json:"oracle"`
	RootAcceptance bool   `json:"root_acceptance"`
	SnapshotID     string `json:"-"`
}

func repoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, producerVerifier)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "", errors.New("pinned Core1 producer verifier unavailable")
}

func contained(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", errors.New("producer input path must be repository-relative")
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("producer input escapes repository")
	}
	abs := filepath.Join(root, clean)
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", errors.New("producer input path missing")
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("producer input symlink escapes repository")
	}
	return real, nil
}

// RunProducerVerifier invokes the pinned Core1 verifier with its actual sealed
// manifest/digest and evidence bundle. It does not emulate producer proof.
func RunProducerVerifier(ctx context.Context, in ProducerInputs) (ProducerResult, error) {
	var zero ProducerResult
	if ctx == nil || ctx.Err() != nil || !digestRE.MatchString(in.ManifestSHA256) {
		return zero, errors.New("producer verifier inputs incomplete")
	}
	root, err := repoRoot()
	if err != nil {
		return zero, err
	}
	manifest, err := contained(root, in.ManifestPath)
	if err != nil {
		return zero, err
	}
	out, err := contained(root, in.OutputDir)
	if err != nil {
		return zero, err
	}
	if !strings.HasPrefix(filepath.ToSlash(in.ManifestPath), captureRoot+"/") || !strings.HasPrefix(filepath.ToSlash(in.OutputDir), captureRoot+"/") {
		return zero, errors.New("producer input outside accepted Core1 work root")
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		return zero, errors.New("producer manifest unreadable")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != in.ManifestSHA256 {
		return zero, errors.New("producer manifest digest mismatch")
	}
	var identity struct {
		Binding struct {
			SnapshotID string `json:"snapshot_id"`
		} `json:"binding"`
	}
	if json.Unmarshal(data, &identity) != nil || identity.Binding.SnapshotID == "" {
		return zero, ErrProducerSnapshotBindingMissing
	}
	cmd := exec.CommandContext(ctx, "python3", producerVerifier, "--manifest", manifest, "--sha256", in.ManifestSHA256, "--output", out)
	cmd.Dir = root
	stdout, err := cmd.Output()
	if err != nil {
		return zero, errors.New("pinned Core1 producer verifier rejected capture")
	}
	var result ProducerResult
	if json.Unmarshal(stdout, &result) != nil || !result.Verified || result.Oracle != "sealed original raw/native export" || result.RootAcceptance {
		return zero, fmt.Errorf("pinned Core1 verifier returned incomplete lineage/custody result")
	}
	result.SnapshotID = identity.Binding.SnapshotID
	return result, nil
}

// VerifyScenario cannot pass on a sequence fixture alone: the real pinned
// producer verifier must first accept the sealed native evidence bundle.
func VerifyScenario(ctx context.Context, trace []Evidence, in ProducerInputs) error {
	producer, err := RunProducerVerifier(ctx, in)
	if err != nil {
		return err
	}
	return verifyScenarioWithProducer(trace, producer)
}

// VerifyScenarioWithProducer joins the independently verified manifest and
// native receipt on their actual shared snapshot_id. The producer's summary
// does not attest to the gear result or to the native receipt digest.
func verifyScenarioWithProducer(trace []Evidence, producer ProducerResult) error {
	if !producer.Verified || producer.Oracle != "sealed original raw/native export" || producer.RootAcceptance || producer.SnapshotID == "" {
		return errors.New("pinned producer result identity is incomplete")
	}
	if err := VerifySequence(trace); err != nil {
		return err
	}
	for _, event := range trace {
		if event.SnapshotID != producer.SnapshotID {
			return errors.New("native receipt and pinned producer manifest snapshot_id mismatch")
		}
	}
	return nil
}
