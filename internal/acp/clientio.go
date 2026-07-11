package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type requester interface {
	Request(ctx context.Context, method string, params any) (json.RawMessage, error)
}

type clientIO struct {
	conn      requester
	sessionID string
	caps      ClientCapabilities
}

func newClientIO(conn requester, sessionID string, caps ClientCapabilities) *clientIO {
	return &clientIO{conn: conn, sessionID: sessionID, caps: caps}
}

func (c *clientIO) hasAny() bool {
	return c.caps.FS.ReadTextFile || c.caps.FS.WriteTextFile || c.caps.Terminal
}

func (c *clientIO) ReadTextFile(ctx context.Context, path string) (string, bool) {
	if !c.caps.FS.ReadTextFile {
		return "", false
	}
	raw, err := c.conn.Request(ctx, "fs/read_text_file", FSReadTextFileParams{SessionID: c.sessionID, Path: path})
	if err != nil {
		return "", false
	}
	var result FSReadTextFileResult
	if json.Unmarshal(raw, &result) != nil {
		return "", false
	}
	return result.Content, true
}

func (c *clientIO) WriteTextFile(ctx context.Context, path, content string) (bool, error) {
	if !c.caps.FS.WriteTextFile {
		return false, nil
	}
	_, err := c.conn.Request(ctx, "fs/write_text_file", FSWriteTextFileParams{SessionID: c.sessionID, Path: path, Content: content})
	return true, err
}

const terminalOutputByteLimit = 1 << 20

func (c *clientIO) RunCommand(ctx context.Context, command, cwd string, timeout time.Duration) (string, bool, error) {
	if !c.caps.Terminal {
		return "", false, nil
	}
	raw, err := c.conn.Request(ctx, "terminal/create", TerminalCreateParams{SessionID: c.sessionID, Command: command, Cwd: cwd, OutputByteLimit: terminalOutputByteLimit})
	if err != nil {
		return "", false, nil
	}
	var created TerminalCreateResult
	if json.Unmarshal(raw, &created) != nil || strings.TrimSpace(created.TerminalID) == "" {
		return "", false, nil
	}
	id := TerminalIDParams{SessionID: c.sessionID, TerminalID: created.TerminalID}
	defer func() { _, _ = c.conn.Request(context.WithoutCancel(ctx), "terminal/release", id) }()

	waitCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	_, waitErr := c.conn.Request(waitCtx, "terminal/wait_for_exit", id)
	timedOut := waitErr != nil && waitCtx.Err() != nil && ctx.Err() == nil
	if timedOut || ctx.Err() != nil {
		_, _ = c.conn.Request(context.WithoutCancel(ctx), "terminal/kill", id)
	}
	output, exit := c.terminalOutput(context.WithoutCancel(ctx), id)
	switch {
	case ctx.Err() != nil:
		return output, true, ctx.Err()
	case timedOut:
		return output, true, fmt.Errorf("command timed out after %s (terminal killed)", timeout)
	case waitErr != nil:
		return output, true, waitErr
	case exit != nil && exit.ExitCode != nil && *exit.ExitCode != 0:
		return output, true, fmt.Errorf("exit status %d", *exit.ExitCode)
	case exit != nil && exit.Signal != nil && *exit.Signal != "":
		return output, true, fmt.Errorf("terminated by signal %s", *exit.Signal)
	}
	return output, true, nil
}

func (c *clientIO) terminalOutput(ctx context.Context, id TerminalIDParams) (string, *TerminalExitStatus) {
	raw, err := c.conn.Request(ctx, "terminal/output", id)
	if err != nil {
		return "", nil
	}
	var result TerminalOutputResult
	if json.Unmarshal(raw, &result) != nil {
		return "", nil
	}
	if result.Truncated {
		result.Output += "\n…(output truncated by the client terminal)"
	}
	return result.Output, result.ExitStatus
}
