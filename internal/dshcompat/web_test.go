package dshcompat

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateWebSpecRejectsEscapingExecutable(t *testing.T) {
	root := t.TempDir()
	anchor := filepath.Join(root, "package.json")
	writeTestFile(t, anchor, `{"bin":{"dsh":"../outside.js"}}`)
	spec := WebSpec{
		RuntimeAnchor: anchor,
		Workspace:     root,
		DSHHome:       filepath.Join(root, "home"),
		NodePath:      os.Args[0],
	}
	if _, err := validateWebSpec(&spec); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("validateWebSpec error = %v", err)
	}
}

func TestRealDSHWebMirror(t *testing.T) {
	repo := strings.TrimSpace(os.Getenv("DSH_COMPAT_TEST_ROOT"))
	if repo == "" {
		t.Skip("set DSH_COMPAT_TEST_ROOT to a deepseek-harness checkout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	host, err := StartWeb(ctx, WebSpec{
		RuntimeAnchor: filepath.Join(repo, "apps", "cli", "package.json"),
		BundlePatch:   filepath.Join(repo, "packages", "bundle", "base", "cordis.patch.yml"),
		BundleName:    "@deepseek-ai/dsh-base",
		Workspace:     filepath.Clean(filepath.Join(repo, "..")),
		DSHHome:       filepath.Join(t.TempDir(), "dsh-home"),
		Stderr:        io.Discard,
	})
	if err != nil {
		t.Fatalf("StartWeb: %v", err)
	}
	t.Cleanup(func() {
		host.Close()
		host.Close()
	})
	response, err := http.Get(host.URL()) //nolint:gosec // loopback URL returned by validated test host
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d", host.URL(), response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, clientPlugin := range []string{
		"@deepseek-ai/dsh-client-ui-goal",
		"@deepseek-ai/dsh-client-ui-skill",
	} {
		if !strings.Contains(string(body), `"id":"`+clientPlugin+`"`) {
			t.Fatalf("DSH bootstrap omitted client plugin %s", clientPlugin)
		}
		pluginURL := host.URL() + "/plugins/" + clientPlugin + "/client.js"
		pluginResponse, requestErr := http.Get(pluginURL) //nolint:gosec // validated loopback test host
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		_ = pluginResponse.Body.Close()
		if pluginResponse.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d", pluginURL, pluginResponse.StatusCode)
		}
	}
}
