package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"testing"
	"time"

	"workground2/internal/config"
	"workground2/internal/control"
)

func TestRetryWidgetConversationRetriesFiveTimesWithSameInput(t *testing.T) {
	input := WidgetConversationInput{Prompt: "fix it", RequestID: "req-1", Workspace: "global"}
	var calls []WidgetConversationInput
	var delays []time.Duration

	result := retryWidgetConversation(input, func(got WidgetConversationInput) WidgetConversationResult {
		calls = append(calls, got)
		return WidgetConversationResult{Status: "retryable_error", Error: "timeout"}
	}, func(delay time.Duration) {
		delays = append(delays, delay)
	})

	if result.Status != "retryable_error" || result.Error != "timeout" {
		t.Fatalf("result = %+v, want final retryable error", result)
	}
	if len(calls) != 6 {
		t.Fatalf("calls = %d, want 6 (initial + 5 retries)", len(calls))
	}
	for i, got := range calls {
		if !reflect.DeepEqual(got, input) {
			t.Fatalf("call %d input = %+v, want %+v", i, got, input)
		}
	}
	wantDelays := []time.Duration{
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1600 * time.Millisecond,
		3200 * time.Millisecond,
	}
	if !reflect.DeepEqual(delays, wantDelays) {
		t.Fatalf("delays = %v, want %v", delays, wantDelays)
	}
}

func TestRetryWidgetConversationStopsAfterSuccess(t *testing.T) {
	calls := 0
	result := retryWidgetConversation(WidgetConversationInput{RequestID: "req-2"}, func(WidgetConversationInput) WidgetConversationResult {
		calls++
		if calls < 3 {
			return WidgetConversationResult{Status: "retryable_error"}
		}
		return WidgetConversationResult{Status: "accepted"}
	}, func(time.Duration) {})

	if result.Status != "accepted" || calls != 3 {
		t.Fatalf("result = %+v, calls = %d; want accepted after 3 calls", result, calls)
	}
}

func TestRetryWidgetConversationDoesNotRetryTerminalResult(t *testing.T) {
	for _, status := range []string{"accepted", "already_applied", "invalid", "unknown"} {
		t.Run(status, func(t *testing.T) {
			calls := 0
			result := retryWidgetConversation(WidgetConversationInput{}, func(WidgetConversationInput) WidgetConversationResult {
				calls++
				return WidgetConversationResult{Status: status}
			}, func(time.Duration) { t.Fatal("terminal result must not sleep") })
			if result.Status != status || calls != 1 {
				t.Fatalf("result = %+v, calls = %d; want one terminal call", result, calls)
			}
		})
	}
}

func TestApplyWidgetConversationDefaultsRefreshesReusableBlankTab(t *testing.T) {
	isolateDesktopUserDirs(t)
	cfg := config.Default()
	cfg.DefaultModel = "new/new-model"
	cfg.Desktop.ProviderAccess = []string{"old", "new"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model"},
		{Name: "new", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "new-model"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:               "blank",
		Scope:            "global",
		WorkspaceRoot:    globalWorkspaceRoot(),
		model:            "old/old-model",
		toolApprovalMode: control.ToolApprovalAsk,
		disabledMCP:      map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	if err := app.applyWidgetConversationDefaults(tab.ID, "new/new-model", control.ToolApprovalAuto); err != nil {
		t.Fatalf("applyWidgetConversationDefaults: %v", err)
	}
	if tab.pendingModel != "new/new-model" {
		t.Fatalf("pending model = %q, want user default", tab.pendingModel)
	}
	if tab.toolApprovalMode != control.ToolApprovalAuto {
		t.Fatalf("approval mode = %q, want user default auto", tab.toolApprovalMode)
	}
}

func TestWidgetConversationRejectsUnknownModelSelection(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = context.Background()

	result := app.startWidgetConversationOnce(WidgetConversationInput{
		Prompt: "fix it", RequestID: "req-model", Workspace: "global",
		Model: "ghost/ghost-model",
	})
	if result.Status != "invalid" {
		t.Fatalf("status = %q, want invalid for an unconfigured model", result.Status)
	}
	if result.Error == "" {
		t.Fatal("missing model must surface an explicit error")
	}
}

func TestWidgetConversationModelApprovalGateIsIdempotentAndStrict(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = context.Background()

	seed := widgetConversationReceipt{
		RequestID: "req-gate", PromptHash: fmt.Sprintf("%x", sha256.Sum256([]byte("fix it"))),
		WorkspaceSelection: "global", Model: "deepseek/deepseek-v4", ToolApprovalMode: control.ToolApprovalAuto,
		Scope: "global", WorkspaceName: "Global", Status: "submitted",
	}
	if err := app.saveWidgetConversationReceipt(seed); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}

	// Same intent retry stays idempotent (the gate accepts the exact retry).
	same := app.startWidgetConversationOnce(WidgetConversationInput{
		Prompt: "fix it", RequestID: "req-gate", Workspace: "global",
		Model: "deepseek/deepseek-v4", ApprovalMode: control.ToolApprovalAuto,
	})
	if same.Status != "already_applied" {
		t.Fatalf("same-intent retry = %+v, want already_applied", same)
	}

	// A model change on the same requestId is an explicit error, never a silent
	// reuse of the old selection.
	changedModel := app.startWidgetConversationOnce(WidgetConversationInput{
		Prompt: "fix it", RequestID: "req-gate", Workspace: "global",
		Model: "deepseek/deepseek-v5", ApprovalMode: control.ToolApprovalAuto,
	})
	if changedModel.Status != "invalid" {
		t.Fatalf("changed model = %+v, want invalid", changedModel)
	}

	// An approval change on the same requestId is rejected too.
	changedApproval := app.startWidgetConversationOnce(WidgetConversationInput{
		Prompt: "fix it", RequestID: "req-gate", Workspace: "global",
		Model: "deepseek/deepseek-v4", ApprovalMode: control.ToolApprovalYolo,
	})
	if changedApproval.Status != "invalid" {
		t.Fatalf("changed approval = %+v, want invalid", changedApproval)
	}
}

func TestWidgetConversationEmptyModelRetryDoesNotDeadlock(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = context.Background()

	// First attempt had no usable model (empty selection), so the receipt was
	// filled with the user defaults. The retry still sends model:"" — the gate
	// must treat an empty selection as "use defaults", never as a change.
	seed := widgetConversationReceipt{
		RequestID: "req-empty-model", PromptHash: fmt.Sprintf("%x", sha256.Sum256([]byte("fix it"))),
		WorkspaceSelection: "global", Model: "default/default-model", ToolApprovalMode: control.ToolApprovalAuto,
		Scope: "global", WorkspaceName: "Global", Status: "submitted",
	}
	if err := app.saveWidgetConversationReceipt(seed); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}

	retry := app.startWidgetConversationOnce(WidgetConversationInput{
		Prompt: "fix it", RequestID: "req-empty-model", Workspace: "global",
		ApprovalMode: control.ToolApprovalAuto,
	})
	if retry.Status != "already_applied" {
		t.Fatalf("empty-model retry = %+v, want already_applied (no deadlock)", retry)
	}
}

func TestWidgetModelRefExists(t *testing.T) {
	models := []ModelInfo{
		{Ref: "deepseek/deepseek-v4", Provider: "deepseek", Model: "deepseek-v4"},
		{Ref: "openai/gpt-5", Provider: "openai", Model: "gpt-5"},
	}
	for _, ref := range []string{"deepseek/deepseek-v4", "openai/gpt-5"} {
		if !widgetModelRefExists(models, ref) {
			t.Fatalf("ref %q should resolve", ref)
		}
	}
	for _, ref := range []string{"", "  ", "ghost/ghost", "deepseek/ deepseek-v4"} {
		if widgetModelRefExists(models, ref) {
			t.Fatalf("ref %q must not resolve", ref)
		}
	}
}

func TestWidgetApprovalModePreservesOptionalDefault(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"", ""},
		{"  ", ""},
		{control.ToolApprovalAsk, control.ToolApprovalAsk},
		{control.ToolApprovalAuto, control.ToolApprovalAuto},
		{control.ToolApprovalYolo, control.ToolApprovalYolo},
	} {
		got, err := widgetApprovalMode(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("widgetApprovalMode(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
	if _, err := widgetApprovalMode("sometimes"); err == nil {
		t.Fatal("unknown approval mode must fail explicitly")
	}
}
