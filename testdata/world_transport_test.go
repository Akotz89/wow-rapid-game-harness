package client

import (
	"bufio"
	"bytes"
	"crypto/rc4"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// All three production entry paths share the same failure guarantees.
var rapidSendModes = []string{"plain", "encrypted", "auth"}

func rapidClient(conn net.Conn, mode string) *WorldClient {
	w := NewWorldClient("SYNTHETIC", make([]byte, 40), nil)
	w.conn = conn
	if mode == "encrypted" {
		w.encryptClient, _ = rc4.NewCipher([]byte("synthetic-key"))
		w.encrypted = true
	}
	return w
}

func rapidSend(w *WorldClient, mode string, data []byte) error {
	if mode == "auth" {
		return w.sendPacketUnencrypted(CmsgAuthSession, data)
	}
	return w.sendPacket(CmsgPing, data)
}

func rapidStopped(t *testing.T, w *WorldClient) {
	t.Helper()
	if !w.IsStopped() {
		t.Fatal("failed connection remains usable")
	}
	select {
	case <-w.StopChan():
	default:
		t.Fatal("terminal write did not signal stop")
	}
}

type rapidConn struct {
	net.Conn
	mode      string
	writes    int
	closed    bool
	deadlines []time.Time
	entered   chan struct{}
}

func (c *rapidConn) SetWriteDeadline(deadline time.Time) error {
	c.deadlines = append(c.deadlines, deadline)
	if c.mode == "deadline_error" {
		return io.ErrClosedPipe
	}
	return c.Conn.SetWriteDeadline(deadline)
}

func (c *rapidConn) Write(b []byte) (int, error) {
	c.writes++
	if c.entered != nil {
		close(c.entered)
	}
	switch c.mode {
	case "write_error":
		return 0, io.ErrClosedPipe
	case "partial_error":
		return 2, io.ErrUnexpectedEOF
	case "after_auth_error":
		if c.writes > 1 {
			return 0, io.ErrClosedPipe
		}
	case "short_nil":
		return len(b) - 1, nil
	}
	return c.Conn.Write(b)
}

func (c *rapidConn) Close() error {
	c.closed = true
	return c.Conn.Close()
}

func TestRapidTransportHealthyFrames(t *testing.T) {
	for _, mode := range rapidSendModes {
		t.Run(mode, func(t *testing.T) {
			local, peer := net.Pipe()
			defer local.Close()
			defer peer.Close()
			conn := &rapidConn{Conn: local}
			w := rapidClient(conn, mode)
			defer w.Close()
			cipher, _ := rc4.NewCipher([]byte("synthetic-key"))
			for _, payload := range [][]byte{{1, 2, 3}, {4, 5}} {
				want := make([]byte, 6)
				binary.BigEndian.PutUint16(want[:2], uint16(len(payload)+4))
				opcode := CmsgPing
				if mode == "auth" {
					opcode = CmsgAuthSession
				}
				binary.LittleEndian.PutUint32(want[2:], uint32(opcode))
				if mode == "encrypted" {
					cipher.XORKeyStream(want, want)
				}
				want = append(want, payload...)
				received := make(chan []byte, 1)
				go func() {
					frame := make([]byte, len(want))
					_, err := io.ReadFull(peer, frame)
					if err != nil {
						frame = nil
					}
					received <- frame
				}()
				before := time.Now()
				if err := rapidSend(w, mode, payload); err != nil {
					t.Fatal(err)
				}
				if got := <-received; !bytes.Equal(got, want) {
					t.Fatalf("frame=%x want=%x", got, want)
				}
				deadline := conn.deadlines[len(conn.deadlines)-1]
				if deadline.Before(before.Add(4*time.Second)) || deadline.After(time.Now().Add(4*time.Second)) {
					t.Fatalf("write deadline is not four seconds from this send: %v", deadline)
				}
				if w.IsStopped() || conn.closed {
					t.Fatal("healthy write stopped connection")
				}
			}
			if conn.writes != 2 || len(conn.deadlines) != 2 || !conn.deadlines[1].After(conn.deadlines[0]) {
				t.Fatalf("each packet needs one write and a fresh deadline: %+v", conn)
			}
		})
	}
}

func TestRapidTransportFailureInvalidates(t *testing.T) {
	for _, mode := range rapidSendModes {
		for _, failure := range []string{"deadline_error", "write_error", "partial_error", "short_nil"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				local, peer := net.Pipe()
				defer peer.Close()
				conn := &rapidConn{Conn: local, mode: failure}
				w := rapidClient(conn, mode)
				defer w.Close()
				readDone := make(chan struct{})
				go func() { w.readLoop(); close(readDone) }()
				logoutDone := make(chan error, 1)
				go func() { logoutDone <- w.WaitForLogout(time.Minute) }()
				err := rapidSend(w, mode, []byte{1, 2, 3})
				if err == nil || !strings.Contains(err.Error(), "write world packet") {
					t.Fatalf("failure not propagated: %v", err)
				}
				if failure == "short_nil" && !errors.Is(err, io.ErrShortWrite) {
					t.Fatalf("short write lost its error: %v", err)
				}
				select {
				case <-readDone:
				case <-time.After(time.Second):
					t.Fatal("terminal write left readLoop blocked")
				}
				select {
				case logoutErr := <-logoutDone:
					if logoutErr != err {
						t.Fatalf("logout waiter lost terminal write failure: %v", logoutErr)
					}
				case <-time.After(time.Second):
					t.Fatal("terminal write left logout waiter blocked")
				}
				if w.lastError != err {
					t.Fatalf("readLoop lost original write failure: %v", w.lastError)
				}
				rapidStopped(t, w)
				if !conn.closed {
					t.Fatal("failed socket was not closed")
				}
				writes := conn.writes
				if writes != 1 && !(failure == "deadline_error" && writes == 0) {
					t.Fatalf("unexpected write count: %d", writes)
				}
				for _, laterMode := range rapidSendModes {
					if err := rapidSend(w, laterMode, nil); err == nil {
						t.Fatal("later send reused failed stream")
					}
				}
				if conn.writes != writes || len(conn.deadlines) != 1 {
					t.Fatal("later send touched failed socket")
				}
			})
		}
	}
}

func TestRapidTransportBlockedWriteAndClose(t *testing.T) {
	for _, mode := range rapidSendModes {
		t.Run(mode, func(t *testing.T) {
			t.Parallel() // Real production four-second deadlines overlap; none are shortened.
			local, peer := net.Pipe()
			defer peer.Close()
			defer local.Close()
			conn := &rapidConn{Conn: local, entered: make(chan struct{})}
			w := rapidClient(conn, mode)
			sent := make(chan error, 1)
			start := time.Now()
			go func() { sent <- rapidSend(w, mode, nil) }()
			select {
			case <-conn.entered:
			case <-time.After(time.Second):
				t.Fatal("write did not start")
			}
			closed := make(chan struct{})
			go func() { w.Close(); close(closed) }()
			select {
			case err := <-sent:
				var netErr net.Error
				if !errors.As(err, &netErr) || !netErr.Timeout() {
					t.Fatalf("blocked write did not fail with deadline: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("write exceeded real four-second deadline plus scheduling allowance")
			}
			if elapsed := time.Since(start); elapsed < 3900*time.Millisecond || elapsed > 5*time.Second {
				t.Fatalf("unexpected deadline duration: %v", elapsed)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("Close goroutine did not finish after terminal write")
			}
			rapidStopped(t, w)
		})
	}
}

func rapidChallenge() []byte {
	frame := make([]byte, 44)
	binary.BigEndian.PutUint16(frame[:2], 42)
	binary.LittleEndian.PutUint16(frame[2:4], SmsgAuthChallenge)
	binary.LittleEndian.PutUint32(frame[4:8], 1)
	return frame
}

func TestRapidTransportRunPreservesWriteFailure(t *testing.T) {
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()
	conn := &rapidConn{Conn: local, mode: "after_auth_error"}
	w := rapidClient(conn, "auth")
	defer w.Close()
	// After the synthetic challenge, subsequent reads must use the owned socket.
	w.readBuf = bufio.NewReader(io.MultiReader(bytes.NewReader(rapidChallenge()), local))
	authFrame := make(chan []byte, 1)
	go func() {
		header := make([]byte, 6)
		if _, err := io.ReadFull(peer, header); err != nil {
			authFrame <- nil
			return
		}
		payload := make([]byte, int(binary.BigEndian.Uint16(header[:2]))-4)
		if _, err := io.ReadFull(peer, payload); err != nil {
			authFrame <- nil
			return
		}
		authFrame <- append(header, payload...)
	}()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run() }()
	select {
	case frame := <-authFrame:
		if len(frame) < 14 || binary.LittleEndian.Uint32(frame[2:6]) != uint32(CmsgAuthSession) ||
			binary.LittleEndian.Uint32(frame[6:10]) != 12340 || !bytes.Contains(frame, []byte("SYNTHETIC\x00")) {
			t.Fatalf("auth response did not preserve unencrypted native frame: %x", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("healthy auth frame was not received")
	}
	if err := w.WaitForSessionPhase(PhaseConnected, time.Second); err != nil {
		t.Fatal(err)
	}
	if !w.encrypted {
		t.Fatal("successful auth send did not enable encryption")
	}
	err := w.SendLogout()
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("post-auth send did not propagate failure: %v", err)
	}
	select {
	case runErr := <-runDone:
		if runErr != err {
			t.Fatalf("Run lost terminal write failure: %v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not finish after failed write closed its reader")
	}
	rapidStopped(t, w)
}

func TestRapidTransportAuthFailure(t *testing.T) {
	for _, failure := range []string{"deadline_error", "write_error", "partial_error", "short_nil"} {
		t.Run(failure, func(t *testing.T) {
			local, peer := net.Pipe()
			defer peer.Close()
			conn := &rapidConn{Conn: local, mode: failure}
			w := rapidClient(conn, "auth")
			w.readBuf = bufio.NewReader(bytes.NewReader(rapidChallenge()))
			err := w.Run()
			cause := io.ErrClosedPipe
			if failure == "partial_error" {
				cause = io.ErrUnexpectedEOF
			}
			if failure == "short_nil" {
				cause = io.ErrShortWrite
			}
			if !errors.Is(err, cause) || !strings.Contains(err.Error(), "auth challenge") {
				t.Fatalf("Run did not propagate auth send cause and stage: %v", err)
			}
			if w.encrypted || w.encryptClient != nil || w.SessionPhase() != PhaseNone {
				t.Fatal("failed auth write advanced encryption or session phase")
			}
			rapidStopped(t, w)
			if !conn.closed {
				t.Fatal("Run retained failed auth connection")
			}
		})
	}
}

func TestRapidTransportLoginWait(t *testing.T) {
	t.Run("already_stopped", func(t *testing.T) {
		w := rapidClient(nil, "plain")
		w.Close()
		close(w.loginDone) // A historical receipt cannot resurrect a dead session.
		start := time.Now()
		if err := w.WaitForLogin(time.Minute); err == nil || !strings.Contains(err.Error(), "stopped") {
			t.Fatalf("stopped login succeeded or lost cause: %v", err)
		}
		if time.Since(start) > 100*time.Millisecond {
			t.Fatal("already stopped login did not return promptly")
		}
	})
	t.Run("read_loop_disconnect", func(t *testing.T) {
		local, peer := net.Pipe()
		defer local.Close()
		defer peer.Close()
		conn := &rapidConn{Conn: local}
		w := rapidClient(conn, "plain")
		defer w.Close()
		readDone := make(chan struct{})
		go func() { w.readLoop(); close(readDone) }()
		waitDone := make(chan error, 1)
		go func() { waitDone <- w.WaitForLogin(time.Minute) }()
		peer.Close()
		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Fatal("readLoop did not finish on disconnect")
		}
		select {
		case err := <-waitDone:
			if !errors.Is(err, io.EOF) || errors.Unwrap(err) != w.lastError {
				t.Fatalf("disconnect did not retain the synchronized login cause: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("login waiter retained its sixty-second timeout")
		}
		rapidStopped(t, w)
		if !conn.closed {
			t.Fatal("readLoop EOF left the owned socket open")
		}
		for _, mode := range rapidSendModes {
			if err := rapidSend(w, mode, nil); err == nil {
				t.Fatal("later send reused EOF stream")
			}
		}
		if conn.writes != 0 || len(conn.deadlines) != 0 {
			t.Fatal("later send touched EOF socket")
		}
	})
	t.Run("stopped_transport_cause", func(t *testing.T) {
		local, peer := net.Pipe()
		defer peer.Close()
		w := rapidClient(&rapidConn{Conn: local, mode: "write_error"}, "plain")
		defer w.Close()
		cause := w.SendLogout()
		err := w.WaitForLogin(time.Minute)
		if !errors.Is(err, io.ErrClosedPipe) || errors.Unwrap(err) != cause {
			t.Fatalf("stopped login lost original transport cause: %v", err)
		}
	})
	for _, name := range []string{"receipt_and_stop_interleaving", "receipt_and_terminal_state"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := NewWorldClient("SYNTHETIC", nil, nil)
				done := make(chan error, 1)
				go func() { done <- w.WaitForLogin(time.Minute) }()
				synctest.Wait() // Waiter has passed its initial check and blocked in select.
				w.sendMu.Lock()
				close(w.loginDone) // Select the receipt branch before publishing stop.
				cause := errors.New("original terminal transport failure")
				w.lastError = cause
				w.stopped = true
				if name == "receipt_and_stop_interleaving" {
					w.signalStop()
				}
				w.sendMu.Unlock()
				err := <-done
				if !errors.Is(err, cause) || errors.Unwrap(err) != cause {
					t.Fatalf("receipt branch accepted terminal stop or lost cause: %v", err)
				}
			})
		})

	}
	t.Run("native_receipt", func(t *testing.T) {
		local, peer := net.Pipe()
		defer peer.Close()
		w := rapidClient(local, "plain")
		defer w.Close()
		payload := make([]byte, 20) // native map plus position/orientation
		w.handlePacket(SmsgLoginVerifyWorld, payload)
		if err := w.WaitForLogin(time.Second); err != nil || !w.IsInWorld() {
			t.Fatalf("native login receipt did not succeed: %v", err)
		}
	})
	t.Run("live_without_receipt_timeout", func(t *testing.T) {
		local, peer := net.Pipe()
		defer peer.Close()
		w := rapidClient(local, "plain")
		defer w.Close()
		start := time.Now()
		err := w.WaitForLogin(30 * time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "timeout") || time.Since(start) < 25*time.Millisecond {
			t.Fatalf("healthy no-receipt timeout changed: %v after %v", err, time.Since(start))
		}
	})
}

func TestRapidTransportBufferedAuthRejectPreservesCause(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	w := rapidClient(&rapidConn{Conn: local, mode: "write_error"}, "plain")
	defer w.Close()
	// A complete auth rejection was buffered before the transport write failed.
	frame := []byte{0, 3, 0, 0, 13}
	binary.LittleEndian.PutUint16(frame[2:4], SmsgAuthResponse)
	w.readBuf = bufio.NewReader(bytes.NewReader(frame))
	cause := w.SendLogout()
	w.readLoop()
	if w.lastError != cause || !errors.Is(w.lastError, io.ErrClosedPipe) {
		t.Fatalf("buffered auth rejection replaced first terminal cause: %v", w.lastError)
	}
	rapidStopped(t, w)
}

func TestRapidTransportLogoutWait(t *testing.T) {
	t.Run("close_without_receipt", func(t *testing.T) {
		local, peer := net.Pipe()
		defer peer.Close()
		w := rapidClient(local, "plain")
		w.Close()
		if err := w.WaitForLogout(time.Minute); err == nil || !strings.Contains(err.Error(), "stopped before logout complete") {
			t.Fatalf("Close without native receipt succeeded: %v", err)
		}
	})
	t.Run("transport_cause", func(t *testing.T) {
		local, peer := net.Pipe()
		defer peer.Close()
		w := rapidClient(&rapidConn{Conn: local, mode: "partial_error"}, "encrypted")
		defer w.Close()
		cause := w.SendLogout()
		if err := w.WaitForLogout(time.Minute); err != cause || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("logout lost transport cause: %v", err)
		}
	})
	t.Run("acknowledged_and_stopped", func(t *testing.T) {
		local, peer := net.Pipe()
		defer peer.Close()
		w := rapidClient(local, "plain")
		w.handlePacket(SmsgLogoutComplete, nil)
		w.Close()
		for range 100 {
			if err := w.WaitForLogout(time.Second); err != nil {
				t.Fatalf("native receipt lost to simultaneously ready stop: %v", err)
			}
		}
	})
	t.Run("live_without_receipt_timeout", func(t *testing.T) {
		local, peer := net.Pipe()
		defer peer.Close()
		w := rapidClient(local, "plain")
		defer w.Close()
		start := time.Now()
		if err := w.WaitForLogout(30 * time.Millisecond); err == nil || !strings.Contains(err.Error(), "timeout") || time.Since(start) < 25*time.Millisecond {
			t.Fatalf("healthy logout no-receipt timeout changed: %v", err)
		}
	})
}

func TestRapidTransportStopError(t *testing.T) {
	for _, mode := range []string{"unconnected", "close", "stopped_bit", "closed_channel", "retained_cause"} {
		t.Run(mode, func(t *testing.T) {
			w := NewWorldClient("SYNTHETIC", nil, nil)
			cause := errors.New("first terminal cause")
			switch mode {
			case "close":
				w.Close()
			case "stopped_bit":
				w.sendMu.Lock()
				w.closeLocked()
				w.sendMu.Unlock()
			case "closed_channel":
				w.signalStop()
			case "retained_cause":
				w.sendMu.Lock()
				w.lastError = cause
				w.closeLocked()
				w.sendMu.Unlock()
			}
			err := w.StopError()
			if (err == nil) != (mode == "unconnected") {
				t.Fatalf("terminal snapshot=%v", err)
			}
			if mode == "retained_cause" && !errors.Is(err, cause) {
				t.Fatalf("lost original cause: %v", err)
			}
			w.Close()
		})
	}
}

func TestRapidTransportPhaseWait(t *testing.T) {
	for _, mode := range []string{"stale_target", "target_stopped_bit", "queued_target_stop", "queued_target_stopped_bit", "stop_during_wait", "timeout_with_stopped_bit", "healthy_target", "healthy_queued_target", "healthy_timeout", "auth_queue"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := NewWorldClient("SYNTHETIC", nil, nil)
				defer w.Close()
				cause := errors.New("phase transport cause")
				terminal := func(signal bool) {
					w.sendMu.Lock()
					w.lastError = cause
					w.closeLocked()
					w.sendMu.Unlock()
					if signal {
						w.signalStop()
					}
				}
				if mode == "stale_target" || mode == "target_stopped_bit" || mode == "healthy_target" {
					w.setPhase(PhaseAuthed, "synthetic")
					if mode != "healthy_target" {
						terminal(mode == "stale_target")
					}
				}
				start := time.Now()
				done := make(chan error, 1)
				go func() { done <- w.WaitForSessionPhase(PhaseAuthed, 20*time.Second) }()
				synctest.Wait()
				switch mode {
				case "queued_target_stop", "queued_target_stopped_bit":
					// Block receipt arbitration until both receipt and terminal bit are committed.
					w.sendMu.Lock()
					w.phaseMu.Lock()
					for _, ch := range w.phaseWaiters {
						ch <- PhaseAuthed
					}
					w.phaseMu.Unlock()
					w.lastError = cause
					w.closeLocked()
					if mode == "queued_target_stop" {
						w.signalStop()
					}
					w.sendMu.Unlock()
				case "stop_during_wait":
					time.Sleep(time.Second)
					terminal(true)
				case "timeout_with_stopped_bit":
					terminal(false)
				case "healthy_queued_target":
					w.setPhase(PhaseAuthed, "synthetic")
				case "auth_queue":
					w.handleAuthResponse([]byte{27})
					synctest.Wait()
					if w.StopError() != nil || w.SessionPhase() == PhaseAuthed {
						t.Fatal("queue stopped or authenticated prematurely")
					}
					select {
					case err := <-done:
						t.Fatalf("queue completed prematurely: %v", err)
					default:
					}
					w.handleAuthResponse([]byte{12})
				}
				err := <-done
				wantElapsed := time.Duration(0)
				if mode == "stop_during_wait" {
					wantElapsed = time.Second
				}
				if mode == "healthy_timeout" || mode == "timeout_with_stopped_bit" {
					wantElapsed = 20 * time.Second
				}
				if elapsed := time.Since(start); elapsed != wantElapsed {
					t.Fatalf("phase elapsed=%v want=%v", elapsed, wantElapsed)
				}
				switch mode {
				case "healthy_target", "healthy_queued_target", "auth_queue":
					if err != nil {
						t.Fatalf("healthy phase failed: %v", err)
					}
				case "healthy_timeout":
					if err == nil || !strings.Contains(err.Error(), "timeout") {
						t.Fatalf("missing timeout: %v", err)
					}
				default:
					if !errors.Is(err, cause) {
						t.Fatalf("terminal phase succeeded or lost cause: %v", err)
					}
				}
			})
		})
	}
}

func TestRapidTransportRunChallengeCause(t *testing.T) {
	for _, mode := range []string{"header_read", "payload_read", "wrong_opcode", "earlier_write"} {
		t.Run(mode, func(t *testing.T) {
			local, peer := net.Pipe()
			defer peer.Close()
			conn := &rapidConn{Conn: local, mode: "write_error"}
			w := rapidClient(conn, "plain")
			defer w.Close()
			var input []byte
			switch mode {
			case "payload_read":
				input = rapidChallenge()[:5]
			case "wrong_opcode", "earlier_write":
				input = []byte{0, 2, 0, 0}
			}
			w.readBuf = bufio.NewReader(bytes.NewReader(input))
			var first error
			if mode == "earlier_write" {
				first = w.SendLogout()
			}
			runErr := w.Run()
			if runErr == nil {
				t.Fatal("challenge failure succeeded")
			}
			cause := errors.Unwrap(runErr)
			if first != nil {
				cause = first
				if !errors.Is(runErr, first) || !strings.Contains(runErr.Error(), "auth challenge") {
					t.Fatalf("Run replaced first cause or lost auth context: %v", runErr)
				}
			}
			if !errors.Is(w.StopError(), cause) || !errors.Is(w.WaitForSessionPhase(PhaseAuthed, time.Minute), cause) || !errors.Is(w.WaitForLogin(time.Minute), cause) {
				t.Fatalf("challenge cause lost: run=%v stop=%v want=%v", runErr, w.StopError(), cause)
			}
			if !conn.closed {
				t.Fatal("challenge failure left socket open")
			}
			w.Close()
			if !errors.Is(w.StopError(), cause) {
				t.Fatal("teardown replaced first cause")
			}
		})
	}
}

func TestRapidTransportFirstCausePublication(t *testing.T) {
	for _, mode := range []string{"write", "auth_reject"} {
		t.Run(mode, func(t *testing.T) {
			local, peer := net.Pipe()
			defer peer.Close()
			w := rapidClient(&rapidConn{Conn: local, mode: "write_error"}, "plain")
			defer w.Close()
			first := errors.New("initial challenge cause before stop publication")
			w.sendMu.Lock()
			w.lastError = first
			w.sendMu.Unlock()
			if mode == "write" {
				if err := w.SendLogout(); !errors.Is(err, first) {
					t.Fatalf("write replaced first cause: %v", err)
				}
			} else {
				w.handleAuthResponse([]byte{13})
			}
			if !errors.Is(w.StopError(), first) {
				t.Fatalf("publication replaced cause: %v", w.StopError())
			}
			rapidStopped(t, w)
		})
	}
}

func TestRapidTransportLoginTimerArbitration(t *testing.T) {
	for _, mode := range []string{"healthy", "stopped_bit"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := NewWorldClient("SYNTHETIC", nil, nil)
				defer w.Close()
				cause := errors.New("terminal before login deadline")
				start := time.Now()
				done := make(chan error, 1)
				go func() { done <- w.WaitForLogin(60 * time.Second) }()
				synctest.Wait()
				if mode == "stopped_bit" {
					w.sendMu.Lock()
					w.lastError = cause
					w.closeLocked() // Hold the signal gap until timer arbitration.
					w.sendMu.Unlock()
				}
				err := <-done
				if time.Since(start) != 60*time.Second {
					t.Fatalf("login deadline changed: %v", time.Since(start))
				}
				if mode == "stopped_bit" {
					if !errors.Is(err, cause) {
						t.Fatalf("login timer lost known stop: %v", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "timeout waiting for login verify world") {
					t.Fatalf("healthy silence did not time out: %v", err)
				}
			})
		})
	}
}

func TestRapidTransportMalformedChallenge(t *testing.T) {
	for _, c := range []struct {
		name    string
		size    uint16
		payload int
	}{
		{"zero_size", 0, 0}, {"underflow", 1, 0}, {"zero_payload", 2, 0},
		{"short_payload", 9, 7}, {"undersized", 41, 39}, {"oversized", 43, 41},
	} {
		t.Run(c.name, func(t *testing.T) {
			local, peer := net.Pipe()
			defer peer.Close()
			conn := &rapidConn{Conn: local, mode: "write_error"}
			w := rapidClient(conn, "plain")
			defer w.Close()
			frame := make([]byte, 4+c.payload)
			binary.BigEndian.PutUint16(frame[:2], c.size)
			binary.LittleEndian.PutUint16(frame[2:4], SmsgAuthChallenge)
			w.readBuf = bufio.NewReader(bytes.NewReader(frame))
			err := w.Run()
			if err == nil || !strings.Contains(err.Error(), "invalid auth challenge size") {
				t.Fatalf("malformed challenge accepted or wrong cause: %v", err)
			}
			if conn.writes != 0 || len(conn.deadlines) != 0 || w.encrypted || w.encryptClient != nil || w.SessionPhase() != PhaseNone {
				t.Fatal("malformed challenge sent a response or enabled auth")
			}
			if !errors.Is(err, w.StopError()) || !errors.Is(w.WaitForSessionPhase(PhaseAuthed, time.Minute), w.StopError()) {
				t.Fatalf("malformed cause lost: run=%v stop=%v", err, w.StopError())
			}
			rapidStopped(t, w)
			if !conn.closed {
				t.Fatal("malformed challenge retained socket")
			}
		})
	}
}

func TestRapidTransportAtomicTerminalPublication(t *testing.T) {
	for _, mode := range []string{"run", "auth_reject"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := NewWorldClient("SYNTHETIC", nil, nil)
				w.readBuf = bufio.NewReader(bytes.NewReader(nil))
				defer w.Close()
				// The old fatal path stored its cause then blocked in Close's pre-stop flush.
				w.spellsMu.Lock()
				done := make(chan error, 1)
				go func() {
					if mode == "run" {
						done <- w.Run()
					} else {
						w.handleAuthResponse([]byte{13})
						done <- nil
					}
				}()
				synctest.Wait()
				cause := w.StopError()
				if cause == nil || !w.IsStopped() {
					t.Error("fatal cause was published without invalidating the stream")
				}
				w.spellsMu.Unlock()
				err := <-done
				if mode == "run" && (!errors.Is(err, io.EOF) || !errors.Is(err, cause)) {
					t.Fatalf("Run lost published cause: %v", err)
				}
				rapidStopped(t, w)
				if !errors.Is(w.StopError(), cause) {
					t.Fatal("fatal publication cause changed")
				}
			})
		})
	}
}
