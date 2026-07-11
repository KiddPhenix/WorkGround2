package skillshare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"workground2/internal/config"
	"workground2/internal/fileutil"
	"workground2/internal/pluginpkg"
	"workground2/internal/skill"
)

const (
	NamespaceSkillShare     = "skill-share"
	NamespaceFlowSkillShare = "flow-skill-share"

	StatusUnconfigured  = "unconfigured"
	StatusNeedsAuth     = "needs_auth"
	StatusSyncing       = "syncing"
	StatusReady         = "ready"
	StatusUpdateFailed  = "update_failed"
	StatusDisabled      = "disabled"
	StatusRemovePending = "remove_pending"

	TaskRunning   = "running"
	TaskSucceeded = "succeeded"
	TaskFailed    = "failed"
)

// Service owns the Skill Share profile state and synchronization side effects.
type Service struct {
	Home      string
	Namespace string
	Remote    bool

	mu sync.Mutex
}

type Options struct {
	Namespace string
	Remote    bool
}

func New(home string) *Service {
	return NewWithOptions(home, Options{Namespace: NamespaceSkillShare})
}

func NewWithOptions(home string, opts Options) *Service {
	namespace := strings.TrimSpace(opts.Namespace)
	if namespace == "" {
		namespace = NamespaceSkillShare
	}
	return &Service{Home: filepath.Clean(home), Namespace: namespace, Remote: opts.Remote}
}

func NewFlow(home string) *Service {
	return NewWithOptions(home, Options{Namespace: NamespaceFlowSkillShare, Remote: true})
}

type ProfileInput struct {
	ID          string        `json:"id"`
	Enabled     bool          `json:"enabled"`
	DisplayName string        `json:"displayName,omitempty"`
	GitURL      string        `json:"gitUrl"`
	Branch      string        `json:"branch,omitempty"`
	Path        string        `json:"path,omitempty"`
	Username    string        `json:"username,omitempty"`
	SecretRef   string        `json:"secretRef,omitempty"`
	PluginName  string        `json:"pluginName,omitempty"`
	Update      UpdateOptions `json:"update,omitempty"`
}

type UpdateOptions struct {
	Auto            bool `json:"auto,omitempty"`
	CheckOnLogin    bool `json:"checkOnLogin,omitempty"`
	IntervalSeconds int  `json:"intervalSeconds,omitempty"`
}

type ProfileState struct {
	Status          string `json:"status"`
	CurrentRevision string `json:"currentRevision,omitempty"`
	LastCheckedAt   string `json:"lastCheckedAt,omitempty"`
	LastUpdatedAt   string `json:"lastUpdatedAt,omitempty"`
	LastError       string `json:"lastError,omitempty"`
}

type ProfileView struct {
	ID           string        `json:"id"`
	Enabled      bool          `json:"enabled"`
	DisplayName  string        `json:"displayName,omitempty"`
	GitURL       string        `json:"gitUrl"`
	Branch       string        `json:"branch,omitempty"`
	Path         string        `json:"path,omitempty"`
	Username     string        `json:"username,omitempty"`
	SecretRef    string        `json:"secretRef,omitempty"`
	AuthStatus   string        `json:"authStatus"`
	PluginName   string        `json:"pluginName,omitempty"`
	Update       UpdateOptions `json:"update,omitempty"`
	State        ProfileState  `json:"state"`
	ManifestKind string        `json:"manifestKind,omitempty"`
	Version      string        `json:"version,omitempty"`
	Skills       int           `json:"skills,omitempty"`
	Hooks        int           `json:"hooks,omitempty"`
	MCPServers   int           `json:"mcpServers,omitempty"`
}

type SyncOptions struct {
	Force   bool   `json:"force,omitempty"`
	Trigger string `json:"trigger,omitempty"`
}

type DeleteOptions struct {
	RemoveSecret bool `json:"removeSecret,omitempty"`
}

type TaskView struct {
	TaskID          string `json:"taskId"`
	ProfileID       string `json:"profileId"`
	Trigger         string `json:"trigger,omitempty"`
	Phase           string `json:"phase"`
	Status          string `json:"status"`
	StartedAt       string `json:"startedAt"`
	FinishedAt      string `json:"finishedAt,omitempty"`
	CurrentRevision string `json:"currentRevision,omitempty"`
	TargetRevision  string `json:"targetRevision,omitempty"`
	Error           string `json:"error,omitempty"`
	Retryable       bool   `json:"retryable,omitempty"`
}

type profileFile struct {
	Version  int             `json:"version"`
	Profiles []profileRecord `json:"profiles"`
}

type profileRecord struct {
	ID           string        `json:"id"`
	Enabled      bool          `json:"enabled"`
	DisplayName  string        `json:"displayName,omitempty"`
	Git          gitProfile    `json:"git"`
	PluginName   string        `json:"pluginName,omitempty"`
	Update       UpdateOptions `json:"update,omitempty"`
	State        ProfileState  `json:"state"`
	ManifestKind string        `json:"manifestKind,omitempty"`
	Version      string        `json:"version,omitempty"`
	Skills       int           `json:"skills,omitempty"`
	Hooks        int           `json:"hooks,omitempty"`
	MCPServers   int           `json:"mcpServers,omitempty"`
}

type gitProfile struct {
	URL    string  `json:"url"`
	Branch string  `json:"branch,omitempty"`
	Path   string  `json:"path,omitempty"`
	Auth   gitAuth `json:"auth,omitempty"`
}

type gitAuth struct {
	Username  string `json:"username,omitempty"`
	SecretRef string `json:"secretRef,omitempty"`
}

type gitCredential struct {
	Username string
	Password string
}

func (s *Service) Profiles() ([]ProfileView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.readProfiles()
	if err != nil {
		return nil, err
	}
	out := make([]ProfileView, 0, len(st.Profiles))
	for _, p := range st.Profiles {
		out = append(out, s.profileView(p))
	}
	return out, nil
}

func (s *Service) Save(ctx context.Context, in ProfileInput) (ProfileView, error) {
	if err := ctx.Err(); err != nil {
		return ProfileView{}, err
	}

	next, err := normalizeInput(in)
	if err != nil {
		return ProfileView{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.readProfiles()
	if err != nil {
		return ProfileView{}, err
	}
	idx := profileIndex(st.Profiles, next.ID)
	if idx >= 0 {
		prev := st.Profiles[idx]
		if sameSource(prev, next) {
			next.State = prev.State
		}
		if next.State.Status == "" || next.Enabled != prev.Enabled {
			next.State.Status = initialStatus(next)
		}
		st.Profiles[idx] = next
	} else {
		next.State.Status = initialStatus(next)
		st.Profiles = append(st.Profiles, next)
	}
	if err := s.writeProfiles(st); err != nil {
		return ProfileView{}, err
	}
	return s.profileView(next), nil
}

func (s *Service) Sync(ctx context.Context, id string, opts SyncOptions) (TaskView, error) {
	start := now()
	task := TaskView{
		TaskID:    taskID(id, start),
		ProfileID: strings.TrimSpace(id),
		Trigger:   strings.TrimSpace(opts.Trigger),
		Phase:     "read_config",
		Status:    TaskRunning,
		StartedAt: formatTime(start),
		Retryable: true,
	}
	if task.Trigger == "" {
		task.Trigger = "manual"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return finishTask(task, TaskFailed, err.Error(), false), err
	}

	st, err := s.readProfiles()
	if err != nil {
		return finishTask(task, TaskFailed, sanitizeError(err), true), err
	}
	idx := profileIndex(st.Profiles, task.ProfileID)
	if idx < 0 {
		err := fmt.Errorf("skill share profile %q not found", task.ProfileID)
		return finishTask(task, TaskFailed, sanitizeError(err), false), err
	}
	p := st.Profiles[idx]
	if !p.Enabled {
		p.State.Status = StatusDisabled
		st.Profiles[idx] = p
		_ = s.writeProfiles(st)
		return finishTask(task.withPhase("disabled"), TaskSucceeded, "", false), nil
	}
	if strings.TrimSpace(p.Git.URL) == "" {
		err := errors.New("git url is required")
		p.State = failedState(p.State, StatusUnconfigured, err, nil, start)
		st.Profiles[idx] = p
		_ = s.writeProfiles(st)
		return finishTask(task, TaskFailed, sanitizeError(err), false), err
	}
	p.State.Status = StatusSyncing
	p.State.LastCheckedAt = formatTime(start)
	p.State.LastError = ""
	st.Profiles[idx] = p
	if err := s.writeProfiles(st); err != nil {
		safe := sanitizeError(err)
		return finishTask(task.withPhase("write_profiles"), TaskFailed, safe, true), errors.New(safe)
	}

	cred, err := s.resolveCredential(p)
	if err != nil {
		safe := sanitizeError(err)
		p.State = failedState(p.State, StatusNeedsAuth, errors.New(safe), nil, start)
		st.Profiles[idx] = p
		_ = s.writeProfiles(st)
		return finishTask(task.withPhase("resolve_secret"), TaskFailed, safe, false), errors.New(safe)
	}

	if s.Remote {
		return s.syncRemote(ctx, st, idx, p, task, start, cred, opts)
	}

	profileDir := s.profileDir(p.ID)
	staging := filepath.Join(profileDir, "staging-"+task.TaskID)
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		safe := sanitizeError(err)
		p.State = failedState(p.State, StatusUpdateFailed, errors.New(safe), []string{cred.Password}, start)
		st.Profiles[idx] = p
		_ = s.writeProfiles(st)
		return finishTask(task.withPhase("prepare_staging"), TaskFailed, safe, true), errors.New(safe)
	}
	_ = os.RemoveAll(staging)

	task.Phase = "clone"
	if err := cloneRepo(ctx, p, cred, staging); err != nil {
		safe := sanitizeError(err, cred.Password, p.Git.URL)
		p.State = failedState(p.State, StatusUpdateFailed, errors.New(safe), []string{cred.Password}, start)
		st.Profiles[idx] = p
		_ = s.writeProfiles(st)
		return finishTask(task, TaskFailed, safe, true), errors.New(safe)
	}

	task.Phase = "read_revision"
	revision, err := gitRevision(ctx, staging)
	if err != nil {
		safe := sanitizeError(err, cred.Password, p.Git.URL)
		p.State = failedState(p.State, StatusUpdateFailed, errors.New(safe), []string{cred.Password}, start)
		st.Profiles[idx] = p
		_ = s.writeProfiles(st)
		return finishTask(task, TaskFailed, safe, true), errors.New(safe)
	}
	task.TargetRevision = revision

	task.Phase = "validate_manifest"
	stagingRoot, err := joinInside(staging, p.Git.Path)
	if err != nil {
		safe := sanitizeError(err)
		p.State = failedState(p.State, StatusUpdateFailed, errors.New(safe), nil, start)
		st.Profiles[idx] = p
		_ = s.writeProfiles(st)
		return finishTask(task, TaskFailed, safe, false), errors.New(safe)
	}
	pkg, _, err := pluginpkg.ParseDir(stagingRoot)
	if err != nil {
		safe := sanitizeError(err, cred.Password, p.Git.URL)
		p.State = failedState(p.State, StatusUpdateFailed, errors.New(safe), []string{cred.Password}, start)
		st.Profiles[idx] = p
		_ = s.writeProfiles(st)
		return finishTask(task, TaskFailed, safe, true), errors.New(safe)
	}

	active := filepath.Join(profileDir, "active")
	activeRoot, err := joinInside(active, p.Git.Path)
	if err != nil {
		safe := sanitizeError(err)
		p.State = failedState(p.State, StatusUpdateFailed, errors.New(safe), nil, start)
		st.Profiles[idx] = p
		_ = s.writeProfiles(st)
		return finishTask(task, TaskFailed, safe, false), errors.New(safe)
	}

	if !opts.Force && p.State.CurrentRevision == revision && dirExists(activeRoot) {
		_ = os.RemoveAll(staging)
		p.State.Status = StatusReady
		p.State.LastCheckedAt = formatTime(start)
		p.State.LastError = ""
		st.Profiles[idx] = p
		if err := s.writeProfiles(st); err != nil {
			safe := sanitizeError(err)
			return finishTask(task.withPhase("write_profiles"), TaskFailed, safe, true), errors.New(safe)
		}
		task.CurrentRevision = revision
		return finishTask(task.withPhase("ready"), TaskSucceeded, "", false), nil
	}

	task.Phase = "switch_active"
	if err := switchActive(staging, active, filepath.Join(profileDir, "previous")); err != nil {
		safe := sanitizeError(err)
		p.State = failedState(p.State, StatusUpdateFailed, errors.New(safe), nil, start)
		st.Profiles[idx] = p
		_ = s.writeProfiles(st)
		return finishTask(task, TaskFailed, safe, true), errors.New(safe)
	}

	task.Phase = "upsert_plugin"
	pluginName := strings.TrimSpace(p.PluginName)
	if pluginName == "" {
		pluginName = pkg.Manifest.Name
	}
	if !pluginpkg.IsValidName(pluginName) {
		err := fmt.Errorf("invalid plugin name %q", pluginName)
		safe := sanitizeError(err)
		p.State = failedState(p.State, StatusUpdateFailed, errors.New(safe), nil, start)
		st.Profiles[idx] = p
		_ = s.writeProfiles(st)
		return finishTask(task, TaskFailed, safe, false), errors.New(safe)
	}
	installed := pluginpkg.InstalledPlugin{
		Name:          pluginName,
		Source:        "skill-share:" + p.ID + ":" + sanitizeURLForStorage(p.Git.URL),
		Root:          pluginpkg.RelativeRoot(s.Home, activeRoot),
		Version:       pkg.Manifest.Version,
		Description:   pkg.Manifest.Description,
		ManifestKind:  pkg.ManifestKind,
		Enabled:       true,
		LastCheckedAt: formatTime(start),
		LastUpdatedAt: formatTime(start),
	}
	if err := pluginpkg.Upsert(s.Home, installed); err != nil {
		safe := sanitizeError(err)
		p.State = failedState(p.State, StatusUpdateFailed, errors.New(safe), nil, start)
		st.Profiles[idx] = p
		_ = s.writeProfiles(st)
		return finishTask(task, TaskFailed, safe, true), errors.New(safe)
	}

	p.PluginName = pluginName
	p.State.Status = StatusReady
	p.State.CurrentRevision = revision
	p.State.LastCheckedAt = formatTime(start)
	p.State.LastUpdatedAt = formatTime(start)
	p.State.LastError = ""
	p.ManifestKind = pkg.ManifestKind
	p.Version = pkg.Manifest.Version
	p.Skills, p.Hooks, p.MCPServers = pkg.CapabilityCounts()
	st.Profiles[idx] = p
	if err := s.writeProfiles(st); err != nil {
		safe := sanitizeError(err)
		return finishTask(task.withPhase("write_profiles"), TaskFailed, safe, true), errors.New(safe)
	}

	task.CurrentRevision = revision
	return finishTask(task.withPhase("ready"), TaskSucceeded, "", false), nil
}

func (s *Service) Delete(ctx context.Context, id string, opts DeleteOptions) (ProfileView, error) {
	if err := ctx.Err(); err != nil {
		return ProfileView{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ProfileView{}, errors.New("profile id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.readProfiles()
	if err != nil {
		return ProfileView{}, err
	}
	idx := profileIndex(st.Profiles, id)
	if idx < 0 {
		_, _, _ = pluginpkg.Remove(s.Home, id)
		return ProfileView{ID: id, Enabled: false, AuthStatus: "none", State: ProfileState{Status: StatusDisabled}}, nil
	}

	p := st.Profiles[idx]
	if !s.Remote {
		for _, name := range removeNames(p) {
			if _, _, err := pluginpkg.Remove(s.Home, name); err != nil {
				safe := sanitizeError(err)
				p.Enabled = false
				p.State.Status = StatusRemovePending
				p.State.LastError = safe
				st.Profiles[idx] = p
				_ = s.writeProfiles(st)
				return s.profileView(p), errors.New(safe)
			}
		}
	}

	if err := os.RemoveAll(s.profileDir(id)); err != nil {
		safe := sanitizeError(err)
		p.Enabled = false
		p.State.Status = StatusRemovePending
		p.State.LastError = safe
		st.Profiles[idx] = p
		_ = s.writeProfiles(st)
		return s.profileView(p), errors.New(safe)
	}

	p.Enabled = false
	p.State.Status = StatusDisabled
	p.State.LastError = ""
	st.Profiles = append(st.Profiles[:idx], st.Profiles[idx+1:]...)
	if err := s.writeProfiles(st); err != nil {
		return s.profileView(p), err
	}
	return s.profileView(p), nil
}

func (s *Service) Recover(ctx context.Context) ([]TaskView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Remote {
		return nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	root := filepath.Join(s.rootDir(), "profiles")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var tasks []TaskView
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		profileID := entry.Name()
		profileDir := filepath.Join(root, profileID)
		children, err := os.ReadDir(profileDir)
		if err != nil {
			return tasks, err
		}
		for _, child := range children {
			if !child.IsDir() || !strings.HasPrefix(child.Name(), "staging-") {
				continue
			}
			start := now()
			task := TaskView{
				TaskID:     strings.TrimPrefix(child.Name(), "staging-"),
				ProfileID:  profileID,
				Phase:      "cleanup_staging",
				Status:     TaskSucceeded,
				StartedAt:  formatTime(start),
				FinishedAt: formatTime(now()),
			}
			if err := os.RemoveAll(filepath.Join(profileDir, child.Name())); err != nil {
				task.Status = TaskFailed
				task.Error = sanitizeError(err)
				task.Retryable = true
				tasks = append(tasks, task)
				return tasks, err
			}
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}

func (s *Service) rootDir() string {
	namespace := strings.TrimSpace(s.Namespace)
	if namespace == "" {
		namespace = NamespaceSkillShare
	}
	return filepath.Join(s.Home, "addons", namespace)
}

func (s *Service) profilesPath() string {
	return filepath.Join(s.rootDir(), "profiles.json")
}

func (s *Service) profileDir(id string) string {
	return filepath.Join(s.rootDir(), "profiles", id)
}

func (s *Service) readProfiles() (profileFile, error) {
	data, err := os.ReadFile(s.profilesPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return profileFile{Version: 1}, nil
		}
		return profileFile{}, err
	}
	var st profileFile
	if err := json.Unmarshal(data, &st); err != nil {
		return profileFile{}, err
	}
	if st.Version == 0 {
		st.Version = 1
	}
	sortProfiles(st.Profiles)
	return st, nil
}

func (s *Service) writeProfiles(st profileFile) error {
	if st.Version == 0 {
		st.Version = 1
	}
	sortProfiles(st.Profiles)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fileutil.AtomicWriteFile(s.profilesPath(), data, 0o644)
}

func (s *Service) profileView(p profileRecord) ProfileView {
	view := ProfileView{
		ID:           p.ID,
		Enabled:      p.Enabled,
		DisplayName:  p.DisplayName,
		GitURL:       p.Git.URL,
		Branch:       p.Git.Branch,
		Path:         p.Git.Path,
		Username:     p.Git.Auth.Username,
		SecretRef:    p.Git.Auth.SecretRef,
		AuthStatus:   authStatus(p),
		PluginName:   p.PluginName,
		Update:       p.Update,
		State:        p.State,
		ManifestKind: p.ManifestKind,
		Version:      p.Version,
		Skills:       p.Skills,
		Hooks:        p.Hooks,
		MCPServers:   p.MCPServers,
	}
	if !s.Remote {
		root, err := joinInside(filepath.Join(s.profileDir(p.ID), "active"), p.Git.Path)
		if err == nil {
			if pkg, _, err := pluginpkg.ParseDir(root); err == nil {
				view.ManifestKind = pkg.ManifestKind
				view.Version = pkg.Manifest.Version
				view.Skills, view.Hooks, view.MCPServers = pkg.CapabilityCounts()
			}
		}
	}
	return view
}

func (s *Service) resolveCredential(p profileRecord) (gitCredential, error) {
	cred := gitCredential{Username: strings.TrimSpace(p.Git.Auth.Username)}
	ref := strings.TrimSpace(p.Git.Auth.SecretRef)
	if ref == "" {
		return cred, nil
	}

	key := strings.TrimSpace(ref)
	envOnly := false
	if after, ok := strings.CutPrefix(key, "env:"); ok {
		key = strings.TrimSpace(after)
		envOnly = true
	}
	if key == "" {
		return cred, errors.New("secretRef is empty")
	}

	var res config.CredentialResolution
	if envOnly {
		res = config.ResolveCredentialForRoot(s.Home, key)
	} else {
		res = config.ResolveCredentialForRootGlobalFirst(s.Home, key)
		if !res.Set {
			res = config.ResolveCredentialForRoot(s.Home, key)
		}
	}
	if !res.Set {
		return cred, fmt.Errorf("credential %q is not set", key)
	}
	cred.Password = res.Value
	return cred, nil
}

func normalizeInput(in ProfileInput) (profileRecord, error) {
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return profileRecord{}, errors.New("profile id is required")
	}
	if !pluginpkg.IsValidName(id) {
		return profileRecord{}, fmt.Errorf("invalid profile id %q", id)
	}
	path, err := cleanRelPath(in.Path)
	if err != nil {
		return profileRecord{}, err
	}
	pluginName := strings.TrimSpace(in.PluginName)
	if pluginName != "" && !pluginpkg.IsValidName(pluginName) {
		return profileRecord{}, fmt.Errorf("invalid plugin name %q", pluginName)
	}
	if in.Update.IntervalSeconds < 0 {
		return profileRecord{}, errors.New("intervalSeconds must be >= 0")
	}
	gitURL, username := normalizeGitURL(strings.TrimSpace(in.GitURL), strings.TrimSpace(in.Username))
	return profileRecord{
		ID:          id,
		Enabled:     in.Enabled,
		DisplayName: strings.TrimSpace(in.DisplayName),
		Git: gitProfile{
			URL:    gitURL,
			Branch: strings.TrimSpace(in.Branch),
			Path:   path,
			Auth: gitAuth{
				Username:  username,
				SecretRef: strings.TrimSpace(in.SecretRef),
			},
		},
		PluginName: pluginName,
		Update:     in.Update,
	}, nil
}

func normalizeGitURL(raw, username string) (string, string) {
	u, err := url.Parse(raw)
	if err != nil || !isHTTPURL(u) || u.User == nil {
		return raw, username
	}
	if username == "" {
		username = u.User.Username()
	}
	u.User = nil
	return u.String(), username
}

func cloneRepo(ctx context.Context, p profileRecord, cred gitCredential, dest string) error {
	args := []string{"clone", "--depth=1"}
	if branch := strings.TrimSpace(p.Git.Branch); branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, p.Git.URL, dest)
	out, err := runGit(ctx, "", args, cred, filepath.Dir(dest))
	if err != nil {
		return fmt.Errorf("git clone: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitRevision(ctx context.Context, repo string) (string, error) {
	out, err := runGit(ctx, repo, []string{"rev-parse", "HEAD"}, gitCredential{}, "")
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func runGit(ctx context.Context, dir string, args []string, cred gitCredential, tempDir string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if cred.Password != "" {
		askPass, cleanup, err := writeAskPass(tempDir)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		username := strings.TrimSpace(cred.Username)
		if username == "" {
			username = "x-access-token"
		}
		cmd.Env = append(cmd.Env,
			"GIT_ASKPASS="+askPass,
			"SSH_ASKPASS="+askPass,
			"SKILLSHARE_GIT_USERNAME="+username,
			"SKILLSHARE_GIT_PASSWORD="+cred.Password,
		)
	}
	return cmd.CombinedOutput()
}

func writeAskPass(dir string) (string, func(), error) {
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", func() {}, err
	}
	ext := ".sh"
	body := "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s\\n' \"$SKILLSHARE_GIT_USERNAME\" ;;\n*) printf '%s\\n' \"$SKILLSHARE_GIT_PASSWORD\" ;;\nesac\n"
	if runtime.GOOS == "windows" {
		ext = ".cmd"
		body = "@echo off\r\necho %1 | findstr /I Username >nul\r\nif not errorlevel 1 (\r\n  echo %SKILLSHARE_GIT_USERNAME%\r\n) else (\r\n  echo %SKILLSHARE_GIT_PASSWORD%\r\n)\r\n"
	}
	f, err := os.CreateTemp(dir, "skillshare-askpass-*"+ext)
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		_ = os.Remove(path)
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", func() {}, err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		_ = os.Remove(path)
		return "", func() {}, err
	}
	return path, func() { _ = os.Remove(path) }, nil
}

func switchActive(staging, active, previous string) error {
	_ = os.RemoveAll(previous)
	activeExists := dirExists(active)
	if activeExists {
		if err := os.Rename(active, previous); err != nil {
			return fmt.Errorf("move active to previous: %w", err)
		}
	}
	if err := os.Rename(staging, active); err != nil {
		if activeExists {
			_ = os.Rename(previous, active)
		}
		return fmt.Errorf("promote staging to active: %w", err)
	}
	return nil
}

func failedState(prev ProfileState, status string, err error, secrets []string, t time.Time) ProfileState {
	prev.Status = status
	prev.LastCheckedAt = formatTime(t)
	prev.LastError = sanitizeError(err, secrets...)
	return prev
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

func sameSource(a, b profileRecord) bool {
	return a.ID == b.ID &&
		a.Git.URL == b.Git.URL &&
		a.Git.Branch == b.Git.Branch &&
		a.Git.Path == b.Git.Path &&
		a.Git.Auth.Username == b.Git.Auth.Username &&
		a.Git.Auth.SecretRef == b.Git.Auth.SecretRef &&
		a.PluginName == b.PluginName
}

func initialStatus(p profileRecord) string {
	if !p.Enabled {
		return StatusDisabled
	}
	if strings.TrimSpace(p.Git.URL) == "" {
		return StatusUnconfigured
	}
	if p.State.CurrentRevision != "" {
		return StatusReady
	}
	return StatusUnconfigured
}

func authStatus(p profileRecord) string {
	if strings.TrimSpace(p.Git.Auth.SecretRef) != "" {
		return "configured"
	}
	if strings.TrimSpace(p.Git.Auth.Username) != "" {
		return "username_only"
	}
	return "anonymous"
}

func profileIndex(profiles []profileRecord, id string) int {
	for i := range profiles {
		if profiles[i].ID == id {
			return i
		}
	}
	return -1
}

func sortProfiles(profiles []profileRecord) {
	sort.SliceStable(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })
}

func removeNames(p profileRecord) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range []string{p.PluginName, p.ID} {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func cleanRelPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		p = "."
	}
	p = filepath.Clean(filepath.FromSlash(p))
	if p == "." {
		return ".", nil
	}
	if filepath.IsAbs(p) || p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q must be relative and stay inside the repository", p)
	}
	return filepath.ToSlash(p), nil
}

func joinInside(root, rel string) (string, error) {
	clean, err := cleanRelPath(rel)
	if err != nil {
		return "", err
	}
	root = filepath.Clean(root)
	full := filepath.Join(root, filepath.FromSlash(clean))
	back, err := filepath.Rel(root, full)
	if err != nil {
		return "", err
	}
	if back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q must stay inside %s", rel, root)
	}
	return full, nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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

var (
	urlUserInfoPattern = regexp.MustCompile(`(?i)(https?://)[^/\s@]+@`)
	querySecretPattern = regexp.MustCompile(`(?i)(password|passwd|token|access_token|secret)=([^&\s]+)`)
)

func sanitizeError(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	text = urlUserInfoPattern.ReplaceAllString(text, `${1}<redacted>@`)
	text = querySecretPattern.ReplaceAllString(text, `${1}=<redacted>`)
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
	if err != nil || !isHTTPURL(u) {
		return raw
	}
	u.User = nil
	q := u.Query()
	for _, key := range []string{"password", "passwd", "token", "access_token", "secret"} {
		if q.Has(key) {
			q.Set(key, "<redacted>")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func isHTTPURL(u *url.URL) bool {
	return u.Scheme == "http" || u.Scheme == "https"
}

// ── addon.skills interface ──────────────────────────────────────────────────

// SkillsList returns skill metadata for every skill found across active
// checkouts.  For local skill-share, this scans the active directory of each
// profile and parses the plugin manifest.  For flow-skill-share (remote), the
// existing Provider() handles this.
func (s *Service) SkillsList() []skill.Skill {
	if s.Remote {
		return nil // handled by Provider()
	}
	profiles, err := s.Profiles()
	if err != nil {
		return nil
	}
	var out []skill.Skill
	seen := map[string]bool{}
	for _, p := range profiles {
		if !p.Enabled {
			continue
		}
		root, err := joinInside(filepath.Join(s.profileDir(p.ID), "active"), p.Path)
		if err != nil {
			continue
		}
		skillDir := filepath.Join(root, "skills")
		entries, readErr := os.ReadDir(skillDir)
		if readErr != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !skill.IsValidName(name) || seen[name] {
				continue
			}
			skillFile := filepath.Join(skillDir, name, "SKILL.md")
			data, readErr := os.ReadFile(skillFile)
			if readErr != nil {
				continue
			}
			sk, ok := skill.ParseMarkdownContent(string(data), skillFile, name, skill.ScopeCustom, true)
			if !ok {
				continue
			}
			sk.Name = name
			sk.Scope = skill.ScopeCustom
			sk.Path = skillFile
			out = append(out, sk)
			seen[name] = true
		}
	}
	return out
}

// SkillsRead returns the full Skill for a given name, or false if not found.
func (s *Service) SkillsRead(name string) (skill.Skill, bool) {
	if s.Remote {
		return skill.Skill{}, false
	}
	profiles, err := s.Profiles()
	if err != nil {
		return skill.Skill{}, false
	}
	for _, p := range profiles {
		if !p.Enabled {
			continue
		}
		root, err := joinInside(filepath.Join(s.profileDir(p.ID), "active"), p.Path)
		if err != nil {
			continue
		}
		skillFile := filepath.Join(root, "skills", name, "SKILL.md")
		data, readErr := os.ReadFile(skillFile)
		if readErr != nil {
			continue
		}
		sk, ok := skill.ParseMarkdownContent(string(data), skillFile, name, skill.ScopeCustom, true)
		if !ok {
			continue
		}
		sk.Name = name
		sk.Scope = skill.ScopeCustom
		sk.Path = skillFile
		return sk, true
	}
	return skill.Skill{}, false
}
