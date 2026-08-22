package browsertool

import (
	"strings"
	"testing"
)

func TestBrowserOpenDescriptionPrefersNativeTools(t *testing.T) {
	description := (&openTool{}).Description()
	for _, want := range []string{"preferred WorkGround2 native browser-use", "browser_* tools first when available", "native tools are unavailable", "Playwright only as a fallback"} {
		if !strings.Contains(description, want) {
			t.Fatalf("browser_open description missing %q: %s", want, description)
		}
	}
}
