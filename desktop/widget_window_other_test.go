//go:build !windows

package main

import "testing"

// TestSetWidgetTaskbarHiddenStubContract verifies the non-Windows stub never
// touches a window and never fails, so widget-mode transitions behave exactly
// as before on macOS/Linux.
func TestSetWidgetTaskbarHiddenStubContract(t *testing.T) {
	for _, hide := range []bool{true, false} {
		if err := setWidgetTaskbarHidden(hide); err != nil {
			t.Fatalf("setWidgetTaskbarHidden(%v) = %v, want nil", hide, err)
		}
	}
}
