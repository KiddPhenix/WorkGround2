package config

import (
	"fmt"
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
	if !c.BrowserAllowPasswordInput() || !c.BrowserAllowFileUpload() {
		t.Fatalf("browser sensitive defaults = allow_password_input %v allow_file_upload %v, want true true", c.BrowserAllowPasswordInput(), c.BrowserAllowFileUpload())
	}
	if c.BrowserIncognito() {
		t.Fatalf("browser incognito default = true, want false")
	}
	got := []int{
		c.BrowserIdleTimeoutSeconds(), c.BrowserActionTimeoutSeconds(),
		c.BrowserStateTimeoutSeconds(), c.BrowserSettleMilliseconds(),
		c.BrowserMaxTextChars(), c.BrowserMaxElements(),
	}
	want := []int{0, 30, 15, 300, 20000, 400}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("browser numeric defaults = %v, want %v", got, want)
	}
}

func TestBrowserIdleTimeoutZeroSemantics(t *testing.T) {
	// 0 is a legal special value: never auto-close from idleness. It must not
	// be clamped and must not produce an out-of-range warning.
	c := Default()
	c.Tools.Browser.IdleTimeoutSeconds = intPtr(0)
	if got := c.BrowserIdleTimeoutSeconds(); got != 0 {
		t.Fatalf("idle_timeout_seconds=0 resolved to %d, want 0", got)
	}
	for _, w := range c.BrowserConfigWarnings() {
		if strings.Contains(w, "idle_timeout_seconds") {
			t.Fatalf("idle_timeout_seconds=0 produced an out-of-range warning: %q", w)
		}
	}

	// 1..29 clamps to 30 with a warning; negative values also clamp to 30
	// (never interpreted as "disabled"); values above 86400 clamp to the cap.
	cases := []struct {
		raw  int
		want int
	}{
		{1, 30}, {29, 30}, {-1, 30}, {-86400, 30}, {86401, 86400}, {999999, 86400},
	}
	for _, tc := range cases {
		c := Default()
		c.Tools.Browser.IdleTimeoutSeconds = intPtr(tc.raw)
		if got := c.BrowserIdleTimeoutSeconds(); got != tc.want {
			t.Fatalf("idle_timeout_seconds=%d resolved to %d, want %d", tc.raw, got, tc.want)
		}
		warnings := c.BrowserConfigWarnings()
		found := false
		for _, w := range warnings {
			if strings.Contains(w, "idle_timeout_seconds") {
				found = true
				if !strings.Contains(w, fmt.Sprintf("%d", tc.want)) {
					t.Fatalf("idle_timeout_seconds=%d warning %q does not name the clamped value %d", tc.raw, w, tc.want)
				}
			}
		}
		if !found {
			t.Fatalf("idle_timeout_seconds=%d produced no out-of-range warning", tc.raw)
		}
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

func TestBrowserIncognitoSwitch(t *testing.T) {
	// nil (old config without the key) defaults to false.
	c := Default()
	c.Tools.Browser.Incognito = nil
	if c.BrowserIncognito() {
		t.Fatalf("nil incognito must default false, got true")
	}

	// Explicit true is respected; explicit false stays off.
	c = Default()
	c.Tools.Browser.Incognito = boolPtr(true)
	if !c.BrowserIncognito() {
		t.Fatal("incognito=true resolved to false")
	}
	c = Default()
	c.Tools.Browser.Incognito = boolPtr(false)
	if c.BrowserIncognito() {
		t.Fatal("incognito=false resolved to true")
	}
}

func TestBrowserSensitivePermissionSwitches(t *testing.T) {
	// nil (old config without the keys) defaults to true.
	c := Default()
	c.Tools.Browser.AllowPasswordInput = nil
	c.Tools.Browser.AllowFileUpload = nil
	if !c.BrowserAllowPasswordInput() || !c.BrowserAllowFileUpload() {
		t.Fatalf("nil sensitive switches must default true: password %v file %v", c.BrowserAllowPasswordInput(), c.BrowserAllowFileUpload())
	}

	// Explicit false is respected independently.
	c = Default()
	c.Tools.Browser.AllowPasswordInput = boolPtr(false)
	if c.BrowserAllowPasswordInput() || !c.BrowserAllowFileUpload() {
		t.Fatalf("password=false must only disable password: password %v file %v", c.BrowserAllowPasswordInput(), c.BrowserAllowFileUpload())
	}
	c = Default()
	c.Tools.Browser.AllowFileUpload = boolPtr(false)
	if !c.BrowserAllowPasswordInput() || c.BrowserAllowFileUpload() {
		t.Fatalf("file=false must only disable upload: password %v file %v", c.BrowserAllowPasswordInput(), c.BrowserAllowFileUpload())
	}
}

func TestRenderTOMLBrowserFullRoundTrip(t *testing.T) {
	c := Default()
	c.Tools.Browser.Kind = "chrome"
	c.Tools.Browser.ExecutablePath = `C:\Program Files\Google\Chrome\Application\chrome.exe`
	c.Tools.Browser.Headless = boolPtr(true)
	c.Tools.Browser.MaxElements = intPtr(777)
	c.Tools.Browser.AllowPasswordInput = boolPtr(false)
	c.Tools.Browser.AllowFileUpload = boolPtr(false)
	c.Tools.Browser.Incognito = boolPtr(true)

	rendered := RenderTOML(c)
	for _, want := range []string{
		"[tools.browser]", `kind = "chrome"`, `headless = true`,
		"idle_timeout_seconds = 0", "max_elements = 777",
		"allow_password_input = false", "allow_file_upload = false",
		"incognito = true",
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
	if got.BrowserAllowPasswordInput() || got.BrowserAllowFileUpload() {
		t.Fatalf("sensitive switch round trip = password %v file %v, want false false", got.BrowserAllowPasswordInput(), got.BrowserAllowFileUpload())
	}
	if !got.BrowserIncognito() {
		t.Fatal("incognito round trip = false, want true")
	}
}

func TestBrowserIncognitoTOMLDecode(t *testing.T) {
	// Old config without the key parses as false.
	var old Config
	if _, err := toml.Decode("[tools.browser]\nheadless = true\n", &old); err != nil {
		t.Fatal(err)
	}
	if old.BrowserIncognito() {
		t.Fatal("old config without incognito resolved to true, want false")
	}
	// Explicit true parses as true.
	var with Config
	if _, err := toml.Decode("[tools.browser]\nincognito = true\n", &with); err != nil {
		t.Fatal(err)
	}
	if !with.BrowserIncognito() {
		t.Fatal("incognito = true did not parse as enabled")
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

func TestProjectDeltaBrowserSensitiveSwitchRoundTrip(t *testing.T) {
	c := Default()
	c.Tools.Browser.AllowFileUpload = boolPtr(false)
	delta := RenderTOMLProjectDelta(c)
	if !strings.Contains(delta, "[tools.browser]") || !strings.Contains(delta, "allow_file_upload = false") {
		t.Fatalf("project browser delta missing sensitive override:\n%s", delta)
	}
	got := Default()
	if _, err := toml.Decode(delta, got); err != nil {
		t.Fatalf("decode sensitive browser delta: %v\n%s", err, delta)
	}
	if !got.BrowserAllowPasswordInput() || got.BrowserAllowFileUpload() {
		t.Fatalf("sensitive browser delta round trip = password %v file %v, want true false", got.BrowserAllowPasswordInput(), got.BrowserAllowFileUpload())
	}
}

func TestProjectDeltaBrowserIncognitoRoundTrip(t *testing.T) {
	c := Default()
	c.Tools.Browser.Incognito = boolPtr(true)
	delta := RenderTOMLProjectDelta(c)
	if !strings.Contains(delta, "[tools.browser]") || !strings.Contains(delta, "incognito = true") {
		t.Fatalf("project browser delta missing incognito override:\n%s", delta)
	}
	got := Default()
	if _, err := toml.Decode(delta, got); err != nil {
		t.Fatalf("decode incognito browser delta: %v\n%s", err, delta)
	}
	if !got.BrowserIncognito() {
		t.Fatal("incognito delta round trip = false, want true")
	}
}
