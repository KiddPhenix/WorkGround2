package plugin

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"workground2/internal/proc"
)

type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }

// TestStdioCallReturnsOnContextCancel pins that a stdio call unblocks when its
// context is cancelled even though the server never replies. The stdio child is
// bound to the session, not the turn, so without this a hung server would hang a
// cancelled turn forever. No reader goroutine runs here, so the reply never
// arrives — only ctx cancellation can return the call.
func TestStdioCallReturnsOnContextCancel(t *testing.T) {
	tr := &stdioTransport{
		name:    "hung",
		stdin:   discardWriteCloser{},
		pending: map[int]chan rpcResponse{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := tr.call(ctx, "tools/call", map[string]any{})
		done <- err
	}()

	time.Sleep(100 * time.Millisecond) // let the call park in its select
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled call returned nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stdio call did not return within 2s of ctx cancel — a hung server hangs the turn")
	}
}

func TestStdioQueuedCallCanCancelAndRetry(t *testing.T) {
	tr := &stdioTransport{name: "queued", stdin: discardWriteCloser{}, pending: map[int]chan rpcResponse{}}
	tr.initGates()
	tr.callGate <- struct{}{} // Another round trip owns the transport.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := tr.call(ctx, "tools/call", nil); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call: %v", err)
		}
	case <-time.After(time.Second):
		<-tr.callGate
		t.Fatal("queued call ignored cancellation")
	}
	<-tr.callGate
	tr.mu.Lock()
	if tr.nextID != 0 {
		t.Error("cancelled queued call was sent")
	}
	tr.mu.Unlock()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _, err := tr.call(ctx2, "tools/call", nil); done <- err }()
	deadline := time.After(time.Second)
	for {
		tr.mu.Lock()
		ch := tr.pending[1]
		if ch != nil {
			delete(tr.pending, 1)
		}
		tr.mu.Unlock()
		if ch != nil {
			ch <- rpcResponse{Result: []byte(`{}`)}
			break
		}
		select {
		case <-deadline:
			t.Fatal("retry never started")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry did not finish")
	}
}

func TestStdioBlockedPipeWriteCancelsAndCloses(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	tr := &stdioTransport{name: "not-reading", stdin: writer, pending: map[int]chan rpcResponse{}}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := tr.call(ctx, "tools/call", make([]byte, 8<<20)); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("write: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("full pipe blocked cancellation")
	}
	tr.close()
	_, err = tr.call(context.Background(), "tools/call", nil)
	if err == nil {
		t.Fatal("partially written transport remained usable")
	}
	tr.initGates()
	select {
	case tr.writeGate <- struct{}{}:
		<-tr.writeGate
	case <-time.After(time.Second):
		t.Fatal("writer goroutine survived close")
	}
}

func TestStdioDiagnosticsDoNotWaitForLiveProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestStdioLiveHelper$")
	cmd.Env = append(os.Environ(), "WG2_STDIO_LIVE_HELPER=1")
	job, err := proc.StartTracked(cmd)
	if err != nil {
		t.Fatal(err)
	}
	tr := &stdioTransport{name: "live", cmd: cmd, job: job, stderr: &tailBuffer{limit: 1024}, pending: map[int]chan rpcResponse{}}
	t.Cleanup(tr.close)
	done := make(chan error, 1)
	go func() { done <- tr.withStderr(io.ErrClosedPipe) }()
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("diagnostic: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("error reporting waited for a live process")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); tr.close() }()
	}
	wg.Wait()
}

func TestStdioLiveHelper(t *testing.T) {
	if os.Getenv("WG2_STDIO_LIVE_HELPER") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestStdioCallRespectsExistingDeadline(t *testing.T) {
	tr := &stdioTransport{
		name:    "server",
		stdin:   discardWriteCloser{},
		pending: map[int]chan rpcResponse{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := tr.call(ctx, "tools/call", map[string]any{})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("timed-out call returned nil error")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("stdio call did not return within caller deadline")
	}
}

func TestStdioCallCancelReturnsContextCanceled(t *testing.T) {
	tr := &stdioTransport{
		name:    "slow-server",
		stdin:   discardWriteCloser{},
		pending: map[int]chan rpcResponse{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := tr.call(ctx, "tools/call", map[string]any{})
		done <- err
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled call returned nil error")
		}
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stdio call did not return within 2s of cancel")
	}
}
