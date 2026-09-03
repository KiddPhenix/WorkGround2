//go:build !windows

package main

import (
	"context"
	"fmt"
)

func applyDesktopWebViewZoom(context.Context, float64) error {
	return fmt.Errorf("live desktop zoom is only supported on Windows")
}
