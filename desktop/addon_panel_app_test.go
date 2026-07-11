package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workground2/internal/control"
)

func TestAddOnPanelFlowSkillShareSyncRefreshesSlashSkills(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repo/WorkGround2-plugin.json":
			w.Header().Set("ETag", `"manifest-v1"`)
			_, _ = w.Write([]byte(`{"name":"flow-skills","version":"1.0.0","skills":"skills"}` + "\n"))
		case "/repo/skills/.flow-skill-index.json":
			_, _ = w.Write([]byte(`{"files":["demo/SKILL.md"]}` + "\n"))
		case "/repo/skills/demo/SKILL.md":
			w.Header().Set("ETag", `"skill-v1"`)
			_, _ = w.Write([]byte("---\nname: demo\ndescription: Remote demo\n---\n\n# Demo\n\nFetched on demand.\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	project := t.TempDir()
	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	old := control.New(control.Options{Label: "old-controller", WorkspaceRoot: project})
	app.setTestCtrl(old, "deepseek-flash/deepseek-v4-flash")
	tab := app.activeTab()
	tab.Scope = "project"
	tab.WorkspaceRoot = project
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	saveRes, err := app.AddOnPanelAction("FlowSkillShare", "sources", "flow-skill-share/profiles.json", AddOnPanelActionInput{
		ActionID: "save",
		Form: map[string]any{
			"id":      "team",
			"enabled": true,
			"gitUrl":  server.URL + "/repo",
			"path":    ".",
		},
	})
	if err != nil {
		t.Fatalf("AddOnPanelAction save: %v", err)
	}
	if saveRes.Error != "" {
		t.Fatalf("AddOnPanelAction save error = %q", saveRes.Error)
	}

	syncRes, err := app.AddOnPanelAction("FlowSkillShare", "sources", "flow-skill-share/profiles.json", AddOnPanelActionInput{
		ActionID: "sync",
		RecordID: "team",
	})
	if err != nil {
		t.Fatalf("AddOnPanelAction sync: %v", err)
	}
	if syncRes.Error != "" {
		t.Fatalf("AddOnPanelAction sync error = %q", syncRes.Error)
	}

	foundCommand := false
	for _, cmd := range app.Commands() {
		if cmd.Kind == "skill" && cmd.Name == "demo" {
			foundCommand = true
			break
		}
	}
	if !foundCommand {
		t.Fatalf("slash Commands() missing remote demo skill: %+v", app.Commands())
	}

	sent, ok := app.activeCtrl().RunSkill("/demo extra")
	if !ok {
		t.Fatal("RunSkill(/demo) should resolve the synced FlowSkillShare skill")
	}
	if !strings.Contains(sent, "Fetched on demand.") {
		t.Fatalf("RunSkill(/demo) body = %q, want remote body", sent)
	}
}
