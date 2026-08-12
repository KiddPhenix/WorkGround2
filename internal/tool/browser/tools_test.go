package browsertool_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"workground2/internal/browser"
	"workground2/internal/jobs"
	browsertool "workground2/internal/tool/browser"
)

type fakeService struct {
	openFn     func(ctx context.Context, owner string, req browser.OpenRequest) (browser.OpenResult, error)
	stateFn    func(ctx context.Context, owner string, req browser.StateRequest) (browser.PageState, error)
	clickFn    func(ctx context.Context, owner string, req browser.ClickRequest) (browser.ActionResult, error)
	navigateFn func(ctx context.Context, owner string, req browser.NavigateRequest) (browser.ActionResult, error)
	typeFn     func(ctx context.Context, owner string, req browser.TypeRequest) (browser.ActionResult, error)
	scrollFn   func(ctx context.Context, owner string, req browser.ScrollRequest) (browser.ActionResult, error)
	tabFn      func(ctx context.Context, owner string, req browser.TabRequest) (browser.ActionResult, error)
	closeFn    func(ctx context.Context, owner string) (browser.CloseResult, error)
}

func (s *fakeService) Open(ctx context.Context, owner string, req browser.OpenRequest) (browser.OpenResult, error) {
	return s.openFn(ctx, owner, req)
}
func (s *fakeService) Navigate(ctx context.Context, owner string, req browser.NavigateRequest) (browser.ActionResult, error) {
	return s.navigateFn(ctx, owner, req)
}
func (s *fakeService) State(ctx context.Context, owner string, req browser.StateRequest) (browser.PageState, error) {
	return s.stateFn(ctx, owner, req)
}
func (s *fakeService) Click(ctx context.Context, owner string, req browser.ClickRequest) (browser.ActionResult, error) {
	return s.clickFn(ctx, owner, req)
}
func (s *fakeService) Type(ctx context.Context, owner string, req browser.TypeRequest) (browser.ActionResult, error) {
	return s.typeFn(ctx, owner, req)
}
func (s *fakeService) Scroll(ctx context.Context, owner string, req browser.ScrollRequest) (browser.ActionResult, error) {
	return s.scrollFn(ctx, owner, req)
}
func (s *fakeService) Tab(ctx context.Context, owner string, req browser.TabRequest) (browser.ActionResult, error) {
	return s.tabFn(ctx, owner, req)
}
func (s *fakeService) CloseSession(ctx context.Context, owner string) (browser.CloseResult, error) {
	return s.closeFn(ctx, owner)
}
func (s *fakeService) Close() error { return nil }

func newFakeService() *fakeService {
	return &fakeService{
		openFn: func(ctx context.Context, owner string, req browser.OpenRequest) (browser.OpenResult, error) {
			return browser.OpenResult{SessionID: "test-session", Created: true, Revision: 1, URL: req.URL}, nil
		},
		stateFn: func(ctx context.Context, owner string, req browser.StateRequest) (browser.PageState, error) {
			return browser.PageState{
				SessionID: "test-session",
				Revision:  1,
				URL:       "https://example.com",
				Title:     "Test",
				Elements:  []browser.Element{{Index: 1, Tag: "button", Role: "button"}},
			}, nil
		},
		clickFn: func(ctx context.Context, owner string, req browser.ClickRequest) (browser.ActionResult, error) {
			return browser.ActionResult{BeforeRevision: 1, AfterRevision: 2, URL: "https://example.com"}, nil
		},
		navigateFn: func(ctx context.Context, owner string, req browser.NavigateRequest) (browser.ActionResult, error) {
			return browser.ActionResult{BeforeRevision: 1, AfterRevision: 2, URL: req.URL}, nil
		},
		typeFn: func(ctx context.Context, owner string, req browser.TypeRequest) (browser.ActionResult, error) {
			return browser.ActionResult{BeforeRevision: 1, AfterRevision: 2}, nil
		},
		scrollFn: func(ctx context.Context, owner string, req browser.ScrollRequest) (browser.ActionResult, error) {
			return browser.ActionResult{BeforeRevision: 1, AfterRevision: 2}, nil
		},
		tabFn: func(ctx context.Context, owner string, req browser.TabRequest) (browser.ActionResult, error) {
			return browser.ActionResult{BeforeRevision: 1, AfterRevision: 2}, nil
		},
		closeFn: func(ctx context.Context, owner string) (browser.CloseResult, error) {
			return browser.CloseResult{SessionID: "test-session", Closed: true}, nil
		},
	}
}

func TestToolsExist(t *testing.T) {
	svc := newFakeService()
	tools := browsertool.NewTools(svc)
	if len(tools) != 8 {
		t.Fatalf("expected 8 tools, got %d", len(tools))
	}
	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name()] = true
	}
	expected := []string{
		"browser_open", "browser_navigate", "browser_state",
		"browser_click", "browser_type", "browser_scroll",
		"browser_tab", "browser_close",
	}
	for _, n := range expected {
		if !names[n] {
			t.Errorf("missing tool: %s", n)
		}
	}
}

func TestStateIsReadOnly(t *testing.T) {
	svc := newFakeService()
	for _, tool := range browsertool.NewTools(svc) {
		if tool.Name() == "browser_state" {
			if !tool.ReadOnly() {
				t.Error("browser_state must be ReadOnly")
			}
			pc, ok := tool.(interface{ PlanModeSafe() bool })
			if !ok || !pc.PlanModeSafe() {
				t.Error("browser_state must be PlanModeSafe")
			}
		} else {
			if tool.ReadOnly() {
				t.Errorf("%s must not be ReadOnly", tool.Name())
			}
		}
	}
}

func TestOpenSchema(t *testing.T) {
	svc := newFakeService()
	tools := browsertool.NewTools(svc)
	var openTool interface{ Schema() json.RawMessage }
	for _, t := range tools {
		if t.Name() == "browser_open" {
			openTool = t
			break
		}
	}
	schema := openTool.Schema()
	var s map[string]interface{}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("invalid schema: %v", err)
	}
	if s["type"] != "object" {
		t.Error("schema must be type=object")
	}
}

func TestExecuteReturnsEnvelope(t *testing.T) {
	svc := newFakeService()
	tools := browsertool.NewTools(svc)
	for _, tool := range tools {
		if tool.Name() == "browser_open" {
			output, err := tool.Execute(context.Background(), json.RawMessage(`{"request_id":"r1","url":"https://example.com"}`))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(output, `"ok":true`) {
				t.Errorf("expected ok:true in output, got: %s", output)
			}
			if !strings.Contains(output, `"result":`) {
				t.Errorf("expected result in output, got: %s", output)
			}
			break
		}
	}
}

func TestExecuteErrorReturnsBothJSONAndError(t *testing.T) {
	svc := newFakeService()
	svc.stateFn = func(ctx context.Context, owner string, req browser.StateRequest) (browser.PageState, error) {
		return browser.PageState{}, browser.NewError(browser.ErrBrowserNotOpen, "not open", nil)
	}
	tools := browsertool.NewTools(svc)
	for _, tool := range tools {
		if tool.Name() == "browser_state" {
			output, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
			if err == nil {
				t.Fatal("expected non-nil error for browser error")
			}
			if !strings.Contains(output, `"ok":false`) {
				t.Errorf("expected ok:false, got: %s", output)
			}
			if !strings.Contains(output, `"error":`) {
				t.Errorf("expected error field, got: %s", output)
			}
			break
		}
	}
}

func TestCloseSchemaNoRequired(t *testing.T) {
	svc := newFakeService()
	tools := browsertool.NewTools(svc)
	for _, tool := range tools {
		if tool.Name() == "browser_close" {
			schema := tool.Schema()
			var s map[string]interface{}
			json.Unmarshal(schema, &s)
			if req, ok := s["required"]; ok && req != nil {
				t.Error("browser_close must have no required fields")
			}
			break
		}
	}
}

func TestScrollAllowsIndexZero(t *testing.T) {
	svc := newFakeService()
	tools := browsertool.NewTools(svc)
	for _, tool := range tools {
		if tool.Name() == "browser_scroll" {
			schema := tool.Schema()
			var s map[string]interface{}
			json.Unmarshal(schema, &s)
			props := s["properties"].(map[string]interface{})
			indexProp := props["index"].(map[string]interface{})
			if min, ok := indexProp["minimum"].(float64); !ok || min != 0 {
				t.Errorf("scroll index minimum must be 0, got %v", indexProp["minimum"])
			}
			break
		}
	}
}

func TestNewToolsAllHaveSchema(t *testing.T) {
	svc := newFakeService()
	tools := browsertool.NewTools(svc)
	for _, tool := range tools {
		schema := tool.Schema()
		if len(schema) == 0 {
			t.Errorf("%s has empty schema", tool.Name())
		}
		var s map[string]interface{}
		if err := json.Unmarshal(schema, &s); err != nil {
			t.Errorf("%s schema is not valid JSON: %v", tool.Name(), err)
		}
	}
}

func TestToolTraitsAndSchemas(t *testing.T) {
	for _, bt := range browsertool.NewTools(newFakeService()) {
		var schema map[string]any
		if err := json.Unmarshal(bt.Schema(), &schema); err != nil {
			t.Fatalf("%s schema: %v", bt.Name(), err)
		}
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("%s must be a closed object schema", bt.Name())
		}
		classifier, ok := bt.(interface{ PlanModeSafe() bool })
		if !ok {
			t.Fatalf("%s does not explicitly classify plan mode", bt.Name())
		}
		wantRead := bt.Name() == "browser_state"
		if bt.ReadOnly() != wantRead || classifier.PlanModeSafe() != wantRead {
			t.Errorf("%s traits readOnly=%v planSafe=%v, want %v", bt.Name(), bt.ReadOnly(), classifier.PlanModeSafe(), wantRead)
		}
		if bt.Name() == "browser_tab" {
			branches, ok := schema["oneOf"].([]any)
			if !ok {
				t.Error("browser_tab schema must use oneOf for action-specific fields")
				continue
			}
			if len(branches) != 3 {
				t.Fatalf("browser_tab oneOf branches = %d, want 3", len(branches))
			}
			for _, index := range []int{1, 2} {
				branch, ok := branches[index].(map[string]any)
				if !ok {
					t.Fatalf("browser_tab branch %d is not an object", index)
				}
				required, ok := branch["required"].([]any)
				if !ok || len(required) != 1 || required[0] != "tab_id" {
					t.Errorf("browser_tab branch %d must require tab_id, got %v", index, branch["required"])
				}
			}
		}
	}
}

func TestTypeTextLimitCountsUnicodeCharacters(t *testing.T) {
	svc := newFakeService()
	called := false
	svc.typeFn = func(ctx context.Context, owner string, req browser.TypeRequest) (browser.ActionResult, error) {
		called = true
		return browser.ActionResult{RequestID: req.RequestID}, nil
	}
	var typeTool interface {
		Execute(context.Context, json.RawMessage) (string, error)
	}
	for _, candidate := range browsertool.NewTools(svc) {
		if candidate.Name() == "browser_type" {
			typeTool = candidate
			break
		}
	}
	text := strings.Repeat("界", 20000)
	args, err := json.Marshal(map[string]any{
		"revision": 1, "index": 1, "text": text, "request_id": "unicode-limit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := typeTool.Execute(context.Background(), args); err != nil {
		t.Fatalf("20000 Unicode characters should be accepted: %v", err)
	}
	if !called {
		t.Fatal("browser_type did not reach service")
	}
}

func TestInvalidArgumentsAlwaysReturnEnvelope(t *testing.T) {
	tools := map[string]json.RawMessage{
		"browser_open":     json.RawMessage(`{"request_id":"","extra":1}`),
		"browser_navigate": json.RawMessage(`{"request_id":"r","url":""}`),
		"browser_state":    json.RawMessage(`{"max_chars":2}`),
		"browser_click":    json.RawMessage(`{"revision":0,"index":0,"request_id":"r"}`),
		"browser_type":     json.RawMessage(`{"revision":1,"index":1,"text":"","request_id":"r"}`),
		"browser_scroll":   json.RawMessage(`{"revision":1,"delta_y":0,"request_id":"r"}`),
		"browser_tab":      json.RawMessage(`{"revision":1,"action":"activate","request_id":"r"}`),
		"browser_close":    json.RawMessage(`{"unexpected":true}`),
	}
	for _, bt := range browsertool.NewTools(newFakeService()) {
		output, err := bt.Execute(context.Background(), tools[bt.Name()])
		if err == nil {
			t.Errorf("%s: expected validation error", bt.Name())
			continue
		}
		if !strings.Contains(output, `"ok":false`) || !strings.Contains(output, `"code":"invalid_arguments"`) {
			t.Errorf("%s: invalid error envelope %q", bt.Name(), output)
		}
	}
}

func TestToolsPassCurrentJobsOwner(t *testing.T) {
	svc := newFakeService()
	svc.openFn = func(_ context.Context, owner string, _ browser.OpenRequest) (browser.OpenResult, error) {
		if owner != "owner-42" {
			return browser.OpenResult{}, fmt.Errorf("owner=%q", owner)
		}
		return browser.OpenResult{SessionID: "ok"}, nil
	}
	ctx := jobs.WithSession(context.Background(), "owner-42")
	for _, bt := range browsertool.NewTools(svc) {
		if bt.Name() != "browser_open" {
			continue
		}
		if _, err := bt.Execute(ctx, json.RawMessage(`{"request_id":"owner-test"}`)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWrappedBrowserErrorKeepsStructuredCode(t *testing.T) {
	svc := newFakeService()
	svc.stateFn = func(context.Context, string, browser.StateRequest) (browser.PageState, error) {
		return browser.PageState{}, fmt.Errorf("observe: %w", browser.NewError(browser.ErrStaleState, "refresh", nil))
	}
	for _, bt := range browsertool.NewTools(svc) {
		if bt.Name() != "browser_state" {
			continue
		}
		output, err := bt.Execute(context.Background(), json.RawMessage(`{}`))
		if err == nil || !strings.Contains(output, `"code":"stale_state"`) {
			t.Fatalf("output=%q err=%v", output, err)
		}
	}
}

func TestNoCredentialToolRegistered(t *testing.T) {
	for _, bt := range browsertool.NewTools(newFakeService()) {
		if strings.Contains(bt.Name(), "credential") || strings.Contains(bt.Name(), "password") {
			t.Fatalf("V1 must not register credential tool: %s", bt.Name())
		}
	}
}
