package dsh

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCallBlockedWriteHonorsDeadline(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inR.Close()
	defer inW.Close()
	defer outR.Close()
	defer outW.Close()
	c := NewClient(inW, outR, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Call(ctx, MethodShutdown, ShutdownParams{}, nil) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown deadline could not interrupt pipe write")
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("partial stream remained live")
	}
	select {
	case c.writeGate <- struct{}{}:
		<-c.writeGate
	case <-time.After(time.Second):
		t.Fatal("blocked writer survived cancellation")
	}
}

func TestCallQueuedWriteCanCancel(t *testing.T) {
	outR, outW := io.Pipe()
	defer outR.Close()
	defer outW.Close()
	c := NewClient(io.Discard, outR, 0)
	c.writeGate <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Call(ctx, MethodShutdown, ShutdownParams{}, nil) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued call: %v", err)
		}
	case <-time.After(time.Second):
		<-c.writeGate
		t.Fatal("queued write ignored deadline")
	}
	<-c.writeGate
	select {
	case <-c.Done():
		t.Fatal("cancelled queued write poisoned the transport")
	default:
	}
	if err := c.Notify("test", nil); err != nil {
		t.Fatalf("reuse: %v", err)
	}
}

const statusNotif = `{"jsonrpc":"2.0","method":"session.status","params":{"sessionId":"s","status":"running"}}`
const eventNotif = `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"s","event":{"type":"turn/start","seq":1,"data":{"turn":1}}}}`

func writeLine(t *testing.T, w io.Writer, line string) {
	t.Helper()
	if _, err := io.WriteString(w, line+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestClientReplaysBufferedNotificationsInOrder covers the pre-handler buffer:
// notifications arriving before SetHandler must not be dropped, and after the
// handler is installed they must replay immediately in wire order; no newer
// stdout frame is required to wake the replay.
func TestClientReplaysBufferedNotificationsInOrder(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	c := NewClient(io.Discard, stdoutR, 0)

	writeLine(t, stdoutW, statusNotif) // F1, buffered (no handler)
	writeLine(t, stdoutW, eventNotif)  // F2, buffered (no handler)

	var mu sync.Mutex
	var methods []string
	c.SetHandler(func(f Frame) {
		mu.Lock()
		methods = append(methods, f.Method)
		mu.Unlock()
	})

	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(methods) == 2
	})
	mu.Lock()
	defer mu.Unlock()
	want := []string{"session.status", "session.event"}
	for i := range want {
		if methods[i] != want[i] {
			t.Fatalf("replay order = %v, want %v", methods, want)
		}
	}
}

// TestClientBufferOverflowIsTransportError covers the hard pre-handler buffer
// cap: overflowing it is an explicit transport error and stops the reader,
// rather than growing memory without bound.
func TestClientBufferOverflowIsTransportError(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	c := NewClient(io.Discard, stdoutR, 0)
	errCh := make(chan error, 1)
	c.SetTransportErrorHandler(func(err error) { errCh <- err })

	for i := 0; i <= maxBufferedNotifications; i++ {
		if _, err := io.WriteString(stdoutW, statusNotif+"\n"); err != nil {
			break // reader stopped after the overflow
		}
	}

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "buffer") {
			t.Fatalf("overflow error = %v, want buffer error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no transport error after overflow")
	}
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatalf("reader did not stop after overflow")
	}
}
