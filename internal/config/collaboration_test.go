package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetCollaborationRequiresExplicitInsecureConsent(t *testing.T) {
	cfg := Default()
	if !cfg.Collaboration.PreferLAN {
		t.Fatal("prefer_lan default = false, want true")
	}
	insecure := CollaborationConfig{PreferLAN: true, ConnectTimeout: 10, RouteStable: 60, Relays: []RelayConfig{{
		ID: "local", URL: "ws://relay.example.test:8443", Enabled: true, Priority: 100,
	}}}
	if err := cfg.SetCollaboration(insecure); err == nil || !strings.Contains(err.Error(), "allow_insecure") {
		t.Fatalf("SetCollaboration insecure error = %v, want explicit consent error", err)
	}
	insecure.Relays[0].AllowInsecure = true
	if err := cfg.SetCollaboration(insecure); err != nil {
		t.Fatalf("SetCollaboration with consent: %v", err)
	}
	if !cfg.Collaboration.Relays[0].AllowInsecure {
		t.Fatal("allow_insecure consent was not persisted")
	}
}

func TestSetCollaborationAllowsLoopbackWSWithoutConsent(t *testing.T) {
	for _, rawURL := range []string{"ws://localhost:8443", "ws://127.0.0.1:8443", "ws://[::1]:8443"} {
		t.Run(rawURL, func(t *testing.T) {
			cfg := Default()
			err := cfg.SetCollaboration(CollaborationConfig{PreferLAN: true, ConnectTimeout: 10, RouteStable: 60, Relays: []RelayConfig{{
				ID: "local", URL: rawURL, Enabled: true, Priority: 100,
			}}})
			if err != nil {
				t.Fatalf("loopback ws relay rejected: %v", err)
			}
		})
	}
}

func TestSetCollaborationValidatesRoutingTimings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		connect int
		stable  int
	}{
		{name: "zero connect", connect: 0, stable: 60},
		{name: "large connect", connect: 121, stable: 60},
		{name: "zero stable", connect: 10, stable: 0},
		{name: "large stable", connect: 10, stable: 3601},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Default().SetCollaboration(CollaborationConfig{
				PreferLAN: true, ConnectTimeout: tc.connect, RouteStable: tc.stable,
			})
			if err == nil {
				t.Fatal("SetCollaboration accepted invalid timing")
			}
		})
	}
}

func TestCollaborationRenderIsUserOnlyAndRoundTrips(t *testing.T) {
	cfg := Default()
	err := cfg.SetCollaboration(CollaborationConfig{PreferLAN: true, ConnectTimeout: 10, RouteStable: 60, Relays: []RelayConfig{{
		ID: "official-sg", Name: "Singapore", URL: "wss://relay.example.test/relay/v1/connect",
		Enabled: true, Priority: 100, Discovery: true, AccessTokenEnv: "WG2_RELAY_TOKEN",
	}}})
	if err != nil {
		t.Fatal(err)
	}

	user := RenderTOMLForScope(cfg, RenderScopeUser)
	for _, want := range []string{"[collaboration]", `prefer_lan = true`, `connect_timeout_seconds = 10`, `route_stable_seconds = 60`, "[[collaboration.relays]]", `id = "official-sg"`, `allow_insecure = false`, `access_token_env = "WG2_RELAY_TOKEN"`} {
		if !strings.Contains(user, want) {
			t.Fatalf("user render missing %q:\n%s", want, user)
		}
	}
	project := RenderTOMLForScope(cfg, RenderScopeProject)
	if strings.Contains(project, "collaboration.relays") || strings.Contains(project, "allow_insecure") {
		t.Fatalf("project render leaked user relay trust:\n%s", project)
	}
	if delta := RenderTOMLProjectDelta(cfg); strings.Contains(delta, "collaboration") {
		t.Fatalf("project delta leaked collaboration config:\n%s", delta)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	got := LoadForEditWithoutCredentials(path)
	if !got.Collaboration.PreferLAN || got.Collaboration.ConnectTimeout != 10 || got.Collaboration.RouteStable != 60 || len(got.Collaboration.Relays) != 1 || got.Collaboration.Relays[0].ID != "official-sg" || !got.Collaboration.Relays[0].Discovery {
		t.Fatalf("round-trip collaboration = %+v", got.Collaboration)
	}
}

func TestLoadForRootProjectCannotOverrideUserRelays(t *testing.T) {
	isolateUserConfigHome(t)
	userPath := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(`
[[collaboration.relays]]
id = "trusted"
name = "Trusted"
url = "wss://trusted.example.test"
enabled = true
priority = 100
discovery = true
allow_insecure = false
`), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "WorkGround2.toml"), []byte(`
[[collaboration.relays]]
id = "project-insecure"
name = "Project insecure"
url = "ws://project.example.test"
enabled = true
priority = 1000
discovery = true
allow_insecure = true
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Collaboration.Relays) != 1 || cfg.Collaboration.Relays[0].ID != "trusted" || cfg.Collaboration.Relays[0].AllowInsecure {
		t.Fatalf("resolved collaboration = %+v, want user-only trusted relay", cfg.Collaboration)
	}
}

func TestLoadForRootRejectsUnsafeUserRelayWithoutConsent(t *testing.T) {
	isolateUserConfigHome(t)
	userPath := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(`
[[collaboration.relays]]
id = "unsafe"
url = "ws://192.168.1.20:8443"
enabled = true
priority = 100
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadForRoot(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "allow_insecure") {
		t.Fatalf("LoadForRoot error = %v, want insecure relay rejection", err)
	}
}
