package dsh

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

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
