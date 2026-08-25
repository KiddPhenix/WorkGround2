package builtin

import (
	"strings"
	"testing"

	"workground2/internal/sandbox"
)

func TestBashSchemaDefinesBrowserFallbackAndPreservation(t *testing.T) {
	schema := string((bash{}).Schema())
	for _, want := range []string{
		"prefer the native browser_* tools when available",
		"Playwright only when those tools are unavailable, lack a required capability, or explicitly fail",
		"never reload the page merely to observe state",
		"Set true when launching browser-use, Playwright, GUI, or session processes",
		"windows must remain open after the command returns",
	} {
		if !strings.Contains(schema, want) {
			t.Fatalf("bash schema missing %q: %s", want, schema)
		}
	}
}

func TestBashDescriptionPrefersNativeBrowserTools(t *testing.T) {
	b := bash{shell: sandbox.Shell{Kind: sandbox.ShellBash, Path: "bash"}}
	description := b.Description()
	for _, want := range []string{
		"prefer the native browser_* tools over Playwright",
		"never reload the page merely to observe state",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("bash description missing %q: %s", want, description)
		}
	}
}
