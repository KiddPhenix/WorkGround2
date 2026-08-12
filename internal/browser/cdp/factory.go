package cdp

import (
	"context"
	"fmt"
	"time"

	"workground2/internal/browser"
)

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

	eventCtx, eventCancel := context.WithCancel(ctx)
	d := &driver{
		opts:         dopts,
		execPath:     execPath,
		browserKind:  info.Kind,
		settleWindow: settleWindow,
		events:       make(chan browser.Invalidation, 128),
		invalCh:      make(chan browser.Invalidation, 64),
		eventDone:    make(chan struct{}),
		eventCancel:  eventCancel,
	}
	go d.runEventLoop(eventCtx)

	// Launch the browser and connect.
	if err := d.start(ctx); err != nil {
		return nil, fmt.Errorf("cdp start: %w", err)
	}

	return d, nil
}
