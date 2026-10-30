package nativeupgrade

import (
	"math"
	"strings"
	"testing"
)

func valid() []Evidence {
	base := Evidence{Actor: "actor-guid-12", Source: "quest-source-17", Generation: "generation-9", SnapshotID: "native-snapshot-17", Session: "session-1", SlotKey: "quest:17:choice:4", ItemID: 4455}
	path, sha := "/srv/wow/work/core1-native-earned-upgrade-delivery-20261021/captures/receipt-"+strings.Repeat("b", 32)+".json", strings.Repeat("a", 64)
	ackPairs := []AckPair{{RecordID: "ack-1", RecordSHA256: strings.Repeat("d", 64)}}
	event := func(kind Kind) Evidence {
		return Evidence{Kind: kind, Actor: base.Actor, Source: base.Source, Generation: base.Generation, SnapshotID: base.SnapshotID, Session: base.Session, SlotKey: base.SlotKey}
	}
	obs := event(Observed)
	obs.ObservationStatus = "observed"
	acq := event(Acquired)
	acq.ItemID = base.ItemID
	acq.CaptureKind = "native_execution"
	acq.ReceiptPath = path
	acq.ReceiptSHA256 = sha
	trace := []Evidence{obs, acq}
	for _, kind := range []Kind{Drained, Released, AckPrepared, AckApplied, AckSettled} {
		e := event(kind)
		e.ItemID = base.ItemID
		e.ReceiptPath = path
		e.ReceiptSHA256 = sha
		if kind == AckPrepared || kind == AckApplied || kind == AckSettled {
			e.AckID = "ack-1"
			e.ACKPairs = append([]AckPair(nil), ackPairs...)
		}
		trace = append(trace, e)
	}
	gear := event(Improved)
	gear.ItemID = base.ItemID
	gear.ScoreBefore = 10
	gear.ScoreAfter = 11
	trace = append(trace, gear)
	relog := event(RelogSaved)
	relog.NewSession = "session-2"
	trace = append(trace, relog)
	persist := event(RelogObserved)
	persist.Session = "session-2"
	persist.NewSession = "session-2"
	persist.PersistedItemID = base.ItemID
	persist.PersistedSlotKey = base.SlotKey
	trace = append(trace, persist)
	return trace
}

func TestValidSequenceFixturePassesOnlySequenceChecks(t *testing.T) {
	if err := VerifySequence(valid()); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsMissingDuplicateReorderedAndMismatchedIdentity(t *testing.T) {
	cases := map[string]func([]Evidence) []Evidence{
		"missing":           func(x []Evidence) []Evidence { return x[:9] },
		"duplicate":         func(x []Evidence) []Evidence { x[4] = x[3]; return x },
		"reordered":         func(x []Evidence) []Evidence { x[4], x[5] = x[5], x[4]; return x },
		"actor":             func(x []Evidence) []Evidence { x[7].Actor = "other"; return x },
		"source":            func(x []Evidence) []Evidence { x[7].Source = "other"; return x },
		"generation":        func(x []Evidence) []Evidence { x[7].Generation = "old"; return x },
		"snapshot":          func(x []Evidence) []Evidence { x[7].SnapshotID = "other"; return x },
		"stale observation": func(x []Evidence) []Evidence { x[0].ObservationStatus = "queued"; return x },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if VerifySequence(mutate(valid())) == nil {
				t.Fatal("accepted invalid trace")
			}
		})
	}
}

func TestRejectsFakeReceiptWrongGearAndUnimprovedItem(t *testing.T) {
	cases := map[string]func([]Evidence) []Evidence{
		"missing receipt digest": func(x []Evidence) []Evidence { x[1].ReceiptSHA256 = ""; return x },
		"wrong receipt path":     func(x []Evidence) []Evidence { x[1].ReceiptPath = "/tmp/receipt.json"; return x },
		"wrong capture kind":     func(x []Evidence) []Evidence { x[1].CaptureKind = "fixture"; return x },
		"wrong drain digest":     func(x []Evidence) []Evidence { x[2].ReceiptSHA256 = strings.Repeat("c", 64); return x },
		"wrong ACK digest":       func(x []Evidence) []Evidence { x[4].ReceiptSHA256 = ""; return x },
		"wrong item":             func(x []Evidence) []Evidence { x[7].ItemID++; return x },
		"wrong slot":             func(x []Evidence) []Evidence { x[7].SlotKey = "quest:18:choice:4"; return x },
		"no improvement":         func(x []Evidence) []Evidence { x[7].ScoreAfter = x[7].ScoreBefore; return x },
		"NaN":                    func(x []Evidence) []Evidence { x[7].ScoreBefore = math.NaN(); return x },
		"infinite":               func(x []Evidence) []Evidence { x[7].ScoreAfter = math.Inf(1); return x },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if VerifySequence(mutate(valid())) == nil {
				t.Fatal("accepted invalid gear/receipt evidence")
			}
		})
	}
}

func TestRejectsPrematureACKAndMissingRelogPersistence(t *testing.T) {
	cases := map[string]func([]Evidence) []Evidence{
		"ACK before release":   func(x []Evidence) []Evidence { x[4], x[5] = x[5], x[4]; return x },
		"ACK mismatch":         func(x []Evidence) []Evidence { x[6].AckID = "other"; return x },
		"ACK digest mismatch":  func(x []Evidence) []Evidence { x[6].ACKPairs[0].RecordSHA256 = strings.Repeat("e", 64); return x },
		"missing applied tick": func(x []Evidence) []Evidence { x[5].Kind = AckSettled; return x },
		"missing new session":  func(x []Evidence) []Evidence { x[8].NewSession = ""; return x },
		"same session":         func(x []Evidence) []Evidence { x[8].NewSession = x[8].Session; x[9].Session = x[8].Session; return x },
		"wrong persisted item": func(x []Evidence) []Evidence { x[9].PersistedItemID++; return x },
		"wrong persisted slot": func(x []Evidence) []Evidence { x[9].PersistedSlotKey = "quest:17:fixed:0"; return x },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if VerifySequence(mutate(valid())) == nil {
				t.Fatal("accepted invalid cleanup/relog trace")
			}
		})
	}
}
