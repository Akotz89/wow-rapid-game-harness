package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"time"
)

type config struct {
	AuthAddr  string `json:"auth_addr"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Character string `json:"character"`
}

func configFingerprint(c config) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{c.AuthAddr, c.Username, c.Password, c.Character}, "\x00")))
	return fmt.Sprintf("%x", sum)
}

func readConfig(path string) (config, error) {
	var c config
	data, err := exec.Command("python3", "config.py", "--json", path).Output()
	if err != nil {
		return c, errors.New("private fixture validation failed")
	}
	if json.Unmarshal(data, &c) != nil {
		return c, errors.New("invalid private fixture JSON")
	}
	return c, nil
}

type observation struct {
	Name   string  `json:"name"`
	GUID   uint64  `json:"guid"`
	Map    uint32  `json:"map"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Z      float64 `json:"z"`
	Level  uint32  `json:"level"`
	Health uint32  `json:"health"`
	Mana   uint32  `json:"mana"`
	Online bool    `json:"online"`
}

func decodeObservation(data []byte) (observation, error) {
	var o observation
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return o, errors.New("invalid native observation")
	}
	for _, key := range []string{"name", "guid", "map", "x", "y", "z", "level", "health", "mana", "online"} {
		if value, ok := fields[key]; !ok || bytes.Equal(value, []byte("null")) {
			return o, errors.New("incomplete native observation")
		}
	}
	if json.Unmarshal(data, &o) != nil {
		return o, errors.New("invalid native observation types")
	}
	if o.Name == "" || o.GUID == 0 || o.Level == 0 || !o.Online {
		return o, errors.New("native player is not in world")
	}
	for _, n := range []float64{o.X, o.Y, o.Z} {
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return o, errors.New("invalid native position")
		}
	}
	return o, nil
}

func identity(o observation, name string, guid uint64) error {
	if o.Name != name || o.GUID != guid || o.Level != 1 || o.Health == 0 {
		return errors.New("native identity, progression, or health mismatch")
	}
	return nil
}

func loginReady(completed bool, hasSession bool, initial observation) bool {
	return completed && hasSession && initial.GUID != 0
}

func distance(a, b observation) float64 {
	return math.Sqrt((a.X-b.X)*(a.X-b.X) + (a.Y-b.Y)*(a.Y-b.Y) + (a.Z-b.Z)*(a.Z-b.Z))
}

func moved(a, expected, b observation) error {
	if err := identity(b, a.Name, a.GUID); err != nil {
		return err
	}
	d := distance(a, b)
	if b.Map != a.Map || !(d >= 0.7 && d <= 1.3) {
		return errors.New("native movement was not about one meter")
	}
	return persisted(expected, b)
}

func persisted(a, b observation) error {
	if err := identity(b, a.Name, a.GUID); err != nil {
		return err
	}
	d := distance(a, b)
	if b.Map != a.Map || math.IsNaN(d) || math.IsInf(d, 0) || d > 0.15 {
		return errors.New("native map or position did not persist")
	}
	return nil
}

// SAY's inspected 3.3.5a layout: type, language, sender, zero, target, length, text, tag.
func chatEcho(data []byte, guid uint64, nonce string) bool {
	if len(data) < 31 || data[0] != 0x01 || binary.LittleEndian.Uint64(data[5:13]) != guid {
		return false
	}
	size := int(binary.LittleEndian.Uint32(data[25:29]))
	return size == len(nonce)+1 && len(data) == 29+size+1 && bytes.Equal(data[29:29+size], append([]byte(nonce), 0))
}

type spellResult struct {
	ID      uint32
	Success bool
	Reason  uint8
}

// shortcut: only the explicit unit-target Lesser Heal GO layout is supported; extend when this scenario emits other flags or targets.
func nativeSpellReceipt(op uint16, data []byte, guid uint64, wanted uint32, count uint8) (spellResult, bool) {
	ignored := spellResult{}
	r := bytes.NewReader(data)
	packedGUID := func() (uint64, bool) {
		mask, err := r.ReadByte()
		if err != nil {
			return 0, false
		}
		var value uint64
		for i := 0; i < 8; i++ {
			if mask&(1<<i) != 0 {
				b, err := r.ReadByte()
				if err != nil {
					return 0, false
				}
				value |= uint64(b) << (8 * i)
			}
		}
		return value, true
	}
	switch op {
	case 0x130: // SMSG_CAST_FAILED has no caster GUID; this is the direct session response.
		var failure struct {
			Count  uint8
			ID     uint32
			Reason uint8
		}
		if binary.Read(r, binary.LittleEndian, &failure) != nil || failure.Count != count || failure.ID != wanted {
			return ignored, false
		}
		// Reason-specific optional arguments do not change rejection.
		return spellResult{wanted, false, failure.Reason}, true
	case 0x133: // SMSG_SPELL_FAILURE
		caster, ok := packedGUID()
		var failure struct {
			Count  uint8
			ID     uint32
			Reason uint8
		}
		if !ok || caster != guid || binary.Read(r, binary.LittleEndian, &failure) != nil || r.Len() != 0 || failure.Count != count || failure.ID != wanted {
			return ignored, false
		}
		return spellResult{wanted, false, failure.Reason}, true
	case 0x132: // SMSG_SPELL_GO
		caster, ok := packedGUID()
		if !ok || caster != guid {
			return ignored, false
		}
		realCaster, ok := packedGUID()
		if !ok || realCaster != guid {
			return ignored, false
		}
		var cast struct {
			Count                uint8
			ID, Flags, Timestamp uint32
		}
		if binary.Read(r, binary.LittleEndian, &cast) != nil || cast.Count != count || cast.ID != wanted || cast.Flags&0x100 == 0 || cast.Flags & ^uint32(0x100|0x800|0x40000|1) != 0 {
			return ignored, false
		}
		hits, err := r.ReadByte()
		if err != nil {
			return ignored, false
		}
		var target uint64
		for i := 0; i < int(hits); i++ {
			if binary.Read(r, binary.LittleEndian, &target) != nil {
				return ignored, false
			}
		}
		misses, err := r.ReadByte()
		if err != nil {
			return ignored, false
		}
		for i := 0; i < int(misses); i++ {
			if binary.Read(r, binary.LittleEndian, &target) != nil {
				return ignored, false
			}
			reason, err := r.ReadByte()
			if err != nil || reason > 11 {
				return ignored, false
			}
			if reason == 11 {
				if reflected, err := r.ReadByte(); err != nil || reflected > 11 {
					return ignored, false
				}
			}
		}
		var mask uint32
		if binary.Read(r, binary.LittleEndian, &mask) != nil || mask != 2 {
			return ignored, false
		}
		target, ok = packedGUID()
		if !ok || target != guid {
			return ignored, false
		}
		if cast.Flags&0x800 != 0 {
			var power uint32
			if binary.Read(r, binary.LittleEndian, &power) != nil {
				return ignored, false
			}
		}
		if r.Len() != 0 {
			return ignored, false
		}
		return spellResult{wanted, true, 0}, true
	}
	return ignored, false
}

func waitSpell(ctx context.Context, results <-chan spellResult, wanted uint32) error {
	for {
		if ctx.Err() != nil {
			return fmt.Errorf("exact server spell result deadline: %w", ctx.Err())
		}
		select {
		case result, open := <-results:
			if !open {
				return errors.New("server spell result channel closed")
			}
			if ctx.Err() != nil {
				return fmt.Errorf("exact server spell result deadline: %w", ctx.Err())
			}
			if result.ID != wanted {
				continue
			}
			if !result.Success {
				return fmt.Errorf("native cast rejected reason=%d", result.Reason)
			}
			return nil
		case <-ctx.Done():
			return fmt.Errorf("exact server spell result deadline: %w", ctx.Err())
		}
	}
}

func poll(ctx context.Context, observe func(context.Context) (observation, error), accept func(observation) error) (observation, error) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return observation{}, fmt.Errorf("native observation deadline: %w", ctx.Err())
		}
		o, err := observe(ctx)
		if ctx.Err() != nil {
			return observation{}, fmt.Errorf("native observation deadline: %w", ctx.Err())
		}
		if err == nil && accept(o) == nil {
			return o, nil
		}
		select {
		case <-ctx.Done():
			return observation{}, fmt.Errorf("native observation deadline: %w", ctx.Err())
		case <-tick.C:
		}
	}
}
