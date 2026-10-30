//go:build live

package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/azerothcore/AzerothGhost/client"
	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
)

func nativeObservation(ctx context.Context, name string) (observation, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "remote.py", "observe", name)
	// Stay in the runner's group; TERM lets Python reap its SSH child.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = time.Second
	data, err := cmd.Output()
	if err != nil {
		return observation{}, errors.New("authenticated native observation unavailable")
	}
	return decodeObservation(data)
}

func waitNative(name string, accept func(observation) error) (observation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return poll(ctx, func(ctx context.Context) (observation, error) { return nativeObservation(ctx, name) }, accept)
}

func logout(session *e2eharness.Session) error {
	if err := session.World.SendLogout(); err != nil {
		return fmt.Errorf("logout request failed: %w", err)
	}
	if err := session.World.WaitForLogout(25 * time.Second); err != nil {
		return fmt.Errorf("native logout completion failed: %w", err)
	}
	return nil
}

func recoveryObservation(session *e2eharness.Session, initial observation) (observation, error) {
	if session.Name != initial.Name || session.GUID != initial.GUID || !session.World.IsInWorld() {
		return observation{}, errors.New("return session identity or phase mismatch")
	}
	current, err := nativeObservation(context.Background(), initial.Name)
	if err != nil {
		return observation{}, err
	}
	if err := identity(current, initial.Name, initial.GUID); err != nil {
		return observation{}, err
	}
	if current.Map != initial.Map || distance(initial, current) > 2 {
		return observation{}, errors.New("native return outside bounded local recovery area")
	}
	return current, nil
}

func safetyStop(session *e2eharness.Session, initial observation) error {
	current, err := recoveryObservation(session, initial)
	if err != nil {
		return err
	}
	facing := float32(math.Atan2(initial.Y-current.Y, initial.X-current.X))
	if err := session.World.MoveStopAtTime(float32(current.X), float32(current.Y), float32(current.Z), facing, time.Now()); err != nil {
		return errors.New("native safety stop write failed")
	}
	return nil
}

func returnToStart(session *e2eharness.Session, initial observation, stopNeeded bool) (returned observation, err error) {
	if stopNeeded {
		if err := safetyStop(session, initial); err != nil {
			return returned, fmt.Errorf("unresolved movement safety stop failed: %w", err)
		}
	}
	current, err := recoveryObservation(session, initial)
	if err != nil {
		return returned, err
	}
	if persisted(initial, current) == nil {
		return current, nil
	}
	facing := float32(math.Atan2(initial.Y-current.Y, initial.X-current.X))
	stopped := false
	defer func() {
		// A failed write may have moved the actor; refresh native state before stopping.
		if !stopped {
			if stopErr := safetyStop(session, initial); stopErr != nil {
				err = errors.Join(err, fmt.Errorf("return movement safety stop failed: %w", stopErr))
			}
		}
	}()
	if err := session.World.MoveForwardAtTime(float32(current.X), float32(current.Y), float32(current.Z), facing, time.Now()); err != nil {
		return returned, errors.New("return movement start failed")
	}
	// Preserve the existing one-meter/200ms pacing for a bounded observed displacement.
	time.Sleep(time.Duration(math.Ceil(distance(initial, current)*200)) * time.Millisecond)
	if err := session.World.MoveStopAtTime(float32(initial.X), float32(initial.Y), float32(initial.Z), facing, time.Now()); err != nil {
		return returned, errors.New("return movement stop failed")
	}
	stopped = true
	returned, err = waitNative(initial.Name, func(o observation) error { return persisted(initial, o) })
	if err != nil {
		return returned, errors.New("native return-to-start observation failed")
	}
	return returned, nil
}

func TestLiveGameplay(t *testing.T) {
	path := os.Getenv("WOW_TEST_CONFIG")
	if path == "" {
		t.Fatal("WOW_TEST_CONFIG is required for live tests")
	}
	c, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("E2E_WORLD_LOG", "silent")
	var session *e2eharness.Session
	var initial, afterMove observation
	loggedOut := false
	movementAttempted := false
	movementUnresolved := false
	stopOutbound := func(t *testing.T) {
		t.Helper()
		if err := safetyStop(session, initial); err != nil {
			t.Errorf("outbound movement safety stop failed: %v", err)
		} else {
			movementUnresolved = false
		}
	}
	login := func(t *testing.T) (*e2eharness.Session, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		verify := exec.CommandContext(ctx, "python3", "remote.py", "verify", path, configFingerprint(c))
		verify.WaitDelay = time.Second
		if err := verify.Run(); err != nil {
			return nil, errors.New("owned fixture verification failed before login")
		}
		return e2eharness.LoginBot(t, e2eharness.LoginOptions{AuthAddr: c.AuthAddr, User: c.Username, Password: c.Password, CharName: c.Character, Race: 1, Class: 5})
	}
	// Parent test owns sessions so callbacks stay valid until the whole serial suite ends.
	t.Cleanup(func() {
		if session == nil {
			if movementAttempted {
				t.Error("native return-to-start unavailable: no usable session")
			}
			return
		}
		defer session.Close()
		if loggedOut {
			if movementAttempted {
				t.Error("native return-to-start unavailable: session logged out")
			}
			return
		}
		if movementAttempted && initial.GUID != 0 {
			returned, err := returnToStart(session, initial, movementUnresolved)
			if err != nil {
				t.Errorf("native return-to-start failed: %v", err)
			} else {
				t.Logf("native return_to_start_delta_m=%.4f", distance(initial, returned))
			}
		}
		if err := logout(session); err != nil {
			t.Error(err)
		}
	})
	loginComplete := t.Run("Login", func(t *testing.T) {
		session, err = login(t)
		if err != nil {
			t.Fatalf("native login failed: %v", err)
		}
		if session.Name != c.Character || session.GUID == 0 || !session.World.IsInWorld() {
			t.Fatal("native session identity or phase mismatch")
		}
		initial, err = waitNative(c.Character, func(o observation) error { return identity(o, c.Character, session.GUID) })
		if err != nil {
			t.Fatal(err)
		}
		// LOGIN_VERIFY_WORLD is server state; outgoing movement coordinates are never an oracle.
		x, y, z, _, m := session.World.Position()
		verify := initial
		verify.X = float64(x)
		verify.Y = float64(y)
		verify.Z = float64(z)
		verify.Map = m
		if err := persisted(initial, verify); err != nil {
			t.Fatal("login packet and native API disagree")
		}
		t.Logf("native guid=%d map=%d level=%d", initial.GUID, initial.Map, initial.Level)
	})
	requireSession := func(t *testing.T) {
		t.Helper()
		if !loginReady(loginComplete, session != nil, initial) {
			t.Fatal("login prerequisite failed")
		}
	}
	t.Run("ChatEcho", func(t *testing.T) {
		requireSession(t)
		var token [12]byte
		if _, err := rand.Read(token[:]); err != nil {
			t.Fatal("nonce generation failed")
		}
		nonce := "rh-" + hex.EncodeToString(token[:])
		echo := make(chan struct{}, 1)
		cancel := session.World.AddPacketHook(func(op uint16, data []byte) {
			if op == client.SmsgMessageChat && chatEcho(data, session.GUID, nonce) {
				select {
				case echo <- struct{}{}:
				default:
				}
			}
		})
		defer cancel()
		if err := session.World.SendChatMessage(client.ChatMsgSay, client.LangCommon, nonce); err != nil {
			t.Fatal("chat send failed")
		}
		select {
		case <-echo:
		case <-time.After(8 * time.Second):
			t.Fatal("exact server chat echo deadline")
		}
	})
	t.Run("NativeMovement", func(t *testing.T) {
		requireSession(t)
		_, _, _, o, _ := session.World.Position()
		if math.IsNaN(float64(o)) || math.IsInf(float64(o), 0) {
			t.Fatal("native movement orientation is not finite")
		}
		start := time.Now()
		movementAttempted = true
		movementUnresolved = true
		if err := session.World.MoveForwardAtTime(float32(initial.X), float32(initial.Y), float32(initial.Z), o, start); err != nil {
			stopOutbound(t)
			t.Fatal("movement start failed")
		}
		time.Sleep(200 * time.Millisecond)
		expected := initial
		expected.X += math.Cos(float64(o))
		expected.Y += math.Sin(float64(o))
		if err := session.World.MoveStopAtTime(float32(expected.X), float32(expected.Y), float32(expected.Z), o, time.Now()); err != nil {
			stopOutbound(t)
			t.Fatal("movement stop failed")
		}
		movementUnresolved = false
		afterMove, err = waitNative(c.Character, func(o observation) error { return moved(initial, expected, o) })
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("native distance_m=%.3f", distance(initial, afterMove))
	})
	t.Run("LearnedSelfCast", func(t *testing.T) {
		requireSession(t)
		const lesserHeal = 2050
		deadline := time.Now().Add(5 * time.Second)
		for !session.World.KnowsSpell(lesserHeal) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if !session.World.KnowsSpell(lesserHeal) {
			t.Fatal("Lesser Heal is not already learned")
		}
		guid := session.GUID
		const castCount uint8 = 0
		cast := make(chan spellResult, 2)
		cancel := session.World.AddPacketHook(func(op uint16, data []byte) {
			if result, ok := nativeSpellReceipt(op, data, guid, lesserHeal, castCount); ok {
				select {
				case cast <- result:
				default:
				}
			}
		})
		defer cancel()
		if err := session.World.CastSpell(lesserHeal, guid); err != nil {
			t.Fatal("cast send failed")
		}
		ctx, done := context.WithTimeout(context.Background(), 8*time.Second)
		defer done()
		if err := waitSpell(ctx, cast, lesserHeal); err != nil {
			t.Fatal(err)
		}
		if _, err := waitNative(c.Character, func(o observation) error { return identity(o, c.Character, guid) }); err != nil {
			t.Fatal(err)
		}
		t.Logf("validated native spell=%d self_target_guid=%d cast_count=%d", lesserHeal, guid, castCount)
	})
	t.Run("LogoutRelogPersistence", func(t *testing.T) {
		requireSession(t)
		if afterMove.GUID == 0 {
			t.Fatal("native movement prerequisite failed")
		}
		if err := logout(session); err != nil {
			t.Fatal(err)
		}
		loggedOut = true
		session.Close()
		session, err = login(t)
		if err != nil {
			t.Fatalf("native relog failed: %v", err)
		}
		loggedOut = false
		if session.Name != initial.Name || session.GUID != initial.GUID || !session.World.IsInWorld() {
			t.Fatal("relog identity or phase mismatch")
		}
		relog, err := waitNative(c.Character, func(o observation) error { return persisted(afterMove, o) })
		if err != nil {
			t.Fatal(err)
		}
		x, y, z, _, m := session.World.Position()
		verify := relog
		verify.X = float64(x)
		verify.Y = float64(y)
		verify.Z = float64(z)
		verify.Map = m
		if err := persisted(afterMove, verify); err != nil {
			t.Fatal("relog native login packet did not preserve position")
		}
		t.Logf("native same_guid=%d position_delta_m=%.4f level=%d", relog.GUID, distance(afterMove, relog), relog.Level)
	})

	// Opt-in candidate child; default five-case selection remains unchanged.
	if os.Getenv("WOW_NATIVE_EARNED_UPGRADE") == "1" {
		t.Run("NativeEarnedUpgrade", func(t *testing.T) {
			requireSession(t)
			var ctx context.Context
			var cancel context.CancelFunc
			if deadline, ok := t.Deadline(); ok {
				ctx, cancel = context.WithDeadline(context.Background(), deadline)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			if err := runNativeEarnedUpgradeOptIn(ctx, c, session, initial); err != nil {
				t.Fatal(err)
			}
		})
	}
}
