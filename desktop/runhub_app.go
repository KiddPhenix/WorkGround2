package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"workground2/internal/runhub"
	"workground2/internal/runhub/dsh"
)

const (
	desktopRunHubProfileID = "dsh-rc8"
	desktopRunHubLimit     = 32
)

// ExternalRunProfileView is the keyless DSH readiness projection consumed by
// Desktop. Missing paths, version mismatches and capabilities stay explicit;
// probing never installs, builds or starts DSH.
type ExternalRunProfileView struct {
	ID           string              `json:"id"`
	Ready        bool                `json:"ready"`
	Root         string              `json:"root,omitempty"`
	Version      string              `json:"version,omitempty"`
	Capabilities runhub.Capabilities `json:"capabilities"`
	Missing      []dsh.Issue         `json:"missing,omitempty"`
	Error        string              `json:"error,omitempty"`
}

type ExternalRunSnapshot struct {
	Runs      []runhub.RunProjection `json:"runs"`
	DSH       ExternalRunProfileView `json:"dsh"`
	Workspace string                 `json:"workspace,omitempty"`
	Revision  string                 `json:"revision"`
	Error     string                 `json:"error,omitempty"`
}

type ExternalRunLaunchInput struct {
	RequestID string `json:"requestId"`
	Workspace string `json:"workspace,omitempty"`
	Prompt    string `json:"prompt"`
}

type ExternalRunLaunchResult struct {
	Receipt  runhub.Receipt       `json:"receipt"`
	Run      runhub.RunProjection `json:"run"`
	Snapshot ExternalRunSnapshot  `json:"snapshot"`
}

type ExternalRunCancelInput struct {
	RunID     string `json:"runId"`
	RequestID string `json:"requestId"`
}

type ExternalRunActionResult struct {
	Receipt  runhub.Receipt       `json:"receipt"`
	Run      runhub.RunProjection `json:"run"`
	Snapshot ExternalRunSnapshot  `json:"snapshot"`
}

type desktopRunSession struct {
	runner  runhub.Runner
	binding runhub.RunnerBinding
}

type desktopRunHub struct {
	root  string
	hub   *runhub.Hub
	store *runhub.Store

	mu        sync.Mutex
	launching map[runhub.RunID]context.CancelFunc
	active    map[runhub.RunID]desktopRunSession
	errors    map[runhub.RunID]string

	// Test seams keep lifecycle/idempotency tests hermetic. Production leaves
	// both nil and therefore uses the concrete rc.8 probe and DSH runner.
	resolveProfile func(string) (ExternalRunProfileView, dsh.Config, dsh.RunnerConfig)
	newRunner      func(dsh.RunnerConfig, *runhub.Store) runhub.Runner
}

func newDesktopRunHub(root string) (*desktopRunHub, error) {
	hub, err := runhub.New(root)
	if err != nil {
		return nil, err
	}
	store, err := runhub.Open(root)
	if err != nil {
		return nil, err
	}
	service := &desktopRunHub{
		root: root, hub: hub, store: store,
		launching: map[runhub.RunID]context.CancelFunc{},
		active:    map[runhub.RunID]desktopRunSession{},
		errors:    map[runhub.RunID]string{},
	}
	if _, err := hub.RecoverBindings(); err != nil {
		return nil, fmt.Errorf("recover external runs: %w", err)
	}
	return service, nil
}

// GetExternalRunSnapshot returns the normalized RunHub projection. It is safe
// to poll: it performs only filesystem probing and read-only Hub access.
func (a *App) GetExternalRunSnapshot() ExternalRunSnapshot {
	workspace := a.activeWorkspaceRoot()
	if resolved, err := a.resolveExternalRunWorkspace(workspace); err == nil {
		workspace = resolved
	}
	if a.runHub == nil {
		return ExternalRunSnapshot{DSH: ExternalRunProfileView{ID: desktopRunHubProfileID}, Workspace: workspace, Error: errorText(a.runHubErr)}
	}
	return a.runHub.snapshot(workspace)
}

// LaunchDSHRun creates one idempotent managed Run and asynchronously starts one
// rc.8 JSON-RPC runtime. Workspace may explicitly override the active project;
// an empty value resolves to the current Desktop workspace.
func (a *App) LaunchDSHRun(input ExternalRunLaunchInput) (ExternalRunLaunchResult, error) {
	if a.runHub == nil {
		return ExternalRunLaunchResult{}, firstError(a.runHubErr, errors.New("external RunHub is unavailable"))
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.Prompt = strings.TrimSpace(input.Prompt)
	if input.RequestID == "" || input.Prompt == "" {
		return ExternalRunLaunchResult{}, errors.New("requestId and prompt are required")
	}
	workspace, err := a.resolveExternalRunWorkspace(input.Workspace)
	if err != nil {
		return ExternalRunLaunchResult{}, err
	}
	return a.runHub.launch(a.bootContext(), input.RequestID, workspace, input.Prompt)
}

// CancelExternalRun exposes only the capability DSH rc.8 actually supports.
// Repeating a cancellation is safe and returns the current terminal projection.
func (a *App) CancelExternalRun(input ExternalRunCancelInput) (ExternalRunActionResult, error) {
	if a.runHub == nil {
		return ExternalRunActionResult{}, firstError(a.runHubErr, errors.New("external RunHub is unavailable"))
	}
	input.RunID = strings.TrimSpace(input.RunID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.RunID == "" || input.RequestID == "" {
		return ExternalRunActionResult{}, errors.New("runId and requestId are required")
	}
	return a.runHub.cancel(a.bootContext(), runhub.RunID(input.RunID), input.RequestID, a.activeWorkspaceRoot())
}

func (a *App) resolveExternalRunWorkspace(override string) (string, error) {
	root := strings.TrimSpace(override)
	if root == "" {
		root = strings.TrimSpace(a.activeWorkspaceRoot())
	}
	if root == "" {
		return "", errors.New("no active workspace; choose a workspace explicitly")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("workspace %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace %s is not a directory", abs)
	}
	return filepath.Clean(abs), nil
}

func (s *desktopRunHub) snapshot(workspace string) ExternalRunSnapshot {
	profile, _, _ := s.profile(workspace)
	runs := s.hub.List(runhub.Filter{})
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].UpdatedAt.Equal(runs[j].UpdatedAt) {
			return runs[i].ID > runs[j].ID
		}
		return runs[i].UpdatedAt.After(runs[j].UpdatedAt)
	})
	if len(runs) > desktopRunHubLimit {
		runs = runs[:desktopRunHubLimit]
	}

	s.mu.Lock()
	for id := range s.active {
		if run, ok := s.hub.Get(id); ok && run.State.IsTerminal() {
			delete(s.active, id)
		}
	}
	var diagnostics []string
	for id, message := range s.errors {
		diagnostics = append(diagnostics, string(id)+": "+message)
	}
	s.mu.Unlock()
	sort.Strings(diagnostics)

	parts := []string{profile.ID, profile.Root, profile.Version, fmt.Sprint(profile.Ready)}
	for _, run := range runs {
		parts = append(parts, string(run.ID), string(run.State), fmt.Sprint(run.Revision))
	}
	return ExternalRunSnapshot{
		Runs: runs, DSH: profile, Workspace: workspace,
		Revision: widgetRevision(parts...),
		Error:    strings.Join(diagnostics, "; "),
	}
}

func (s *desktopRunHub) launch(parent context.Context, requestID, workspace, prompt string) (ExternalRunLaunchResult, error) {
	profile, probeCfg, runnerCfg := s.profile(workspace)
	if profile.Error != "" {
		return ExternalRunLaunchResult{}, errors.New(profile.Error)
	}
	if !profile.Ready {
		return ExternalRunLaunchResult{}, fmt.Errorf("DSH rc.8 is not ready: %s", dshIssues(profile.Missing))
	}
	runnerCfg.Probe = probeCfg
	runID := runhub.DeriveRunID(requestID)
	runnerCfg.Env = desktopDSHEnv(workspace, filepath.Join(s.root, "dsh-sessions", string(runID)))
	runner := s.runner(runnerCfg)

	intent := runhub.LaunchIntent{
		RequestID: requestID, RunnerProfileID: desktopRunHubProfileID,
		Source: runhub.SourceDSH, Workspace: workspace, Prompt: prompt,
		PermissionProfile: "dsh-config", Capabilities: profile.Capabilities,
	}
	receipt, run := s.hub.Launch(intent)
	if receipt.Status == runhub.ReceiptAccepted {
		_, _ = s.hub.Report(runhub.RunEvent{
			EventID: runhub.EventID(string(run.ID) + ":desktop-title"), RunID: run.ID,
			Source: runhub.SourceDSH, OccurredAt: time.Now(), Type: runhub.EventTitle,
			Payload: runhub.EventPayload{Title: "DSH · " + filepath.Base(workspace)},
		})
		run, _ = s.hub.Get(run.ID)
	}
	if receipt.Status != runhub.ReceiptAccepted && receipt.Status != runhub.ReceiptAlreadyApplied {
		return ExternalRunLaunchResult{Receipt: receipt, Run: run.Projection(), Snapshot: s.snapshot(workspace)}, nil
	}
	if run.State == runhub.StateQueued {
		s.mu.Lock()
		_, launching := s.launching[run.ID]
		_, active := s.active[run.ID]
		if !launching && !active {
			ctx, cancel := context.WithCancel(parent)
			s.launching[run.ID] = cancel
			go s.start(ctx, runner, intent, run.ID)
		}
		s.mu.Unlock()
	}
	return ExternalRunLaunchResult{Receipt: receipt, Run: run.Projection(), Snapshot: s.snapshot(workspace)}, nil
}

func (s *desktopRunHub) profile(workspace string) (ExternalRunProfileView, dsh.Config, dsh.RunnerConfig) {
	if s.resolveProfile != nil {
		return s.resolveProfile(workspace)
	}
	return resolveDesktopDSHProfile(workspace)
}

func (s *desktopRunHub) runner(cfg dsh.RunnerConfig) runhub.Runner {
	if s.newRunner != nil {
		return s.newRunner(cfg, s.store)
	}
	return dsh.NewRunner(cfg, s.store)
}

func (s *desktopRunHub) start(ctx context.Context, runner runhub.Runner, intent runhub.LaunchIntent, id runhub.RunID) {
	binding, err := runner.Start(ctx, runhub.LaunchRequest{LaunchIntent: intent}, s.hub)
	s.mu.Lock()
	delete(s.launching, id)
	if err == nil {
		s.active[id] = desktopRunSession{runner: runner, binding: binding}
		delete(s.errors, id)
	} else {
		s.errors[id] = err.Error()
	}
	s.mu.Unlock()
	if err == nil {
		return
	}
	if run, ok := s.hub.Get(id); ok && !run.State.IsTerminal() {
		_, _ = s.hub.Report(runhub.RunEvent{
			EventID: runhub.EventID(string(id) + ":desktop-start-failed"), RunID: id,
			Source: runhub.SourceDSH, OccurredAt: time.Now(), Type: runhub.EventFailed,
			Payload: runhub.EventPayload{Detail: "desktop-start-failed"},
		})
	}
}

func (s *desktopRunHub) cancel(ctx context.Context, id runhub.RunID, requestID, workspace string) (ExternalRunActionResult, error) {
	run, ok := s.hub.Get(id)
	if !ok {
		return ExternalRunActionResult{}, fmt.Errorf("external run %s was not found", id)
	}
	if run.State.IsTerminal() {
		return ExternalRunActionResult{
			Receipt: runhub.Receipt{Status: runhub.ReceiptAlreadyApplied, RunID: id, Revision: run.Revision},
			Run:     run.Projection(), Snapshot: s.snapshot(workspace),
		}, nil
	}
	if !run.Capabilities.Cancel {
		return ExternalRunActionResult{}, errors.New("this external run does not support cancel")
	}

	s.mu.Lock()
	cancelStart := s.launching[id]
	session, active := s.active[id]
	s.mu.Unlock()
	if cancelStart != nil {
		cancelStart()
		receipt, after := s.hub.Report(runhub.RunEvent{
			EventID: runhub.EventID(string(id) + ":desktop-cancel"), RunID: id,
			Source: runhub.SourceDSH, OccurredAt: time.Now(), Type: runhub.EventCancelled,
		})
		return ExternalRunActionResult{Receipt: receipt, Run: after.Projection(), Snapshot: s.snapshot(workspace)}, nil
	}
	if !active {
		return ExternalRunActionResult{}, errors.New("external run has no live Desktop binding; refresh to recover its terminal state")
	}
	cancelCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := session.runner.Cancel(cancelCtx, session.binding); err != nil {
		return ExternalRunActionResult{}, fmt.Errorf("cancel external run: %w", err)
	}
	s.mu.Lock()
	delete(s.active, id)
	delete(s.errors, id)
	s.mu.Unlock()
	after, _ := s.hub.Get(id)
	return ExternalRunActionResult{
		Receipt: runhub.Receipt{Status: runhub.ReceiptAccepted, RunID: id, Revision: after.Revision},
		Run:     after.Projection(), Snapshot: s.snapshot(workspace),
	}, nil
}

func (s *desktopRunHub) close() {
	s.mu.Lock()
	launching := make([]context.CancelFunc, 0, len(s.launching))
	for _, cancel := range s.launching {
		launching = append(launching, cancel)
	}
	active := make([]desktopRunSession, 0, len(s.active))
	for _, session := range s.active {
		active = append(active, session)
	}
	s.mu.Unlock()
	for _, cancel := range launching {
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, session := range active {
		_ = session.runner.Cancel(ctx, session.binding)
	}
}

func resolveDesktopDSHProfile(workspace string) (ExternalRunProfileView, dsh.Config, dsh.RunnerConfig) {
	profile := ExternalRunProfileView{ID: desktopRunHubProfileID}
	root, err := resolveDesktopDSHRoot(workspace)
	if err != nil {
		profile.Error = err.Error()
		return profile, dsh.Config{}, dsh.RunnerConfig{}
	}
	profile.Root = root
	entry := strings.TrimSpace(os.Getenv("DSH_RUNHUB_ENTRY"))
	if entry == "" {
		entry = filepath.Join(root, "packages", "examples", "jsonrpc-demo", "lib", "bin.js")
	}
	configPath := strings.TrimSpace(os.Getenv("DSH_RUNHUB_CONFIG"))
	if configPath == "" {
		configPath = filepath.Join(root, "examples", "jsonrpc-agent", "cordis.yml")
	}
	probeCfg := dsh.Config{EntryPath: entry, ConfigPath: configPath, VersionPath: filepath.Join(root, "package.json")}
	result, err := dsh.Probe(probeCfg)
	if err != nil {
		profile.Error = err.Error()
		return profile, probeCfg, dsh.RunnerConfig{}
	}
	profile.Version = result.Version
	profile.Missing = result.Missing
	profile.Ready = result.Ready() && strings.TrimPrefix(result.Version, "v") == "0.1.0-rc.8"
	if result.Ready() && !profile.Ready {
		profile.Missing = append(profile.Missing, dsh.Issue{Kind: dsh.IssueVersion, Detail: "unsupported version " + result.Version + ", require 0.1.0-rc.8"})
	}
	if profile.Ready {
		profile.Capabilities = runhub.Capabilities{Cancel: true}
	}
	provider := firstNonEmpty(strings.TrimSpace(os.Getenv("DSH_RUNHUB_PROVIDER")), "deepseek-official")
	model := firstNonEmpty(strings.TrimSpace(os.Getenv("DSH_RUNHUB_MODEL")), strings.TrimSpace(os.Getenv("DSH_MODEL")), "deepseek-v4-pro")
	return profile, probeCfg, dsh.RunnerConfig{Provider: provider, Model: model}
}

func resolveDesktopDSHRoot(workspace string) (string, error) {
	explicit := strings.TrimSpace(os.Getenv("DSH_RUNHUB_ROOT"))
	if explicit != "" {
		if !filepath.IsAbs(explicit) {
			return "", errors.New("DSH_RUNHUB_ROOT must be an absolute path")
		}
		return filepath.Clean(explicit), nil
	}
	var candidates []string
	addTree := func(seed string) {
		seed = strings.TrimSpace(seed)
		if seed == "" {
			return
		}
		for dir, depth := filepath.Clean(seed), 0; depth < 8; depth++ {
			candidates = append(candidates, dir, filepath.Join(dir, "dsh"))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if anchor := strings.TrimSpace(os.Getenv("DSH_RUNTIME_ANCHOR")); anchor != "" {
		addTree(filepath.Dir(anchor))
	}
	addTree(workspace)
	if cwd, err := os.Getwd(); err == nil {
		addTree(cwd)
	}
	if executable, err := os.Executable(); err == nil {
		addTree(filepath.Dir(executable))
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		key := strings.ToLower(candidate)
		if seen[key] {
			continue
		}
		seen[key] = true
		if desktopDSHRootReady(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("cannot locate a built dsh-v0.1.0-rc.8 checkout; set DSH_RUNHUB_ROOT")
}

func desktopDSHRootReady(root string) bool {
	for _, path := range []string{
		filepath.Join(root, "package.json"),
		filepath.Join(root, "packages", "examples", "jsonrpc-demo", "lib", "bin.js"),
		filepath.Join(root, "examples", "jsonrpc-agent", "cordis.yml"),
	} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func desktopDSHEnv(workspace, sessionRoot string) []string {
	allowedExact := map[string]bool{
		"PATH": true, "PATHEXT": true, "SYSTEMROOT": true, "WINDIR": true,
		"COMSPEC": true, "TEMP": true, "TMP": true, "HOME": true,
		"USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true,
		"PROGRAMDATA": true, "HTTP_PROXY": true, "HTTPS_PROXY": true,
		"NO_PROXY": true, "ALL_PROXY": true,
	}
	var env []string
	for _, pair := range os.Environ() {
		key, _, ok := strings.Cut(pair, "=")
		upper := strings.ToUpper(key)
		if ok && (allowedExact[upper] || strings.HasPrefix(upper, "DSH_") || strings.HasPrefix(upper, "DEEPSEEK_") || strings.HasPrefix(upper, "NODE_")) {
			env = append(env, pair)
		}
	}
	env = append(env,
		"DSH_CWD="+workspace,
		"DSH_SESSION_ROOT="+sessionRoot,
		"DSH_TELEMETRY_DISABLED=1",
	)
	return env
}

func dshIssues(issues []dsh.Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, issue.Detail)
	}
	return strings.Join(parts, "; ")
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
