package main

import (
	"net"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

const (
	widgetSystemSampleInterval = 3 * time.Second
	widgetModelRefreshInterval = 30 * time.Second
	widgetModelLimit           = 12
)

// WidgetInfo contains low-frequency ambient information for the compact
// widget. Task state remains owned by WidgetSnapshot; this projection only
// carries display data that can be safely polled and retried.
type WidgetInfo struct {
	TotalTokens  int64             `json:"totalTokens"`
	TokenPartial bool              `json:"tokenPartial"`
	IdleSince    int64             `json:"idleSince,omitempty"`
	System       WidgetSystemInfo  `json:"system"`
	Models       []WidgetModelInfo `json:"models"`
}

type WidgetSystemInfo struct {
	Available bool   `json:"available"`
	Network   string `json:"network"`
	CPU       int    `json:"cpu"`
	Memory    int    `json:"memory"`
	SampledAt int64  `json:"sampledAt,omitempty"`
}

type WidgetModelInfo struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model"`
	Brand    string `json:"brand"`
}

type widgetInfoCache struct {
	systemAt time.Time
	system   WidgetSystemInfo
	modelsAt time.Time
	models   []WidgetModelInfo
}

func (a *App) widgetInfo(sources []widgetSource, idleSince int64) WidgetInfo {
	info := WidgetInfo{
		IdleSince: idleSince,
		System:    a.widgetSystemInfo(),
		Models:    a.widgetModels(sources),
	}
	for _, source := range sources {
		if source.totalTokens > 0 {
			info.TotalTokens += int64(source.totalTokens)
		}
		if source.meta.RunningWork && !source.tokenTracked {
			info.TokenPartial = true
		}
	}
	return info
}

func (a *App) widgetSystemInfo() WidgetSystemInfo {
	now := time.Now()
	a.widgetInfoMu.Lock()
	if !a.widgetInfoCache.systemAt.IsZero() && now.Sub(a.widgetInfoCache.systemAt) < widgetSystemSampleInterval {
		result := a.widgetInfoCache.system
		a.widgetInfoMu.Unlock()
		return result
	}
	probe := a.widgetSystemProbe
	a.widgetInfoMu.Unlock()

	if probe == nil {
		probe = readWidgetSystemInfo
	}
	result := probe()
	result.SampledAt = now.UnixMilli()

	a.widgetInfoMu.Lock()
	if a.widgetInfoCache.systemAt.IsZero() || now.After(a.widgetInfoCache.systemAt) {
		a.widgetInfoCache.systemAt = now
		a.widgetInfoCache.system = result
	}
	result = a.widgetInfoCache.system
	a.widgetInfoMu.Unlock()
	return result
}

func readWidgetSystemInfo() WidgetSystemInfo {
	result := WidgetSystemInfo{Network: widgetNetworkState()}
	if values, err := cpu.Percent(0, false); err == nil && len(values) > 0 {
		result.CPU = percentInt(values[0])
		result.Available = true
	}
	if memory, err := mem.VirtualMemory(); err == nil {
		result.Memory = percentInt(memory.UsedPercent)
		result.Available = true
	}
	if result.Network != "unknown" {
		result.Available = true
	}
	return result
}

func widgetNetworkState() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "unknown"
	}
	for _, item := range interfaces {
		if item.Flags&net.FlagUp == 0 || item.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := item.Addrs()
		if err == nil && len(addresses) > 0 {
			return "online"
		}
	}
	return "offline"
}

func percentInt(value float64) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return int(value + 0.5)
}

func (a *App) widgetModels(sources []widgetSource) []WidgetModelInfo {
	now := time.Now()
	a.widgetInfoMu.Lock()
	models := append([]WidgetModelInfo(nil), a.widgetInfoCache.models...)
	refresh := a.widgetInfoCache.modelsAt.IsZero() || now.Sub(a.widgetInfoCache.modelsAt) >= widgetModelRefreshInterval
	a.widgetInfoMu.Unlock()

	if refresh {
		models = widgetConfiguredModels(a.Settings())
		a.widgetInfoMu.Lock()
		if a.widgetInfoCache.modelsAt.IsZero() || now.After(a.widgetInfoCache.modelsAt) {
			a.widgetInfoCache.modelsAt = now
			a.widgetInfoCache.models = append([]WidgetModelInfo(nil), models...)
		}
		models = append([]WidgetModelInfo(nil), a.widgetInfoCache.models...)
		a.widgetInfoMu.Unlock()
	}

	active := make([]WidgetModelInfo, 0, len(sources))
	for _, source := range sources {
		if model := strings.TrimSpace(source.model); model != "" {
			active = append(active, newWidgetModel("", model))
		}
	}
	return uniqueWidgetModels(append(active, models...), widgetModelLimit)
}

func widgetConfiguredModels(settings SettingsView) []WidgetModelInfo {
	models := make([]WidgetModelInfo, 0, len(settings.Providers))
	for _, provider := range settings.Providers {
		if !provider.Configured {
			continue
		}
		model := strings.TrimSpace(provider.Default)
		if model == "" && len(provider.Models) > 0 {
			model = strings.TrimSpace(provider.Models[0])
		}
		if model != "" {
			models = append(models, newWidgetModel(provider.Name, model))
		}
	}
	sort.SliceStable(models, func(i, j int) bool {
		return strings.ToLower(models[i].Provider+"/"+models[i].Model) < strings.ToLower(models[j].Provider+"/"+models[j].Model)
	})
	return uniqueWidgetModels(models, widgetModelLimit)
}

func uniqueWidgetModels(models []WidgetModelInfo, limit int) []WidgetModelInfo {
	result := make([]WidgetModelInfo, 0, min(len(models), limit))
	seen := map[string]struct{}{}
	for _, model := range models {
		model.Model = strings.TrimSpace(model.Model)
		if model.Model == "" {
			continue
		}
		key := strings.ToLower(model.Model)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, model)
		if len(result) == limit {
			break
		}
	}
	return result
}

func newWidgetModel(provider, model string) WidgetModelInfo {
	return WidgetModelInfo{
		Provider: strings.TrimSpace(provider),
		Model:    strings.TrimSpace(model),
		Brand:    widgetModelBrand(provider + " " + model),
	}
}

func widgetModelBrand(value string) string {
	value = strings.ToLower(value)
	brands := []struct {
		brand string
		keys  []string
	}{
		{"deepseek", []string{"deepseek"}},
		{"openai", []string{"openai", "gpt-", "o1", "o3", "o4"}},
		{"anthropic", []string{"anthropic", "claude"}},
		{"gemini", []string{"google", "gemini"}},
		{"ollama", []string{"ollama"}},
		{"qwen", []string{"qwen", "dashscope", "通义"}},
		{"mistral", []string{"mistral", "mixtral"}},
		{"groq", []string{"groq"}},
		{"xai", []string{"xai", "grok"}},
		{"zhipu", []string{"zhipu", "glm", "智谱"}},
	}
	for _, candidate := range brands {
		for _, key := range candidate.keys {
			if strings.Contains(value, key) {
				return candidate.brand
			}
		}
	}
	return "generic"
}

func widgetModelSignature(models []WidgetModelInfo) string {
	parts := make([]string, 0, len(models))
	for _, model := range models {
		parts = append(parts, model.Provider+"/"+model.Model+":"+model.Brand)
	}
	return strings.Join(parts, "|")
}
