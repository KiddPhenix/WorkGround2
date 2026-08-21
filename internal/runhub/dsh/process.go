package dsh

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"workground2/internal/proc"
)

// DefaultStderrTail bounds how much stderr the runner keeps for diagnostics.
// It is a tail, not a transcript: a runaway runtime cannot force unbounded
// memory, and the prompt, reasoning, tool arguments and tool results never
// belong here.
const DefaultStderrTail = 4096

// Proc is the process handle a runner drives. It is an interface so tests can
// substitute an in-memory JSON-RPC peer; the production launcher backs it with
// a real DSH runtime child.
type Proc interface {
	// Stdin returns the write side of the child's stdin. JSON-RPC requests go
	// here; closing it signals EOF during quiesce.
	Stdin() io.WriteCloser
	// Stdout returns the read side of the child's stdout: newline-delimited JSON.
	Stdout() io.Reader
	// StderrTail returns a bounded, sanitized tail of the child's stderr.
	StderrTail(maxBytes int) string
	// Wait returns a channel that delivers the exit error exactly once, when the
	// child has exited. It is single-reader: callers must not consume the value
	// from multiple goroutines.
	Wait() <-chan error
	// Exited returns a channel that closes once the child has exited. Unlike
	// Wait it carries no value and is safe for multiple readers.
	Exited() <-chan struct{}
	// CloseStdin closes the child's stdin, the first force-quiesce step.
	CloseStdin() error
	// Kill force-terminates the whole process tree, the last resort step.
	Kill()
	// Cleanup releases any process-level resources (such as a Windows Job Object
	// handle) after the child has exited. It is idempotent and safe to call on
	// both the natural-exit and the kill path.
	Cleanup()
	// Ref returns a short diagnostic process reference (e.g. a pid) for the
	// persisted binding. It never carries the prompt or environment.
	Ref() string
}

// ProcessSpec is the explicit launch instruction for one DSH runtime child.
// Every field is caller-supplied; no install path is assumed.
type ProcessSpec struct {
	NodePath   string
	EntryPath  string
	ConfigPath string
	Dir        string   // working directory for the child
	Env        []string // complete child environment
}

// Launcher starts one DSH runtime child from spec. It is injectable so tests
// can wire a fake peer; LaunchProcess is the production implementation.
type Launcher func(spec ProcessSpec) (Proc, error)

// LaunchProcess starts node <entry> <config> as an argv array (never a shell
// string) inside a killable process group/job object, and drains its stderr
// into a bounded tail. It returns once the child is running; the caller owns
// the returned Proc and must Kill it on every failure path.
func LaunchProcess(spec ProcessSpec) (Proc, error) {
	cmd := exec.Command(spec.NodePath, spec.EntryPath, spec.ConfigPath)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("dsh: open stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("dsh: open stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("dsh: open stderr pipe: %w", err)
	}

	job, err := proc.StartTracked(cmd)
	if err != nil {
		return nil, fmt.Errorf("dsh: start runtime: %w", err)
	}

	p := &trackedProc{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		job:      job,
		tail:     newStderrTail(DefaultStderrTail),
		waitCh:   make(chan error, 1),
		exitedCh: make(chan struct{}),
	}
	go p.drainStderr(stderr)
	go func() {
		err := cmd.Wait()
		close(p.exitedCh)
		p.waitCh <- err
		close(p.waitCh)
		p.Cleanup() // release the Job Object handle on the natural-exit path
	}()
	return p, nil
}

type trackedProc struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.Reader
	tail     *stderrTail
	waitCh   chan error
	exitedCh chan struct{}

	jobMu sync.Mutex
	job   uintptr
}

func (p *trackedProc) Stdin() io.WriteCloser     { return p.stdin }
func (p *trackedProc) Stdout() io.Reader         { return p.stdout }
func (p *trackedProc) Wait() <-chan error        { return p.waitCh }
func (p *trackedProc) Exited() <-chan struct{}   { return p.exitedCh }
func (p *trackedProc) Ref() string               { return fmt.Sprintf("pid:%d", p.cmd.Process.Pid) }
func (p *trackedProc) StderrTail(max int) string { return p.tail.Tail(max) }
func (p *trackedProc) CloseStdin() error         { return p.stdin.Close() }

// Kill terminates the process tree and releases the Job Object handle exactly
// once, so a later Cleanup cannot double-close it.
func (p *trackedProc) Kill() {
	p.jobMu.Lock()
	job := p.job
	p.job = 0
	p.jobMu.Unlock()
	proc.KillTracked(p.cmd, job)
}

// Cleanup releases the Job Object handle after a natural exit. It is idempotent
// with Kill: whichever runs first zeroes the handle so the other is a no-op.
func (p *trackedProc) Cleanup() {
	p.jobMu.Lock()
	job := p.job
	p.job = 0
	p.jobMu.Unlock()
	proc.ReleaseTracked(job)
}

func (p *trackedProc) drainStderr(r io.Reader) {
	_, _ = io.Copy(p.tail, r)
}

// stderrTail keeps only the trailing max bytes, so a chatty runtime cannot grow
// memory without bound and the persisted diagnostic stays small.
type stderrTail struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newStderrTail(max int) *stderrTail {
	if max <= 0 {
		max = DefaultStderrTail
	}
	return &stderrTail{max: max}
}

func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *stderrTail) Tail(maxBytes int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.buf
	if maxBytes > 0 && len(b) > maxBytes {
		b = b[len(b)-maxBytes:]
	}
	return sanitizeDiagnostic(string(b))
}

// sanitizeDiagnostic flattens a diagnostic blob to a single line of printable
// runes. It is a transport-hygiene guard, not a credential policy: full secret
// redaction against a known secret list belongs to the reporter boundary.
func sanitizeDiagnostic(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}
