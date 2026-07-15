package boot

import (
	"context"
	"strings"
	"testing"

	"workground2/internal/config"
	"workground2/internal/event"
)

func TestRequestHelpToolExposedWhenAssistEnabled(t *testing.T) {
	cfg := config.Default()
	if !cfg.AssistEnabled() {
		t.Fatal("default config should have assist enabled")
	}
	ctrl, err := Build(context.Background(), Options{
		Model: "deepseek-flash",
		Sink:  event.Discard,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	entries := ctrl.ToolContractEntries()
	found := false
	for _, e := range entries {
		if e.Name == "request_help" {
			found = true
			break
		}
	}
	if !found {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name)
		}
		t.Fatalf("request_help tool should be registered when assist is enabled; registered tools: %s", strings.Join(names, ", "))
	}
}

func TestRequestHelpToolNotInTokenEconomy(t *testing.T) {
	cfg := config.Default()
	if !cfg.AssistEnabled() {
		t.Fatal("default config should have assist enabled")
	}
	ctrl, err := Build(context.Background(), Options{
		Model:     "deepseek-flash",
		Sink:      event.Discard,
		TokenMode: "economy",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	for _, e := range ctrl.ToolContractEntries() {
		if e.Name == "request_help" {
			t.Fatal("request_help tool should NOT be registered in token economy mode")
		}
	}
}
