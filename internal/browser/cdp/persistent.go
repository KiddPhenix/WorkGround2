package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"workground2/internal/browser"
	"workground2/internal/proc"
)

const persistentLaunchTimeout = 30 * time.Second

// launchPersistent starts Chromium as an OS-detached process, waits for its
// loopback CDP endpoint, then controls it through a remote allocator. This
// cleanly separates process ownership from client ownership: Close can tear
// down every Go/CDP resource without terminating Chromium.
func launchPersistent(ctx context.Context, dopts browser.DriverOptions, execPath string, kind browser.BrowserKind) (*driver, error) {
	port, err := pickDebugPort()
	if err != nil {
		return nil, fmt.Errorf("pick debug port: %w", err)
	}
	endpoint := "http://" + debugEndpointFor(port)
	cmd := exec.Command(execPath, persistentArgs(dopts, port)...)
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start persistent browser: %w", err)
	}

	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()

	launchCtx, cancel := context.WithTimeout(ctx, persistentLaunchTimeout)
	defer cancel()
	var info browser.EndpointInfo
	for {
		info, err = browser.ValidateEndpoint(launchCtx, nil, endpoint)
		if err == nil {
			break
		}
		select {
		case <-done:
			if waitErr == nil {
				return nil, fmt.Errorf("persistent browser exited before CDP was ready")
			}
			return nil, fmt.Errorf("persistent browser exited before CDP was ready: %w", waitErr)
		case <-launchCtx.Done():
			proc.KillTree(cmd)
			waitForProcess(done)
			return nil, fmt.Errorf("persistent browser CDP readiness: %w", errors.Join(launchCtx.Err(), err))
		case <-time.After(100 * time.Millisecond):
		}
	}

	remoteOpts := dopts
	remoteOpts.Attach = true
	remoteOpts.DebugURL = info.Endpoint
	remoteOpts.WebSocketURL = info.WebSocketURL
	d := newDriver(remoteOpts, execPath, kind, settleWindow(dopts), context.WithoutCancel(ctx))
	d.mu.Lock()
	d.launchCmd = cmd
	d.launchDone = done
	d.processID = cmd.Process.Pid
	d.debugEndpoint = debugEndpointFor(port)
	d.mu.Unlock()
	if err := d.startRemote(context.WithoutCancel(ctx)); err != nil {
		proc.KillTree(cmd)
		waitForProcess(done)
		_ = d.Close()
		return nil, fmt.Errorf("attach freshly launched persistent browser: %w", err)
	}
	return d, nil
}

func persistentArgs(opts browser.DriverOptions, port int) []string {
	args := []string{
		"--no-first-run",
		"--no-default-browser-check",
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=" + strconv.Itoa(port),
		"--disable-save-password-bubble",
		"--disable-password-manager-reauthentication",
		"--password-store=basic",
	}
	if opts.UserDataDir != "" {
		args = append(args, "--user-data-dir="+opts.UserDataDir)
	}
	if opts.Headless {
		args = append(args, "--headless=new", "--window-size=1280,720")
	}
	if opts.Incognito {
		args = append(args, "--incognito")
	}
	return append(args, "about:blank")
}

func reusablePageTarget(ctx context.Context, endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	u.Path = "/json/list"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s status %d", u.String(), resp.StatusCode)
	}
	var targets []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return "", err
	}
	for _, target := range targets {
		if target.Type == "page" && target.ID != "" && !strings.HasPrefix(target.URL, "devtools://") {
			return target.ID, nil
		}
	}
	return "", nil
}

func waitForProcess(done <-chan struct{}) {
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}
