package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"workground2/internal/skillshare"
	"workground2/pkg/drawaddon"
)

// AddOnPanelQueryResult is the generic response for addon panel queries.
type AddOnPanelQueryResult struct {
	Records []map[string]any `json:"records"`
	Form    map[string]any   `json:"form,omitempty"`
}

// AddOnPanelActionInput is the generic request for addon panel actions.
type AddOnPanelActionInput struct {
	ActionID string         `json:"actionId"`
	Form     map[string]any `json:"form,omitempty"`
	RecordID string         `json:"recordId,omitempty"`
	Extra    map[string]any `json:"extra,omitempty"`
}

// AddOnPanelActionResult is the generic response for addon panel actions.
type AddOnPanelActionResult struct {
	Notice string `json:"notice,omitempty"`
	Error  string `json:"error,omitempty"`
}

// AddOnPanelQuery queries an AddOn panel for its current records and form data.
// It routes to the correct backend service based on the adapter key.
func (a *App) AddOnPanelQuery(pluginName, panelID, adapter string) (AddOnPanelQueryResult, error) {
	pluginName = strings.TrimSpace(pluginName)
	adapter = strings.TrimSpace(adapter)
	if pluginName == "" {
		return AddOnPanelQueryResult{}, fmt.Errorf("plugin name is required")
	}

	// Check if the AddOn has an MCP runtime and forward there.
	if mcpServer, ok := a.mcpServerForAddOn(pluginName); ok {
		return a.mcpPanelQuery(mcpServer, adapter)
	}

	switch {
	case strings.HasSuffix(adapter, "/profiles.json"):
		return a.querySkillShareProfiles(adapter)
	case strings.HasSuffix(adapter, "/config.json"):
		return a.queryDrawAddonConfig()
	default:
		return AddOnPanelQueryResult{Records: []map[string]any{}, Form: map[string]any{}}, nil
	}
}

func (a *App) querySkillShareProfiles(adapter string) (AddOnPanelQueryResult, error) {
	var svc *skillshare.Service
	if strings.HasPrefix(adapter, "flow") {
		svc = a.flowSkillShareService()
	} else {
		svc = a.skillShareService()
	}
	profiles, err := svc.Profiles()
	if err != nil {
		return AddOnPanelQueryResult{}, err
	}
	records := make([]map[string]any, 0, len(profiles))
	for _, p := range profiles {
		records = append(records, profileViewToMap(p))
	}
	return AddOnPanelQueryResult{Records: records, Form: map[string]any{}}, nil
}

func (a *App) queryDrawAddonConfig() (AddOnPanelQueryResult, error) {
	providers, err := a.drawAddonService().Providers()
	if err != nil {
		return AddOnPanelQueryResult{}, err
	}
	records := make([]map[string]any, 0, len(providers))
	for _, p := range providers {
		records = append(records, providerViewToMap(p))
	}
	return AddOnPanelQueryResult{Records: records, Form: map[string]any{}}, nil
}

// AddOnPanelAction routes a panel action to the correct backend service.
func (a *App) AddOnPanelAction(pluginName, panelID, adapter string, action AddOnPanelActionInput) (AddOnPanelActionResult, error) {
	adapter = strings.TrimSpace(adapter)
	if adapter == "" {
		return AddOnPanelActionResult{}, fmt.Errorf("adapter is required")
	}

	// Check if the AddOn has an MCP runtime and forward there.
	if mcpServer, ok := a.mcpServerForAddOn(pluginName); ok {
		return a.mcpPanelAction(mcpServer, adapter, action)
	}

	switch {
	case strings.HasSuffix(adapter, "/profiles.json"):
		return a.routeSkillShareAction(adapter, action)
	case strings.HasSuffix(adapter, "/config.json"):
		return a.routeDrawAddonAction(action)
	default:
		return AddOnPanelActionResult{}, nil
	}
}

// mcpServerForAddOn returns the MCP server name if the named plugin is an MCP AddOn.
func (a *App) mcpServerForAddOn(pluginName string) (string, bool) {
	for _, p := range a.Plugins() {
		if p.Name == pluginName && p.AddOn != nil && p.AddOn.Runtime != nil && p.AddOn.Runtime.MCPServer != "" {
			return p.AddOn.Runtime.MCPServer, true
		}
	}
	return "", false
}

func (a *App) mcpPanelQuery(mcpServer, adapter string) (AddOnPanelQueryResult, error) {
	ctx := context.Background()
	root := a.activeWorkspaceRoot()
	host := a.lookupSharedHost(root)
	if host == nil {
		return AddOnPanelQueryResult{}, fmt.Errorf("no plugin host available for workspace")
	}
	params := map[string]any{"adapter": adapter}
	raw, err := host.CallMethod(ctx, mcpServer, "panel/query", params)
	if err != nil {
		return AddOnPanelQueryResult{}, err
	}
	var res AddOnPanelQueryResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return AddOnPanelQueryResult{}, fmt.Errorf("invalid panel/query response: %w", err)
	}
	return res, nil
}

func (a *App) mcpPanelAction(mcpServer, adapter string, action AddOnPanelActionInput) (AddOnPanelActionResult, error) {
	ctx := context.Background()
	root := a.activeWorkspaceRoot()
	host := a.lookupSharedHost(root)
	if host == nil {
		return AddOnPanelActionResult{}, fmt.Errorf("no plugin host available for workspace")
	}
	params := map[string]any{
		"adapter":  adapter,
		"actionId": action.ActionID,
		"form":     action.Form,
		"recordId": action.RecordID,
	}
	raw, err := host.CallMethod(ctx, mcpServer, "panel/action", params)
	if err != nil {
		return AddOnPanelActionResult{}, err
	}
	var res AddOnPanelActionResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return AddOnPanelActionResult{}, fmt.Errorf("invalid panel/action response: %w", err)
	}
	return res, nil
}

func (a *App) routeSkillShareAction(adapter string, action AddOnPanelActionInput) (AddOnPanelActionResult, error) {
	var svc *skillshare.Service
	isFlow := strings.HasPrefix(adapter, "flow")
	setting := "skill-share"
	if isFlow {
		svc = a.flowSkillShareService()
		setting = "flow-skill-share"
	} else {
		svc = a.skillShareService()
	}

	ctx := context.Background()

	switch action.ActionID {
	case "recover":
		if err := a.ensureActiveTabRebuildAllowed(setting); err != nil {
			return AddOnPanelActionResult{Error: err.Error()}, nil
		}
		if _, err := svc.Recover(ctx); err != nil {
			return AddOnPanelActionResult{Error: err.Error()}, nil
		}
		return a.refreshSkillSharePanelState(), nil

	case "save":
		if err := a.ensureActiveTabRebuildAllowed(setting); err != nil {
			return AddOnPanelActionResult{Error: err.Error()}, nil
		}
		input, err := formToSkillShareInput(action.Form)
		if err != nil {
			return AddOnPanelActionResult{Error: err.Error()}, nil
		}
		if _, err := svc.Save(ctx, input); err != nil {
			return AddOnPanelActionResult{Error: err.Error()}, nil
		}
		return a.refreshSkillSharePanelState(), nil

	case "sync":
		if err := a.ensureActiveTabRebuildAllowed(setting); err != nil {
			return AddOnPanelActionResult{Error: err.Error()}, nil
		}
		recordID := strings.TrimSpace(action.RecordID)
		if recordID == "" {
			return AddOnPanelActionResult{Error: "recordId is required for sync"}, nil
		}
		force := false
		if action.Extra != nil {
			if v, ok := action.Extra["force"]; ok {
				force, _ = v.(bool)
			}
		}
		if _, err := svc.Sync(ctx, recordID, skillshare.SyncOptions{Force: force}); err != nil {
			return AddOnPanelActionResult{Error: err.Error()}, nil
		}
		return a.refreshSkillSharePanelState(), nil

	case "delete", "delete-secret":
		if err := a.ensureActiveTabRebuildAllowed(setting); err != nil {
			return AddOnPanelActionResult{Error: err.Error()}, nil
		}
		recordID := strings.TrimSpace(action.RecordID)
		if recordID == "" {
			return AddOnPanelActionResult{Error: "recordId is required for delete"}, nil
		}
		removeSecret := action.ActionID == "delete-secret"
		if _, err := svc.Delete(ctx, recordID, skillshare.DeleteOptions{RemoveSecret: removeSecret}); err != nil {
			return AddOnPanelActionResult{Error: err.Error()}, nil
		}
		return a.refreshSkillSharePanelState(), nil

	default:
		// "edit" and "reset" are frontend-only; no backend work.
		return AddOnPanelActionResult{}, nil
	}
}

func (a *App) refreshSkillSharePanelState() AddOnPanelActionResult {
	a.invalidateSkillRootsCache()
	if err := a.rebuild(); err != nil {
		return AddOnPanelActionResult{Error: err.Error()}
	}
	return AddOnPanelActionResult{}
}

func (a *App) routeDrawAddonAction(action AddOnPanelActionInput) (AddOnPanelActionResult, error) {
	svc := a.drawAddonService()
	ctx := context.Background()

	switch action.ActionID {
	case "save":
		input, err := formToDrawAddonInput(action.Form)
		if err != nil {
			return AddOnPanelActionResult{Error: err.Error()}, nil
		}
		// SaveDrawAddonProvider handles secret storage internally.
		if _, err := svc.Save(ctx, input); err != nil {
			return AddOnPanelActionResult{Error: err.Error()}, nil
		}
		return AddOnPanelActionResult{}, nil

	case "delete":
		recordID := strings.TrimSpace(action.RecordID)
		if recordID == "" {
			return AddOnPanelActionResult{Error: "recordId is required for delete"}, nil
		}
		if _, err := svc.Delete(ctx, recordID); err != nil {
			return AddOnPanelActionResult{Error: err.Error()}, nil
		}
		return AddOnPanelActionResult{}, nil

	case "generate":
		input, err := formToDrawAddonGenerateInput(action.Form)
		if err != nil {
			return AddOnPanelActionResult{Error: err.Error()}, nil
		}
		_, err = svc.Generate(ctx, input)
		if err != nil {
			return AddOnPanelActionResult{Error: err.Error()}, nil
		}
		return AddOnPanelActionResult{Notice: "Generate task started"}, nil

	default:
		return AddOnPanelActionResult{}, nil
	}
}

// ── conversion helpers ─────────────────────────────────────────────────────

func profileViewToMap(p skillshare.ProfileView) map[string]any {
	m := map[string]any{
		"id":           p.ID,
		"enabled":      p.Enabled,
		"displayName":  p.DisplayName,
		"gitUrl":       p.GitURL,
		"branch":       p.Branch,
		"path":         p.Path,
		"username":     p.Username,
		"secretRef":    p.SecretRef,
		"authStatus":   p.AuthStatus,
		"pluginName":   p.PluginName,
		"manifestKind": p.ManifestKind,
		"version":      p.Version,
		"skills":       p.Skills,
		"hooks":        p.Hooks,
		"mcpServers":   p.MCPServers,
		"state": map[string]any{
			"status":          p.State.Status,
			"currentRevision": p.State.CurrentRevision,
			"lastCheckedAt":   p.State.LastCheckedAt,
			"lastUpdatedAt":   p.State.LastUpdatedAt,
			"lastError":       p.State.LastError,
		},
	}
	if p.Update.Auto {
		m["update"] = map[string]any{"auto": p.Update.Auto}
	}
	return m
}

func providerViewToMap(p drawaddon.ProviderView) map[string]any {
	return map[string]any{
		"id":          p.ID,
		"enabled":     p.Enabled,
		"displayName": p.DisplayName,
		"mode":        p.Mode,
		"baseUrl":     p.BaseURL,
		"model":       p.Model,
		"apiKeyRef":   p.APIKeyRef,
		"cliCommand":  p.CLICommand,
		"cliArgs":     p.CLIArgs,
		"outputDir":   p.OutputDir,
		"authStatus":  p.AuthStatus,
		"state": map[string]any{
			"status":         p.State.Status,
			"lastTaskId":     p.State.LastTaskID,
			"lastStartedAt":  p.State.LastStartedAt,
			"lastFinishedAt": p.State.LastFinishedAt,
			"lastOutputPath": p.State.LastOutputPath,
			"lastError":      p.State.LastError,
		},
	}
}

func formToSkillShareInput(form map[string]any) (skillshare.ProfileInput, error) {
	if form == nil {
		return skillshare.ProfileInput{}, fmt.Errorf("form is required")
	}
	in := skillshare.ProfileInput{
		ID:          stringField(form, "id"),
		Enabled:     boolField(form, "enabled"),
		DisplayName: stringField(form, "displayName"),
		GitURL:      stringField(form, "gitUrl"),
		Branch:      stringField(form, "branch"),
		Path:        stringField(form, "path"),
		Username:    stringField(form, "username"),
		SecretRef:   stringField(form, "secretRef"),
		PluginName:  stringField(form, "pluginName"),
	}
	if updateRaw, ok := form["update"]; ok {
		if updateMap, ok := updateRaw.(map[string]any); ok {
			in.Update.Auto = boolField(updateMap, "auto")
		}
	}
	return in, nil
}

func formToDrawAddonInput(form map[string]any) (drawaddon.ProviderInput, error) {
	if form == nil {
		return drawaddon.ProviderInput{}, fmt.Errorf("form is required")
	}
	in := drawaddon.ProviderInput{
		ID:          stringField(form, "id"),
		Enabled:     boolField(form, "enabled"),
		DisplayName: stringField(form, "displayName"),
		Mode:        stringField(form, "mode"),
		BaseURL:     stringField(form, "baseUrl"),
		Model:       stringField(form, "model"),
		APIKeyRef:   stringField(form, "apiKeyRef"),
		CLICommand:  stringField(form, "cliCommand"),
		OutputDir:   stringField(form, "outputDir"),
	}
	if cliArgsRaw, ok := form["cliArgs"]; ok {
		if args, ok := cliArgsRaw.([]any); ok {
			for _, a := range args {
				if s, ok := a.(string); ok {
					in.CLIArgs = append(in.CLIArgs, s)
				}
			}
		}
	}
	return in, nil
}

func formToDrawAddonGenerateInput(form map[string]any) (drawaddon.GenerateInput, error) {
	if form == nil {
		return drawaddon.GenerateInput{}, fmt.Errorf("form is required")
	}
	return drawaddon.GenerateInput{
		ProviderID: stringField(form, "providerId"),
		Prompt:     stringField(form, "prompt"),
	}, nil
}

func stringField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func boolField(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// Ensure json is used (Wails serializes parameters as JSON).
var _ = json.Marshal
