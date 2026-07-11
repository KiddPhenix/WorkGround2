package drawaddon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"workground2/internal/config"
	"workground2/internal/fileutil"
	"workground2/internal/proc"
)

const (
	ModeAPI = "api"
	ModeCLI = "cli"

	StatusUnconfigured = "unconfigured"
	StatusNeedsAuth    = "needs_auth"
	StatusReady        = "ready"
	StatusRunning      = "running"
	StatusFailed       = "failed"
	StatusDisabled     = "disabled"

	TaskRunning   = "running"
	TaskSucceeded = "succeeded"
	TaskFailed    = "failed"

	defaultTimeout = 2 * time.Minute
)

// Service owns draw-tool AddOn config and synchronous generation tasks.
type Service struct {
	Home    string
	Timeout time.Duration

	mu sync.Mutex
}

func New(home string) *Service {
	return &Service{Home: filepath.Clean(home), Timeout: defaultTimeout}
}

type ProviderInput struct {
	ID          string   `json:"id"`
	Enabled     bool     `json:"enabled"`
	DisplayName string   `json:"displayName,omitempty"`
	Mode        string   `json:"mode"`
	BaseURL     string   `json:"baseUrl,omitempty"`
	Model       string   `json:"model,omitempty"`
	APIKeyRef   string   `json:"apiKeyRef,omitempty"`
	CLICommand  string   `json:"cliCommand,omitempty"`
	CLIArgs     []string `json:"cliArgs,omitempty"`
	OutputDir   string   `json:"outputDir,omitempty"`
}

type ProviderState struct {
	Status         string `json:"status"`
	LastTaskID     string `json:"lastTaskId,omitempty"`
	LastStartedAt  string `json:"lastStartedAt,omitempty"`
	LastFinishedAt string `json:"lastFinishedAt,omitempty"`
	LastOutputPath string `json:"lastOutputPath,omitempty"`
	LastError      string `json:"lastError,omitempty"`
}

type ProviderView struct {
	ID          string        `json:"id"`
	Enabled     bool          `json:"enabled"`
	DisplayName string        `json:"displayName,omitempty"`
	Mode        string        `json:"mode"`
	BaseURL     string        `json:"baseUrl,omitempty"`
	Model       string        `json:"model,omitempty"`
	APIKeyRef   string        `json:"apiKeyRef,omitempty"`
	AuthStatus  string        `json:"authStatus"`
	CLICommand  string        `json:"cliCommand,omitempty"`
	CLIArgs     []string      `json:"cliArgs,omitempty"`
	OutputDir   string        `json:"outputDir,omitempty"`
	State       ProviderState `json:"state"`
}

type GenerateInput struct {
	ProviderID string `json:"providerId"`
	Prompt     string `json:"prompt"`
}

type TaskView struct {
	TaskID     string `json:"taskId"`
	ProviderID string `json:"providerId"`
	Status     string `json:"status"`
	Phase      string `json:"phase"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Prompt     string `json:"prompt"`
	OutputPath string `json:"outputPath,omitempty"`
	Error      string `json:"error,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`
}

type configFile struct {
	Version   int              `json:"version"`
	Providers []providerRecord `json:"providers"`
}

type providerRecord struct {
	ID          string        `json:"id"`
	Enabled     bool          `json:"enabled"`
	DisplayName string        `json:"displayName,omitempty"`
	Mode        string        `json:"mode"`
	BaseURL     string        `json:"baseUrl,omitempty"`
	Model       string        `json:"model,omitempty"`
	APIKeyRef   string        `json:"apiKeyRef,omitempty"`
	CLICommand  string        `json:"cliCommand,omitempty"`
	CLIArgs     []string      `json:"cliArgs,omitempty"`
	OutputDir   string        `json:"outputDir,omitempty"`
	State       ProviderState `json:"state"`
}

// Providers returns configured providers and recovers interrupted running state.
func (s *Service) Providers() ([]ProviderView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.readConfig()
	if err != nil {
		return nil, err
	}
	changed := recoverRunningProviders(st.Providers)
	if changed {
		if err := s.writeConfig(st); err != nil {
			return nil, err
		}
	}
	out := make([]ProviderView, 0, len(st.Providers))
	for _, p := range st.Providers {
		out = append(out, s.providerView(p))
	}
	return out, nil
}

func (s *Service) List() ([]ProviderView, error) {
	return s.Providers()
}

func (s *Service) HasEnabledProvider() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.readConfig()
	if err != nil {
		return false, err
	}
	for _, p := range st.Providers {
		if p.Enabled {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) Save(ctx context.Context, in ProviderInput) (ProviderView, error) {
	if err := ctx.Err(); err != nil {
		return ProviderView{}, err
	}
	next, err := normalizeInput(in)
	if err != nil {
		return ProviderView{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.readConfig()
	if err != nil {
		return ProviderView{}, err
	}
	idx := providerIndex(st.Providers, next.ID)
	if idx >= 0 {
		prev := st.Providers[idx]
		next.State = prev.State
		if providerConfigChanged(prev, next) || next.State.Status == "" || prev.Enabled != next.Enabled {
			next.State.Status = initialStatus(next)
			next.State.LastError = ""
		}
		st.Providers[idx] = next
	} else {
		next.State.Status = initialStatus(next)
		st.Providers = append(st.Providers, next)
	}
	if err := s.writeConfig(st); err != nil {
		return ProviderView{}, err
	}
	return s.providerView(next), nil
}

func (s *Service) Delete(ctx context.Context, id string) (ProviderView, error) {
	if err := ctx.Err(); err != nil {
		return ProviderView{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ProviderView{}, errors.New("provider id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.readConfig()
	if err != nil {
		return ProviderView{}, err
	}
	idx := providerIndex(st.Providers, id)
	if idx < 0 {
		return ProviderView{ID: id, Enabled: false, Mode: ModeAPI, AuthStatus: "none", State: ProviderState{Status: StatusDisabled}}, nil
	}
	prev := st.Providers[idx]
	prev.Enabled = false
	prev.State.Status = StatusDisabled
	prev.State.LastError = ""
	st.Providers = append(st.Providers[:idx], st.Providers[idx+1:]...)
	if err := s.writeConfig(st); err != nil {
		return s.providerView(prev), err
	}
	return s.providerView(prev), nil
}

func (s *Service) Generate(ctx context.Context, in GenerateInput) (TaskView, error) {
	start := now()
	task := TaskView{
		TaskID:     taskID(in.ProviderID, start),
		ProviderID: strings.TrimSpace(in.ProviderID),
		Status:     TaskRunning,
		Phase:      "read_config",
		StartedAt:  formatTime(start),
		Prompt:     strings.TrimSpace(in.Prompt),
		Retryable:  true,
	}
	if task.ProviderID == "" {
		err := errors.New("provider id is required")
		return finishTask(task, TaskFailed, err.Error(), false), err
	}
	if task.Prompt == "" {
		err := errors.New("prompt is required")
		return finishTask(task, TaskFailed, err.Error(), false), err
	}
	if err := ctx.Err(); err != nil {
		return finishTask(task, TaskFailed, err.Error(), true), err
	}

	p, err := s.markRunning(ctx, task)
	if err != nil {
		safe := SanitizeError(err)
		task = finishTask(task.withPhase("validate_provider"), TaskFailed, safe, false)
		return task, errors.New(safe)
	}

	switch p.Mode {
	case ModeCLI:
		task, err = s.runCLI(ctx, p, task)
	case ModeAPI:
		task, err = s.planAPI(ctx, p, task)
	default:
		err = fmt.Errorf("unsupported provider mode %q", p.Mode)
		task = finishTask(task.withPhase("validate_provider"), TaskFailed, err.Error(), false)
	}
	s.recordTaskResult(p.ID, task)
	return task, err
}

func (s *Service) Run(ctx context.Context, in GenerateInput) (TaskView, error) {
	return s.Generate(ctx, in)
}

func (s *Service) markRunning(ctx context.Context, task TaskView) (providerRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return providerRecord{}, err
	}
	st, err := s.readConfig()
	if err != nil {
		safe := SanitizeError(err)
		return providerRecord{}, errors.New(safe)
	}
	idx := providerIndex(st.Providers, task.ProviderID)
	if idx < 0 {
		err := fmt.Errorf("draw addon provider %q not found", task.ProviderID)
		return providerRecord{}, err
	}
	p := st.Providers[idx]
	if err := validateProviderForRun(p); err != nil {
		safe := SanitizeError(err)
		p.State.Status = statusFromValidation(err)
		p.State.LastTaskID = task.TaskID
		p.State.LastStartedAt = task.StartedAt
		p.State.LastFinishedAt = formatTime(now())
		p.State.LastError = safe
		st.Providers[idx] = p
		_ = s.writeConfig(st)
		return p, errors.New(safe)
	}
	p.State.Status = StatusRunning
	p.State.LastTaskID = task.TaskID
	p.State.LastStartedAt = task.StartedAt
	p.State.LastFinishedAt = ""
	p.State.LastOutputPath = ""
	p.State.LastError = ""
	st.Providers[idx] = p
	if err := s.writeConfig(st); err != nil {
		return providerRecord{}, err
	}
	return p, nil
}

func (s *Service) recordTaskResult(id string, task TaskView) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.readConfig()
	if err != nil {
		return
	}
	idx := providerIndex(st.Providers, id)
	if idx < 0 {
		return
	}
	p := st.Providers[idx]
	p.State.LastTaskID = task.TaskID
	p.State.LastStartedAt = task.StartedAt
	p.State.LastFinishedAt = task.FinishedAt
	p.State.LastOutputPath = task.OutputPath
	p.State.LastError = task.Error
	if task.Status == TaskSucceeded {
		p.State.Status = StatusReady
	} else if task.Phase == "resolve_secret" {
		p.State.Status = StatusNeedsAuth
	} else if !p.Enabled {
		p.State.Status = StatusDisabled
	} else {
		p.State.Status = StatusFailed
	}
	st.Providers[idx] = p
	_ = s.writeConfig(st)
}

func (s *Service) planAPI(ctx context.Context, p providerRecord, task TaskView) (TaskView, error) {
	if err := ctx.Err(); err != nil {
		return finishTask(task.withPhase("api_dry_run"), TaskFailed, err.Error(), true), err
	}
	if p.APIKeyRef != "" {
		if _, err := ResolveCredential(s.Home, p.APIKeyRef); err != nil {
			safe := SanitizeError(err)
			task = finishTask(task.withPhase("resolve_secret"), TaskFailed, safe, false)
			return task, errors.New(safe)
		}
	}
	task.Phase = "api_dry_run"
	return finishTask(task, TaskSucceeded, "", false), nil
}

func (s *Service) runCLI(ctx context.Context, p providerRecord, task TaskView) (TaskView, error) {
	task.Phase = "prepare_cli"
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	outputTarget, outputDir, err := s.prepareOutput(p, task.TaskID)
	if err != nil {
		safe := SanitizeError(err)
		task = finishTask(task, TaskFailed, safe, true)
		return task, errors.New(safe)
	}
	values := map[string]string{
		"prompt":     task.Prompt,
		"output":     outputTarget,
		"outputPath": outputTarget,
		"model":      p.Model,
		"baseURL":    p.BaseURL,
		"providerId": p.ID,
	}
	args, promptInArgs := expandArgs(p.CLIArgs, values)

	task.Phase = "resolve_secret"
	var secret string
	var secretName string
	if p.APIKeyRef != "" {
		res, err := ResolveCredential(s.Home, p.APIKeyRef)
		if err != nil {
			safe := SanitizeError(err)
			task = finishTask(task, TaskFailed, safe, false)
			return task, errors.New(safe)
		}
		secret = res.Value
		secretName = res.Name
	}

	task.Phase = "run_cli"
	cmd := exec.CommandContext(runCtx, p.CLICommand, args...)
	proc.HideWindow(cmd)
	cmd.Env = append(os.Environ(),
		"DRAW_ADDON_PROVIDER_ID="+p.ID,
		"DRAW_ADDON_MODEL="+p.Model,
		"DRAW_ADDON_BASE_URL="+p.BaseURL,
		"DRAW_ADDON_OUTPUT="+outputTarget,
	)
	if secret != "" {
		cmd.Env = append(cmd.Env, "DRAW_ADDON_API_KEY="+secret)
		if isCredentialName(secretName) {
			cmd.Env = append(cmd.Env, secretName+"="+secret)
		}
	}
	if !promptInArgs {
		cmd.Stdin = strings.NewReader(task.Prompt)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if runCtx.Err() != nil {
			safe := SanitizeError(fmt.Errorf("local cli timed out or was cancelled: %w", runCtx.Err()), secret)
			task = finishTask(task, TaskFailed, safe, true)
			return task, errors.New(safe)
		}
		safe := SanitizeError(fmt.Errorf("local cli failed: %w: %s", err, strings.TrimSpace(stderr.String())), secret)
		task = finishTask(task, TaskFailed, safe, true)
		return task, errors.New(safe)
	}

	outputPath := outputPathFromStdout(stdout.String(), outputDir)
	if outputPath == "" {
		outputPath = outputTarget
	}
	if outputPath == "" {
		err := errors.New("local cli did not report an output path and outputDir is empty")
		task = finishTask(task, TaskFailed, err.Error(), true)
		return task, err
	}
	task.OutputPath = outputPath
	return finishTask(task.withPhase("done"), TaskSucceeded, "", false), nil
}

func (s *Service) prepareOutput(p providerRecord, taskID string) (string, string, error) {
	if strings.TrimSpace(p.OutputDir) == "" {
		return "", "", nil
	}
	dir := p.OutputDir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.rootDir(), "outputs", p.ID, filepath.FromSlash(dir))
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	return filepath.Join(dir, safeFileName(taskID)+".png"), dir, nil
}

func (s *Service) providerView(p providerRecord) ProviderView {
	return ProviderView{
		ID:          p.ID,
		Enabled:     p.Enabled,
		DisplayName: p.DisplayName,
		Mode:        p.Mode,
		BaseURL:     p.BaseURL,
		Model:       p.Model,
		APIKeyRef:   p.APIKeyRef,
		AuthStatus:  credentialStatus(s.Home, p.APIKeyRef),
		CLICommand:  p.CLICommand,
		CLIArgs:     cloneArgs(p.CLIArgs),
		OutputDir:   p.OutputDir,
		State:       p.State,
	}
}

func (s *Service) readConfig() (configFile, error) {
	data, err := os.ReadFile(s.configPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return configFile{Version: 1}, nil
		}
		return configFile{}, err
	}
	var st configFile
	if err := json.Unmarshal(data, &st); err != nil {
		return configFile{}, err
	}
	if st.Version == 0 {
		st.Version = 1
	}
	for i := range st.Providers {
		if st.Providers[i].Mode == "" {
			st.Providers[i].Mode = ModeAPI
		}
		if st.Providers[i].State.Status == "" {
			st.Providers[i].State.Status = initialStatus(st.Providers[i])
		}
		st.Providers[i].CLIArgs = cloneArgs(st.Providers[i].CLIArgs)
	}
	sortProviders(st.Providers)
	return st, nil
}

func (s *Service) writeConfig(st configFile) error {
	if st.Version == 0 {
		st.Version = 1
	}
	sortProviders(st.Providers)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fileutil.AtomicWriteFile(s.configPath(), data, 0o644)
}

func (s *Service) rootDir() string {
	return filepath.Join(s.Home, "addons", "draw-tool")
}

func (s *Service) configPath() string {
	return filepath.Join(s.rootDir(), "config.json")
}

type ResolvedCredential = config.CredentialResolution

func ResolveCredential(home, ref string) (ResolvedCredential, error) {
	key, envOnly, err := credentialName(ref)
	if err != nil {
		return ResolvedCredential{}, err
	}
	var res config.CredentialResolution
	if envOnly {
		res = config.ResolveCredentialForRoot(home, key)
	} else {
		res = config.ResolveCredentialForRootGlobalFirst(home, key)
		if !res.Set {
			res = config.ResolveCredentialForRoot(home, key)
		}
	}
	if !res.Set || res.Value == "" {
		return ResolvedCredential{Name: key}, fmt.Errorf("credential %q is not set", key)
	}
	return res, nil
}

func credentialName(ref string) (string, bool, error) {
	key := strings.TrimSpace(ref)
	if key == "" {
		return "", false, errors.New("apiKeyRef is empty")
	}
	envOnly := false
	if _, ok := strings.CutPrefix(strings.ToLower(key), "env:"); ok {
		key = strings.TrimSpace(key[4:])
		envOnly = true
	} else if strings.Contains(key, "://") {
		return "", false, fmt.Errorf("unsupported credential ref %q", ref)
	}
	if key == "" {
		return "", envOnly, errors.New("apiKeyRef is empty")
	}
	return key, envOnly, nil
}

func credentialStatus(home, ref string) string {
	if strings.TrimSpace(ref) == "" {
		return "none"
	}
	if _, err := ResolveCredential(home, ref); err != nil {
		return "missing"
	}
	return "set"
}

func normalizeInput(in ProviderInput) (providerRecord, error) {
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return providerRecord{}, errors.New("provider id is required")
	}
	if !validID.MatchString(id) {
		return providerRecord{}, fmt.Errorf("invalid provider id %q", id)
	}
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if mode == "" {
		mode = ModeAPI
	}
	if mode != ModeAPI && mode != ModeCLI {
		return providerRecord{}, fmt.Errorf("unsupported provider mode %q", in.Mode)
	}
	outputDir, err := cleanOutputDir(in.OutputDir)
	if err != nil {
		return providerRecord{}, err
	}
	return providerRecord{
		ID:          id,
		Enabled:     in.Enabled,
		DisplayName: strings.TrimSpace(in.DisplayName),
		Mode:        mode,
		BaseURL:     sanitizeURLForStorage(strings.TrimSpace(in.BaseURL)),
		Model:       strings.TrimSpace(in.Model),
		APIKeyRef:   strings.TrimSpace(in.APIKeyRef),
		CLICommand:  strings.TrimSpace(in.CLICommand),
		CLIArgs:     cleanArgs(in.CLIArgs),
		OutputDir:   outputDir,
	}, nil
}

func cleanArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		out = append(out, strings.TrimSpace(arg))
	}
	return out
}

func cleanOutputDir(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	path := filepath.Clean(filepath.FromSlash(raw))
	if filepath.IsAbs(path) {
		return path, nil
	}
	if path == "." {
		return ".", nil
	}
	if path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("outputDir %q must stay inside draw-tool outputs when it is relative", raw)
	}
	return filepath.ToSlash(path), nil
}

func validateProviderForRun(p providerRecord) error {
	if !p.Enabled {
		return errors.New("provider is disabled")
	}
	switch p.Mode {
	case ModeAPI:
		if strings.TrimSpace(p.BaseURL) == "" {
			return errors.New("baseUrl is required for api mode")
		}
		if strings.TrimSpace(p.Model) == "" {
			return errors.New("model is required for api mode")
		}
	case ModeCLI:
		if strings.TrimSpace(p.CLICommand) == "" {
			return errors.New("cliCommand is required for cli mode")
		}
	default:
		return fmt.Errorf("unsupported provider mode %q", p.Mode)
	}
	return nil
}

func statusFromValidation(err error) string {
	if err == nil {
		return StatusReady
	}
	text := err.Error()
	if strings.Contains(text, "disabled") {
		return StatusDisabled
	}
	if strings.Contains(text, "required") {
		return StatusUnconfigured
	}
	return StatusFailed
}

func initialStatus(p providerRecord) string {
	if !p.Enabled {
		return StatusDisabled
	}
	if err := validateProviderForRun(p); err != nil {
		return statusFromValidation(err)
	}
	return StatusReady
}

func providerConfigChanged(a, b providerRecord) bool {
	return a.Enabled != b.Enabled ||
		a.DisplayName != b.DisplayName ||
		a.Mode != b.Mode ||
		a.BaseURL != b.BaseURL ||
		a.Model != b.Model ||
		a.APIKeyRef != b.APIKeyRef ||
		a.CLICommand != b.CLICommand ||
		a.OutputDir != b.OutputDir ||
		!sameArgs(a.CLIArgs, b.CLIArgs)
}

func sameArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func recoverRunningProviders(providers []providerRecord) bool {
	changed := false
	for i := range providers {
		if providers[i].State.Status != StatusRunning {
			continue
		}
		providers[i].State.Status = StatusFailed
		providers[i].State.LastFinishedAt = formatTime(now())
		providers[i].State.LastError = "previous generation did not finish; retry is safe"
		changed = true
	}
	return changed
}

func expandArgs(args []string, values map[string]string) ([]string, bool) {
	out := make([]string, 0, len(args))
	promptInArgs := false
	for _, arg := range args {
		next := arg
		for key, value := range values {
			for _, token := range []string{"{{" + key + "}}", "{" + key + "}"} {
				if strings.Contains(next, token) && key == "prompt" {
					promptInArgs = true
				}
				next = strings.ReplaceAll(next, token, value)
			}
		}
		out = append(out, next)
	}
	return out, promptInArgs
}

func outputPathFromStdout(stdout, outputDir string) string {
	text := strings.TrimSpace(stdout)
	if text == "" {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal([]byte(text), &obj) == nil {
		for _, key := range []string{"outputPath", "output_path", "path", "file"} {
			if value, ok := obj[key].(string); ok && strings.TrimSpace(value) != "" {
				return normalizeOutputPath(value, outputDir)
			}
		}
	}
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.Trim(strings.TrimSpace(lines[i]), `"'`)
		if line != "" {
			return normalizeOutputPath(line, outputDir)
		}
	}
	return ""
}

func normalizeOutputPath(path, outputDir string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) || outputDir == "" {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(outputDir, path))
}

func finishTask(task TaskView, status, safeError string, retryable bool) TaskView {
	task.Status = status
	task.FinishedAt = formatTime(now())
	task.Error = safeError
	task.Retryable = retryable
	return task
}

func (task TaskView) withPhase(phase string) TaskView {
	task.Phase = phase
	return task
}

func providerIndex(providers []providerRecord, id string) int {
	for i := range providers {
		if providers[i].ID == id {
			return i
		}
	}
	return -1
}

func sortProviders(providers []providerRecord) {
	sort.SliceStable(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
}

func cloneArgs(args []string) []string {
	if args == nil {
		return []string{}
	}
	return append([]string(nil), args...)
}

func taskID(id string, t time.Time) string {
	return strings.TrimSpace(id) + "-" + fmt.Sprintf("%d", t.UTC().UnixNano())
}

func now() time.Time {
	return time.Now().UTC()
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func safeFileName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "task"
	}
	return out
}

func isCredentialName(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

var (
	validID             = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	urlUserInfoPattern  = regexp.MustCompile(`(?i)(https?://)[^/\s@]+@`)
	querySecretPattern  = regexp.MustCompile(`(?i)(password|passwd|token|access_token|secret|api_key|apikey)=([^&\s]+)`)
	headerSecretPattern = regexp.MustCompile(`(?i)(authorization|api[_-]?key|token)\s*[:=]\s*([^\s]+)`)
)

func SanitizeError(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	text = urlUserInfoPattern.ReplaceAllString(text, `${1}<redacted>@`)
	text = querySecretPattern.ReplaceAllString(text, `${1}=<redacted>`)
	text = headerSecretPattern.ReplaceAllString(text, `${1}=<redacted>`)
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret, "<redacted>")
	}
	return text
}

func sanitizeURLForStorage(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return raw
	}
	u.User = nil
	q := u.Query()
	for _, key := range []string{"password", "passwd", "token", "access_token", "secret", "api_key", "apikey"} {
		if q.Has(key) {
			q.Set(key, "<redacted>")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
