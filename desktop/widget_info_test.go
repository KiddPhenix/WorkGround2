package main

import (
	"testing"
	"time"
)

func TestWidgetInfoAggregatesTrackedTokensAndMarksPartialRunningSources(t *testing.T) {
	now := time.Now()
	app := &App{widgetInfoCache: widgetInfoCache{
		systemAt: now,
		system:   WidgetSystemInfo{Available: true, Network: "online", CPU: 20, Memory: 40},
		modelsAt: now,
		models:   []WidgetModelInfo{},
	}}
	sources := []widgetSource{
		{meta: TabMeta{RunningWork: true}, totalTokens: 1200, tokenTracked: true, model: "deepseek-chat"},
		{meta: TabMeta{RunningWork: true}, totalTokens: 3400, tokenTracked: false, model: "deepseek-chat"},
	}

	got := app.widgetInfo(sources, 123)
	if got.TotalTokens != 4600 {
		t.Fatalf("TotalTokens = %d, want 4600", got.TotalTokens)
	}
	if !got.TokenPartial {
		t.Fatal("TokenPartial = false, want true for an untracked running source")
	}
	if got.IdleSince != 123 {
		t.Fatalf("IdleSince = %d, want 123", got.IdleSince)
	}
	if len(got.Models) != 1 || got.Models[0].Brand != "deepseek" {
		t.Fatalf("Models = %#v, want one deduplicated DeepSeek model", got.Models)
	}
}

func TestWidgetSystemInfoUsesShortLivedCache(t *testing.T) {
	calls := 0
	app := &App{widgetSystemProbe: func() WidgetSystemInfo {
		calls++
		return WidgetSystemInfo{Available: true, Network: "online", CPU: 23, Memory: 61}
	}}

	first := app.widgetSystemInfo()
	second := app.widgetSystemInfo()
	if calls != 1 {
		t.Fatalf("probe calls = %d, want 1", calls)
	}
	if first.CPU != 23 || second.Memory != 61 || first.SampledAt == 0 {
		t.Fatalf("unexpected cached samples: first=%#v second=%#v", first, second)
	}
}

func TestWidgetModelBrandAndDeduplication(t *testing.T) {
	models := uniqueWidgetModels([]WidgetModelInfo{
		newWidgetModel("Anthropic", "claude-sonnet-4"),
		newWidgetModel("", "CLAUDE-SONNET-4"),
		newWidgetModel("Google", "gemini-2.5-pro"),
	}, 12)
	if len(models) != 2 {
		t.Fatalf("models = %#v, want two unique models", models)
	}
	if models[0].Brand != "anthropic" || models[1].Brand != "gemini" {
		t.Fatalf("brands = %q, %q", models[0].Brand, models[1].Brand)
	}
}

func TestNextWidgetIdleSincePreservesAndResets(t *testing.T) {
	if got := nextWidgetIdleSince(0, true, 100); got != 100 {
		t.Fatalf("first idle = %d, want 100", got)
	}
	if got := nextWidgetIdleSince(100, true, 200); got != 100 {
		t.Fatalf("continued idle = %d, want 100", got)
	}
	if got := nextWidgetIdleSince(100, false, 300); got != 0 {
		t.Fatalf("active reset = %d, want 0", got)
	}
}

func TestWidgetSnapshotVersionTracksAmbientInfo(t *testing.T) {
	base := WidgetSnapshot{Info: WidgetInfo{System: WidgetSystemInfo{Network: "online"}, Models: []WidgetModelInfo{}}}
	changed := base
	changed.Info.TotalTokens = 1
	if widgetSnapshotVersion(base) == widgetSnapshotVersion(changed) {
		t.Fatal("version did not change with token telemetry")
	}
	changed = base
	changed.Info.Models = []WidgetModelInfo{{Model: "deepseek-chat", Brand: "deepseek"}}
	if widgetSnapshotVersion(base) == widgetSnapshotVersion(changed) {
		t.Fatal("version did not change with model telemetry")
	}
}
