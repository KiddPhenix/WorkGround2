package drawaddon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type DrawImageTool struct {
	service *Service
}

func NewTool(home string) *DrawImageTool {
	return &DrawImageTool{service: New(home)}
}

func (t *DrawImageTool) Name() string { return "draw_image" }

func (t *DrawImageTool) Description() string {
	return "Generate an image through the configured draw-tool AddOn provider. Configure providers in the draw-tool AddOn first."
}

func (t *DrawImageTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"providerId":{"type":"string","description":"Optional draw-tool provider id. When omitted, the first enabled provider is used."},"prompt":{"type":"string","description":"Image prompt to send to the provider or local CLI."}},"required":["prompt"]}`)
}

func (t *DrawImageTool) ReadOnly() bool { return false }

func (t *DrawImageTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in GenerateInput
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return "", errors.New("prompt is required")
	}
	if strings.TrimSpace(in.ProviderID) == "" {
		id, err := t.defaultProviderID()
		if err != nil {
			return "", err
		}
		in.ProviderID = id
	}
	task, err := t.service.Generate(ctx, in)
	data, marshalErr := json.MarshalIndent(task, "", "  ")
	if marshalErr != nil {
		return "", marshalErr
	}
	if err != nil {
		return string(data), err
	}
	return string(data), nil
}

func (t *DrawImageTool) defaultProviderID() (string, error) {
	providers, err := t.service.Providers()
	if err != nil {
		return "", err
	}
	for _, provider := range providers {
		if provider.Enabled {
			return provider.ID, nil
		}
	}
	return "", errors.New("draw-tool has no enabled providers; configure a provider in the draw-tool AddOn")
}

// PanelQuery returns all configured providers as a JSON array.
func (t *DrawImageTool) PanelQuery() ([]map[string]any, error) {
	providers, err := t.service.Providers()
	if err != nil {
		return nil, err
	}
	records := make([]map[string]any, 0, len(providers))
	for _, p := range providers {
		records = append(records, providerToMap(p))
	}
	return records, nil
}

// PanelAction routes a panel action to the service.
func (t *DrawImageTool) PanelAction(ctx context.Context, actionID string, form map[string]any, recordID string) (map[string]any, error) {
	switch actionID {
	case "save":
		in, err := formToInput(form)
		if err != nil {
			return nil, err
		}
		if _, err := t.service.Save(ctx, in); err != nil {
			return nil, err
		}
	case "delete":
		if recordID == "" {
			return nil, errors.New("recordId is required for delete")
		}
		if _, err := t.service.Delete(ctx, recordID); err != nil {
			return nil, err
		}
	case "generate":
		in, err := formToGenerate(form)
		if err != nil {
			return nil, err
		}
		if _, err := t.service.Generate(ctx, in); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func providerToMap(p ProviderView) map[string]any {
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

func formToInput(form map[string]any) (ProviderInput, error) {
	in := ProviderInput{
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
	if args, ok := form["cliArgs"]; ok {
		if arr, ok := args.([]any); ok {
			for _, a := range arr {
				in.CLIArgs = append(in.CLIArgs, fmt.Sprint(a))
			}
		}
	}
	return in, nil
}

func formToGenerate(form map[string]any) (GenerateInput, error) {
	return GenerateInput{
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
