package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRoleCallsCarryFrozenAssistantWorkspace(t *testing.T) {
	store := testStore(t, t.TempDir())
	workspace := t.TempDir()
	snapshot, err := store.Create(CreateInput{
		RequestID: "create-role-context",
		Assistant: Assistant{
			ID: "helper-role-context", Name: "Helper", Mission: "Keep the workspace healthy",
			Scope: ScopeWorkspace, WorkspaceRoot: workspace, Lifecycle: LifecycleActive, Policy: DefaultPolicy(),
		},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	var contexts []RoleContext
	model := RoleModelFunc(func(ctx context.Context, prompt string) (string, error) {
		role, ok := RoleContextFrom(ctx)
		if !ok {
			t.Fatal("role model call has no Assistant context")
		}
		contexts = append(contexts, role)
		switch {
		case strings.HasPrefix(prompt, "你是长期助手的 Dispatcher"):
			return `{"kind":"task","reply":"ok","jobs":[]}`, nil
		case strings.HasPrefix(prompt, "你是长期助手的 Reflector"):
			return `{"conclusion":"done"}`, nil
		case strings.HasPrefix(prompt, "你是长期助手的 Ideator"):
			return `{"summary":"try another route"}`, nil
		default:
			t.Fatalf("unexpected role prompt: %s", prompt)
			return "", nil
		}
	})

	dispatcher, _ := NewDispatcher(store, model)
	dispatch, err := dispatcher.Dispatch(context.Background(), OpenDispatchInput{
		AssistantID: snapshot.Assistant.ID, RequestID: "dispatch-role-context", Input: "scan", Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDispatchExecuted(MarkDispatchExecutedInput{
		AssistantID: snapshot.Assistant.ID, DispatchID: dispatch.ID, RequestID: "execute-role-context", Now: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	reflector, _ := NewReflector(store, model)
	if _, err := reflector.Reflect(context.Background(), snapshot.Assistant.ID, dispatch.ID, "reflect-role-context", time.Now()); err != nil {
		t.Fatal(err)
	}
	ideator, _ := NewIdeator(store, model)
	if _, err := ideator.Ideate(context.Background(), OpenIdeaInput{
		AssistantID: snapshot.Assistant.ID, RequestID: "idea-role-context", Trigger: IdeaTriggerManual, Now: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if len(contexts) != 3 {
		t.Fatalf("role contexts = %d, want 3", len(contexts))
	}
	for _, role := range contexts {
		if role.AssistantID != snapshot.Assistant.ID || role.Scope != ScopeWorkspace || role.WorkspaceRoot != workspace {
			t.Fatalf("role context = %+v, want Assistant workspace %q", role, workspace)
		}
	}
}
