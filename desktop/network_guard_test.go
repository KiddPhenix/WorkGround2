package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultTestsOpenNoOSNetworkListeners is the regression guard that keeps
// the default Desktop test suite free of OS-level network listeners.
//
// Windows Defender Firewall prompts for every new test executable path that
// binds an inbound socket (each `go test` build produces a fresh
// desktop.test.exe under D:\temp\go-build*\b001\...), and firewall program
// rules cannot wildcard-match those paths — so "Allow" is never durable.
//
// All listener/dialing in tests must go through workground2/desktop/internal/memhttp
// (net.Pipe based, never touches the OS network stack), and production
// listeners are reachable through its injectable listener seams. The memhttp
// package itself is the sanctioned implementation and is exempt.
func TestDefaultTestsOpenNoOSNetworkListeners(t *testing.T) {
	testForbidden := []string{
		"httptest.NewServer",
		"httptest.NewTLSServer",
		"httptest.NewUnstartedServer",
		"net.Listen(",
		"net.ListenTCP",
		"net.ListenUDP",
		"net.ListenPacket",
		"net.Dial(",
		"net.DialTimeout",
		"net.DialTCP",
		"net.DialUDP",
		"net.DialUnix",
	}
	productionForbidden := []string{
		"net.Listen(",
		"net.ListenTCP",
		"net.ListenUDP",
		"net.ListenPacket",
	}
	var violations []string
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == "internal/memhttp" {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || name == "network_guard_test.go" || name == "network.go" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		forbidden := productionForbidden
		if strings.HasSuffix(name, "_test.go") {
			forbidden = testForbidden
		}
		for _, token := range forbidden {
			if strings.Contains(string(data), token) {
				violations = append(violations, path+": "+token)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) > 0 {
		t.Fatalf("default Desktop tests open OS sockets directly; use workground2/desktop/internal/memhttp (in-memory transport) instead:\n%s", strings.Join(violations, "\n"))
	}
}
