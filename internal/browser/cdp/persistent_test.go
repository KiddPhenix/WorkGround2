package cdp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workground2/internal/browser"
)

func TestPersistentArgsUseExplicitLoopbackEndpointAndProfile(t *testing.T) {
	args := persistentArgs(browser.DriverOptions{
		UserDataDir: `C:\automation-profile`, Headless: true, Incognito: true,
	}, 4321)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=4321",
		`--user-data-dir=C:\automation-profile`,
		"--headless=new",
		"--incognito",
		"about:blank",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("persistent args missing %q: %v", want, args)
		}
	}
	if strings.Contains(joined, "--enable-automation") || strings.Contains(joined, "--remote-debugging-port=0") {
		t.Fatalf("persistent args contain unwanted automation launch signal: %v", args)
	}
}

func TestReusablePageTargetPrefersExistingPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/list" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `[{"id":"worker","type":"service_worker","url":"https://example.test/sw.js"},{"id":"page-1","type":"page","url":"https://example.test/"}]`)
	}))
	defer srv.Close()
	id, err := reusablePageTarget(context.Background(), srv.URL)
	if err != nil || id != "page-1" {
		t.Fatalf("reusablePageTarget = %q, %v", id, err)
	}
}
