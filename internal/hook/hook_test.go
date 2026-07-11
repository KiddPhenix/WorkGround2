package hook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	fileencoding "workground2/internal/fileutil/encoding"
	"workground2/internal/pluginpkg"
)

func writeSettings(t *testing.T, dir, json string) {
	t.Helper()
	d := filepath.Join(dir, SettingsDirname)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, SettingsFilename), []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeHookTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const sampleSettings = `{"hooks":{"PreToolUse":[{"match":"bash","command":"echo pre"}],"Stop":[{"command":"echo stop"}]}}`

func TestLoadTrustGating(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeSettings(t, proj, sampleSettings)
	writeSettings(t, home, `{"hooks":{"PostToolUse":[{"command":"echo g"}]}}`)

	// Untrusted: only the global hook loads.
	got := Load(LoadOptions{ProjectRoot: proj, HomeDir: home, Trusted: false})
	if len(got) != 1 || got[0].Scope != ScopeGlobal {
		t.Fatalf("untrusted load should be global-only, got %d %+v", len(got), got)
	}
	// Trusted: project hooks (before global) load too.
	got = Load(LoadOptions{ProjectRoot: proj, HomeDir: home, Trusted: true})
	if len(got) != 3 {
		t.Fatalf("trusted load should include project + global, got %d", len(got))
	}
	if got[0].Scope != ScopeProject {
		t.Errorf("project hooks should sort first, got %s", got[0].Scope)
	}
}

func TestLoadDecodesGB18030GlobalSettings(t *testing.T) {
	home := t.TempDir()
	body := `{"hooks":{"Stop":[{"command":"echo 中文","description":"全局"}]}}`
	path := GlobalSettingsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fileencoding.Encode(body, fileencoding.GB18030), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Load(LoadOptions{HomeDir: home})
	if len(got) != 1 || got[0].Scope != ScopeGlobal || got[0].Event != Stop || got[0].Command != "echo 中文" || got[0].Description != "全局" {
		t.Fatalf("decoded hooks = %+v", got)
	}
}

func TestLoadPermissionRequestHook(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, home, `{"hooks":{"PermissionRequest":[{"match":"bash","command":"notify"}]}}`)

	got := Load(LoadOptions{HomeDir: home})
	if len(got) != 1 {
		t.Fatalf("hooks count = %d, want 1", len(got))
	}
	if got[0].Event != PermissionRequest || got[0].Match != "bash" || got[0].Command != "notify" {
		t.Fatalf("loaded hook = %+v, want PermissionRequest/bash/notify", got[0])
	}
}

func TestLoadIncludesPluginSessionStartHook(t *testing.T) {
	home := t.TempDir()
	WorkGround2Home := filepath.Join(home, ".WorkGround2")
	root := filepath.Join(WorkGround2Home, "plugins", "superpowers")
	writeSettings(t, home, `{"hooks":{"PostToolUse":[{"command":"echo global"}]}}`)
	writeHookTestFile(t, filepath.Join(root, pluginpkg.CodexManifest), `{
  "name": "superpowers",
  "version": "6.1.0",
  "skills": "./skills/"
}`)
	writeHookTestFile(t, filepath.Join(root, "hooks", "session-start-codex"), "#!/usr/bin/env bash\necho ok\n")
	if err := pluginpkg.Upsert(WorkGround2Home, pluginpkg.InstalledPlugin{
		Name:         "superpowers",
		Root:         "plugins/superpowers",
		Version:      "6.1.0",
		ManifestKind: "codex",
		Enabled:      true,
	}); err != nil {
		t.Fatal(err)
	}

	got := Load(LoadOptions{HomeDir: home, ProjectRoot: "/workspace", Trusted: true})
	if len(got) != 2 {
		t.Fatalf("hooks = %+v, want plugin + global", got)
	}
	if got[0].Scope != ScopePlugin || got[0].Event != SessionStart {
		t.Fatalf("first hook = %+v, want plugin SessionStart", got[0])
	}
	if got[0].Env["WorkGround2_PLUGIN_NAME"] != "superpowers" || got[0].Env["WorkGround2_WORKSPACE_ROOT"] != "/workspace" {
		t.Fatalf("plugin env = %#v", got[0].Env)
	}
	if got[1].Scope != ScopeGlobal {
		t.Fatalf("second hook = %+v, want global", got[1])
	}
}

func TestWorkGround2HomeOverridesGlobalHookPaths(t *testing.T) {
	home := t.TempDir()
	WorkGround2Home := filepath.Join(t.TempDir(), "rx-home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("WorkGround2_HOME", WorkGround2Home)
	if err := os.MkdirAll(WorkGround2Home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(WorkGround2Home, SettingsFilename), []byte(`{"hooks":{"PostToolUse":[{"command":"echo rx"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSettings(t, home, `{"hooks":{"PostToolUse":[{"command":"echo old"}]}}`)

	if got := GlobalSettingsPath(""); got != filepath.Join(WorkGround2Home, SettingsFilename) {
		t.Fatalf("GlobalSettingsPath = %q, want WorkGround2 home", got)
	}
	if got := TrustPath(""); got != filepath.Join(WorkGround2Home, TrustFilename) {
		t.Fatalf("TrustPath = %q, want WorkGround2 home", got)
	}
	hooks := Load(LoadOptions{})
	if len(hooks) != 1 || hooks[0].Command != "echo rx" {
		t.Fatalf("Load hooks = %+v, want WorkGround2 home hook only", hooks)
	}
}

func TestWorkGround2HomeFallsBackToLegacyGlobalHooksAndTrust(t *testing.T) {
	home := t.TempDir()
	WorkGround2Home := filepath.Join(t.TempDir(), "rx-home")
	proj := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("WorkGround2_HOME", WorkGround2Home)
	writeSettings(t, home, `{"hooks":{"PostToolUse":[{"command":"echo old"}]}}`)

	hooks := Load(LoadOptions{})
	if len(hooks) != 1 || hooks[0].Command != "echo old" {
		t.Fatalf("Load hooks = %+v, want legacy global hook", hooks)
	}
	if hooks[0].Source != filepath.Join(home, SettingsDirname, SettingsFilename) {
		t.Fatalf("legacy hook source = %q", hooks[0].Source)
	}

	absProj, err := filepath.Abs(proj)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(trustFile{Projects: map[string]bool{absProj: true}})
	if err != nil {
		t.Fatal(err)
	}
	legacyTrust := filepath.Join(home, SettingsDirname, TrustFilename)
	if err := os.WriteFile(legacyTrust, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsTrusted(proj, "") {
		t.Fatal("legacy trust should be honored when new trust.json is absent")
	}
	if err := Trust(proj, ""); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if _, err := os.Stat(filepath.Join(WorkGround2Home, TrustFilename)); err != nil {
		t.Fatalf("Trust should write current WorkGround2 home trust file: %v", err)
	}
}

func TestProjectDefinesHooks(t *testing.T) {
	proj := t.TempDir()
	if ProjectDefinesHooks(proj) {
		t.Error("empty project should define no hooks")
	}
	writeSettings(t, proj, sampleSettings)
	if !ProjectDefinesHooks(proj) {
		t.Error("project with settings.json should define hooks")
	}
}

func TestMalformedSettingsIgnored(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, home, `{not valid json`)
	if got := Load(LoadOptions{HomeDir: home}); len(got) != 0 {
		t.Errorf("malformed settings should yield no hooks, got %d", len(got))
	}
}

func TestMatchesTool(t *testing.T) {
	pre := func(match string) ResolvedHook {
		return ResolvedHook{HookConfig: HookConfig{Match: match}, Event: PreToolUse}
	}
	if MatchesTool(pre("file"), "read_file") {
		t.Error(`anchored "file" must not match "read_file"`)
	}
	if !MatchesTool(pre(".*file"), "read_file") {
		t.Error(`".*file" should match "read_file"`)
	}
	if !MatchesTool(pre("bash"), "bash") {
		t.Error(`"bash" should match "bash"`)
	}
	if !MatchesTool(pre("*"), "anything") || !MatchesTool(pre(""), "anything") {
		t.Error(`"*"/"" should match every tool`)
	}
	if MatchesTool(pre("["), "bash") {
		t.Error("malformed regex should not fire")
	}
	perm := func(match string) ResolvedHook {
		return ResolvedHook{HookConfig: HookConfig{Match: match}, Event: PermissionRequest}
	}
	if !MatchesTool(perm("bash"), "bash") {
		t.Error(`PermissionRequest "bash" should match "bash"`)
	}
	if MatchesTool(perm("bash"), "read_file") {
		t.Error(`PermissionRequest "bash" must not match "read_file"`)
	}
	if MatchesTool(perm("["), "bash") {
		t.Error("malformed PermissionRequest regex should not fire")
	}
	// Non-tool events always match regardless of the match field.
	prompt := ResolvedHook{HookConfig: HookConfig{Match: "bash"}, Event: UserPromptSubmit}
	if !MatchesTool(prompt, "") {
		t.Error("non-tool events should always match")
	}
}

func TestDecideOutcome(t *testing.T) {
	cases := []struct {
		name  string
		event Event
		r     SpawnResult
		want  Decision
	}{
		{"pass", PreToolUse, SpawnResult{ExitCode: 0}, DecisionPass},
		{"block-exit2", PreToolUse, SpawnResult{ExitCode: 2}, DecisionBlock},
		{"exit2-nonblocking-warns", PostToolUse, SpawnResult{ExitCode: 2}, DecisionWarn},
		{"permission-exit2-warns", PermissionRequest, SpawnResult{ExitCode: 2}, DecisionWarn},
		{"other-nonzero-warns", PreToolUse, SpawnResult{ExitCode: 1}, DecisionWarn},
		{"timeout-blocking", UserPromptSubmit, SpawnResult{TimedOut: true}, DecisionBlock},
		{"permission-timeout-warns", PermissionRequest, SpawnResult{TimedOut: true}, DecisionWarn},
		{"timeout-nonblocking", Stop, SpawnResult{TimedOut: true}, DecisionWarn},
		{"spawn-error", PreToolUse, SpawnResult{SpawnErr: os.ErrNotExist}, DecisionError},
	}
	for _, c := range cases {
		if got := decideOutcome(c.event, c.r); got != c.want {
			t.Errorf("%s: decideOutcome = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestParseOutputSessionStartJSONAdditionalContext(t *testing.T) {
	out, warnings := ParseOutput(SessionStart, `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"Load conventions."}}`)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if out.AdditionalContext != "Load conventions." {
		t.Fatalf("AdditionalContext = %q, want context", out.AdditionalContext)
	}
}

func TestParseOutputSessionStartPlainText(t *testing.T) {
	out, warnings := ParseOutput(SessionStart, "  Load workspace notes.  ")
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if out.AdditionalContext != "Load workspace notes." {
		t.Fatalf("AdditionalContext = %q, want plain text", out.AdditionalContext)
	}
}

func TestParseOutputRejectsMismatchedEvent(t *testing.T) {
	out, warnings := ParseOutput(SessionStart, `{"hookSpecificOutput":{"hookEventName":"Stop","additionalContext":"wrong"}}`)
	if out.AdditionalContext != "" {
		t.Fatalf("AdditionalContext = %q, want empty", out.AdditionalContext)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one warning", warnings)
	}
}

func TestParseOutputInvalidJSONWarns(t *testing.T) {
	out, warnings := ParseOutput(SessionStart, `{"hookSpecificOutput":`)
	if out.AdditionalContext != "" {
		t.Fatalf("AdditionalContext = %q, want empty", out.AdditionalContext)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one warning", warnings)
	}
}

func TestRunStopsAtFirstBlock(t *testing.T) {
	hooks := []ResolvedHook{
		{HookConfig: HookConfig{Command: "first"}, Event: PreToolUse, Scope: ScopeProject},
		{HookConfig: HookConfig{Command: "second"}, Event: PreToolUse, Scope: ScopeProject},
	}
	var ran []string
	spawner := func(_ context.Context, in SpawnInput) SpawnResult {
		ran = append(ran, in.Command)
		return SpawnResult{ExitCode: 2} // first blocks
	}
	rep := Run(context.Background(), Payload{Event: PreToolUse, ToolName: "bash"}, hooks, spawner)
	if !rep.Blocked {
		t.Error("report should be blocked")
	}
	if len(ran) != 1 || ran[0] != "first" {
		t.Errorf("should stop after the first block, ran %v", ran)
	}
}

func TestRunFiltersByEventAndTool(t *testing.T) {
	hooks := []ResolvedHook{
		{HookConfig: HookConfig{Command: "a", Match: "bash"}, Event: PreToolUse},
		{HookConfig: HookConfig{Command: "b", Match: "read_file"}, Event: PreToolUse},
		{HookConfig: HookConfig{Command: "c"}, Event: PostToolUse},
	}
	var ran []string
	spawner := func(_ context.Context, in SpawnInput) SpawnResult {
		ran = append(ran, in.Command)
		return SpawnResult{ExitCode: 0}
	}
	Run(context.Background(), Payload{Event: PreToolUse, ToolName: "bash"}, hooks, spawner)
	if len(ran) != 1 || ran[0] != "a" {
		t.Errorf("only the matching PreToolUse hook should run, got %v", ran)
	}
}

func TestRunFiltersPermissionRequestByTool(t *testing.T) {
	hooks := []ResolvedHook{
		{HookConfig: HookConfig{Command: "a", Match: "bash"}, Event: PermissionRequest},
		{HookConfig: HookConfig{Command: "b", Match: "read_file"}, Event: PermissionRequest},
		{HookConfig: HookConfig{Command: "c"}, Event: Notification},
	}
	var ran []string
	spawner := func(_ context.Context, in SpawnInput) SpawnResult {
		ran = append(ran, in.Command)
		return SpawnResult{ExitCode: 0}
	}
	Run(context.Background(), Payload{Event: PermissionRequest, ToolName: "bash"}, hooks, spawner)
	if len(ran) != 1 || ran[0] != "a" {
		t.Errorf("only the matching PermissionRequest hook should run, got %v", ran)
	}
}

func TestTrustStore(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	if IsTrusted(proj, home) {
		t.Error("project should start untrusted")
	}
	if err := Trust(proj, home); err != nil {
		t.Fatalf("trust: %v", err)
	}
	if !IsTrusted(proj, home) {
		t.Error("project should be trusted after Trust")
	}
}

func TestDefaultSpawner(t *testing.T) {
	ctx := context.Background()
	stdoutCmd := "echo hi"
	catCmd := "cat"
	if runtime.GOOS == "windows" {
		catCmd = "findstr ."
	}
	// exit 0 with stdout
	r := DefaultSpawner(ctx, SpawnInput{Command: stdoutCmd, Timeout: 2 * time.Second})
	if r.ExitCode != 0 || !strings.Contains(r.Stdout, "hi") {
		t.Errorf("expected exit 0 / hi, got code=%d out=%q err=%v", r.ExitCode, r.Stdout, r.SpawnErr)
	}
	// exit 2 (block verdict on a gating event)
	r = DefaultSpawner(ctx, SpawnInput{Command: "exit 2", Timeout: 2 * time.Second})
	if r.ExitCode != 2 {
		t.Errorf("expected exit 2, got %d", r.ExitCode)
	}
	// stdin is delivered as the payload
	r = DefaultSpawner(ctx, SpawnInput{Command: catCmd, Stdin: "payload-here", Timeout: 2 * time.Second})
	if strings.TrimSpace(r.Stdout) != "payload-here" {
		t.Errorf("stdin not delivered: %q", r.Stdout)
	}
	// timeout kills the command
	sleepCmd := "sleep 5"
	if runtime.GOOS == "windows" {
		sleepCmd = "powershell -Command \"Start-Sleep 5\""
	}
	r = DefaultSpawner(ctx, SpawnInput{Command: sleepCmd, Timeout: 100 * time.Millisecond})
	if !r.TimedOut {
		t.Errorf("expected timeout, got %+v", r)
	}
}

func TestDefaultSpawnerOutputCap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	// Emit more than the cap; expect truncation flagged and bounded capture.
	r := DefaultSpawner(context.Background(), SpawnInput{
		Command: "yes x | head -c 400000",
		Timeout: 5 * time.Second,
	})
	if !r.Truncated {
		t.Error("oversized output should be flagged truncated")
	}
	if len(r.Stdout) > outputCapBytes {
		t.Errorf("captured output %d exceeds cap %d", len(r.Stdout), outputCapBytes)
	}
}
