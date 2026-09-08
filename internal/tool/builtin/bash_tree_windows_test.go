//go:build windows

package builtin

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"workground2/internal/proc"
)

// The launcher exits while its child holds stdout, as with a Node/CDP shim.
// Preservation must only take effect after a normal completion, not on Stop.
func TestPreservedShellCancelReapsOrphan(t *testing.T) {
	sentinel := treeHelper("sleep", "")
	if err := sentinel.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sentinel.Process.Kill(); _ = sentinel.Wait() })

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := treeHelper("launch", pidFile)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = bashWaitDelay
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// CommandContext's watcher is required for the production Cancel path.
	cmd = helperContext(ctx, cmd)
	done := make(chan error, 1)
	go func() {
		p, err := runShellProcess(ctx, cmd, false)
		if ctx.Err() != nil {
			p.kill()
		}
		done <- err
	}()
	childPID := waitForWindowsPIDFile(t, pidFile)
	child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(childPID))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.TerminateProcess(child, 1); _ = windows.CloseHandle(child) })
	// The leader has exited, but Wait is still held by the inherited pipe.
	leader, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil && !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		t.Fatal(err)
	}
	if err == nil {
		status, waitErr := windows.WaitForSingleObject(leader, 2000)
		_ = windows.CloseHandle(leader)
		if waitErr != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatalf("launcher did not exit: %d %v", status, waitErr)
		}
	}
	cancel()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("cancel remained blocked after launcher exit")
	}
	status, err := windows.WaitForSingleObject(child, 1000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("owned child survived: %d %v", status, err)
	}
	sentinelHandle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(sentinel.Process.Pid))
	if err != nil {
		t.Fatal("unrelated process was killed", err)
	}
	defer windows.CloseHandle(sentinelHandle)
	status, _ = windows.WaitForSingleObject(sentinelHandle, 0)
	if status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("unrelated process exited")
	}
}

func TestPreservedShellNormalExitKeepsChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := helperContext(context.Background(), treeHelper("launch", pidFile))
	// Direct file handles let Wait finish once the launcher exits.
	sink, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	cmd.Stdout, cmd.Stderr = sink, sink
	p, err := runShellProcess(context.Background(), cmd, false)
	if err != nil {
		t.Fatal(err)
	}
	p.kill() // repeated post-completion cleanup must not reclaim a detached job
	childPID := waitForWindowsPIDFile(t, pidFile)
	child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(childPID))
	if err != nil {
		t.Fatal("preserved child missing", err)
	}
	defer windows.CloseHandle(child)
	defer windows.TerminateProcess(child, 1)
	status, _ := windows.WaitForSingleObject(child, 0)
	if status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("preserved child exited")
	}
}

func helperContext(ctx context.Context, source *exec.Cmd) *exec.Cmd {
	cmd := exec.CommandContext(ctx, source.Path, source.Args[1:]...)
	cmd.Env, cmd.Stdout, cmd.Stderr, cmd.WaitDelay = source.Env, source.Stdout, source.Stderr, source.WaitDelay
	return cmd
}

func treeHelper(mode, path string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestShellTreeHelper$")
	cmd.Env = append(os.Environ(), "WG2_TREE_MODE="+mode, "WG2_TREE_PID="+path)
	proc.HideWindow(cmd)
	return cmd
}

func TestShellTreeHelper(t *testing.T) {
	switch os.Getenv("WG2_TREE_MODE") {
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "launch":
		cmd := treeHelper("sleep", "")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("WG2_TREE_PID"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
}
