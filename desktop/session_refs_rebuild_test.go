package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/work"
)

type recordingSessionRefStore struct {
	work.SessionRefStore
	mu     sync.Mutex
	scopes []string
}

func (s *recordingSessionRefStore) RebuildScope(scopeID string, works []work.WorkProjectionSummary) error {
	s.mu.Lock()
	s.scopes = append(s.scopes, scopeID)
	s.mu.Unlock()
	return s.SessionRefStore.RebuildScope(scopeID, works)
}

func (s *recordingSessionRefStore) rebuiltScopes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.scopes...)
}

func TestSettingsRebuildKeepsAppSessionRefStore(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectRoot := t.TempDir()
	writeWorkEnabledConfig(t, projectRoot)

	refPath := filepath.Join(t.TempDir(), "work-session-refs-v1.json")
	base, err := work.NewFileSessionRefStore(refPath, work.WithRetention(0))
	if err != nil {
		t.Fatal(err)
	}
	refs := &recordingSessionRefStore{SessionRefStore: base}
	sessionPath := filepath.Join(t.TempDir(), "owned.jsonl")
	owner := work.SessionOwner{
		OwnerType: work.OwnerWork,
		OwnerID:   "work-existing",
		ScopeID:   "another-work-store",
		WorkID:    "work-existing",
		State:     work.OwnerActive,
	}
	if err := refs.AcquireRef(sessionPath, owner, "settings-rebuild-owner"); err != nil {
		t.Fatal(err)
	}
	if err := refs.RecordCleanupPending(sessionPath, "force purge", "settings-rebuild-cleanup"); err != nil {
		t.Fatal(err)
	}
	if err := refs.UpdateCleanupPending(sessionPath, "settings-rebuild-cleanup", "failed", "retry me", nil); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	app.setSessionRefStore(refs)
	old := control.New(control.Options{
		WorkspaceRoot: projectRoot,
		SessionDir:    desktopSessionDir(projectRoot),
	})
	app.setTestCtrl(old, "deepseek-flash/deepseek-v4-flash")
	tab := app.activeTab()
	tab.Scope = "project"
	tab.WorkspaceRoot = projectRoot
	defer func() {
		if ctrl := app.activeCtrl(); ctrl != nil {
			ctrl.Close()
		}
	}()

	if err := app.rebuild(); err != nil {
		t.Fatalf("settings rebuild: %v", err)
	}
	if app.sessionRefs != refs {
		t.Fatal("settings rebuild replaced the App SessionRef store")
	}
	scopes := refs.rebuiltScopes()
	if len(scopes) != 1 || scopes[0] != work.SessionRefScopeID(config.ProjectWorkDir(projectRoot)) {
		t.Fatalf("SessionRef rebuild scopes = %v", scopes)
	}
	reopened, err := work.NewFileSessionRefStore(refPath, work.WithRetention(0))
	if err != nil {
		t.Fatal(err)
	}
	if referenced, err := reopened.IsReferenced(sessionPath); err != nil || !referenced {
		t.Fatalf("owner/ref lost across settings rebuild: referenced=%v err=%v", referenced, err)
	}
	pending, ok, err := reopened.GetCleanupPending(sessionPath, "settings-rebuild-cleanup")
	if err != nil || !ok || pending.Stage != "failed" || pending.Error != "retry me" {
		t.Fatalf("cleanup-pending lost across settings rebuild: pending=%+v ok=%v err=%v", pending, ok, err)
	}
	workViewCtrl, ok := app.activeCtrl().(interface {
		WorkViews() *control.WorkViewBroadcaster
	})
	if !ok || workViewCtrl.WorkViews() == nil {
		t.Fatal("settings rebuild did not enable the Work service")
	}
}

func TestSettingsRebuildSessionRefInitErrorFailsClosed(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectRoot := t.TempDir()
	writeWorkEnabledConfig(t, projectRoot)

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	initErr := errors.New("injected SessionRef init failure")
	app.sessionRefs = nil
	app.sessionRefsErr = initErr
	old := control.New(control.Options{
		WorkspaceRoot: projectRoot,
		SessionDir:    desktopSessionDir(projectRoot),
	})
	app.setTestCtrl(old, "deepseek-flash/deepseek-v4-flash")
	tab := app.activeTab()
	tab.Scope = "project"
	tab.WorkspaceRoot = projectRoot
	defer old.Close()

	err := app.rebuild()
	if !errors.Is(err, initErr) || !strings.Contains(err.Error(), "initialize Work Session refs") {
		t.Fatalf("settings rebuild error = %v", err)
	}
	if app.activeCtrl() != old {
		t.Fatal("failed settings rebuild replaced the existing controller")
	}
	if guardErr := app.requireSessionRefs(); !errors.Is(guardErr, initErr) {
		t.Fatalf("purge guard error = %v", guardErr)
	}
}

func TestDesktopBootBuildsInjectAppSessionRefStore(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	builds := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isBootBuildCall(call) {
				return true
			}
			builds++
			options := bootOptionsLiteral(call)
			if options == nil {
				t.Errorf("%s: boot.Build must receive a keyed boot.Options literal", fset.Position(call.Pos()))
				return true
			}
			for field, selector := range map[string]string{
				"SessionRefs":    "sessionRefs",
				"SessionRefsErr": "sessionRefsErr",
			} {
				value := keyedField(options, field)
				if !isAppSelector(value, selector) {
					t.Errorf("%s: boot.Build %s must use a.%s", fset.Position(call.Pos()), field, selector)
				}
			}
			return true
		})
	}
	if builds == 0 {
		t.Fatal("no production Desktop boot.Build calls found")
	}
}

func writeWorkEnabledConfig(t *testing.T, root string) {
	t.Helper()
	content := "config_version = 3\ndefault_model = \"deepseek-flash\"\n\n[work]\nenabled = true\n"
	if err := os.WriteFile(filepath.Join(root, "WorkGround2.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func isBootBuildCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Build" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "boot"
}

func bootOptionsLiteral(call *ast.CallExpr) *ast.CompositeLit {
	for _, arg := range call.Args {
		literal, ok := arg.(*ast.CompositeLit)
		if !ok {
			continue
		}
		selector, ok := literal.Type.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Options" {
			continue
		}
		pkg, ok := selector.X.(*ast.Ident)
		if ok && pkg.Name == "boot" {
			return literal
		}
	}
	return nil
}

func keyedField(literal *ast.CompositeLit, name string) ast.Expr {
	for _, elt := range literal.Elts {
		pair, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := pair.Key.(*ast.Ident)
		if ok && key.Name == name {
			return pair.Value
		}
	}
	return nil
}

func isAppSelector(expr ast.Expr, name string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	receiver, ok := selector.X.(*ast.Ident)
	return ok && receiver.Name == "a"
}
