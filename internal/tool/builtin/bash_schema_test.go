package builtin

import (
	"strings"
	"testing"
)

func TestBashSchemaDefinesBrowserFallbackAndPreservation(t *testing.T) {
	schema := string((bash{}).Schema())
	for _, want := range []string{
		"prefer the native browser_* tools when available",
		"Playwright only when those tools are unavailable, lack a required capability, or explicitly fail",
		"Set true when launching browser-use, Playwright, GUI, or session processes",
		"windows must remain open after the command returns",
	} {
		if !strings.Contains(schema, want) {
			t.Fatalf("bash schema missing %q: %s", want, schema)
		}
	}
}
