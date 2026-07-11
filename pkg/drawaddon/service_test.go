package drawaddon

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/config"
)

func TestSaveMultipleProviders(t *testing.T) {
	home := t.TempDir()
	s := New(home)

	if _, err := s.Save(context.Background(), ProviderInput{
		ID:      "api-main",
		Enabled: true,
		Mode:    ModeAPI,
		BaseURL: "https://images.example.com/v1/images/generations",
		Model:   "image-test",
	}); err != nil {
		t.Fatalf("save api provider: %v", err)
	}
	if _, err := s.Save(context.Background(), ProviderInput{
		ID:          "cli-local",
		Enabled:     true,
		DisplayName: "Local CLI",
		Mode:        ModeCLI,
		CLICommand:  "fake-draw",
		CLIArgs:     []string{"--prompt", "{{prompt}}"},
		OutputDir:   "renders",
	}); err != nil {
		t.Fatalf("save cli provider: %v", err)
	}

	got, err := s.Providers()
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("providers len = %d, want 2", len(got))
	}
	if got[0].ID != "api-main" || got[1].ID != "cli-local" {
		t.Fatalf("providers sorted by id = %#v", got)
	}
	if got[0].State.Status != StatusReady || got[1].State.Status != StatusReady {
		t.Fatalf("unexpected provider states: %#v", got)
	}
}

func TestSecretRefDoesNotLeak(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_HOME", home)
	const key = "DRAWADDON_SECRET_TEST"
	const secret = "sk-secret-value"
	if _, err := config.SetCredential(key, secret); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}

	s := New(home)
	view, err := s.Save(context.Background(), ProviderInput{
		ID:        "api-secret",
		Enabled:   true,
		Mode:      ModeAPI,
		BaseURL:   "https://example.com/v1/images?token=inline-token",
		Model:     "image-test",
		APIKeyRef: key,
	})
	if err != nil {
		t.Fatalf("save provider: %v", err)
	}
	if view.AuthStatus != "set" {
		t.Fatalf("auth status = %q, want set", view.AuthStatus)
	}

	cfgPath := filepath.Join(home, "addons", "draw-tool", "config.json")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("config leaked secret value: %s", raw)
	}
	if strings.Contains(fmt.Sprintf("%#v", view), secret) {
		t.Fatalf("view leaked secret value: %#v", view)
	}
	if !strings.Contains(string(raw), key) {
		t.Fatalf("config did not keep credential ref: %s", raw)
	}
}

func TestCLIFakeGenerateSuccess(t *testing.T) {
	home := t.TempDir()
	outDir := filepath.Join(home, "images")
	t.Setenv("WorkGround2_HOME", home)
	t.Setenv("DRAWADDON_TEST_HELPER", "1")
	t.Setenv("DRAWADDON_HELPER_MODE", "success")

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	s := New(home)
	if _, err := s.Save(context.Background(), ProviderInput{
		ID:         "cli-ok",
		Enabled:    true,
		Mode:       ModeCLI,
		CLICommand: exe,
		CLIArgs:    []string{"-test.run=TestHelperProcess", "--", "--out", "{{output}}"},
		OutputDir:  outDir,
	}); err != nil {
		t.Fatalf("save provider: %v", err)
	}

	task, err := s.Generate(context.Background(), GenerateInput{ProviderID: "cli-ok", Prompt: "draw a red square"})
	if err != nil {
		t.Fatalf("generate: %v; task=%#v", err, task)
	}
	if task.Status != TaskSucceeded {
		t.Fatalf("task status = %q, want succeeded: %#v", task.Status, task)
	}
	if task.OutputPath == "" {
		t.Fatal("output path is empty")
	}
	body, err := os.ReadFile(task.OutputPath)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	if !strings.Contains(string(body), "draw a red square") {
		t.Fatalf("fake cli did not receive prompt via stdin: %s", body)
	}
}

func TestDrawImageToolUsesFirstEnabledProvider(t *testing.T) {
	home := t.TempDir()
	outDir := filepath.Join(home, "tool-images")
	t.Setenv("WorkGround2_HOME", home)
	t.Setenv("DRAWADDON_TEST_HELPER", "1")
	t.Setenv("DRAWADDON_HELPER_MODE", "success")

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	s := New(home)
	if _, err := s.Save(context.Background(), ProviderInput{
		ID:      "disabled",
		Enabled: false,
		Mode:    ModeCLI,
	}); err != nil {
		t.Fatalf("save disabled provider: %v", err)
	}
	if _, err := s.Save(context.Background(), ProviderInput{
		ID:         "cli-tool",
		Enabled:    true,
		Mode:       ModeCLI,
		CLICommand: exe,
		CLIArgs:    []string{"-test.run=TestHelperProcess", "--", "--out", "{{output}}"},
		OutputDir:  outDir,
	}); err != nil {
		t.Fatalf("save tool provider: %v", err)
	}

	result, err := NewTool(home).Execute(context.Background(), []byte(`{"prompt":"draw from tool"}`))
	if err != nil {
		t.Fatalf("tool execute: %v\n%s", err, result)
	}
	if !strings.Contains(result, `"providerId": "cli-tool"`) || !strings.Contains(result, `"status": "succeeded"`) {
		t.Fatalf("unexpected tool result:\n%s", result)
	}
	providers, err := s.Providers()
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	var outputPath string
	for _, provider := range providers {
		if provider.ID == "cli-tool" {
			outputPath = provider.State.LastOutputPath
			break
		}
	}
	if outputPath == "" {
		t.Fatalf("provider state missing output path: %#v", providers)
	}
	body, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(body), "draw from tool") {
		t.Fatalf("tool prompt did not reach cli: %s", body)
	}
}

func TestDeleteProviderIsIdempotent(t *testing.T) {
	home := t.TempDir()
	s := New(home)
	if _, err := s.Save(context.Background(), ProviderInput{
		ID:      "delete-me",
		Enabled: true,
		Mode:    ModeAPI,
		BaseURL: "https://example.com/v1/images",
		Model:   "image-test",
	}); err != nil {
		t.Fatalf("save provider: %v", err)
	}
	if _, err := s.Delete(context.Background(), "delete-me"); err != nil {
		t.Fatalf("delete first: %v", err)
	}
	if view, err := s.Delete(context.Background(), "delete-me"); err != nil {
		t.Fatalf("delete second: %v", err)
	} else if view.State.Status != StatusDisabled {
		t.Fatalf("second delete status = %q, want disabled", view.State.Status)
	}
	providers, err := s.Providers()
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	if len(providers) != 0 {
		t.Fatalf("providers after delete = %#v, want empty", providers)
	}
}

func TestCLIErrorsAreSanitized(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_HOME", home)
	t.Setenv("DRAWADDON_TEST_HELPER", "1")
	t.Setenv("DRAWADDON_HELPER_MODE", "fail")
	const key = "DRAWADDON_FAIL_KEY"
	const secret = "sk-fail-secret"
	if _, err := config.SetCredential(key, secret); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}

	s := New(home)
	if _, err := s.Save(context.Background(), ProviderInput{
		ID:         "cli-fail",
		Enabled:    true,
		Mode:       ModeCLI,
		APIKeyRef:  key,
		CLICommand: exe,
		CLIArgs:    []string{"-test.run=TestHelperProcess", "--"},
		OutputDir:  filepath.Join(home, "failed-images"),
	}); err != nil {
		t.Fatalf("save provider: %v", err)
	}

	task, err := s.Generate(context.Background(), GenerateInput{ProviderID: "cli-fail", Prompt: "fail"})
	if err == nil {
		t.Fatalf("generate unexpectedly succeeded: %#v", task)
	}
	if task.Status != TaskFailed {
		t.Fatalf("task status = %q, want failed", task.Status)
	}
	for _, forbidden := range []string{secret, "user:" + secret, "token=" + secret} {
		if strings.Contains(task.Error, forbidden) || strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaked %q: task=%q err=%q", forbidden, task.Error, err.Error())
		}
	}
	if !strings.Contains(task.Error, "<redacted>") {
		t.Fatalf("sanitized error did not include redaction marker: %q", task.Error)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("DRAWADDON_TEST_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	switch os.Getenv("DRAWADDON_HELPER_MODE") {
	case "success":
		prompt, _ := io.ReadAll(os.Stdin)
		out := ""
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--out" {
				out = args[i+1]
				break
			}
		}
		if out == "" {
			fmt.Fprintln(os.Stderr, "missing --out")
			os.Exit(2)
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := os.WriteFile(out, []byte("prompt="+string(prompt)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println(out)
		os.Exit(0)
	case "fail":
		secret := os.Getenv("DRAW_ADDON_API_KEY")
		fmt.Fprintf(os.Stderr, "failed with %s https://user:%s@example.com/v1?token=%s\n", secret, secret, secret)
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode")
		os.Exit(2)
	}
}
