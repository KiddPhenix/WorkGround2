package cdp

import (
	"context"
	"fmt"
	"time"

	"workground2/internal/browser"
)

// maxLaunchAttempts bounds how many fresh browser launches the Factory will
// attempt when Chrome fails to bind the chosen debug port (the probe-to-bind
// TOCTOU window). Non-port startup errors never retry.
const maxLaunchAttempts = 3

// Options configures the CDP DriverFactory.
type Options struct {
	// No special options in V1.
}

// factory implements browser.DriverFactory using chromedp.
type factory struct {
	opts Options
}

// NewFactory creates a production DriverFactory that launches real Chromium.
func NewFactory(opts Options) browser.DriverFactory {
	return &factory{opts: opts}
}

// driverLauncher is the minimal surface launchWithRetry needs from a driver:
// full browser.Driver behaviour plus its internal start step. *driver satisfies
// it; tests use fakes to exercise the retry matrix without launching Chrome.
type driverLauncher interface {
	browser.Driver
	start(context.Context) error
}

func (f *factory) New(ctx context.Context, dopts browser.DriverOptions) (browser.Driver, error) {
	// Discover the browser executable.
	info, err := Discover(dopts.BrowserKind, dopts.ExecutablePath)
	if err != nil {
		return nil, fmt.Errorf("cdp factory: %w", err)
	}

	// Use explicit path if discovered.
	execPath := dopts.ExecutablePath
	if execPath == "" {
		execPath = info.ExecutablePath
	}

	settleWindow := dopts.SettleWindow
	if settleWindow <= 0 {
		settleWindow = 300 * time.Millisecond
	}

	return launchWithRetry(ctx, func() driverLauncher {
		return newDriver(dopts, execPath, info.Kind, settleWindow, ctx)
	}, maxLaunchAttempts)
}

// newDriver builds a fresh driver with its own event loop. Every retry attempt
// must create a brand-new instance so a failed launch never leaks an event
// loop, allocator, or process into the next attempt.
func newDriver(dopts browser.DriverOptions, execPath string, kind browser.BrowserKind, settleWindow time.Duration, parent context.Context) *driver {
	eventCtx, eventCancel := context.WithCancel(parent)
	d := &driver{
		opts:         dopts,
		execPath:     execPath,
		browserKind:  kind,
		settleWindow: settleWindow,
		pickPort:     pickDebugPort,
		events:       make(chan browser.Invalidation, 128),
		invalCh:      make(chan browser.Invalidation, 64),
		eventDone:    make(chan struct{}),
		eventCancel:  eventCancel,
	}
	go d.runEventLoop(eventCtx)
	return d
}

// launchWithRetry starts a fresh driver up to maxAttempts times. A debug-port
// conflict (Chrome failed to bind the probed port) is the only retryable
// failure: the failed instance is fully Closed first, then a brand-new driver
// is tried. Any other startup error returns immediately, and the final
// port-conflict error carries the attempt count and reason so upper layers can
// safely retry.
func launchWithRetry(ctx context.Context, makeDriver func() driverLauncher, maxAttempts int) (browser.Driver, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		d := makeDriver()
		if err := d.start(ctx); err != nil {
			lastErr = err
			// Idempotent: driver.start already closes itself on failure; the
			// extra Close guarantees no event loop/allocator/process survives a
			// failed attempt before the next retry.
			_ = d.Close()
			if !isDebugPortConflict(err) {
				return nil, fmt.Errorf("cdp start: %w", err)
			}
			if attempt == maxAttempts {
				return nil, fmt.Errorf("cdp factory: browser debug port unavailable after %d attempts: %w", attempt, err)
			}
			continue
		}
		return d, nil
	}
	return nil, lastErr
}
