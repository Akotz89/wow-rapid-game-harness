package harness

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPrivateConfig(t *testing.T) {
	good := config{"127.0.0.1:3724", "RHABCDEF123456", "LOCAL-TEST-ONLY", "Rhfixture"}
	for _, test := range []struct {
		name  string
		alter func(*config)
		mode  os.FileMode
		bad   bool
	}{
		{"valid", func(*config) {}, 0600, false},
		{"foreign_account", func(c *config) { c.Username = "PLAYER" }, 0600, true},
		{"foreign_character", func(c *config) { c.Character = "Existing" }, 0600, true},
		{"missing_password", func(c *config) { c.Password = "" }, 0600, true},
		{"native_password_limit", func(c *config) { c.Password = strings.Repeat("A", 17) }, 0600, true},
		{"malformed_address", func(c *config) { c.AuthAddr = "wrong" }, 0600, true},
		{"invalid_port", func(c *config) { c.AuthAddr = "localhost:99999" }, 0600, true},
		{"group_read", func(*config) {}, 0640, true},
		{"world_read", func(*config) {}, 0604, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := good
			test.alter(&c)
			path := filepath.Join(t.TempDir(), "fixture.json")
			data, _ := json.Marshal(c)
			if err := os.WriteFile(path, data, test.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, test.mode); err != nil {
				t.Fatal(err)
			}
			_, err := readConfig(path)
			if (err != nil) != test.bad {
				t.Fatalf("validation error=%v want rejection=%v", err, test.bad)
			}
			if err != nil && strings.Contains(err.Error(), good.Password) {
				t.Fatal("secret in error")
			}
		})
	}
	t.Run("missing_malformed_symlink", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fixture.json")
		if _, err := readConfig(path); err == nil {
			t.Fatal("missing accepted")
		}
		if err := os.WriteFile(path, []byte("{secret-invalid"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readConfig(path); err == nil {
			t.Fatal("malformed accepted")
		}
		link := path + ".link"
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if _, err := readConfig(link); err == nil {
			t.Fatal("symlink accepted")
		}
	})
	t.Run("fifo_and_oversize", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fifo")
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "python3", "config.py", path)
		if err := cmd.Run(); err == nil || ctx.Err() != nil {
			t.Fatal("FIFO must reject without blocking")
		}
		large := path + ".json"
		if err := os.WriteFile(large, []byte(strings.Repeat(" ", 4097)), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readConfig(large); err == nil {
			t.Fatal("oversize config accepted")
		}
	})
}

func TestConfigConsumesValidatedSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.json")
	c := config{"127.0.0.1:3724", "RHABCDEF123456", "LOCAL-TEST-ONLY", "Rhfixture"}
	data, _ := json.Marshal(c)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	// Replace the pathname after validation emits its snapshot but before it exits.
	wrapper := "#!/bin/sh\n'" + python + "' \"$@\" || exit 1\nprintf '{}' > \"$3\"\n"
	if err := os.WriteFile(filepath.Join(dir, "python3"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	got, err := readConfig(path)
	if err != nil || got != c {
		t.Fatal("validated snapshot was not consumed")
	}
}

func TestConfigFingerprintBindsEveryField(t *testing.T) {
	c := config{"127.0.0.1:3724", "RHABCDEF123456", "LOCAL-TEST-ONLY", "Rhfixture"}
	for _, alter := range []func(*config){func(c *config) { c.AuthAddr += "1" }, func(c *config) { c.Username += "1" }, func(c *config) { c.Password += "1" }, func(c *config) { c.Character += "a" }} {
		other := c
		alter(&other)
		if configFingerprint(other) == configFingerprint(c) {
			t.Fatal("fingerprint did not bind a fixture field")
		}
	}
}

func TestLoginReadyRequiresFullSuccessfulSubtest(t *testing.T) {
	o := observation{GUID: 42}
	if loginReady(false, true, o) || loginReady(true, false, o) || loginReady(true, true, observation{}) {
		t.Fatal("partial login enabled dependent actions")
	}
	if !loginReady(true, true, o) {
		t.Fatal("completed login rejected")
	}
}

func TestOracleIdentityMovementPersistence(t *testing.T) {
	a := observation{Name: "Rhfixture", GUID: 42, Map: 0, X: 1, Y: 2, Z: 3, Level: 1, Health: 52, Mana: 100, Online: true}
	b := a
	b.X++
	if err := moved(a, b, b); err != nil {
		t.Fatal(err)
	}
	if err := persisted(b, b); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		alter func(*observation)
	}{
		{"wrong_name", func(o *observation) { o.Name = "Other" }},
		{"wrong_guid", func(o *observation) { o.GUID++ }},
		{"dead", func(o *observation) { o.Health = 0 }},
		{"progression_changed", func(o *observation) { o.Level++ }},
		{"wrong_map", func(o *observation) { o.Map++ }},
		{"no_movement", func(o *observation) { o.X = a.X }},
		{"excess_movement", func(o *observation) { o.X += 3 }},
		{"backwards", func(o *observation) { o.X = a.X - 1 }},
		{"sideways", func(o *observation) { o.X = a.X; o.Y++ }},
		{"vertical", func(o *observation) { o.X = a.X; o.Z++ }},
		{"off_angle", func(o *observation) { o.X = a.X + math.Cos(0.2); o.Y = a.Y + math.Sin(0.2) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := b
			test.alter(&bad)
			if moved(a, b, bad) == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}
	bad := b
	bad.Y++
	if persisted(b, bad) == nil {
		t.Fatal("relog position mismatch accepted")
	}
	data, _ := json.Marshal(a)
	if _, err := decodeObservation(data); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"{}", "null", `{"name":"Rhfixture","guid":42}`, strings.Replace(string(data), "true", "false", 1), strings.Replace(string(data), `"x":1`, `"x":null`, 1)} {
		if _, err := decodeObservation([]byte(data)); err == nil {
			t.Fatal("absent/offline/malformed observation accepted")
		}
	}
}

func TestMovementEndpointAtNonzeroFacing(t *testing.T) {
	a := observation{Name: "Rhfixture", GUID: 42, Map: 0, X: 10, Y: 20, Z: 30, Level: 1, Health: 52, Online: true}
	for _, facing := range []float64{math.Pi / 2, -2.4} {
		expected := a
		expected.X += math.Cos(facing)
		expected.Y += math.Sin(facing)
		if err := moved(a, expected, expected); err != nil {
			t.Fatal(err)
		}
		for _, angle := range []float64{0.2, math.Pi / 2, math.Pi} {
			bad := a
			bad.X += math.Cos(facing + angle)
			bad.Y += math.Sin(facing + angle)
			if moved(a, expected, bad) == nil {
				t.Fatalf("wrong direction accepted at facing=%f angle=%f", facing, angle)
			}
		}
		near := expected
		near.Z += 0.14
		if err := moved(a, expected, near); err != nil {
			t.Fatal("endpoint within tolerance rejected", err)
		}
		near.Z = expected.Z + 0.16
		if moved(a, expected, near) == nil {
			t.Fatal("endpoint outside tolerance accepted")
		}
	}
}

func TestPositionOraclesRejectNonfiniteCoordinates(t *testing.T) {
	a := observation{Name: "Rhfixture", GUID: 42, Level: 1, Health: 52}
	expected := a
	expected.X = 1
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, axis := range []string{"x", "y", "z"} {
			bad := expected
			switch axis {
			case "x":
				bad.X = value
			case "y":
				bad.Y = value
			case "z":
				bad.Z = value
			}
			if moved(a, bad, expected) == nil || moved(a, expected, bad) == nil || persisted(bad, expected) == nil || persisted(expected, bad) == nil {
				t.Fatalf("nonfinite %s coordinate accepted", axis)
			}
		}
	}
}

func TestChatEchoRequiresExactServerMessage(t *testing.T) {
	nonce := "rh-nonce"
	packet := make([]byte, 29+len(nonce)+1+1)
	packet[0] = 0x01 // Native SharedDefines.h CHAT_MSG_SAY.
	binary.LittleEndian.PutUint64(packet[5:13], 42)
	binary.LittleEndian.PutUint32(packet[25:29], uint32(len(nonce)+1))
	copy(packet[29:], nonce)
	if !chatEcho(packet, 42, nonce) {
		t.Fatal("exact echo rejected")
	}
	if chatEcho(packet, 43, nonce) || chatEcho(packet, 42, "nonce") || chatEcho(packet[:30], 42, nonce) {
		t.Fatal("mismatch accepted")
	}
	packet[0] = 6
	if chatEcho(packet, 42, nonce) {
		t.Fatal("wrong chat type accepted")
	}
}

func TestDeadlineDoesNotSoftPass(t *testing.T) {
	for _, test := range []struct {
		name    string
		observe func(context.Context) (observation, error)
	}{
		{"absent", func(context.Context) (observation, error) { return observation{}, errors.New("absent") }},
		{"mismatch", func(context.Context) (observation, error) { return observation{GUID: 99}, nil }},
		{"valid_after_deadline", func(context.Context) (observation, error) {
			time.Sleep(40 * time.Millisecond)
			return observation{GUID: 42}, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			_, err := poll(ctx, test.observe, func(o observation) error {
				if o.GUID != 42 {
					return errors.New("wrong identity")
				}
				return nil
			})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("want deadline, got %v", err)
			}
		})
	}
}

func TestSpellRequiresMatchingSuccessfulServerResult(t *testing.T) {
	t.Run("expired_with_buffered_success", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		results := make(chan spellResult, 1)
		results <- spellResult{2050, true, 0}
		if err := waitSpell(ctx, results, 2050); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("expired successful result accepted")
		}
	})
	t.Run("closed_channel", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		results := make(chan spellResult)
		close(results)
		if err := waitSpell(ctx, results, 2050); err == nil || ctx.Err() != nil {
			t.Fatal("closed result channel did not fail immediately")
		}
	})
	for _, test := range []struct {
		name    string
		result  spellResult
		wantErr bool
	}{
		{"success", spellResult{2050, true, 0}, false},
		{"rejected", spellResult{2050, false, 5}, true},
		{"wrong_spell", spellResult{99, true, 0}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			results := make(chan spellResult, 1)
			results <- test.result
			if err := waitSpell(ctx, results, 2050); (err != nil) != test.wantErr {
				t.Fatalf("error=%v want rejection=%v", err, test.wantErr)
			}
		})
	}
}

func TestNativeSpellReceipts(t *testing.T) {
	const guid uint64 = 1120
	packed := func(value uint64) []byte {
		p := []byte{0}
		for i := 0; i < 8; i++ {
			if b := byte(value >> (8 * i)); b != 0 {
				p[0] |= 1 << i
				p = append(p, b)
			}
		}
		return p
	}
	goPacket := func(caster, real, target uint64, flags uint32, hits []uint64, miss int) []byte {
		p := append(packed(caster), packed(real)...)
		p = append(p, 0)
		p = binary.LittleEndian.AppendUint32(p, 2050)
		p = binary.LittleEndian.AppendUint32(p, flags)
		p = binary.LittleEndian.AppendUint32(p, 123)
		p = append(p, byte(len(hits)))
		for _, hit := range hits {
			p = binary.LittleEndian.AppendUint64(p, hit)
		}
		if miss < 0 {
			p = append(p, 0)
		} else {
			p = append(p, 1)
			p = binary.LittleEndian.AppendUint64(p, guid)
			p = append(p, byte(miss))
			if miss == 11 {
				p = append(p, 7)
			}
		}
		p = binary.LittleEndian.AppendUint32(p, 2)
		p = append(p, packed(target)...)
		if flags&0x800 != 0 {
			p = binary.LittleEndian.AppendUint32(p, 88)
		}
		return p
	}
	failurePacket := func(caster uint64) []byte {
		p := append(packed(caster), 0)
		p = binary.LittleEndian.AppendUint32(p, 2050)
		return append(p, 40)
	}
	check := func(t *testing.T, op uint16, p []byte, owner uint64, want spellResult, accepted bool) {
		t.Helper()
		got, ok := nativeSpellReceipt(op, p, owner, 2050, 0)
		if ok != accepted || (ok && got != want) {
			t.Fatalf("opcode=%x packet=%x result=%+v accepted=%v; want %+v accepted=%v", op, p, got, ok, want, accepted)
		}
	}
	success := spellResult{2050, true, 0}
	rejected := spellResult{2050, false, 40}
	valid := goPacket(guid, guid, guid, 0x900, []uint64{guid}, -1)
	for _, test := range []struct {
		name   string
		packet []byte
	}{
		{"self_hit", valid},
		{"no_power", goPacket(guid, guid, guid, 0x100, []uint64{guid}, -1)},
		{"pending_no_gcd", goPacket(guid, guid, guid, 0x40901, []uint64{guid}, -1)},
		{"immune", goPacket(guid, guid, guid, 0x900, nil, 7)},
		{"immune2", goPacket(guid, guid, guid, 0x900, nil, 8)},
		{"reflect", goPacket(guid, guid, guid, 0x900, nil, 11)},
		{"empty_lists", goPacket(guid, guid, guid, 0x100, nil, -1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			check(t, 0x132, test.packet, guid, success, true)
			for n := 0; n < len(test.packet); n++ {
				check(t, 0x132, test.packet[:n], guid, spellResult{}, false)
			}
		})
	}
	alter := func(p []byte, offset int, value byte) []byte {
		q := append([]byte(nil), p...)
		q[offset] = value
		return q
	}
	reflect := goPacket(guid, guid, guid, 0x900, nil, 11)
	for _, test := range []struct {
		name   string
		packet []byte
	}{
		{"first_caster", goPacket(42, guid, guid, 0x900, []uint64{guid}, -1)},
		{"real_caster", goPacket(guid, 42, guid, 0x900, []uint64{guid}, -1)},
		{"foreign_hit_and_target", goPacket(guid, guid, 42, 0x900, []uint64{42}, -1)},
		{"self_hit_foreign_target", goPacket(guid, guid, 42, 0x900, []uint64{guid}, -1)},
		{"spell", alter(valid, 7, 1)},
		{"count", alter(valid, 6, 1)},
		{"implicit_self", alter(valid, 29, 0)},
		{"other_mask", alter(valid, 29, 0x10)},
		{"extra_mask", alter(valid, 29, 3)},
		{"unsupported_flags", alter(valid, 11, 0x20)},
		{"unsupported_high_flags", alter(valid, 13, 0x20)},
		{"missing_normal_flag", alter(valid, 12, 8)},
		{"hit_count", alter(valid, 19, 255)},
		{"miss_count", alter(valid, 28, 255)},
		{"packed_caster", []byte{255, 1}},
		{"packed_real_caster", append(packed(guid), 255, 1)},
		{"packed_target", append(append([]byte(nil), valid[:33]...), 255, 1)},
		{"truncated_hit", valid[:27]},
		{"truncated_miss_guid", reflect[:28]},
		{"missing_miss_reason", reflect[:29]},
		{"missing_reflect_result", reflect[:30]},
		{"undefined_miss_reason_12", goPacket(guid, guid, guid, 0x900, nil, 12)},
		{"undefined_miss_reason_255", goPacket(guid, guid, guid, 0x900, nil, 255)},
		{"undefined_reflect_result_12", alter(reflect, 30, 12)},
		{"undefined_reflect_result_255", alter(reflect, 30, 255)},
		{"trailing", append(append([]byte(nil), valid...), 0)},
	} {
		t.Run(test.name, func(t *testing.T) { check(t, 0x132, test.packet, guid, spellResult{}, false) })
	}
	for _, test := range []struct {
		owner  uint64
		length int
	}{{42, 8}, {1120, 9}, {0x010203, 10}, {0x0102030405060708, 15}} {
		t.Run(fmt.Sprintf("failure_guid_%x", test.owner), func(t *testing.T) {
			p := failurePacket(test.owner)
			if len(p) != test.length {
				t.Fatalf("failure length=%d want %d", len(p), test.length)
			}
			check(t, 0x133, p, test.owner, rejected, true)
			for n := 0; n < len(p); n++ {
				check(t, 0x133, p[:n], test.owner, spellResult{}, false)
			}
			check(t, 0x133, p, test.owner+1, spellResult{}, false)
			check(t, 0x133, alter(p, len(p)-6, 1), test.owner, spellResult{}, false)
			check(t, 0x133, alter(p, len(p)-5, 1), test.owner, spellResult{}, false)
			check(t, 0x133, append(p, 0), test.owner, spellResult{}, false)
			results := make(chan spellResult, 1)
			result, _ := nativeSpellReceipt(0x133, p, test.owner, 2050, 0)
			results <- result
			if err := waitSpell(context.Background(), results, 2050); err == nil || !strings.Contains(err.Error(), "reason=40") {
				t.Fatalf("lost native rejection: %v", err)
			}
		})
	}
	castFailed := append([]byte{0}, binary.LittleEndian.AppendUint32(nil, 2050)...)
	castFailed = append(castFailed, 40)
	for _, args := range []int{0, 4, 8} {
		check(t, 0x130, append(append([]byte(nil), castFailed...), make([]byte, args)...), guid, rejected, true)
	}
	for n := 0; n < len(castFailed); n++ {
		check(t, 0x130, castFailed[:n], guid, spellResult{}, false)
	}
	check(t, 0x130, alter(castFailed, 0, 1), guid, spellResult{}, false)
	check(t, 0x130, alter(castFailed, 1, 1), guid, spellResult{}, false)
	check(t, 0x130, alter(castFailed, 5, 0), guid, spellResult{2050, false, 0}, true)
	check(t, 0xffff, valid, guid, spellResult{}, false)
	t.Run("ignored_receipt_deadline", func(t *testing.T) {
		results := make(chan spellResult, 1)
		if result, ok := nativeSpellReceipt(0x132, alter(valid, 6, 1), guid, 2050, 0); ok {
			results <- result
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := waitSpell(ctx, results, 2050); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ignored packet passed: %v", err)
		}
	})
	t.Run("cancelled_buffered_success", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		results := make(chan spellResult, 1)
		result, _ := nativeSpellReceipt(0x132, valid, guid, 2050, 0)
		results <- result
		if err := waitSpell(ctx, results, 2050); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled receipt passed: %v", err)
		}
	})
}

func TestRunnerRejectsBeforeSideEffects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.json")
	data, _ := json.Marshal(config{"127.0.0.1:3724", "FOREIGN", "LOCAL-TEST-ONLY", "Rhfixture"})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "network-called")
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"check", "prepare", "run"} {
		cmd := exec.Command("../../scripts/wow-test", mode)
		cmd.Env = append(os.Environ(), "WOW_TEST_CONFIG="+path, "PATH="+dir+":"+os.Getenv("PATH"))
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("%s accepted invalid fixture", mode)
		}
		if strings.Contains(string(output), "Harness operation=") || strings.Contains(string(output), "LOCAL-TEST-ONLY") {
			t.Fatal("validation ran too late or exposed a secret")
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("network invoked before rejection")
	}
}

func TestRedactionPreservesJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.json")
	c := config{"127.0.0.1:3724", "RHABCDEF123456", "LOCAL-TEST-ONLY", "Rhfixture"}
	data, _ := json.Marshal(c)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"Output": c.Username + " " + c.Password + "\n"})
	cmd := exec.Command("python3", "redact.py", path)
	cmd.Stdin = strings.NewReader(string(input) + "\n")
	output, err := cmd.Output()
	if err != nil {
		t.Fatal("redaction failed")
	}
	var result map[string]string
	if json.Unmarshal(output, &result) != nil {
		t.Fatal("redaction corrupted JSON")
	}
	if strings.Contains(string(output), c.Username) || strings.Contains(string(output), c.Password) || result["Output"] != "[REDACTED] [REDACTED]\n" {
		t.Fatal("credential redaction failed")
	}
}

func TestRunnerRetainsAndPropagatesRemoteFailure(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"scripts", "tools/rapid-game-harness", "work/rapid-game-harness-20261012/go/bin"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile("../../scripts/wow-test")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"scripts/wow-test":                           script,
		"scripts/wow-build-host-guard":               []byte("#!/bin/sh\nexit 0\n"),
		"work/rapid-game-harness-20261012/go/bin/go": []byte("#!/bin/sh\necho 'go version go1.26.9 linux/amd64'\n"),
		"tools/rapid-game-harness/test_remote.py":    []byte("import unittest\nclass Failure(unittest.TestCase):\n def test_failure(self):\n  self.fail('REMOTE_FAILURE_CANARY')\n"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(filepath.Join(root, "scripts/wow-test"), "check")
	cmd.Env = append(os.Environ(), "WOW_TEST_CONFIG=")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "REMOTE_FAILURE_CANARY") {
		t.Fatal("runner did not propagate remote test failure")
	}
	logs, err := filepath.Glob(filepath.Join(root, "work/rapid-game-harness-20261012/run-*/remote-tests.txt"))
	if err != nil || len(logs) != 1 {
		t.Fatal("remote test output not retained")
	}
	data, err := os.ReadFile(logs[0])
	if err != nil || !strings.Contains(string(data), "REMOTE_FAILURE_CANARY") {
		t.Fatal("retained remote output missing")
	}
}
