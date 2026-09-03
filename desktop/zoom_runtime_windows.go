//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/wailsapp/go-webview2/pkg/edge"
)

const wailsWindowsFrontendPackage = "github.com/wailsapp/wails/v2/internal/frontend/desktop/windows"

// applyDesktopWebViewZoom uses the active Wails WebView2 controller. Wails
// v2.12 exposes ZoomFactor only as a startup option even though its pinned
// go-webview2 controller supports live PutZoomFactor, so this narrow adapter is
// isolated here and fails explicitly if the pinned Wails layout changes.
func applyDesktopWebViewZoom(ctx context.Context, factor float64) error {
	controller, err := desktopWebViewController(ctx)
	if err != nil {
		return err
	}
	previous, err := controller.GetZoomFactor()
	if err != nil {
		return fmt.Errorf("read active WebView2 zoom: %w", err)
	}
	if err := controller.PutZoomFactor(factor); err != nil {
		return fmt.Errorf("set active WebView2 zoom: %w", err)
	}
	actual, err := controller.GetZoomFactor()
	if err == nil && math.Abs(actual-factor) <= 0.0001 {
		return nil
	}

	verifyErr := err
	if verifyErr == nil {
		verifyErr = fmt.Errorf("WebView2 reported %.4f after requesting %.4f", actual, factor)
	}
	if rollbackErr := controller.PutZoomFactor(previous); rollbackErr != nil {
		return errors.Join(verifyErr, fmt.Errorf("restore active WebView2 zoom to %.4f: %w", previous, rollbackErr))
	}
	return verifyErr
}

func desktopWebViewController(ctx context.Context) (*edge.ICoreWebView2Controller, error) {
	if ctx == nil {
		return nil, fmt.Errorf("Wails context is unavailable")
	}
	frontend := reflect.ValueOf(ctx.Value("frontend"))
	if frontend.Kind() != reflect.Pointer || frontend.IsNil() {
		return nil, fmt.Errorf("active Wails frontend is unavailable")
	}
	frontendValue := frontend.Elem()
	frontendType := frontendValue.Type()
	if frontendType.PkgPath() != wailsWindowsFrontendPackage || frontendType.Name() != "Frontend" {
		return nil, fmt.Errorf("unsupported Wails frontend %s", frontend.Type())
	}

	chromiumField := frontendValue.FieldByName("chromium")
	if chromiumField.Kind() != reflect.Pointer || chromiumField.IsNil() {
		return nil, fmt.Errorf("active Wails Chromium instance is unavailable")
	}
	chromium := (*edge.Chromium)(chromiumField.UnsafePointer())
	controllerField := reflect.ValueOf(chromium).Elem().FieldByName("controller")
	if controllerField.Kind() != reflect.Pointer || controllerField.IsNil() {
		return nil, fmt.Errorf("active WebView2 controller is unavailable")
	}
	return (*edge.ICoreWebView2Controller)(controllerField.UnsafePointer()), nil
}
