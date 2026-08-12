package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestBrowserConfigDefaults(t *testing.T) {
	c := Default()
	if !c.BrowserEnabled() || c.BrowserKind() != "auto" || c.BrowserHeadless() {
		t.Fatalf("browser defaults = enabled %v kind %q headless %v", c.BrowserEnabled(), c.BrowserKind(), c.BrowserHeadless())
	}
	got := []int{
		c.BrowserIdleTimeoutSeconds(), c.BrowserActionTimeoutSeconds(),
		c.BrowserStateTimeoutSeconds(), c.BrowserSettleMilliseconds(),
		c.BrowserMaxTextChars(), c.BrowserMaxElements(),
	}
	want := []int{600, 30, 15, 300, 20000, 400}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("browser numeric defaults = %v, want %v", got, want)
	}
}

func TestBrowserConfigClampsAndValidatesKind(t *testing.T) {
	c := Default()
	c.Tools.Browser.Kind = " FIREFOX "
	c.Tools.Browser.IdleTimeoutSeconds = intPtr(-1)
	c.Tools.Browser.ActionTimeoutSeconds = intPtr(999)
	c.Tools.Browser.StateTimeoutSeconds = intPtr(0)
	c.Tools.Browser.SettleMilliseconds = intPtr(99999)
	c.Tools.Browser.MaxTextChars = intPtr(10)
	c.Tools.Browser.MaxElements = intPtr(99999)
	if c.BrowserKind() != "auto" {
		t.Fatalf("invalid browser kind = %q, want auto", c.BrowserKind())
	}
	if warnings := c.BrowserConfigWarnings(); len(warnings) != 7 || !strings.Contains(warnings[0], "unsupported") {
		t.Fatalf("invalid browser diagnostics = %v, want kind plus six numeric warnings", warnings)
	}
	got := []int{
		c.BrowserIdleTimeoutSeconds(), c.BrowserActionTimeoutSeconds(),
		c.BrowserStateTimeoutSeconds(), c.BrowserSettleMilliseconds(),
		c.BrowserMaxTextChars(), c.BrowserMaxElements(),
	}
	want := []int{30, 300, 1, 5000, 1000, 2000}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clamped browser values = %v, want %v", got, want)
	}
	c.Tools.Browser.Kind = "CHROME_FOR_TESTING"
	if c.BrowserKind() != "chrome_for_testing" {
		t.Fatalf("canonical browser kind = %q", c.BrowserKind())
	}
	if warnings := c.BrowserConfigWarnings(); len(warnings) != 6 {
		// The valid kind no longer warns; all six out-of-range numbers do.
		t.Fatalf("browser config warnings = %v, want 6 numeric warnings", warnings)
	}
}

func TestRenderTOMLBrowserFullRoundTrip(t *testing.T) {
	c := Default()
	c.Tools.Browser.Kind = "chrome"
	c.Tools.Browser.ExecutablePath = `C:\Program Files\Google\Chrome\Application\chrome.exe`
	c.Tools.Browser.Headless = boolPtr(true)
	c.Tools.Browser.MaxElements = intPtr(777)

	rendered := RenderTOML(c)
	for _, want := range []string{
		"[tools.browser]", `kind = "chrome"`, `headless = true`,
		"idle_timeout_seconds = 600", "max_elements = 777",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("full render missing %q:\n%s", want, rendered)
		}
	}
	var got Config
	if _, err := toml.Decode(rendered, &got); err != nil {
		t.Fatalf("decode browser render: %v", err)
	}
	if got.BrowserKind() != "chrome" || !got.BrowserHeadless() || got.BrowserMaxElements() != 777 || got.Tools.Browser.ExecutablePath != c.Tools.Browser.ExecutablePath {
		t.Fatalf("browser round trip = %+v", got.Tools.Browser)
	}
}

func TestProjectDeltaBrowserDefaultsStayAbsent(t *testing.T) {
	delta := RenderTOMLProjectDelta(Default())
	if strings.Contains(delta, "[tools.browser]") {
		t.Fatalf("default browser config polluted project delta:\n%s", delta)
	}
}

func TestProjectDeltaBrowserOverrideRoundTrip(t *testing.T) {
	c := Default()
	c.Tools.Browser.Kind = "edge"
	c.Tools.Browser.Headless = boolPtr(true)
	delta := RenderTOMLProjectDelta(c)
	if !strings.Contains(delta, "[tools.browser]") || !strings.Contains(delta, `kind = "edge"`) {
		t.Fatalf("project browser delta missing override:\n%s", delta)
	}
	got := Default()
	if _, err := toml.Decode(delta, got); err != nil {
		t.Fatalf("decode browser delta: %v\n%s", err, delta)
	}
	if got.BrowserKind() != "edge" || !got.BrowserHeadless() {
		t.Fatalf("project browser round trip = %+v", got.Tools.Browser)
	}
}
