package builtin

import (
	"context"
	"time"
)

// FileOverlay lets a host expose unsaved editor text to file tools without
// widening path resolution or confinement.
type FileOverlay interface {
	ReadTextFile(ctx context.Context, path string) (content string, ok bool)
	WriteTextFile(ctx context.Context, path, content string) (ok bool, err error)
}

// TerminalRunner runs foreground commands in a host-owned terminal. ok=false
// asks bash to use its normal local implementation.
type TerminalRunner interface {
	RunCommand(ctx context.Context, command, cwd string, timeout time.Duration) (output string, ok bool, err error)
}
