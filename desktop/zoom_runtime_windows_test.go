//go:build windows

package main

import (
	"context"
	"testing"
)

func TestDesktopWebViewControllerRejectsMissingFrontend(t *testing.T) {
	if _, err := desktopWebViewController(context.Background()); err == nil {
		t.Fatal("desktopWebViewController accepted context without active Wails frontend")
	}
}
