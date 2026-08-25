// Package boot assembles a ready-to-drive control.Controller from configuration:
// it loads config, resolves the model(s), builds the tool registry (built-ins +
// plugins), wires the permission gate, and constructs the executor — optionally
// wrapping it in a two-model Coordinator. It is the one place that turns "what the
// user configured" into "a Controller a frontend can drive", so every frontend —
// the terminal TUI, the HTTP/SSE server, the desktop webview — shares the exact
// same assembly instead of each re-deriving it. Frontends pass only a sink and a
// couple of run knobs; everything else comes from config.
package boot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"workground2/internal/agent"
	browserpkg "workground2/internal/browser"
	"workground2/internal/browser/cdp"
	"workground2/internal/command"
	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/dshcompat"
	"workground2/internal/environment"
	"workground2/internal/event"
	"workground2/internal/guardian"
	"workground2/internal/history"
	"workground2/internal/hook"
	"workground2/internal/installsource"
	"workground2/internal/instruction"
	"workground2/internal/jobs"
	"workground2/internal/lsp"
	"workground2/internal/memory"
	"workground2/internal/memorycompiler"
	"workground2/internal/migration"
	"workground2/internal/netclient"
	"workground2/internal/outputstyle"
	"workground2/internal/permission"
	"workground2/internal/planmode"
	"workground2/internal/plugin"
	"workground2/internal/provider"
	"workground2/internal/provider/openai"
	"workground2/internal/sandbox"
	"workground2/internal/skill"
	"workground2/internal/skillshare"
	"workground2/internal/tool"
	browsertool "workground2/internal/tool/browser"
	"workground2/internal/tool/builtin"
	"workground2/internal/tool/sessiontool"
	"workground2/internal/vocabulary"
	"workground2/internal/work"
)

// ProductName is the user-visible product name used throughout the UI,
// system prompts, and documentation. It is the single source of truth for
// the product brand.
const ProductName = "WorkGround2"

var browserRuntimeTools = map[string]bool{
	"browser_open":     true,
	"browser_attach":   true,
	"browser_navigate": true,
	"browser_state":    true,
	"browser_click":    true,
	"browser_type":     true,
	"browser_scroll":   true,
	"browser_tab":      true,
	"browser_upload":   true,
	"browser_close":    true,
}

type browserCloseAll interface {
	Close() error
}

type browserSessionCloser interface {
	CloseSession(context.Context, string) (browserpkg.CloseResult, error)
}

// browserLifecycle keeps Build responsible for a newly-created manager until a
// Controller has actually been returned. Cleanup retries once because a
// Controller invokes its cleanup hook only once, while Manager.Close can expose
// a transient process/profile cleanup failure that is safe to retry.
type browserLifecycle struct {
	closer browserCloseAll
	sink   event.Sink
	mu     sync.Mutex
	closed bool
	owned  bool
}

func newBrowserLifecycle(closer browserCloseAll, sink event.Sink) *browserLifecycle {
	return &browserLifecycle{closer: closer, sink: sink, owned: true}
}

func (l *browserLifecycle) transfer() {
	if l != nil {
		l.owned = false
	}
}

func (l *browserLifecycle) releaseIfOwned() {
	if l != nil && l.owned {
		l.close("browser manager cleanup after Build failure")
	}
}

func (l *browserLifecycle) close(label string) {
	if l == nil || l.closer == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	var err error
	for range 2 {
		if err = l.closer.Close(); err == nil {
			l.closed = true
			return
		}
	}
	slog.Warn(label, "err", err)
	if l.sink != nil {
		l.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: label + ": " + err.Error()})
	}
}

func browserTaskCleanup(closer browserSessionCloser, owner string, sink event.Sink) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			if closer == nil || strings.TrimSpace(owner) == "" {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := closer.CloseSession(ctx, owner); err != nil {
				slog.Warn("browser task session cleanup failed", "owner", owner, "err", err)
				if sink != nil {
					sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "browser task session cleanup failed: " + err.Error()})
				}
			}
		})
	}
}

// WorkTaskSystemPrompt is the system prompt injected into every Work V2 task
// session. It defines the role as a business delivery executor — distinct from
// the coding-agent prompt used for normal conversational sessions.
const WorkTaskSystemPrompt = `You are a work delivery executor. Your job is to produce concrete, verifiable
deliverables for a single work node. You are NOT a coding assistant, NOT a
conversational agent.

Core rules:
- Use every required capability and tool for this node. Never answer from "common
  knowledge" when a tool hint (web_search, image_generation, etc.) mandates
  active search or generation. If you cannot use a required capability, return a
  clear failure with the reason — never substitute a plausible-sounding answer.
- Before finishing, check each acceptance criterion listed in the task prompt.
  Address every criterion explicitly. If any criterion cannot be met, state which
  one and why — do not silently skip it.
- When web_search is required, every factual claim must be backed by a cited,
  accessible source URL. Include at least the URLs in your final response. A
  response without URLs when search was required is a delivery failure.
- Produce every declared artifact slot output. Text slots require the complete
  deliverable in your final response — not a summary, not a claim that a file
  was created somewhere else. Structured slots must be produced by the
  appropriate tool.
- If upstream results are provided in the task prompt, use them as authoritative
  input. Do not re-derive, re-search, or second-guess upstream output unless the
  task explicitly asks you to validate it.
- When you cannot deliver (missing capability, insufficient input, contradictory
  requirements), signal failure clearly: state what is missing, why it blocks
  delivery, and whether retrying could help. Do not produce partial or
  fabricated output and claim success.
- Your final response is the delivery record. The host runs a mandatory quality
  pass against the acceptance criteria and verifies objective evidence such as
  successful capability calls and declared artifact outputs. Missing objective
  evidence returns the node to failed_retryable state.`

func workTaskSystemPrompt(hostPrompt string) string {
	hostPrompt = strings.TrimSpace(hostPrompt)
	hostPrompt = strings.TrimSpace(strings.Replace(hostPrompt, config.DefaultSystemPrompt, "", 1))
	if hostPrompt == "" {
		return WorkTaskSystemPrompt
	}
	return WorkTaskSystemPrompt + "\n\n# Workspace policies and context\n" + hostPrompt
}

// AssistantSystemPrompt is the stable system prompt used by Assistant execution
// sessions. It defines a long-running outcome executor — distinct from the
// coding-agent prompt used by normal, work, and collaboration sessions.
const AssistantSystemPrompt = `You are a long-running outcome executor driving one Run of a persistent Assistant.
You are NOT a coding assistant and NOT a conversational agent; you execute a
frozen mission and the current routine, then leave verifiable evidence.

Core rules:
- Use every required capability and tool for this run. Never answer from "common
  knowledge" when a required capability mandates active use of the matching
  tool. If a required capability is blocked or unavailable, say so explicitly:
  state the capability, why it is blocked, and do not fabricate a substitute.
- Never silently replace requested live website inspection with local cache,
  archive, or memory. If the task requires live web (live_web), you must obtain
  a successful result from an appropriate live web/browser tool (for example
  browser open/navigate/state or web fetch/search). A tool dispatch alone or a
  failed result does not satisfy it.
- Leave verifiable evidence: cite what you did, which tool produced which result,
  and the concrete outcome. The host validates required-capability evidence
  against your successful tool results before recording the run as successful.
- Preserve the frozen mission and policy. Do not modify the assistant's running
  frequency, scope, or permissions; do not grant yourself new capabilities.
- When you cannot deliver (missing capability, insufficient input, contradictory
  requirements), signal failure clearly: what is missing, why it blocks, and
  whether retrying could help. Do not claim success without objective evidence.
- Your final response is the delivery record. Missing objective evidence returns
  the run to a recoverable, observable failure state.`

func assistantSystemPrompt(hostPrompt string) string {
	hostPrompt = strings.TrimSpace(hostPrompt)
	hostPrompt = strings.TrimSpace(strings.Replace(hostPrompt, config.DefaultSystemPrompt, "", 1))
	if hostPrompt == "" {
		return AssistantSystemPrompt
	}
	return AssistantSystemPrompt + "\n\n# Workspace policies and context\n" + hostPrompt
}

// ErrUnknownModel is returned by Build when the configured model can't be
// resolved to a provider — e.g. a default_model left over from a renamed or
// removed provider. Callers can detect it (errors.Is) to re-run setup.
var ErrUnknownModel = errors.New("unknown model")

func agentKeepPolicy(keep []string) agent.KeepPolicy {
	if keep == nil {
		return agent.KeepErrors
	}
	var p agent.KeepPolicy
	for _, k := range keep {
		switch strings.TrimSpace(k) {
		case "errors":
			p |= agent.KeepErrors
		case "user_marked":
			p |= agent.KeepUserMarked
		}
	}
	return p
}

func probeCLICapabilities(ctx context.Context, cfg *config.Config, stderr io.Writer) {
	if cfg == nil {
		return
	}
	for i := range cfg.Providers {
		entry := &cfg.Providers[i]
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		capabilities, err := config.ProbeCLICapabilities(probeCtx, entry)
		cancel()
		if err != nil {
			fmt.Fprintf(stderr, "warning: %s capability probe failed: %v\n", entry.Name, err)
			continue
		}
		entry.AddCapabilities(capabilities...)
	}
}

// Options carries the per-run knobs a frontend chooses; everything else is read
// from configuration. Model "" falls back to the configured default_model;
// MaxSteps 0 uses the config/default. RequireKey forces the executor's API key to
// be present (run/serve pass true so a missing key fails fast; chat/desktop pass
// false so the UI is reachable before a key is set). Sink receives the agent's
// typed event stream.
type Options struct {
	Model      string
	MaxSteps   int
	RequireKey bool
	Sink       event.Sink
	// EffortOverride is a session-local reasoning effort override. Nil means use
	// the resolved provider config; a non-nil empty string means provider default.
	EffortOverride *string
	// Stderr is the writer for diagnostic warnings and plugin subprocess
	// stderr output. When nil, defaults to os.Stderr. Set to io.Discard
	// during model switch inside a bubbletea session to prevent any output
	// from corrupting the TUI's terminal raw mode.
	Stderr io.Writer
	// WorkspaceRoot is the project root directory for config, skills, memory,
	// commands, hooks, and tool confinement. When empty, the current working
	// directory is used (CLI default). Desktop tabs pass their project root here
	// so each tab loads its own config/skills/hooks without changing the process
	// cwd — enabling concurrent multi-project sessions.
	WorkspaceRoot string
	// WorkDir optionally overrides the per-project Work data directory for hosts
	// and tests. It is ignored while work.enabled is false. When enabled, a
	// non-empty path must be writable or Build fails explicitly.
	WorkDir string
	// SessionRefs is a process-wide reverse index shared by every Desktop tab.
	// Nil keeps non-Desktop hosts independent from Desktop session cleanup.
	SessionRefs work.SessionRefStore
	// CornerstoneResolver revalidates live Work cornerstone sources before each Run.
	CornerstoneResolver work.CornerstoneResolver
	// ArtifactSourceResolver resolves binary V2 preview/conversion sources. Nil
	// uses the production WorkStore+BlobStore+workspace boundary.
	ArtifactSourceResolver work.ArtifactSourceResolver
	// PreviewApprovalVerifier authorises external converters. Nil is local-only
	// and rejects every external conversion.
	PreviewApprovalVerifier work.ApprovalVerifier
	// WorkTaskExecutor optionally replaces the Controller-backed adapter. Tests
	// and embedding hosts use it to make boot recovery deterministic.
	WorkTaskExecutor work.TaskExecutor
	// SessionRefsErr carries host initialization failure into Work boot so the
	// feature cannot run while its cleanup guard is unavailable.
	SessionRefsErr error
	// ExtraPlugins are session-scoped MCP servers supplied by a host transport
	// (for example ACP session/new). They are connected eagerly for this
	// controller but are not persisted to WorkGround2.toml.
	ExtraPlugins []plugin.Spec
	// TokenMode selects how much optional context/tool surface this session exposes
	// at boot. Empty/full preserves the normal capability surface. "economy" keeps
	// the core coding tools visible and moves skills, MCP, LSP, web_fetch,
	// install_source, and task behind connect_tool_source.
	TokenMode string
	// SessionDir overrides where persisted chat transcripts are written. When
	// empty, the shared CLI/global session directory is used.
	SessionDir string
	// SharedHost is an optional plugin.Host shared across controllers for the
	// same workspace root. When set, boot.Build reuses its running clients
	// instead of creating new subprocesses, and the caller manages the host's
	// lifecycle. When nil, Build creates and owns a new host as before.
	SharedHost *plugin.Host
	// CleanupPendingReconciler retries delayed physical cleanup for session
	// artifacts left by a previous process. Nil uses the core physical-delete
	// reconciler; frontends with different deletion semantics can override it.
	CleanupPendingReconciler func(sessionDir string) error
	// ApprovalTimeout bounds how long a tool-approval or ask prompt blocks for a
	// user decision. Zero (default) waits forever — correct for an interactive
	// terminal. Headless/bot frontends pass a positive value so an unanswered
	// prompt can't wedge the session indefinitely (#4626, #4402).
	ApprovalTimeout time.Duration
	// SessionRecoveryMeta and OnSessionRecovered let richer frontends attach
	// local UI metadata to automatic transcript recovery branches.
	SessionRecoveryMeta func(control.SessionRecoveryRequest) agent.BranchMeta
	OnSessionRecovered  func(control.SessionRecoveryInfo) error
	// FileOverlay and TerminalRunner are optional host integrations used by ACP
	// for unsaved editor buffers and foreground client terminals.
	FileOverlay    builtin.FileOverlay
	TerminalRunner builtin.TerminalRunner
	// SessionKind selects the stable system prompt for this session. Empty or
	// agent.SessionKindNormal keeps the coding-agent prompt; assistant uses the
	// long-running outcome executor prompt. Work and collaboration retain their
	// existing behavior.
	SessionKind agent.SessionKind
}

// Build loads config, resolves the model(s), and returns a Controller wrapping a
// single Agent, or a two-model Coordinator when agent.planner_model is set. The
// returned controller owns plugin subprocesses; call Close (via Controller.Close)
// to release them.
func Build(ctx context.Context, opts Options) (*control.Controller, error) {
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	root := resolveWorkspaceRoot(opts.WorkspaceRoot)
	// One-time import of v1/v0.5 legacy config — runs before Load so the freshly
	// written config + ~/.env are picked up this same boot. CLI Run also calls this
	// before config-only commands; this call stays as the shared frontend fallback.
	migrated, migErr := config.MigrateLegacyIfNeededForRoot(root)
	cfg, err := config.LoadForRoot(root)
	if err != nil {
		return nil, err
	}
	probeCLICapabilities(ctx, cfg, stderr)
	modelName := opts.Model
	if modelName == "" {
		modelName = cfg.DefaultModel
	}
	config.NormalizeLegacyMimoCustomProvidersForRefs(cfg, modelName)
	tokenMode := NormalizeTokenMode(opts.TokenMode)
	tokenEconomy := tokenMode == TokenModeEconomy
	keepPolicy := agentKeepPolicy(cfg.Agent.Keep)
	entry, ok := cfg.ResolveModel(modelName)
	if !ok {
		return nil, fmt.Errorf("%w %q (configured: %s); note: defining [[providers]] replaces the built-in presets, so add a [[providers]] entry for it or use a configured name, or run `WorkGround2 setup` to reconfigure", ErrUnknownModel, modelName, providerNames(cfg))
	}
	// Anchored bootstrap applies to DeepSeek-family providers only, using the
	// same rule the openai provider uses for its deepseek protocol detection
	// (openai.go: protocol == "deepseek", or an unset protocol against a
	// deepseek base URL). Config defaults to enabled; opt out per install.
	useAnchoredBootstrap := cfg.AnchoredBootstrapEnabled() && isDeepSeekProvider(entry)
	modelRef := entry.Name + "/" + entry.Model
	if opts.EffortOverride != nil {
		entry.Effort = *opts.EffortOverride
		if entry.Kind == "anthropic" && strings.TrimSpace(entry.Effort) != "" && strings.TrimSpace(entry.Thinking) == "" {
			entry.Thinking = "adaptive"
		}
	}
	if opts.RequireKey {
		if err := cfg.Validate(modelName); err != nil {
			return nil, err
		}
	}

	// Serialize the frontend's sink once: background jobs (below) emit from their
	// own goroutines, which can overlap a running turn's emission, so every emitter
	// shares this synchronized sink. The job manager is session-scoped — its jobs
	// outlive a turn and are cancelled by Controller.Close.
	sink := event.Sync(opts.Sink)

	if ignored := (planmode.Policy{AllowedTools: cfg.Agent.PlanModeAllowedTools}).IgnoredAllowedTools(); len(ignored) > 0 {
		sink.Emit(event.Event{
			Kind:  event.Notice,
			Level: event.LevelWarn,
			Text:  fmt.Sprintf("plan_mode_allowed_tools ignored known blocked entries: %s; this setting only declares extra read-only custom tools and cannot unlock known blocked tools or unsafe bash", strings.Join(ignored, ", ")),
		})
	}
	for _, warning := range cfg.BrowserConfigWarnings() {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: warning})
	}
	if migErr != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "config migration from ~/.WorkGround2 failed: " + migErr.Error()})
	} else if migrated != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: migrated.Notice()})
	}
	migration.MigrateLegacyMemorySources(sink)
	migration.MigrateLegacySessionSources(sink)

	// A resolvable model whose API key env is unset would otherwise build fine
	// (RequireKey is false so the UI stays reachable) and then fail silently on the
	// first request, showing as an empty/dead model. Surface the cause up front.
	if !opts.RequireKey && entry.RequiresAPIKey() && entry.APIKey() == "" {
		sink.Emit(event.Event{Kind: event.Notice, Text: fmt.Sprintf("model %q is selected but its API key %s is not set — requests will fail until you set it", modelName, entry.APIKeyEnv)})
	}
	jm := jobs.NewManager(sink, jobs.WithStalledWarningAfter(time.Duration(cfg.BackgroundJobStalledWarningSeconds())*time.Second))
	sessionDir := opts.SessionDir
	if sessionDir == "" {
		sessionDir = config.SessionDir()
	}
	reconcileCleanupPending := opts.CleanupPendingReconciler
	if reconcileCleanupPending == nil {
		reconcileCleanupPending = control.ReconcileCleanupPending
	}
	if err := reconcileCleanupPending(sessionDir); err != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "cleanup-pending reconciliation failed: " + err.Error()})
	}

	proxySpec := cfg.NetworkProxySpec()
	if err := netclient.Validate(proxySpec); err != nil {
		return nil, err
	}
	balanceClient, err := netclient.NewHTTPClient(proxySpec, netclient.TransportOptions{})
	if err != nil {
		return nil, err
	}

	execProv, err := NewProviderWithProxy(entry, proxySpec)
	if err != nil {
		return nil, err
	}
	shell := sandbox.ResolveShell(cfg.Tools.Shell.Prefer, cfg.Tools.Shell.Path, stderr)

	sysPrompt, err := cfg.ResolveSystemPromptForRoot(root)
	if err != nil {
		return nil, err
	}
	// Output style: fold the selected persona/tone block into the base prompt
	// before language/memory/skills append, so a "replace" style (keep-coding
	// false) still keeps those. Applied once, into the cache-stable prefix.
	if st, ok := outputstyle.Resolve(cfg.Agent.OutputStyle, outputstyle.Dirs()); ok {
		sysPrompt = outputstyle.Apply(sysPrompt, st)
	}
	// Assistant sessions swap the coding-agent role for the long-running outcome
	// executor role while keeping any user-supplied policy/context. Normal, work,
	// and collaboration sessions keep the coding-agent prompt unchanged.
	if opts.SessionKind == agent.SessionKindAssistant {
		sysPrompt = assistantSystemPrompt(sysPrompt)
	}
	sysPrompt += "\n\n" + config.UserDecisionPolicy
	sysPrompt += "\n\n" + config.LanguagePolicy
	if tokenEconomy {
		sysPrompt += "\n\n" + tokenEconomyPrompt
	}
	if cfg.EnvironmentEnabled() {
		shellLabel := shell.Kind.String()
		if strings.TrimSpace(cfg.Tools.Shell.Path) != "" {
			shellLabel = shell.Path
		}
		envSection := environment.FormatSection(
			environment.RunProbesWithOptions(ctx, environment.DefaultProbes(), environment.ProbeOptions{
				Overrides: cfg.Environment.Tools,
				DenyRoots: []string{root},
			}),
			runtime.GOOS+"/"+runtime.GOARCH,
			shellLabel,
			cfg.Environment.Tools,
		)
		if envSection != "" {
			sysPrompt += "\n\n" + envSection
		}
	}

	// Two-phase DeepSeek bootstrap: snapshot the prompt assembled so far
	// (base + style + policies + environment) BEFORE the memory/skills
	// injection appends below. The snapshot is a strict prefix of the full
	// prompt, so the provider's prefix cache keeps these leading tokens warm
	// when promotion restores the injected sections. The agent swaps it into
	// the first request only; the durable session log always keeps the full
	// prompt.
	bootstrapPrompt := ""
	if useAnchoredBootstrap {
		bootstrapPrompt = sysPrompt
	}

	// Persistent memory (WorkGround2.md / AGENTS.md hierarchy + auto-memory index)
	// folds into the system prompt exactly here, once: it becomes part of the
	// durable, cache-stable prefix every turn reuses, so memory costs nothing per
	// turn. Mid-session changes never touch this prefix — they ride the
	// controller's transient turn-injection and fold in on the next session.
	mem := memory.Load(memory.Options{CWD: root, UserDir: config.MemoryUserDir()})
	projectChecks := instruction.ExtractHostChecks(mem.Docs)
	sysPrompt = memory.Compose(sysPrompt, mem)

	// Skills: discover playbooks (built-in + project/custom/global) and fold their
	// one-liner index into the same cache-stable prefix — names + descriptions
	// only; bodies load on demand via run_skill or "/<name>". Bodies never enter
	// the prefix, so the index costs a fixed, small amount per turn.
	remoteSkillProviders := []skill.Provider{skillshare.NewFlow(config.WorkGround2HomeDir()).Provider()}
	skillStore := skill.New(skill.Options{
		ProjectRoot:   root,
		CustomPaths:   cfg.SkillCustomPaths(),
		ExcludedPaths: cfg.SkillExcludedPaths(),
		DisabledNames: cfg.DisabledSkillNames(),
		Providers:     remoteSkillProviders,
		MaxDepth:      cfg.SkillMaxDepth(),
		Stderr:        opts.Stderr,
	})
	skills := skillStore.List()
	allSkillStore := skill.New(skill.Options{ProjectRoot: root, CustomPaths: cfg.SkillCustomPaths(), ExcludedPaths: cfg.SkillExcludedPaths(), Providers: remoteSkillProviders, MaxDepth: cfg.SkillMaxDepth(), Stderr: io.Discard})
	allSkills := allSkillStore.List()
	vocabSkills := make([]vocabulary.SkillSource, 0, len(skills))
	for _, sk := range skills {
		if sk.Protected {
			continue
		}
		vocabSkills = append(vocabSkills, vocabulary.SkillSource{Name: sk.Name, Path: sk.Path, Terms: sk.Vocabulary})
	}
	vocabAgents := make([]vocabulary.AgentSource, 0, len(mem.Docs))
	for _, doc := range mem.Docs {
		if base := strings.ToLower(filepath.Base(doc.Path)); strings.HasPrefix(base, "agents") {
			vocabAgents = append(vocabAgents, vocabulary.AgentSource{Name: filepath.Base(doc.Path), Path: doc.Path, Body: doc.Body})
		}
	}
	vocab := vocabulary.New(vocabulary.Options{
		WorkspaceRoot: root,
		StateDir:      config.ProjectVocabularyDir(root),
		Skills:        vocabSkills,
		Agents:        vocabAgents,
	})
	for _, warning := range vocab.Warnings() {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "vocabulary: " + warning})
	}
	if !tokenEconomy {
		sysPrompt = skill.ApplyIndex(sysPrompt, skills)
	}

	reg := tool.NewRegistry()
	reg.Add(vocabulary.NewRebuildTool(vocab))
	bashSpec := sandbox.Spec{Mode: cfg.BashMode(), WriteRoots: cfg.WriteRootsForRoot(root), ForbidReadRoots: cfg.ForbidReadRootsForRoot(root), Network: cfg.Sandbox.Network}
	bashSpec.Shell = shell
	if bashSpec.Mode == "enforce" && !sandbox.Available() {
		fmt.Fprintln(stderr, "warning: bash sandbox requested but unavailable on this platform; running bash unconfined")
	}
	if autoShellPrefer(cfg.Tools.Shell.Prefer) && shell.Kind == sandbox.ShellPowerShell {
		fmt.Fprintln(stderr, "warning: bash not found on PATH; the shell tool will run commands under Windows PowerShell. Install Git for Windows or WSL to use bash, or set [tools.shell] prefer=\"powershell\" to silence this.")
	}
	searchSpec := builtin.ResolveSearch(cfg.Tools.Search.Engine, cfg.Tools.Search.RgPath, stderr)
	bashTimeout := time.Duration(cfg.BashTimeoutSeconds()) * time.Second
	enabledBuiltins := cfg.Tools.Enabled
	if tokenEconomy {
		enabledBuiltins = tokenEconomyBuiltins(enabledBuiltins)
	}
	readPathResolver := builtin.NewPathResolver()
	addBuiltins(reg, enabledBuiltins, cfg.WriteRootsForRoot(root), bashSpec, bashTimeout, searchSpec, stderr, root, proxySpec, cfg.ForbidReadRootsForRoot(root), readPathResolver, opts.FileOverlay, opts.TerminalRunner, config.EffectiveVision(entry))

	// DSH Bundles run out-of-process, but remain session-owned: each Controller
	// gets one Cordis Agent per enabled Bundle and closes it with the session.
	dshSpecs, dshWarnings := dshcompat.Discover(config.WorkGround2HomeDir(), root, stderr)
	for _, warning := range dshWarnings {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: warning})
	}
	var dshClients []*dshcompat.Client
	dshTransferred := false
	defer func() {
		if dshTransferred {
			return
		}
		for i := len(dshClients) - 1; i >= 0; i-- {
			_ = dshClients[i].Close()
		}
	}()
	for _, spec := range dshSpecs {
		startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		client, startErr := dshcompat.Start(startCtx, spec)
		cancel()
		if startErr != nil {
			sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: fmt.Sprintf("dsh %s: %v", spec.Name, startErr)})
			continue
		}
		dshClients = append(dshClients, client)
		for _, dshTool := range client.Tools() {
			reg.Add(dshTool)
		}
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: fmt.Sprintf("dsh %s: %d tools bridged", spec.Name, len(client.Tools()))})
	}
	// Use the caller-supplied shared host when set, so controllers for the same
	// workspace root reuse running MCP processes (e.g. one CodeGraph daemon
	// instead of one per tab). Otherwise construct a private host per controller.
	pluginHost := opts.SharedHost
	if pluginHost == nil {
		pluginHost = plugin.NewHost()
	}

	// Partition configured plugins by tier so eager can block when explicitly
	// requested while every other enabled MCP warms up in the background.
	pluginSpecOptions := PluginSpecOptions{
		DefaultCallTimeout:   time.Duration(cfg.MCPCallTimeoutSeconds()) * time.Second,
		PlanModeAllowedTools: cfg.Agent.PlanModeAllowedTools,
	}
	autoStartEntries := cfg.AutoStartPlugins()
	eagerEntries, bgEntries := partitionByTier(autoStartEntries)
	extraSpecs := applyDefaultMCPCallTimeout(
		applyPlanModeAllowedMCPToolTrust(applyKnownPluginOverrides(opts.ExtraPlugins, root), cfg.Agent.PlanModeAllowedTools),
		pluginSpecOptions.DefaultCallTimeout,
	)
	onDemandMCPSpecs := map[string]plugin.Spec{}
	onDemandMCPNames := []string{}
	if tokenEconomy {
		for _, spec := range append(PluginSpecsForRootWithOptions(autoStartEntries, root, pluginSpecOptions), extraSpecs...) {
			name := strings.TrimSpace(spec.Name)
			if name == "" {
				continue
			}
			if _, exists := onDemandMCPSpecs[name]; !exists {
				onDemandMCPNames = append(onDemandMCPNames, name)
			}
			onDemandMCPSpecs[name] = spec
		}
		eagerEntries, bgEntries = nil, nil
	}
	trustedMCPServers := planModeTrustedMCPServers(onDemandMCPSpecs)

	// Auto-demote: any eager plugin that has been chronically slow (recent
	// samples repeatedly hit the blocking startup budget) drops to background
	// for this session. The user keeps eager intent, just doesn't pay for it
	// on a server that's been misbehaving. A notice surfaces the demotion.
	var demoteMessages []string
	budget := plugin.DefaultStartupBudget()
	kept := eagerEntries[:0]
	for _, e := range eagerEntries {
		rec := plugin.Recommend(e.Name, budget, 0)
		if rec.Demote {
			demoteMessages = append(demoteMessages, rec.Reason)
			bgEntries = append(bgEntries, e)
			continue
		}
		kept = append(kept, e)
	}
	eagerEntries = kept

	eagerSpecs := PluginSpecsForRootWithOptions(eagerEntries, root, pluginSpecOptions)
	bgSpecs := PluginSpecsForRootWithOptions(bgEntries, root, pluginSpecOptions)

	if !tokenEconomy {
		eagerSpecs = append(eagerSpecs, extraSpecs...)
	}

	// Apply caller-supplied stderr override to every spec across tiers.
	if opts.Stderr != nil {
		for i := range eagerSpecs {
			eagerSpecs[i].Stderr = opts.Stderr
		}
		for i := range bgSpecs {
			bgSpecs[i].Stderr = opts.Stderr
		}
	}

	// Eager: block until handshake. Failures show up in /mcp.
	if len(eagerSpecs) > 0 {
		// When using a shared host, reuse already-connected clients and
		// add new ones directly to the host instead of creating a separate one.
		if opts.SharedHost != nil {
			for _, s := range eagerSpecs {
				if pluginHost.HasClient(s.Name) {
					tools, err := pluginHost.ToolsFor(ctx, s.Name)
					if err == nil {
						for _, t := range tools {
							reg.Add(t)
						}
						continue
					}
				}
				// Use a bounded per-plugin timeout matching StartAvailable's
				// defaultStartTimeout (5s) so a hanging MCP server doesn't
				// block the tab boot indefinitely.
				addCtx, addCancel := context.WithTimeout(ctx, 5*time.Second)
				tools, err := pluginHost.Add(addCtx, s)
				addCancel()
				if err != nil {
					if plugin.IsServerAlreadyConnected(err) || errors.Is(err, plugin.ErrSpawningInFlight) {
						// Race: another tab connected the same server between
						// HasClient and Add, or is currently spawning it.
						// Fetch tools from the existing client, or wait briefly.
						tools, err2 := pluginHost.ToolsFor(ctx, s.Name)
						if err2 == nil {
							for _, t := range tools {
								reg.Add(t)
							}
							continue
						}
					}
					sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn,
						Text: fmt.Sprintf("mcp %s: %v", s.Name, err)})
					continue
				}
				for _, t := range tools {
					reg.Add(t)
				}
			}
		} else {
			host, ptools := plugin.StartAvailable(ctx, eagerSpecs)
			pluginHost = host
			for _, t := range ptools {
				reg.Add(t)
			}
			// PhaseB (prompts + resources) runs on the boot ctx — which is the
			// controller's session-scoped PluginCtx — so the auxiliary surfaces
			// keep streaming in after Start returns without holding up the agent.
			go host.StartPhaseB(ctx, sink)
			if text, ok := MCPStartupNotice(host.Failures()); ok {
				sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: text})
			}
		}
	}

	// Background: register placeholder tools now and kick off the real spawn.
	// Everything shares the same pluginHost so /mcp status, hot-add, and Close
	// see one cohesive set of servers.
	registerBackground := func(specs []plugin.Spec) {
		for _, s := range specs {
			// Already running on the shared host? Register tools directly.
			if pluginHost.HasClient(s.Name) {
				tools, err := pluginHost.ToolsFor(ctx, s.Name)
				if err == nil {
					for _, t := range tools {
						reg.Add(t)
					}
					continue
				}
			}
			if opts.SharedHost != nil {
				// Shared host relies on Host's spawn guard to avoid duplicate
				// processes across tabs for the same workspace root.
				cs, _ := plugin.LoadCachedSchema(s.Name, plugin.SpecFingerprint(s))
				for _, t := range plugin.LazyToolset(s, cs, pluginHost, reg, ctx, true) {
					reg.Add(t)
				}
			} else {
				cs, _ := plugin.LoadCachedSchema(s.Name, plugin.SpecFingerprint(s))
				for _, t := range plugin.LazyToolset(s, cs, pluginHost, reg, ctx, true) {
					reg.Add(t)
				}
			}
		}
	}
	registerBackground(bgSpecs)

	for _, msg := range demoteMessages {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: msg})
	}

	cleanup := pluginHost.Close
	if opts.SharedHost != nil {
		// The caller owns the shared host's lifecycle; the controller must not
		// close it. A no-op cleanup keeps Controller.Close happy without
		// shutting down MCP processes that other controllers still use.
		cleanup = func() {}
	}
	if len(dshClients) > 0 {
		previousCleanup := cleanup
		cleanup = func() {
			for i := len(dshClients) - 1; i >= 0; i-- {
				_ = dshClients[i].Close()
			}
			previousCleanup()
		}
	}

	// LSP tools resolve their servers on PATH and spawn lazily on first query, so
	// registering them is cheap even when no server is installed (a query then
	// returns an install hint). The manager is session-scoped; chain its shutdown
	// into the controller's cleanup so servers stop with the session, not the turn.
	var lspMgr *lsp.Manager
	lspToolsAdded := false
	addLSPTools := func() []string {
		if lspMgr == nil || lspToolsAdded {
			return nil
		}
		lspToolsAdded = true
		return addTools(reg, lsp.Tools(lspMgr))
	}
	if cfg.LSP.Enabled {
		lspMgr = lsp.NewManager(root, LSPSpecs(cfg.LSP))
		if !tokenEconomy {
			addLSPTools()
		}
		prev := cleanup
		cleanup = func() { prev(); lspMgr.Close() }
	}

	// Browser tools: manager is created but no browser is launched until
	// the first browser_open call. Browser-absent hosts boot successfully.
	var browserMgr *browserpkg.Manager
	var browserOwner *browserLifecycle
	if cfg.BrowserEnabled() && !tokenEconomy && browserToolsSelected(enabledBuiltins) {
		factory := cdp.NewFactory(cdp.Options{})
		bm, err := browserpkg.NewManager(ctx, browserManagerOptions(cfg, factory))
		if err != nil {
			sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn,
				Text: "browser manager init failed: " + err.Error()})
		} else {
			browserMgr = bm
			browserOwner = newBrowserLifecycle(browserMgr, sink)
			defer browserOwner.releaseIfOwned()
			for _, t := range browsertool.NewTools(browserMgr) {
				if builtinToolEnabled(enabledBuiltins, t.Name()) {
					reg.Add(t)
				}
			}
			prev := cleanup
			cleanup = func() { prev(); browserOwner.close("browser manager cleanup failed") }
		}
	}

	maxSteps := cfg.Agent.MaxSteps
	if opts.MaxSteps > 0 {
		maxSteps = opts.MaxSteps
	}
	subagentStore, err := newSubagentStore(sessionDir)
	if err != nil {
		return nil, err
	}
	if subagentStore != nil {
		subagentStore.WithDestroyedChecker(jm.IsDestroying)
	}

	// Permission policy gates every tool call. The headless gate (no Approver)
	// resolves "ask" to allow — preserving `WorkGround2 run` autonomy — while deny
	// rules hard-block in every mode. Interactive frontends (chat, desktop) swap
	// in an interactive gate later via Controller.EnableInteractiveApproval.
	// Sub-agents always run headless: they have no UI to answer a prompt, so they
	// inherit this same gate.
	// Completion notifications are explicitly requested through notify-me and
	// must still work after the owner has gone AFK. Allow them by default while
	// preserving configured ask/deny precedence.
	permissionAllow := append([]string(nil), cfg.Permissions.Allow...)
	permissionAllow = append(permissionAllow, "notify_me")
	policy := permission.New(cfg.Permissions.Mode, permissionAllow, cfg.Permissions.Ask, cfg.Permissions.Deny)
	headlessGate := permission.NewGate(policy, nil)

	// Hooks: load the global settings.json plus the project's (only when trusted —
	// project hooks run arbitrary shell commands, so cloning a repo must not
	// silently execute them). Non-blocking hook output is surfaced to the user as
	// a Notice through the shared sink. The runner fires PreToolUse/PostToolUse in
	// the agent loop and PermissionRequest/UserPromptSubmit/Stop at the controller
	// boundary.
	hooksTrusted := hook.IsTrusted(root, "")
	hookRunner := hook.NewRunner(
		hook.Load(hook.LoadOptions{ProjectRoot: root, Trusted: hooksTrusted}),
		root, nil,
		func(msg string) { sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: msg}) },
	)
	if hook.ProjectDefinesHooks(root) && !hooksTrusted {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
			Text: "this project defines hooks but they are not trusted — run /hooks trust to enable them"})
	}

	// The `task` tool spawns sub-agents that reuse the parent's provider and
	// tool registry. Wired here after the built-ins / plugins are loaded so
	// sub-agents inherit the full tool set (minus `task` itself, to keep
	// nesting out of the picture). It registers into the same reg the
	// executor uses, so the model surfaces it like any other tool.
	resolveSubagentProvider := func(modelRef, effort string) (provider.Provider, *provider.Pricing, int, error) {
		me := *entry
		if strings.TrimSpace(modelRef) != "" {
			resolved, ok := cfg.ResolveModel(modelRef)
			if !ok {
				return nil, nil, 0, fmt.Errorf("unknown model %q", modelRef)
			}
			me = *resolved
		}
		if strings.TrimSpace(effort) != "" {
			normalized, err := config.NormalizeEffort(&me, effort)
			if err != nil {
				return nil, nil, 0, err
			}
			me.Effort = normalized
			if me.Kind == "anthropic" && strings.TrimSpace(me.Effort) != "" && strings.TrimSpace(me.Thinking) == "" {
				me.Thinking = "adaptive"
			}
		}
		p, err := NewProviderWithProxy(&me, proxySpec)
		if err != nil {
			return nil, nil, 0, err
		}
		return p, me.Price, me.ContextWindow, nil
	}
	subagentIdentity := func(modelRef, effort string) (string, string) {
		return subagentEffectiveIdentity(cfg, modelName, entry, modelRef, effort)
	}
	taskModel := firstNonEmpty(cfg.Agent.SubagentModels["task"], cfg.Agent.SubagentModel)
	taskEffort := firstNonEmpty(cfg.Agent.SubagentEfforts["task"], cfg.Agent.SubagentEffort)
	maxSubagentDepth := agent.NormalizeMaxSubagentDepth(cfg.Agent.MaxSubagentDepth)
	taskToolAdded := false
	readOnlyTaskToolAdded := false
	var taskTool *agent.TaskTool
	newTaskTool := func() *agent.TaskTool {
		return agent.NewTaskTool(execProv, entry.Price, reg, maxSteps,
			entry.ContextWindow, cfg.Agent.RecentKeep, cfg.Agent.SoftCompactRatio, cfg.Agent.ToolResultSnipRatio, cfg.Agent.CompactRatio, cfg.Agent.CompactForceRatio,
			cfg.Agent.Temperature, config.ArchiveDir(), "", headlessGate,
			keepPolicy,
			taskModel, taskEffort, resolveSubagentProvider).
			WithTranscripts(subagentStore, root, modelName, entry.Effort).
			WithTranscriptIdentityResolver(subagentIdentity).
			WithMaxSubagentDepth(maxSubagentDepth)
	}
	addTaskTool := func() string {
		if taskToolAdded {
			return "task tool is already enabled."
		}
		taskToolAdded = true
		if taskTool == nil {
			taskTool = newTaskTool()
		}
		reg.Add(taskTool)
		reg.Add(agent.NewParallelTasksTool(taskTool, reg))
		return "enabled task."
	}
	addReadOnlyTaskTool := func() string {
		if readOnlyTaskToolAdded {
			return "read_only_task tool is already enabled."
		}
		readOnlyTaskToolAdded = true
		if taskTool == nil {
			taskTool = newTaskTool()
		}
		reg.Add(agent.NewReadOnlyTaskTool(taskTool))
		return "enabled read_only_task."
	}
	if !tokenEconomy {
		addTaskTool()
		addReadOnlyTaskTool()
	}

	// request_help lets the primary model delegate to a capability-matched
	// helper when it lacks a requested capability (e.g. web_search).
	// Hidden when assist_mode is "off" or in token-economy mode.
	if cfg.AssistEnabled() && !tokenEconomy {
		requestHelpTool := agent.NewRequestHelpTool(
			cfg, reg, modelRef,
			resolveSubagentProvider,
			maxSteps, cfg.Agent.Temperature, entry.ContextWindow,
			cfg.Agent.RecentKeep,
			cfg.Agent.SoftCompactRatio, cfg.Agent.ToolResultSnipRatio,
			cfg.Agent.CompactRatio, cfg.Agent.CompactForceRatio,
			headlessGate, keepPolicy,
			maxSubagentDepth,
		).WithTranscripts(subagentStore, root, modelName, entry.Effort)
		reg.Add(requestHelpTool)
	}

	// The `memory` tool searches/reads saved facts on demand; `remember` persists
	// durable facts to the project's auto-memory store; `forget` prunes ones that
	// turn out wrong. The saved index loads into the prefix on the next session.
	reg.Add(history.NewTool(history.Options{SessionDir: sessionDir, GlobalSessionDir: config.SessionDir(), ArchiveDir: config.ArchiveDir()}))

	// Session history tools let the AI discover and read past conversations.
	// `list_sessions` returns all saved session files; `read_session` loads one
	// and renders the full conversation as readable text.
	reg.Add(sessiontool.NewListSessionsTool(sessionDir))
	reg.Add(sessiontool.NewReadSessionTool(sessionDir))

	reg.Add(memory.NewRecallTool(mem.Store))
	reg.Add(memory.NewRememberTool(mem.Store))
	reg.Add(memory.NewForgetTool(mem.Store))

	// The `ask` tool puts structured multiple-choice questions to the user. It
	// reaches them through the Asker on the call context, which interactive
	// frontends wire to the controller (EnableInteractiveApproval); a headless run
	// has none, so ask resolves to "decide for yourself".
	reg.Add(agent.NewAskTool())
	if builtinToolEnabled(enabledBuiltins, "notify_me") {
		reg.Add(agent.NewNotifyMeTool())
	}

	childOptions := func(sctx context.Context, steps, childDepth, ctxWin int, price *provider.Pricing) agent.Options {
		return agent.Options{
			MaxSteps:            steps,
			Temperature:         cfg.Agent.Temperature,
			Pricing:             price,
			UsageSource:         event.UsageSourceSubagent,
			Gate:                headlessGate,
			ContextWindow:       ctxWin,
			RecentKeep:          cfg.Agent.RecentKeep,
			SoftCompactRatio:    cfg.Agent.SoftCompactRatio,
			ToolResultSnipRatio: cfg.Agent.ToolResultSnipRatio,
			CompactRatio:        cfg.Agent.CompactRatio,
			CompactForceRatio:   cfg.Agent.CompactForceRatio,
			ArchiveDir:          config.ArchiveDir(),
			KeepPolicy:          keepPolicy,
			ResponseLanguage:    agent.ResponseLanguageFromContext(sctx),
			ReasoningLanguage:   agent.ReasoningLanguageFromContext(sctx),
			SubagentDepth:       childDepth,
			MaxSubagentDepth:    maxSubagentDepth,
		}
	}

	// Skill tools: read_only_skill is a narrow plan-mode-safe entry point; the
	// full skills source adds run_skill / install_skill plus the dedicated
	// subagent wrappers (explore / research / review / security_review). Read-only
	// subagent skills run ephemerally with the same registry boundary as
	// read_only_task, so they cannot write, install, mutate memory, resume/fork
	// transcripts, or delegate further.
	readOnlySkillRunner := func(sctx context.Context, sk skill.Skill, task string, runOpts skill.SubagentRunOptions) (string, error) {
		if strings.TrimSpace(runOpts.ContinueFrom) != "" || strings.TrimSpace(runOpts.ForkFrom) != "" {
			return "", fmt.Errorf("read_only_skill does not support continue_from/fork_from")
		}
		sk = skill.WithCodeGraphTools(sk, skill.CodeGraphReadTools(reg))
		prov, price, ctxWin := execProv, entry.Price, entry.ContextWindow
		modelRef := subagentModelRef(cfg, sk)
		effortRef := subagentEffortRef(cfg, sk)
		if modelRef != "" || effortRef != "" {
			p, pr, cw, err := resolveSubagentProvider(modelRef, effortRef)
			if err != nil {
				return "", fmt.Errorf("read-only subagent skill %q profile: %w", sk.Name, err)
			}
			prov, price, ctxWin = p, pr, cw
		}
		childDepth := agent.SubagentDepth(sctx) + 1
		if childDepth > maxSubagentDepth {
			return "", fmt.Errorf("subagent delegation depth limit reached (max_subagent_depth=%d)", maxSubagentDepth)
		}
		subReg := agent.ReadOnlySubagentToolRegistryForDepth(reg, sk.AllowedTools, childDepth, maxSubagentDepth)
		if subReg.Len() == 0 {
			return "", fmt.Errorf("read_only_skill: skill %q has no read-only tools available", sk.Name)
		}
		steps := agent.ChildStepBudget(maxSteps, 0)
		sysPrompt := agent.DefaultReadOnlyTaskSystemPrompt + "\n\nSkill instructions:\n" + sk.Body
		return agent.RunSubAgentWithSession(sctx, prov, subReg, agent.NewSession(sysPrompt), task,
			childOptions(sctx, steps, childDepth, ctxWin, price), agent.NestedSink(sctx, event.Discard))
	}
	// Writer-capable subagent skills reuse the sub-agent machinery via this
	// runner: an isolated loop with the skill body as system prompt, a tool set
	// scoped to the skill's allowed-tools (minus recursive meta-tools), optional
	// per-skill model, and resumable transcripts when the parent session supports
	// them. Its tool activity nests under the invoking call, like `task`.
	// When the skill declares read-only: true in its frontmatter, the runner
	// selects the read-only subagent registry while retaining the same safe
	// transcript continuation semantics.
	skillRunner := func(sctx context.Context, sk skill.Skill, task string, runOpts skill.SubagentRunOptions) (string, error) {
		sk = skill.WithCodeGraphTools(sk, skill.CodeGraphReadTools(reg))
		readOnly := sk.ReadOnly
		prov, price, ctxWin := execProv, entry.Price, entry.ContextWindow
		modelRef := subagentModelRef(cfg, sk)
		effortRef := subagentEffortRef(cfg, sk)
		if modelRef != "" || effortRef != "" {
			p, pr, cw, err := resolveSubagentProvider(modelRef, effortRef)
			if err != nil {
				return "", fmt.Errorf("subagent skill %q profile: %w", sk.Name, err)
			}
			prov, price, ctxWin = p, pr, cw
		}
		childDepth := agent.SubagentDepth(sctx) + 1
		if childDepth > maxSubagentDepth {
			return "", fmt.Errorf("subagent delegation depth limit reached (max_subagent_depth=%d)", maxSubagentDepth)
		}
		var subReg *tool.Registry
		if readOnly {
			subReg = agent.ReadOnlySubagentToolRegistryForDepth(reg, sk.AllowedTools, childDepth, maxSubagentDepth)
		} else {
			subReg = agent.SubagentToolRegistryForDepth(reg, sk.AllowedTools, childDepth, maxSubagentDepth)
		}
		continueFrom := strings.TrimSpace(runOpts.ContinueFrom)
		legacyForkFrom := strings.TrimSpace(runOpts.ForkFrom)
		if continueFrom != "" && legacyForkFrom != "" {
			return "", fmt.Errorf("continue_from and fork_from are mutually exclusive; pass only continue_from")
		}
		parentID, _, _, _ := agent.CallContext(sctx)
		parentSession := agent.ParentSession(sctx)
		var run *agent.SubagentRun
		if subagentStore == nil || parentSession == "" {
			// Headless runs (e.g. `WorkGround2 run`) have no persistent session to
			// own a transcript. Run the skill sub-agent ephemerally, as before
			// persisted transcripts existed, instead of failing. Continuation needs
			// a persisted owner, so it errors here.
			if continueFrom != "" || legacyForkFrom != "" {
				return "", fmt.Errorf("subagent continuation requires a persisted session; none is active in this run")
			}
			run = agent.EphemeralSubagentRun(sk.Body)
		} else {
			identityModel, identityEffort := subagentIdentity(modelRef, effortRef)
			spec := agent.SubagentSpec{
				Kind:             "skill",
				Name:             sk.Name,
				Description:      task,
				WorkspaceRoot:    root,
				ParentSession:    parentSession,
				ParentToolCallID: parentID,
				SystemPrompt:     sk.Body,
				Registry:         subReg,
				Model:            identityModel,
				Effort:           identityEffort,
			}
			var prepErr error
			if continueFrom != "" {
				run, prepErr = subagentStore.PrepareContinue(continueFrom, spec)
			} else if legacyForkFrom != "" {
				run, prepErr = subagentStore.PrepareLegacyForkFrom(legacyForkFrom, spec)
			} else {
				run, prepErr = subagentStore.PrepareFresh(spec)
			}
			if prepErr != nil {
				return "", prepErr
			}
		}
		defer run.Release()
		steps := agent.ChildStepBudget(maxSteps, 0)
		answer, err := agent.RunSubAgentWithSession(sctx, prov, subReg, run.Session, task,
			childOptions(sctx, steps, childDepth, ctxWin, price), agent.NestedSink(sctx, event.Discard))
		if err != nil {
			return "", errors.Join(err, subagentStore.SaveFailed(run))
		}
		if err := subagentStore.SaveCompleted(run); err != nil {
			return "", errors.Join(err, subagentStore.SaveFailed(run))
		}
		return agent.FormatSubagentRunResult(answer, run, false), nil
	}
	skillProfile := func(sk skill.Skill) *event.Profile {
		model, effort := subagentModelRef(cfg, sk), subagentEffortRef(cfg, sk)
		if model == "" && effort == "" {
			return nil
		}
		return &event.Profile{Model: model, Effort: effort}
	}
	// Custom slash commands (.workground2/commands + user dir). Best-effort: a malformed
	// file is skipped, and a load error never blocks the session.
	cmds, _ := command.Load(config.CommandDirsForRoot(root)...)
	addSlashCommandTool := func(includeSkills bool) {
		// Expose loaded slash commands to the model via slash_command. In economy
		// mode skills join this list only after the skills source is enabled.
		var slashEntries []command.SlashEntry
		if includeSkills {
			for _, sk := range skills {
				sk := sk
				slashEntries = append(slashEntries, command.SlashEntry{
					Name:        sk.Name,
					Description: sk.Description,
					Render:      func(args []string) string { return skill.Render(sk, strings.Join(args, " ")) },
				})
			}
		}
		for _, cmd := range cmds {
			cmd := cmd
			slashEntries = append(slashEntries, command.SlashEntry{
				Name:        cmd.Name,
				Description: cmd.Description,
				ArgHint:     cmd.ArgHint,
				Render:      func(args []string) string { return cmd.Render(args) },
			})
		}
		reg.Add(command.NewSlashCommandTool(slashEntries))
	}
	installSourceAdded := false
	addInstallSourceTool := func() string {
		if installSourceAdded {
			return "install_source is already enabled."
		}
		installSourceAdded = true
		reg.Add(installsource.NewTool(installsource.Options{
			ProjectRoot: root,
			HTTPClient:  balanceClient,
			ConnectMCP: func(e config.PluginEntry) (installsource.MCPConnectResult, error) {
				spec := pluginSpecFromEntryWithOptions(e, root, pluginSpecOptions)
				if opts.Stderr != nil {
					spec.Stderr = opts.Stderr
				}
				tools, err := pluginHost.Add(ctx, spec)
				if err != nil {
					return installsource.MCPConnectResult{}, err
				}
				reg.RemovePrefix(plugin.ToolPrefix(spec.Name))
				for _, t := range tools {
					reg.Add(t)
				}
				// Disconnect closes the server and drops its namespaced tools.
				// Used by the install_source rollback path when SaveTo fails.
				disconnect := func() {
					if prefix, ok := pluginHost.Remove(spec.Name); ok {
						reg.RemovePrefix(prefix)
					}
				}
				return installsource.MCPConnectResult{
					ToolCount:  len(tools),
					Disconnect: disconnect,
				}, nil
			},
			OnDisconnect: func(serverName string) bool {
				if prefix, ok := pluginHost.Remove(serverName); ok {
					reg.RemovePrefix(prefix)
					return true
				}
				return false
			},
		}))
		return "enabled install_source."
	}
	readOnlySkillToolsAdded := false
	addReadOnlySkillTools := func() string {
		if readOnlySkillToolsAdded {
			return "read_only_skill tool is already enabled.\n\n" + skill.ReadOnlyIndexBlock(skills)
		}
		readOnlySkillToolsAdded = true
		reg.Add(skill.NewReadOnlySkillTool(skillStore, readOnlySkillRunner, skillProfile))
		return "enabled read_only_skill. Use read_only_skill for inline skills or read-only subagent skills on the next model request.\n\n" + skill.ReadOnlyIndexBlock(skills)
	}
	skillToolsAdded := false
	addSkillTools := func() string {
		if skillToolsAdded {
			return "skills are already enabled.\n\n" + skill.IndexBlock(skills)
		}
		skillToolsAdded = true
		addReadOnlySkillTools()
		reg.Add(skill.NewRunSkillTool(skillStore, skillRunner, skillProfile))
		reg.Add(skill.NewReadSkillTool(skillStore))
		reg.Add(skill.NewInstallSkillTool(skillStore, nil))
		for _, t := range skill.BuiltinSubagentTools(skillStore, skillRunner, skillProfile) {
			reg.Add(t)
		}
		addSlashCommandTool(true)
		return "enabled skills. Use run_skill/read_skill/read_only_skill or the dedicated skill tools on the next model request.\n\n" + skill.IndexBlock(skills)
	}
	if tokenEconomy {
		addSlashCommandTool(false)
	} else {
		addInstallSourceTool()
		addSkillTools()
	}
	if tokenEconomy {
		reg.Add(&toolSourceConnector{
			skills: func(context.Context) (string, error) {
				return addSkillTools(), nil
			},
			task: func(context.Context) (string, error) {
				return addTaskTool(), nil
			},
			readOnlyTask: func(context.Context) (string, error) {
				return addReadOnlyTaskTool(), nil
			},
			readOnlySkill: func(context.Context) (string, error) {
				return addReadOnlySkillTools(), nil
			},
			install: func(context.Context) (string, error) {
				return addInstallSourceTool(), nil
			},
			webFetch: func(context.Context) (string, error) {
				if !builtinToolEnabled(cfg.Tools.Enabled, "web_fetch") {
					return "web_fetch is disabled by [tools].enabled.", nil
				}
				names := addTools(reg, builtin.Workspace{
					Dir:         root,
					WriteRoots:  cfg.WriteRootsForRoot(root),
					Bash:        bashSpec,
					BashTimeout: bashTimeout,
					Search:      searchSpec,
					ProxySpec:   proxySpec,
				}.Tools("web_fetch"))
				if len(names) == 0 {
					return "web_fetch is already enabled or unavailable.", nil
				}
				return "enabled " + strings.Join(names, ", ") + ".", nil
			},
			lsp: func(context.Context) (string, error) {
				if lspMgr == nil {
					return "", fmt.Errorf("LSP is disabled in config")
				}
				names := addLSPTools()
				if len(names) == 0 {
					return "LSP tools are already enabled.", nil
				}
				return "enabled " + strings.Join(names, ", ") + ".", nil
			},
			mcp: func(_ context.Context, name string) (string, error) {
				spec, ok := onDemandMCPSpecs[name]
				if !ok {
					return "", fmt.Errorf("no configured MCP server named %q", name)
				}
				if opts.Stderr != nil {
					spec.Stderr = opts.Stderr
				}
				tools, err := pluginHost.Add(ctx, spec)
				if err != nil {
					// On a shared host the server may already be connected
					// (e.g. another tab started it). Fall back to fetching
					// its tools from the existing client.
					if errors.Is(err, plugin.ErrServerAlreadyConnected) || errors.Is(err, plugin.ErrSpawningInFlight) {
						tools, err2 := pluginHost.ToolsFor(ctx, spec.Name)
						if err2 != nil {
							return "", err2
						}
						reg.RemovePrefix(plugin.ToolPrefix(spec.Name))
						names := addTools(reg, tools)
						if len(names) == 0 {
							return fmt.Sprintf("MCP server %q connected but exposed no tools.", spec.Name), nil
						}
						return fmt.Sprintf("enabled MCP server %q tools: %s.", spec.Name, strings.Join(names, ", ")), nil
					}
					return "", err
				}
				reg.RemovePrefix(plugin.ToolPrefix(spec.Name))
				names := addTools(reg, tools)
				if len(names) == 0 {
					return fmt.Sprintf("MCP server %q connected but exposed no tools.", spec.Name), nil
				}
				return fmt.Sprintf("enabled MCP server %q tools: %s.", spec.Name, strings.Join(names, ", ")), nil
			},
			mcpNames:                 onDemandMCPNames,
			planModeAllowedTools:     cfg.Agent.PlanModeAllowedTools,
			planModeTrustedMCPServer: trustedMCPServers,
		})
	}

	execSess := agent.NewSession(sysPrompt)
	var memCompiler *memorycompiler.Runtime
	if cfg.MemoryCompilerEnabled() {
		memCompiler = memorycompiler.New(config.MemoryCompilerDir(root))
	}
	var semanticIntentProv provider.Provider
	newAgentOptions := func(sessionJobs *jobs.Manager) agent.Options {
		return agent.Options{
			MaxSteps:                           maxSteps,
			Temperature:                        cfg.Agent.Temperature,
			Pricing:                            entry.Price,
			Gate:                               headlessGate,
			Hooks:                              hookRunner,
			Jobs:                               sessionJobs,
			ProjectChecks:                      projectChecks,
			ContextWindow:                      entry.ContextWindow,
			SoftCompactRatio:                   cfg.Agent.SoftCompactRatio,
			ToolResultSnipRatio:                cfg.Agent.ToolResultSnipRatio,
			CompactRatio:                       cfg.Agent.CompactRatio,
			CompactForceRatio:                  cfg.Agent.CompactForceRatio,
			RecentKeep:                         cfg.Agent.RecentKeep,
			ArchiveDir:                         config.ArchiveDir(),
			KeepPolicy:                         keepPolicy,
			ResponseLanguage:                   cfg.ResponseLanguage(),
			ReasoningLanguage:                  cfg.ReasoningLanguage(),
			PlanModeAllowedTools:               cfg.Agent.PlanModeAllowedTools,
			SubagentDepth:                      0,
			MaxSubagentDepth:                   maxSubagentDepth,
			MemoryCompiler:                     memCompiler,
			MemoryCompilerVerbosity:            cfg.MemoryCompilerVerbosity(),
			UseMemoryCompilerLLMClassification: strings.TrimSpace(os.Getenv("WorkGround2_MEMORY_COMPILER_LLM_CLASSIFICATION")) == "true",
			SemanticIntentProvider:             semanticIntentProv,
		}
	}

	// Resolve a standalone semantic intent provider for Room message
	// classification. It never shares the main session model and degrades
	// safely when no lightweight model is available.
	if !tokenEconomy {
		semanticIntentProv, err = resolveSemanticIntentProvider(cfg, proxySpec)
		if err != nil {
			return nil, err
		}
	}

	// Two-phase DeepSeek bootstrap arms ONLY the main executor session — task
	// executors, subagents, and the planner keep their own prompts and full
	// catalogs. The durable session log always stores the full prompt; the
	// agent swaps the bootstrap prefix in for the first request only.
	execOpts := newAgentOptions(jm)
	if useAnchoredBootstrap && bootstrapPrompt != "" {
		execOpts.AnchoredBootstrapSystemPrompt = bootstrapPrompt
	}
	executor := agent.New(execProv, reg, execSess, execOpts, sink)

	var runner agent.Runner = executor
	label := entry.Model
	var classifier *control.ProviderAutoPlanClassifier
	var workDefinitionProv provider.Provider = execProv

	if !tokenEconomy && !strings.EqualFold(strings.TrimSpace(cfg.Agent.AutoPlan), "off") && cfg.Agent.AutoPlanClassifier != "" {
		cm := cfg.Agent.AutoPlanClassifier
		ce, ok := cfg.ResolveModel(cm)
		if !ok {
			return nil, fmt.Errorf("auto_plan_classifier %q is not a configured provider", cm)
		}
		classifierProv, err := NewProviderWithProxy(ce, proxySpec)
		if err != nil {
			return nil, fmt.Errorf("auto_plan_classifier %q: %w", cm, err)
		}
		classifier = control.NewBillableProviderAutoPlanClassifier(classifierProv, ce.Price, sink)
	}

	// Two-model collaboration: a distinct planner_model wraps the executor in a
	// Coordinator with its own session, kept separate for cache stability. The
	// planner gets the same standing memory context and a filtered read-only
	// research tool set, so it can inspect rules/code without side effects.
	if pm := cfg.Agent.PlannerModel; pm != "" && !tokenEconomy {
		pe, ok := cfg.ResolveModel(pm)
		if !ok {
			return nil, fmt.Errorf("planner_model %q is not a configured provider", pm)
		}
		if pe.Model != entry.Model {
			plannerProv, err := NewProviderWithProxy(pe, proxySpec)
			if err != nil {
				return nil, fmt.Errorf("planner %q: %w", pm, err)
			}
			plannerSess := agent.NewSession(agent.PlannerPromptWithContext(mem.Block()))
			plannerTools := agent.PlannerToolRegistry(reg)
			runner = agent.NewCoordinator(plannerProv, plannerSess, pe.Price, plannerTools, agent.Options{
				MaxSteps:            cfg.Agent.PlannerMaxSteps,
				MaxStepsKey:         "agent.planner_max_steps",
				Gate:                headlessGate,
				ContextWindow:       pe.ContextWindow,
				SoftCompactRatio:    cfg.Agent.SoftCompactRatio,
				ToolResultSnipRatio: cfg.Agent.ToolResultSnipRatio,
				CompactRatio:        cfg.Agent.CompactRatio,
				CompactForceRatio:   cfg.Agent.CompactForceRatio,
				RecentKeep:          cfg.Agent.RecentKeep,
				ArchiveDir:          config.ArchiveDir(),
				KeepPolicy:          keepPolicy,
				ResponseLanguage:    cfg.ResponseLanguage(),
				ReasoningLanguage:   cfg.ReasoningLanguage(),
			}, executor, cfg.Agent.Temperature, sink, control.NewPlannerGate(classifier))
			workDefinitionProv = plannerProv
			label = entry.Model + " + planner " + pe.Model
		}
	}

	// Work: assemble the structured Work feature by default. An explicit
	// [work].enabled=false keeps the cache-stable prefix untouched and does not
	// create writable Work directories.
	var workSvc *work.Service
	var workViews *control.WorkViewBroadcaster
	var taskExec work.TaskExecutor
	var cornerstoneManager *work.CornerstoneManager
	var cornerstoneStore work.WorkStore
	var cornerstoneBlobs work.BlobStore
	var workDir string
	if cfg.Work.Enabled {
		if opts.SessionRefsErr != nil {
			jm.Close()
			cleanup()
			return nil, fmt.Errorf("initialize Work Session refs: %w", opts.SessionRefsErr)
		}
		workDir = strings.TrimSpace(opts.WorkDir)
		if workDir == "" {
			workDir = config.ProjectWorkDir(root)
		}
		if workDir == "" {
			sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "work: project data directory is unavailable — feature disabled this session"})
		} else {
			if err := ensureWorkDir(workDir); err != nil {
				jm.Close()
				cleanup()
				return nil, fmt.Errorf("initialize Work store %q: %w", workDir, err)
			}
			store, err := work.NewFileWorkStore(workDir, 30*24*time.Hour)
			if err != nil {
				jm.Close()
				cleanup()
				return nil, fmt.Errorf("initialize Work store %q: %w", workDir, err)
			}
			bp := work.NewBlueprintRegistry()
			workViews = control.NewWorkViewBroadcaster()
			workSvc = work.NewService(store, bp, workViews)
			workSvc.SetV2TransportEnabled(cfg.Work.CollaborationWorkbenchV2)

			// Wire optional LLM interaction diagnostic logger.
			// TEMPORARY DIAGNOSTIC — sensitive: controlled by [work].llm_interaction_log.
			var workLLMLog *workLLMInteractionLogger
			if cfg.Work.LLMInteractionLog {
				logPath := filepath.Join(config.WorkGround2HomeDir(), "work-llm-interactions.jsonl")
				workLLMLog = newWorkLLMInteractionLogger(logPath)
			}

			workSvc.SetV2PatchPlanner(newBootPatchPlanner(execProv, cfg.Agent.Temperature, 4096, workLLMLog))
			workSvc.SetV2DefinitionPlanner(newBootDefinitionPlanner(workDefinitionProv, cfg.Agent.Temperature, 8192, workLLMLog))
			workSvc.SetInputInferrer(newBootInputInferrer(workDefinitionProv, cfg.Agent.Temperature, 4096, workLLMLog))
			artifactSources := opts.ArtifactSourceResolver
			if artifactSources == nil {
				artifactSources = work.NewStoreArtifactSourceResolver(store, store, root)
			}
			workSvc.SetArtifactSourceResolver(artifactSources)
			workSvc.SetPreviewApprovalVerifier(opts.PreviewApprovalVerifier)
			// Wire CornerstoneManager for T5 transport-alignment. FileWorkStore
			// implements both WorkStore and BlobStore, enabling >4096-byte
			// cornerstone content via content-addressed blob storage.
			cornerstoneManager = work.NewCornerstoneManager(store, store, nil)
			cornerstoneStore = store
			cornerstoneBlobs = store
			workSvc.SetCornerstoneManager(cornerstoneManager)
			workSvc.SetCornerstoneResolver(opts.CornerstoneResolver)
			workSvc.SetSkillResolver(newBootSkillResolver(skillStore))
			if opts.SessionRefs != nil {
				if err := workSvc.SetSessionRefStore(opts.SessionRefs, work.SessionRefScopeID(workDir)); err != nil {
					jm.Close()
					cleanup()
					return nil, fmt.Errorf("initialize Work Session refs: %w", err)
				}
				if err := workSvc.RebuildSessionRefs(ctx); err != nil {
					jm.Close()
					cleanup()
					return nil, err
				}
			}
			taskPrompt := workTaskSystemPrompt(sysPrompt)
			taskAdapter := control.NewTaskExecutorAdapter(
				control.TaskExecutorProfile{
					Provider:           execProv.Name(),
					Model:              modelRef,
					NativeCapabilities: append([]string(nil), entry.Capabilities...),
				},
				func(_ context.Context, input work.TaskExecuteInput) (*control.Controller, func(), error) {
					taskPath := agent.NewSessionPath(sessionDir, "work-"+label)
					if _, err := control.PublishTaskSession(input, taskPath, modelRef); err != nil {
						return nil, nil, fmt.Errorf("publish hidden Task Session: %w", err)
					}
					taskSink := control.NewTaskLiveSink(input.Live)
					taskJobs := jobs.NewManager(taskSink, jobs.WithStalledWarningAfter(time.Duration(cfg.BackgroundJobStalledWarningSeconds())*time.Second))
					taskSession := agent.NewSession(taskPrompt)
					taskAgent := agent.New(execProv, reg, taskSession, newAgentOptions(taskJobs), taskSink)
					taskCleanup := func() {}
					if browserMgr != nil {
						taskCleanup = browserTaskCleanup(browserMgr, agent.BranchID(taskPath), taskSink)
					}
					taskCtrl := control.New(control.Options{
						Runner:               taskAgent,
						Executor:             taskAgent,
						Sink:                 taskSink,
						Policy:               policy,
						Label:                label,
						ModelRef:             modelRef,
						SystemPrompt:         taskPrompt,
						SessionDir:           sessionDir,
						SessionPath:          taskPath,
						Hooks:                hookRunner,
						WorkspaceRoot:        root,
						AutoPlan:             "off",
						ResponseLanguage:     cfg.ResponseLanguage(),
						ReasoningLanguage:    cfg.ReasoningLanguage(),
						Shell:                shell,
						ApprovalTimeout:      opts.ApprovalTimeout,
						PlanModeAllowedTools: cfg.Agent.PlanModeAllowedTools,
						BalanceURL:           entry.BalanceURL,
						BalanceKey:           entry.APIKey(),
						BalanceClient:        balanceClient,
						Jobs:                 taskJobs,
						Registry:             reg,
						Cleanup:              taskCleanup,
					})
					return taskCtrl, func() {}, nil
				},
			)
			taskAdapter.SetWorkService(workSvc)
			taskAdapter.SetArtifactStore(store)
			taskExec = taskAdapter
			if opts.WorkTaskExecutor != nil {
				taskExec = opts.WorkTaskExecutor
			}
			sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: "work: feature enabled"})
		}
	}

	// Prevent typed-nil injection: workSvc is *work.Service; assigning a nil
	// concrete pointer to the WorkService interface creates a non-nil interface
	// holding nil, which defeats plain == nil checks. Only assign when non-nil.
	var workSvcIface control.WorkService
	if workSvc != nil {
		workSvcIface = workSvc
	}
	workRecoveryCtx, cancelWorkRecovery := context.WithCancel(ctx)
	previousCleanup := cleanup
	cleanup = func() {
		cancelWorkRecovery()
		previousCleanup()
	}
	ctrlOpts := control.Options{
		Runner:                 runner,
		Executor:               executor,
		Sink:                   sink,
		Policy:                 policy,
		Label:                  label,
		ModelRef:               modelRef,
		SystemPrompt:           sysPrompt,
		SessionDir:             sessionDir,
		Host:                   pluginHost,
		Commands:               cmds,
		Skills:                 skills,
		AllSkills:              allSkills,
		SkillStore:             skillStore,
		AllSkillStore:          allSkillStore,
		Hooks:                  hookRunner,
		Memory:                 mem,
		Vocabulary:             vocab,
		Cleanup:                cleanup,
		BalanceURL:             entry.BalanceURL,
		BalanceKey:             entry.APIKey(),
		BalanceClient:          balanceClient,
		Jobs:                   jm,
		Registry:               reg,
		PluginCtx:              ctx,
		WorkspaceRoot:          root,
		ExternalFolderToolRefs: readPathResolver,
		AutoPlan:               cfg.Agent.AutoPlan,
		ResponseLanguage:       cfg.ResponseLanguage(),
		ReasoningLanguage:      cfg.ReasoningLanguage(),
		DisableColdResumePrune: !cfg.ColdResumePruneEnabled(),
		Shell:                  shell,
		PlanModeAllowedTools:   cfg.Agent.PlanModeAllowedTools,
		ApprovalTimeout:        opts.ApprovalTimeout,
		OnRemember: func(rule string) control.RememberResult {
			return rememberPermissionRule(root, rule)
		},
		OnRememberMCPReadOnlyTrust: func(serverName, rawToolName string) control.MCPReadOnlyTrustResult {
			return rememberMCPReadOnlyTrust(root, serverName, rawToolName)
		},
		SessionRecoveryMeta: opts.SessionRecoveryMeta,
		OnSessionRecovered:  opts.OnSessionRecovered,
		Work:                workSvcIface,
		WorkV2Enabled:       cfg.Work.CollaborationWorkbenchV2,
		WorkViews:           workViews,
		TaskExecutor:        taskExec,
	}
	// Guardian: when guardian_model is configured, spawn an LLM safety reviewer
	// that can auto-allow safe Ask decisions and annotate risky ones before
	// escalating to the human approval prompt.
	if guardianModel := cfg.Agent.GuardianModel; guardianModel != "" {
		ge, ok := cfg.ResolveModel(guardianModel)
		if !ok {
			slog.Warn("guardian model is not a configured provider — guardian disabled", "model", guardianModel)
			sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: fmt.Sprintf("guardian_model %q not found — guardian disabled", guardianModel)})
		} else {
			pProv, err := NewProviderWithProxy(ge, proxySpec)
			if err != nil {
				slog.Warn("guardian provider construction failed — guardian disabled", "model", guardianModel, "err", err)
				sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: fmt.Sprintf("guardian construction failed: %v — guardian disabled", err)})
			} else {
				guardianReg := agent.FilterReadOnlyRegistry(reg, agent.SubagentMetaTools()...)
				ctrlOpts.Guardian = guardian.NewSession(pProv, guardianReg, guardian.PolicyPrompt(), guardianModel, cfg.Agent.GuardianTemperature, ge.Price, sink)
				sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: fmt.Sprintf("guardian enabled · model=%s", ge.Model)})
			}
		}
	}
	if classifier != nil {
		ctrlOpts.Classifier = classifier
	}
	// Vision delegate: auto-discover or use explicit vision_delegate config.
	vd := cfg.Agent.VisionDelegate
	if vd == "" {
		vd = autoDiscoverVisionDelegate(cfg, entry)
	}
	if vd != "" {
		ve, ok := cfg.ResolveModel(vd)
		if !ok {
			if cfg.Agent.VisionDelegate != "" {
				sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: fmt.Sprintf("vision_delegate %q is not a configured provider \u2014 image delegation disabled", vd)})
			}
		} else {
			vp, err := NewProviderWithProxy(ve, proxySpec)
			if err != nil {
				sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: fmt.Sprintf("vision_delegate %q provider construction failed: %v \u2014 image delegation disabled", vd, err)})
			} else {
				ctrlOpts.VisionDelegateProvider = vp
				sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: fmt.Sprintf("vision delegate: auto-discovered %q for image analysis", vd)})
			}
		}
	}
	ctrl := control.New(ctrlOpts)
	if browserOwner != nil {
		browserOwner.transfer()
	}
	// Post-init: wire every live Cornerstone source through production adapters.
	// URL resolution reuses the configured web_fetch tool, including its proxy,
	// timeout, response cap, and SSRF policy. If web_fetch is disabled, URL refs
	// fail explicitly as denied without creating a second network path.
	if cornerstoneManager != nil && opts.CornerstoneResolver == nil {
		webFetch, _ := reg.Get("web_fetch")
		resolver := control.NewLiveCornerstoneResolver(control.LiveCornerstoneResolverOptions{
			WorkspaceRoot: root,
			SessionTurns:  ctrl,
			WorkStore:     cornerstoneStore,
			BlobStore:     cornerstoneBlobs,
			URLTool:       webFetch,
		})
		cornerstoneManager.SetResolver(resolver)
		if workSvc != nil && opts.ArtifactSourceResolver == nil {
			workSvc.SetArtifactSourceResolver(resolver)
		}
	}
	if workSvc != nil && cfg.Work.CollaborationWorkbenchV2 {
		startBackgroundWorkRecovery(workRecoveryCtx, workDir, workSvc, sink)
	}
	dshTransferred = true
	return ctrl, nil
}

func ensureWorkDir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	probe, err := os.CreateTemp(path, ".work-store-probe-*")
	if err != nil {
		return err
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probePath)
	return errors.Join(closeErr, removeErr)
}

func autoDiscoverVisionDelegate(cfg *config.Config, current *config.ProviderEntry) string {
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if !config.EffectiveVision(p) {
			continue
		}
		if p.Name == current.Name && p.EffectiveModel() == current.EffectiveModel() {
			continue
		}
		return p.Name + "/" + p.EffectiveModel()
	}
	return ""
}

func rememberPermissionRule(workspaceRoot, rule string) control.RememberResult {
	path := rememberPermissionConfigPath(workspaceRoot)
	edit := config.LoadForEdit(path)
	result := control.RememberResult{Rule: strings.TrimSpace(rule), Path: path}
	if coveredBy := coveredPermissionRule(edit.Permissions.Allow, result.Rule); coveredBy != "" {
		result.CoveredBy = coveredBy
		return result
	}
	edit.Permissions.Allow = pruneCoveredPermissionRules(edit.Permissions.Allow, result.Rule)
	if err := edit.AddPermissionRule("allow", rule); err != nil {
		slog.Warn("persist permission rule", "rule", rule, "err", err)
		result.Err = err
		return result
	}
	if err := config.WritePermissionsSection(path, edit.Permissions.Allow); err != nil {
		slog.Warn("save config after permission rule", "err", err)
		result.Err = err
		return result
	}
	result.Saved = true
	return result
}

func rememberPermissionConfigPath(workspaceRoot string) string {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot != "" {
		return filepath.Join(workspaceRoot, "WorkGround2.toml")
	}
	path := config.SourcePath()
	if path == "" {
		path = "WorkGround2.toml" // match Config.Save() fallback
	}
	return path
}

func rememberMCPReadOnlyTrust(workspaceRoot, serverName, rawToolName string) control.MCPReadOnlyTrustResult {
	serverName = strings.TrimSpace(serverName)
	rawToolName = strings.TrimSpace(rawToolName)
	result := control.MCPReadOnlyTrustResult{Server: serverName, Tool: rawToolName}
	_, changed, path, err := config.TrustPluginReadOnlyToolInSourceForRoot(workspaceRoot, serverName, rawToolName)
	result.Path = path
	if err != nil {
		slog.Warn("persist MCP read-only trust", "server", serverName, "tool", rawToolName, "err", err)
		result.Err = err
		return result
	}
	if changed {
		result.Saved = true
		return result
	}
	result.CoveredBy = rawToolName
	return result
}

func coveredPermissionRule(rules []string, rule string) string {
	for _, existing := range rules {
		if permission.RuleCoversString(existing, rule) {
			return strings.TrimSpace(existing)
		}
	}
	return ""
}

func pruneCoveredPermissionRules(rules []string, rule string) []string {
	out := rules[:0]
	for _, existing := range rules {
		if strings.TrimSpace(existing) == "" || permission.RuleCoversString(rule, existing) {
			continue
		}
		out = append(out, existing)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func subagentModelRef(cfg *config.Config, sk skill.Skill) string {
	if cfg != nil {
		for _, key := range subagentModelKeys(sk.Name) {
			if m := strings.TrimSpace(cfg.Agent.SubagentModels[key]); m != "" {
				return m
			}
		}
	}
	if m := strings.TrimSpace(sk.Model); m != "" {
		return m
	}
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Agent.SubagentModel)
}

func subagentEffortRef(cfg *config.Config, sk skill.Skill) string {
	if cfg != nil {
		for _, key := range subagentModelKeys(sk.Name) {
			if e := strings.TrimSpace(cfg.Agent.SubagentEfforts[key]); e != "" {
				return e
			}
		}
	}
	if e := strings.TrimSpace(sk.Effort); e != "" {
		return e
	}
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Agent.SubagentEffort)
}

func subagentModelKeys(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	keys := []string{name}
	for _, alias := range []string{
		strings.ReplaceAll(name, "-", "_"),
		strings.ReplaceAll(name, "_", "-"),
	} {
		if alias == "" {
			continue
		}
		seen := false
		for _, key := range keys {
			if key == alias {
				seen = true
				break
			}
		}
		if !seen {
			keys = append(keys, alias)
		}
	}
	return keys
}

func resolveWorkspaceRoot(explicit string) string {
	if explicit != "" {
		return explicit
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if root, ok := nearestGitRoot(wd); ok {
		return root
	}
	return wd
}

func nearestGitRoot(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		dir = filepath.Clean(start)
	}
	for {
		if isGitMarker(filepath.Join(dir, ".git")) {
			return dir, true
		}
		next := filepath.Dir(dir)
		if next == dir {
			return "", false
		}
		dir = next
	}
}

func isGitMarker(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && (fi.IsDir() || fi.Mode().IsRegular())
}

func newSubagentStore(sessionDir string) (*agent.SubagentStore, error) {
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" {
		return nil, nil
	}
	store := agent.NewSubagentStore(filepath.Join(sessionDir, "subagents"))
	if _, err := store.CleanupStaleRunning(); err != nil {
		return nil, fmt.Errorf("cleanup stale subagents: %w", err)
	}
	return store, nil
}

func subagentEffectiveIdentity(cfg *config.Config, baseModelRef string, base *config.ProviderEntry, modelRef, effort string) (string, string) {
	var entry config.ProviderEntry
	if base != nil {
		entry = *base
	}
	ref := strings.TrimSpace(modelRef)
	if ref == "" {
		ref = strings.TrimSpace(baseModelRef)
	}
	if cfg != nil && ref != "" {
		if resolved, ok := cfg.ResolveModel(ref); ok {
			entry = *resolved
		} else if strings.TrimSpace(modelRef) != "" {
			entry.Model = ref
		}
	} else if strings.TrimSpace(modelRef) != "" {
		entry.Model = strings.TrimSpace(modelRef)
	}
	if rawEffort := strings.TrimSpace(effort); rawEffort != "" {
		if normalized, err := config.NormalizeEffort(&entry, rawEffort); err == nil {
			entry.Effort = normalized
		} else {
			entry.Effort = rawEffort
		}
	}
	modelID := strings.TrimSpace(entry.Name)
	model := strings.TrimSpace(entry.Model)
	if modelID != "" && model != "" {
		modelID += "/" + model
	} else if model != "" {
		modelID = model
	} else if modelID == "" {
		modelID = ref
	}
	return modelID, strings.TrimSpace(config.EffectiveEffort(&entry))
}

// NewProvider builds a provider.Provider from a configured entry. Exported so
// custom assemblers (e.g. the ACP per-session factory) can reuse it without
// going through the full Build.
func NewProvider(e *config.ProviderEntry) (provider.Provider, error) {
	return NewProviderWithProxy(e, netclient.ProxySpec{Mode: netclient.ModeAuto})
}

// isDeepSeekProvider reports whether the entry is a DeepSeek-family provider,
// using the same rule the openai provider applies to its deepseek protocol
// detection: an explicit "deepseek" reasoning_protocol, or an unset protocol
// against a deepseek base URL.
func isDeepSeekProvider(e *config.ProviderEntry) bool {
	if e == nil || e.Kind != "openai" {
		return false
	}
	protocol := config.ReasoningProtocolForEntry(e)
	return protocol == "deepseek" || (protocol == "" && openai.IsDeepSeek(e.BaseURL))
}

// NewProviderWithProxy builds a provider.Provider with the configured ordinary
// network proxy settings.
func NewProviderWithProxy(e *config.ProviderEntry, proxy netclient.ProxySpec) (provider.Provider, error) {
	return provider.New(e.Kind, provider.Config{
		Name:    e.Name,
		BaseURL: e.BaseURL,
		Model:   e.Model,
		APIKey:  e.APIKey(),
		// Pass the key's env var so auth failures can name where to fix it, plus
		// provider-kind-specific knobs. EffectiveEffort applies a configured
		// default_effort when the user has not explicitly selected /effort.
		Extra: map[string]any{
			"api_key_env":        e.APIKeyEnv,
			"api_key_source":     e.APIKeySourceLabel(),
			"command":            e.Command,
			"args":               append([]string(nil), e.Args...),
			"protocol":           e.Protocol,
			"timeout_seconds":    e.TimeoutSeconds,
			"thinking":           e.Thinking,
			"effort":             config.EffectiveEffort(e),
			"reasoning_protocol": config.ReasoningProtocolForEntry(e),
			"chat_url":           e.ChatURL,
			"headers":            e.Headers,
			"proxy_spec":         proxy,
			"vision":             config.EffectiveVision(e),
			"vision_detail":      e.VisionDetail,
			"capabilities":       config.EntryCapabilities(e),
		},
	})
}

// addBuiltins adds enabled built-in tools to reg. An empty list means all of
// them. writeRoots confines the file-writing built-ins to the workspace: after
// the (unconfined) defaults are added, each enabled writer is replaced by an
// instance bound to writeRoots (preserving registry order).
// forbidReadRoots confines the read/list/search built-ins so they cannot peek at
// the listed directories.
// When workDir is non-empty, tools resolve relative paths against it instead of
// the process cwd, enabling concurrent multi-project sessions.
func addBuiltins(reg *tool.Registry, enabled, writeRoots []string, bashSpec sandbox.Spec, bashTimeout time.Duration, searchSpec builtin.SearchSpec, stderr io.Writer, workDir string, proxySpec netclient.ProxySpec, forbidReadRoots []string, readPathResolver *builtin.PathResolver, overlay builtin.FileOverlay, terminal builtin.TerminalRunner, vision bool) {
	// view_image reads images back into the model as pixels, so it is exposed
	// only when the current direct model can see them (never to text-only or
	// vision-delegate-only main models). The filter runs at boot, keeping the
	// exposed tool set fixed for the session's lifetime.
	addTool := func(t tool.Tool) {
		if !vision && t.Name() == builtin.ViewImageName {
			return
		}
		reg.Add(t)
	}
	// If a workspace directory is set, use workspace-bound tools that resolve
	// paths relative to that directory. Otherwise fall back to the process-cwd
	// compile-time builtins.
	if workDir != "" {
		ws := builtin.Workspace{Dir: workDir, WriteRoots: writeRoots, ForbidReadRoots: forbidReadRoots, Bash: bashSpec, BashTimeout: bashTimeout, Search: searchSpec, ProxySpec: proxySpec, ReadPaths: readPathResolver, FileOverlay: overlay, Terminal: terminal}
		for _, t := range ws.Tools(enabled...) {
			addTool(t)
		}
		return
	}

	if len(enabled) == 0 {
		for _, t := range tool.Builtins() {
			addTool(t)
		}
	} else {
		for _, name := range enabled {
			if t, ok := tool.LookupBuiltin(name); ok {
				addTool(t)
			} else if !browserRuntimeTools[strings.TrimSpace(name)] {
				fmt.Fprintf(stderr, "warning: unknown built-in tool %q\n", name)
			}
		}
	}
	// Replace the unconfined defaults with confined instances (registry order is
	// preserved on replace): file-writers bound to the workspace, read tools
	// bound to forbid-read roots, bash to the OS sandbox, web_fetch to the proxy.
	// Only replace tools actually enabled/present.
	confined := append(builtin.ConfineWriters(writeRoots),
		builtin.ConfineBash(bashSpec, bashTimeout),
		builtin.ConfineSearch(searchSpec, bashSpec, forbidReadRoots),
		builtin.ConfineWebFetch(proxySpec))
	confined = append(confined, builtin.ConfineReaders(forbidReadRoots)...)
	for _, t := range confined {
		if _, ok := reg.Get(t.Name()); ok {
			reg.Add(t)
		}
	}
}

func builtinToolEnabled(enabled []string, name string) bool {
	if len(enabled) == 0 {
		return true
	}
	name = strings.TrimSpace(name)
	for _, candidate := range enabled {
		if strings.TrimSpace(candidate) == name {
			return true
		}
	}
	return false
}

func browserToolsSelected(enabled []string) bool {
	if len(enabled) == 0 {
		return true
	}
	for _, name := range enabled {
		if browserRuntimeTools[strings.TrimSpace(name)] {
			return true
		}
	}
	return false
}

// browserManagerOptions maps the resolved browser config onto the Manager
// options. It is a separate function so tests can verify the config→runtime
// value flow (including incognito) without booting a controller.
func browserManagerOptions(cfg *config.Config, factory browserpkg.DriverFactory) browserpkg.Options {
	return browserpkg.Options{
		Factory:            factory,
		BrowserKind:        browserpkg.BrowserKind(cfg.BrowserKind()),
		ExecutablePath:     cfg.Tools.Browser.ExecutablePath,
		Headless:           cfg.BrowserHeadless(),
		Incognito:          cfg.BrowserIncognito(),
		IdleTimeout:        time.Duration(cfg.BrowserIdleTimeoutSeconds()) * time.Second,
		ActionTimeout:      time.Duration(cfg.BrowserActionTimeoutSeconds()) * time.Second,
		StateTimeout:       time.Duration(cfg.BrowserStateTimeoutSeconds()) * time.Second,
		SettleWindow:       time.Duration(cfg.BrowserSettleMilliseconds()) * time.Millisecond,
		MaxTextChars:       cfg.BrowserMaxTextChars(),
		MaxElements:        cfg.BrowserMaxElements(),
		AllowPasswordInput: cfg.BrowserAllowPasswordInput(),
		AllowFileUpload:    cfg.BrowserAllowFileUpload(),
		// Shared persistent browser: one automation profile + endpoint record
		// per user, reused across controllers/tasks/rebuilds/restarts.
		RuntimeDir:  config.BrowserStateDir(),
		ProfileRoot: config.BrowserProfileDir(),
	}
}

// partitionByTier splits configured plugin entries into eager (block boot until
// ready) and background (placeholder + start spawn now). Entries with an empty,
// legacy lazy, or unrecognised tier land in background.
func partitionByTier(entries []config.PluginEntry) (eager, bg []config.PluginEntry) {
	for _, e := range entries {
		switch e.ResolvedTier() {
		case "eager":
			eager = append(eager, e)
		default:
			bg = append(bg, e)
		}
	}
	return eager, bg
}

// PluginSpecs maps configured plugin entries to plugin.Spec, expanding ${VAR}
// references. Exported so custom assemblers can connect the config's plugins
// alongside their own (e.g. ACP's per-session MCP servers).
func PluginSpecs(entries []config.PluginEntry) []plugin.Spec {
	return PluginSpecsForRoot(entries, "")
}

// PluginSpecsForRoot maps configured plugin entries to plugin.Spec and applies
// workspace-aware compatibility overrides for known cwd-sensitive servers.
func PluginSpecsForRoot(entries []config.PluginEntry, workspaceRoot string) []plugin.Spec {
	return PluginSpecsForRootWithPlanModeAllowedTools(entries, workspaceRoot, nil)
}

// PluginSpecOptions carries runtime policy that is not stored on each plugin
// entry but still needs to reach plugin.Spec.
type PluginSpecOptions struct {
	DefaultCallTimeout   time.Duration
	PlanModeAllowedTools []string
}

// PluginSpecsForRootWithPlanModeAllowedTools also promotes model-visible MCP
// names declared in agent.plan_mode_allowed_tools to trusted read-only model
// names for their matching server. This keeps the planner/read-only research
// trust path aligned with the plan-mode execution escape valve.
func PluginSpecsForRootWithPlanModeAllowedTools(entries []config.PluginEntry, workspaceRoot string, allowedTools []string) []plugin.Spec {
	return PluginSpecsForRootWithOptions(entries, workspaceRoot, PluginSpecOptions{
		PlanModeAllowedTools: allowedTools,
	})
}

// PluginSpecsForRootWithOptions maps configured plugin entries to plugin.Spec
// and injects runtime policy such as the global MCP call timeout.
func PluginSpecsForRootWithOptions(entries []config.PluginEntry, workspaceRoot string, opts PluginSpecOptions) []plugin.Spec {
	specs := make([]plugin.Spec, len(entries))
	for i, e := range entries {
		specs[i] = pluginSpecFromEntryWithOptions(e, workspaceRoot, opts)
	}
	return applyPlanModeAllowedMCPToolTrust(specs, opts.PlanModeAllowedTools)
}

func pluginSpecFromEntryWithOptions(e config.PluginEntry, workspaceRoot string, opts PluginSpecOptions) plugin.Spec {
	e = e.ExpandedPlugin() // resolve ${VAR} / ${VAR:-default} from the environment
	return plugin.ApplyKnownOverrides(plugin.Spec{
		Name:               e.Name,
		Type:               e.Type,
		Command:            e.Command,
		Args:               e.Args,
		Env:                e.Env,
		URL:                e.URL,
		Headers:            e.Headers,
		DefaultCallTimeout: opts.DefaultCallTimeout,
		CallTimeout:        secondsDuration(e.CallTimeoutSeconds),
		ToolTimeouts:       toolTimeoutDurations(e.ToolTimeoutSeconds),
		ReadOnlyToolNames:  trustedRawReadOnlyToolNames(e.TrustedReadOnlyTools),
	}, workspaceRoot)
}

func secondsDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func toolTimeoutDurations(seconds map[string]int) map[string]time.Duration {
	if len(seconds) == 0 {
		return nil
	}
	out := make(map[string]time.Duration, len(seconds))
	for name, sec := range seconds {
		name = strings.TrimSpace(name)
		if name == "" || sec <= 0 {
			continue
		}
		out[name] = time.Duration(sec) * time.Second
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func applyKnownPluginOverrides(specs []plugin.Spec, workspaceRoot string) []plugin.Spec {
	out := make([]plugin.Spec, len(specs))
	for i, spec := range specs {
		out[i] = plugin.ApplyKnownOverrides(spec, workspaceRoot)
	}
	return out
}

func applyDefaultMCPCallTimeout(specs []plugin.Spec, timeout time.Duration) []plugin.Spec {
	if len(specs) == 0 || timeout <= 0 {
		return specs
	}
	out := make([]plugin.Spec, len(specs))
	for i, spec := range specs {
		out[i] = spec
		if out[i].DefaultCallTimeout <= 0 {
			out[i].DefaultCallTimeout = timeout
		}
	}
	return out
}

func applyPlanModeAllowedMCPToolTrust(specs []plugin.Spec, allowedTools []string) []plugin.Spec {
	if len(specs) == 0 || len(allowedTools) == 0 {
		return specs
	}
	out := make([]plugin.Spec, len(specs))
	for i, spec := range specs {
		out[i] = spec
		prefix := plugin.ToolPrefix(spec.Name)
		clonedModelNames := false
		for _, name := range allowedTools {
			name = strings.TrimSpace(name)
			if !strings.HasPrefix(name, prefix) || len(name) <= len(prefix) {
				continue
			}
			if out[i].ReadOnlyModelToolNames == nil {
				out[i].ReadOnlyModelToolNames = map[string]bool{}
				clonedModelNames = true
			} else if !clonedModelNames {
				out[i].ReadOnlyModelToolNames = cloneBoolMap(spec.ReadOnlyModelToolNames)
				clonedModelNames = true
			}
			out[i].ReadOnlyModelToolNames[name] = true
		}
	}
	return out
}

func trustedRawReadOnlyToolNames(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			out[name] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func planModeTrustedMCPServers(specs map[string]plugin.Spec) map[string]bool {
	if len(specs) == 0 {
		return nil
	}
	out := map[string]bool{}
	for name, spec := range specs {
		if len(spec.ReadOnlyToolNames) > 0 || len(spec.ReadOnlyModelToolNames) > 0 {
			out[name] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneBoolMap(in map[string]bool) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// autoShellPrefer reports whether [tools.shell] left the interpreter to
// auto-detection, so the "fell back to PowerShell" hint is suppressed once the
// user has explicitly chosen a shell.
func autoShellPrefer(prefer string) bool {
	p := strings.ToLower(strings.TrimSpace(prefer))
	return p == "" || p == "auto"
}

// MCPStartupNotice formats the warning shown when configured MCP servers failed
// to connect, naming the first few; ok is false when none failed.
func MCPStartupNotice(failures []plugin.Failure) (text string, ok bool) {
	if len(failures) == 0 {
		return "", false
	}
	names := make([]string, 0, min(len(failures), 3))
	for i, f := range failures {
		if i >= 3 {
			break
		}
		names = append(names, f.Name)
	}
	more := ""
	if len(failures) > len(names) {
		more = fmt.Sprintf(" (+%d more)", len(failures)-len(names))
	}
	return fmt.Sprintf("%d MCP server(s) failed to start: %s%s — run /mcp for details",
		len(failures), strings.Join(names, ", "), more), true
}

// LSPSpecs returns the language → server map: the built-in defaults overlaid with
// any user overrides. A user entry may set only the fields it wants to change;
// empty fields keep the default for that language.
func LSPSpecs(cfg config.LSPConfig) map[string]lsp.ServerSpec {
	specs := lsp.DefaultSpecs()
	for lang, s := range cfg.Servers {
		spec := specs[lang]
		if s.Command != "" {
			spec.Command = s.Command
		}
		if s.Args != nil {
			spec.Args = s.Args
		}
		if s.Env != nil {
			spec.Env = s.Env
		}
		if s.LanguageID != "" {
			spec.LanguageID = s.LanguageID
		}
		if s.Extensions != nil {
			spec.Extensions = s.Extensions
		}
		if s.InstallHint != "" {
			spec.InstallHint = s.InstallHint
		}
		if spec.LanguageID == "" {
			spec.LanguageID = lang
		}
		specs[lang] = spec
	}
	return specs
}

func providerNames(cfg *config.Config) string {
	names := make([]string, len(cfg.Providers))
	for i, p := range cfg.Providers {
		names[i] = p.Name
	}
	return strings.Join(names, "/")
}

// resolveSemanticIntentProvider picks a standalone provider for Room semantic
// intent classification. Resolution order:
//  1. Explicit cfg.Agent.SemanticIntentModel — must resolve, or Build fails.
//  2. Auto-plan classifier model, if configured and not reasoning-capable.
//  3. Auto-select the first configured lightweight non-reasoning chat model
//     (matching flash/mini/haiku/lite/small/nano in the model name).
//  4. nil — classification unavailable, safe degradation.
func resolveSemanticIntentProvider(cfg *config.Config, proxySpec netclient.ProxySpec) (provider.Provider, error) {
	// 1. Explicit config.
	if m := strings.TrimSpace(cfg.Agent.SemanticIntentModel); m != "" {
		entry, ok := cfg.ResolveModel(m)
		if !ok {
			return nil, fmt.Errorf("semantic_intent_model %q is not a configured provider", m)
		}
		if entry.HasCapability(config.CapReasoning) {
			return nil, fmt.Errorf("semantic_intent_model %q must be a non-reasoning model", m)
		}
		if !entry.Configured() {
			return nil, fmt.Errorf("semantic_intent_model %q is not configured", m)
		}
		prov, err := NewProviderWithProxy(entry, proxySpec)
		if err != nil {
			return nil, fmt.Errorf("semantic_intent_model %q: %w", m, err)
		}
		return prov, nil
	}

	// 2. Reuse auto-plan classifier if it has no reasoning capability.
	if ac := strings.TrimSpace(cfg.Agent.AutoPlanClassifier); ac != "" {
		if entry, ok := cfg.ResolveModel(ac); ok && entry.Configured() && !entry.HasCapability(config.CapReasoning) {
			prov, err := NewProviderWithProxy(entry, proxySpec)
			if err == nil {
				return prov, nil
			}
		}
	}

	// 3. Auto-select a lightweight non-reasoning chat model.
	for i := range cfg.Providers {
		entry := &cfg.Providers[i]
		for _, model := range entry.ModelList() {
			lower := strings.ToLower(model)
			if !isLightweightModelName(lower) {
				continue
			}
			resolved, ok := cfg.ResolveModel(entry.Name + "/" + model)
			if !ok || !resolved.Configured() || resolved.HasCapability(config.CapReasoning) {
				continue
			}
			prov, err := NewProviderWithProxy(resolved, proxySpec)
			if err == nil {
				return prov, nil
			}
		}
	}

	// 4. No qualified model — classifier unavailable.
	return nil, nil
}

// lightweightModelKeywords lists model-name substrings that indicate a
// lightweight/fast variant suitable for low-stakes classification.
var lightweightModelKeywords = []string{
	"flash",
	"mini",
	"haiku",
	"lite",
	"small",
	"nano",
}

func isLightweightModelName(lower string) bool {
	for _, kw := range lightweightModelKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// ── Boot SkillResolver adapter ────────────────────────────────────────────

type bootSkillResolver struct {
	store *skill.Store
}

func newBootSkillResolver(store *skill.Store) work.SkillResolver {
	return &bootSkillResolver{store: store}
}

func (r *bootSkillResolver) Resolve(ctx context.Context, name string) (work.SkillBody, error) {
	sk, ok := r.store.Read(name)
	if !ok {
		return work.SkillBody{}, fmt.Errorf("skill %q not found", name)
	}
	if sk.Name == "" {
		return work.SkillBody{}, fmt.Errorf("skill %q resolved with empty name", name)
	}
	return work.SkillBody{
		Name:        sk.Name,
		Description: sk.Description,
		Body:        sk.Body,
		Enabled:     true, // store.Read already filters disabled names
	}, nil
}
