package browsertool

import (
	"strings"
	"testing"
)

func TestBrowserOpenDescriptionPrefersNativeTools(t *testing.T) {
	description := (&openTool{}).Description()
	for _, want := range []string{
		"preferred built-in WorkGround2 browser_* session",
		"browser_* tools first when available",
		"native tools are unavailable",
		"Playwright only as a fallback",
		"Never reload, refresh, or navigate to the same URL merely to observe",
		"browser_state(refresh=true) only re-observes state and never reloads the page",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("browser_open description missing %q: %s", want, description)
		}
	}
}

func TestBrowserStateDescriptionAndSchemaNoReload(t *testing.T) {
	description := (&stateTool{}).Description()
	for _, want := range []string{
		"refresh=true only re-observes the page state and never reloads the page",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("browser_state description missing %q: %s", want, description)
		}
	}
	schema := string((&stateTool{}).Schema())
	for _, want := range []string{
		"never reloads the page",
	} {
		if !strings.Contains(schema, want) {
			t.Fatalf("browser_state schema missing %q: %s", want, schema)
		}
	}
}

func TestBrowserAttachDescriptionFallbackOnly(t *testing.T) {
	description := (&attachTool{}).Description()
	for _, want := range []string{
		"Playwright is fallback-only",
		"attach to this same WorkGround2 browser",
		"Requires browser_open first",
		"browser_state(refresh=true)",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("browser_attach description missing %q: %s", want, description)
		}
	}
}
