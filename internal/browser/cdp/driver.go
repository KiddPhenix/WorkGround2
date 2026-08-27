package cdp

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	browsercdp "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"workground2/internal/browser"
	"workground2/internal/proc"
)

// defaultDialogSettle bounds how long an action waits for a beforeunload
// event to land after the underlying CDP action returned. It is far shorter
// than any action timeout, so a blocked navigation reports dialog_blocked
// promptly instead of hanging. Every successful action pays this small tax
// because a dialog event can arrive just after the CDP command returns; the
// window is polled so a blocked dialog is detected in milliseconds.
const defaultDialogSettle = 100 * time.Millisecond

// defaultDialogResolveWait bounds how long an action waits for the dialog
// dismissal CDP command to complete before reporting the blocked outcome.
// HandleJavaScriptDialog round-trips in milliseconds; this only guards against
// a wedged connection and stays far below any action timeout.
const defaultDialogResolveWait = 2 * time.Second

type driver struct {
	opts         browser.DriverOptions
	execPath     string
	browserKind  browser.BrowserKind
	settleWindow time.Duration
	pickPort     func() (int, error)

	mu             sync.RWMutex
	debugEndpoint  string
	allocCtx       context.Context
	allocCancel    context.CancelFunc
	browserBase    context.Context
	rootCtx        context.Context
	cdpCtx         context.Context
	targetCancels  map[string]context.CancelFunc
	targetContexts map[string]context.Context
	activeTarget   string
	product        string
	version        string
	protocol       string
	processID      int
	launchCmd      *exec.Cmd
	launchDone     chan struct{}

	events          chan browser.Invalidation
	invalCh         chan browser.Invalidation
	eventDone       chan struct{}
	eventCancel     context.CancelFunc
	closeOnce       sync.Once
	closeErr        error
	closeErrPending bool
	closed          atomic.Bool
	processExited   atomic.Bool

	// dialogs holds the per-target dialog policy for the in-flight operations.
	// Guarded by mu; entries are acquired/released around each operation.
	dialogs      map[string]*dialogPolicy
	dialogExec   dialogExecutor
	dialogSettle time.Duration
}

func (d *driver) Info() browser.BrowserInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return browser.BrowserInfo{
		Kind: d.browserKind, Product: d.product, Version: d.version,
		ProtocolVersion: d.protocol, ExecutablePath: d.execPath,
	}
}

// prepareDebugLaunch picks a free nonzero loopback port and builds the explicit
// remote-debugging flags. The actual endpoint is recorded on the driver for
// lifecycle/package-internal evidence only.
func (d *driver) prepareDebugLaunch() ([]chromedp.ExecAllocatorOption, error) {
	port, err := d.pickPort()
	if err != nil {
		return nil, fmt.Errorf("pick debug port: %w", err)
	}
	flags, err := debugLaunchFlags(port)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.debugEndpoint = debugEndpointFor(port)
	d.mu.Unlock()
	return flags, nil
}

func (d *driver) start(ctx context.Context) error {
	debugFlags, err := d.prepareDebugLaunch()
	if err != nil {
		_ = d.Close()
		return err
	}
	allocOpts := launchAllocOptions(d.opts, d.execPath, debugFlags)

	d.allocCtx, d.allocCancel = chromedp.NewExecAllocator(ctx, allocOpts...)
	return d.startWithAllocator("")
}

// startRemote attaches to an already-running browser via its websocket URL.
// The remote browser is not owned and must never be killed by Close. A normal
// attach never claims an existing page because there is no durable owner-to-
// target binding yet; chromedp creates a fresh blank target for this client.
func (d *driver) startRemote(ctx context.Context) error {
	return d.startRemoteWithBootstrap(ctx, false)
}

// startFreshPersistent attaches to a browser process that this call just
// launched. Its bootstrap about:blank page is known to be unowned, so it is
// safe to reuse and avoids leaking an extra blank target on first launch.
func (d *driver) startFreshPersistent(ctx context.Context) error {
	return d.startRemoteWithBootstrap(ctx, true)
}

func (d *driver) startRemoteWithBootstrap(ctx context.Context, reuseBootstrap bool) error {
	if d.opts.DebugURL == "" || d.opts.WebSocketURL == "" {
		return fmt.Errorf("attach requires DebugURL and WebSocketURL")
	}
	info, err := browser.ValidateEndpoint(ctx, nil, d.opts.DebugURL)
	if err != nil {
		return fmt.Errorf("validate attach endpoint: %w", err)
	}
	endpointURL, _ := url.Parse(info.Endpoint)
	d.mu.Lock()
	d.debugEndpoint = endpointURL.Host
	d.mu.Unlock()

	initialTarget, err := initialPageTarget(ctx, info.Endpoint, reuseBootstrap)
	if err != nil {
		return fmt.Errorf("resolve initial page target: %w", err)
	}
	d.allocCtx, d.allocCancel = chromedp.NewRemoteAllocator(ctx, info.WebSocketURL)
	return d.startWithAllocator(initialTarget)
}

func (d *driver) startWithAllocator(initialTarget string) error {
	var contextOpts []chromedp.ContextOption
	if initialTarget != "" {
		contextOpts = append(contextOpts, chromedp.WithTargetID(target.ID(initialTarget)))
	}
	initialCtx, initialCancel := chromedp.NewContext(d.allocCtx, contextOpts...)
	d.mu.Lock()
	d.browserBase = context.WithoutCancel(initialCtx)
	d.rootCtx = initialCtx
	d.cdpCtx = initialCtx
	if d.targetCancels == nil {
		d.targetCancels = make(map[string]context.CancelFunc)
	}
	if d.targetContexts == nil {
		d.targetContexts = make(map[string]context.Context)
	}
	d.mu.Unlock()
	if err := chromedp.Run(initialCtx); err != nil {
		_ = d.Close()
		return fmt.Errorf("browser startup failed: %w", err)
	}

	chromedpContext := chromedp.FromContext(initialCtx)
	if chromedpContext == nil || chromedpContext.Target == nil {
		_ = d.Close()
		return fmt.Errorf("browser startup did not create a target")
	}
	d.mu.Lock()
	d.activeTarget = string(chromedpContext.Target.TargetID)
	d.targetCancels[d.activeTarget] = initialCancel
	d.targetContexts[d.activeTarget] = initialCtx
	d.mu.Unlock()

	versionCtx, cancel := context.WithTimeout(initialCtx, 10*time.Second)
	var protocol, product string
	err := chromedp.Run(versionCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		protocol, product, _, _, _, err = browsercdp.GetVersion().Do(ctx)
		return err
	}))
	cancel()
	if err != nil {
		_ = d.Close()
		return fmt.Errorf("read browser version: %w", err)
	}
	if err := validateBrowserVersion(d.browserKind, product, protocol); err != nil {
		_ = d.Close()
		return err
	}
	d.mu.Lock()
	d.product = product
	d.version = productVersion(product)
	d.protocol = protocol
	d.mu.Unlock()
	if process := chromedpContext.Browser.Process(); process != nil {
		d.mu.Lock()
		d.processID = process.Pid
		d.mu.Unlock()
	}

	if d.opts.DenyDownloads {
		downloadPath := filepath.Join(d.opts.UserDataDir, "Downloads")
		if err := os.MkdirAll(downloadPath, 0o700); err != nil {
			_ = d.Close()
			return fmt.Errorf("create denied-download directory: %w", err)
		}
		downloadCtx, cancel := context.WithTimeout(initialCtx, 10*time.Second)
		err = chromedp.Run(downloadCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			return browsercdp.SetDownloadBehavior(browsercdp.SetDownloadBehaviorBehaviorDeny).
				WithDownloadPath(downloadPath).Do(ctx)
		}))
		cancel()
		if err != nil {
			_ = d.Close()
			return fmt.Errorf("deny downloads: %w", err)
		}
	}
	d.mu.Lock()
	if d.dialogExec == nil {
		d.dialogExec = cdpDialogExecutor{}
	}
	d.mu.Unlock()
	d.listenTarget(initialCtx)
	// Browser-level events (target created/changed/destroyed) must outlive the
	// initial tab: closing the active tab later cancels the initial context,
	// which would silently drop the target listener if it were registered on
	// it. browserBase never cancels, so the listener survives tab closes.
	chromedp.ListenBrowser(d.browserBase, d.handleEvent)
	return nil
}

// launchAllocOptions builds the chromedp ExecAllocatorOption list for one
// browser launch. execPath is the discovered executable (may differ from
// dopts.ExecutablePath when kind discovery resolves it); debugFlags carry the
// explicit nonzero loopback remote-debugging flags. The Chromium incognito flag
// is appended only when dopts.Incognito is true, so a disabled switch never
// leaks --incognito into the process args.
func launchAllocOptions(dopts browser.DriverOptions, execPath string, debugFlags []chromedp.ExecAllocatorOption) []chromedp.ExecAllocatorOption {
	allocOpts := []chromedp.ExecAllocatorOption{chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck}
	allocOpts = append(allocOpts, debugFlags...)
	if dopts.Headless {
		allocOpts = append(allocOpts, chromedp.Headless, chromedp.WindowSize(1280, 720))
	}
	if dopts.Incognito {
		allocOpts = append(allocOpts, chromedp.Flag("incognito", true))
	}
	if execPath != "" {
		allocOpts = append(allocOpts, chromedp.ExecPath(execPath))
	}
	if dopts.UserDataDir != "" {
		allocOpts = append(allocOpts, chromedp.UserDataDir(dopts.UserDataDir))
	}
	allocOpts = append(allocOpts,
		chromedp.Flag("disable-save-password-bubble", true),
		chromedp.Flag("disable-password-manager-reauthentication", true),
		chromedp.Flag("password-store", "basic"),
	)
	return allocOpts
}

func productVersion(product string) string {
	if _, version, ok := strings.Cut(product, "/"); ok {
		return version
	}
	return product
}

func validateBrowserVersion(kind browser.BrowserKind, product, protocol string) error {
	if strings.TrimSpace(product) == "" || strings.TrimSpace(protocol) == "" {
		return browser.NewError(browser.ErrUnsupportedBrowser, "Browser.getVersion returned empty product or protocolVersion", nil)
	}
	lower := strings.ToLower(product)
	validProduct := strings.Contains(lower, "chrome") || strings.Contains(lower, "chromium") || strings.Contains(lower, "edge") || strings.Contains(lower, "edg/") || strings.Contains(lower, "headless")
	if !validProduct {
		return browser.NewError(browser.ErrUnsupportedBrowser, fmt.Sprintf("unsupported CDP product %q", product), nil)
	}
	if kind == browser.BrowserEdge && !strings.Contains(lower, "edge") && !strings.Contains(lower, "edg") {
		return browser.NewError(browser.ErrUnsupportedBrowser, fmt.Sprintf("expected Edge product, got %q", product), nil)
	}
	return nil
}

func (d *driver) Navigate(ctx context.Context, url string) error {
	return d.NavigateWithOptions(ctx, url, browser.ActionOptions{})
}

func (d *driver) NavigateWithOptions(ctx context.Context, url string, opts browser.ActionOptions) error {
	if err := validateNavigationURL(url); err != nil {
		return err
	}
	opCtx, cancel, err := d.operationContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	targetID := d.activeTargetID()
	policy := d.acquireDialogPolicy(targetID, opts)
	policy.setOpCancel(cancel)
	defer d.releaseDialogPolicy(targetID, policy)
	err = chromedp.Run(opCtx, chromedp.Navigate(url))
	dialog, blocked, resolveErr := d.dialogOutcome(policy)
	if resolveErr != nil {
		return browser.NewDialogResolutionError(dialog, resolveErr)
	}
	if blocked {
		return browser.NewDialogBlockedError(dialog, fmt.Sprintf("navigation blocked by %s dialog; dialog dismissed", dialog.Type), err)
	}
	return err
}

func (d *driver) Observe(ctx context.Context, opts browser.ObserveOptions) (browser.Observation, error) {
	if opts.MaxTextChars <= 0 {
		opts.MaxTextChars = 20000
	}
	if opts.MaxElements <= 0 {
		opts.MaxElements = 400
	}
	opCtx, cancel, err := d.operationContext(ctx)
	if err != nil {
		return browser.Observation{}, err
	}
	defer cancel()
	_, active := d.current()
	main, err := observe(opCtx, active, opts)
	if err != nil {
		return browser.Observation{}, err
	}
	var infos []*target.Info
	if err := chromedp.Run(opCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		infos, err = target.GetTargets().Do(ctx)
		return err
	})); err != nil {
		return browser.Observation{}, fmt.Errorf("list iframe targets: %w", err)
	}
	for _, info := range infos {
		if info == nil || info.Type != "iframe" {
			continue
		}
		childCtx, childCancel, err := d.operationContextForTarget(ctx, string(info.TargetID))
		if err != nil {
			main.Warnings = append(main.Warnings, browser.StateWarning{Code: "frame_target_unavailable", FrameID: string(info.TargetID), Message: err.Error()})
			continue
		}
		child, childErr := observe(childCtx, string(info.TargetID), opts)
		childCancel()
		if childErr != nil {
			main.Warnings = append(main.Warnings, browser.StateWarning{Code: "frame_target_unavailable", FrameID: string(info.TargetID), Message: childErr.Error()})
			continue
		}
		main.Nodes = append(main.Nodes, child.Nodes...)
		if child.Text != "" {
			main.Text = strings.TrimSpace(main.Text + " " + child.Text)
		}
		main.Warnings = append(main.Warnings, child.Warnings...)
		main.Fingerprint += ":" + child.Fingerprint
		main.Truncated = main.Truncated || child.Truncated
	}
	if len(main.Nodes) > opts.MaxElements {
		main.Nodes = main.Nodes[:opts.MaxElements]
		main.Truncated = true
	}
	var textTruncated bool
	main.Text, textTruncated = truncateRunes(main.Text, opts.MaxTextChars)
	main.Truncated = main.Truncated || textTruncated
	return main, nil
}

func (d *driver) Click(ctx context.Context, ref browser.NodeRef) error {
	return d.ClickWithOptions(ctx, ref, browser.ActionOptions{})
}

func (d *driver) ClickWithOptions(ctx context.Context, ref browser.NodeRef, opts browser.ActionOptions) error {
	opCtx, cancel, err := d.operationContextForTarget(ctx, ref.TargetID)
	if err != nil {
		return err
	}
	defer cancel()
	targetID := ref.TargetID
	if targetID == "" {
		targetID = d.activeTargetID()
	}
	policy := d.acquireDialogPolicy(targetID, opts)
	policy.setOpCancel(cancel)
	defer d.releaseDialogPolicy(targetID, policy)
	_, err = clickNodeWithMethod(opCtx, ref)
	dialog, blocked, resolveErr := d.dialogOutcome(policy)
	if resolveErr != nil {
		return browser.NewDialogResolutionError(dialog, resolveErr)
	}
	if blocked {
		return browser.NewDialogBlockedUnknownError(dialog, fmt.Sprintf("click blocked by %s dialog; dialog dismissed", dialog.Type), err)
	}
	return err
}

func (d *driver) Type(ctx context.Context, ref browser.NodeRef, value browser.TypeInput) error {
	opCtx, cancel, err := d.operationContextForTarget(ctx, ref.TargetID)
	if err != nil {
		return err
	}
	defer cancel()
	return typeText(opCtx, ref, value, d.opts.AllowPasswordInput)
}

// Upload sets local files on an input[type=file] routed by the node's target
// (supports OOPIF). The AllowFileUpload switch is re-checked here so a disabled
// capability can never reach DOM.setFileInputFiles even if the Manager were
// bypassed.
func (d *driver) Upload(ctx context.Context, ref browser.NodeRef, files []string) error {
	if !d.opts.AllowFileUpload {
		return browser.NewError(browser.ErrSensitiveInputBlocked, "file upload is disabled by allow_file_upload", nil)
	}
	opCtx, cancel, err := d.operationContextForTarget(ctx, ref.TargetID)
	if err != nil {
		return err
	}
	defer cancel()
	return uploadFiles(opCtx, ref, files)
}

func (d *driver) Scroll(ctx context.Context, value browser.ScrollInput) error {
	targetID := ""
	if value.Node != nil {
		targetID = value.Node.TargetID
	}
	opCtx, cancel, err := d.operationContextForTarget(ctx, targetID)
	if err != nil {
		return err
	}
	defer cancel()
	return scrollPage(opCtx, value)
}

func (d *driver) NewTab(ctx context.Context, url string) (string, error) {
	if err := validateNavigationURL(url); err != nil {
		return "", err
	}
	opCtx, cancel, err := d.operationContext(ctx)
	if err != nil {
		return "", err
	}
	targetID, err := newTab(opCtx, url)
	cancel()
	if err != nil {
		return "", err
	}
	if err := d.switchTarget(ctx, targetID); err != nil {
		return "", dispatchedError("target created but activation failed", err)
	}
	return targetID, nil
}

func (d *driver) ActivateTab(ctx context.Context, targetID string) error {
	opCtx, cancel, err := d.operationContext(ctx)
	if err != nil {
		return err
	}
	err = activateTab(opCtx, targetID)
	cancel()
	if err != nil {
		return err
	}
	if err := d.switchTarget(ctx, targetID); err != nil {
		return dispatchedError("target activated but context switch failed", err)
	}
	return nil
}

func (d *driver) CloseTab(ctx context.Context, targetID string) error {
	return d.CloseTabWithOptions(ctx, targetID, browser.ActionOptions{})
}

func (d *driver) CloseTabWithOptions(ctx context.Context, targetID string, opts browser.ActionOptions) error {
	// Page.close must run on the target being closed. This also guarantees that
	// its beforeunload events are observed even when it is not the active tab.
	opCtx, cancel, err := d.operationContextForTarget(ctx, targetID)
	if err != nil {
		return err
	}
	policy := d.acquireDialogPolicy(targetID, opts)
	policy.setOpCancel(cancel)
	defer d.releaseDialogPolicy(targetID, policy)
	tabs, err := listTabsInternal(opCtx, d.activeTargetID())
	if err != nil {
		cancel()
		return fmt.Errorf("list tabs before close: %w", err)
	}
	replacement, replacementErr := replacementTab(tabs, targetID)
	if replacementErr != nil {
		cancel()
		return replacementErr
	}
	// Chrome's Page.close/Target.closeTarget may bypass beforeunload entirely.
	// Navigate the target to about:blank first so the page gets the same leave
	// decision as a user-initiated close. If the default policy dismisses the
	// dialog the original page remains intact; once navigation is allowed, the
	// now-blank target can be closed without losing unapproved page state.
	err = chromedp.Run(opCtx, chromedp.Navigate("about:blank"))
	dialog, blocked, resolveErr := d.dialogOutcome(policy)
	if resolveErr != nil {
		cancel()
		return browser.NewDialogResolutionError(dialog, resolveErr)
	}
	if blocked {
		cancel()
		return browser.NewDialogBlockedError(dialog, fmt.Sprintf("tab close blocked by %s dialog; dialog dismissed", dialog.Type), err)
	}
	if err != nil {
		cancel()
		return dispatchedError("prepare target close", err)
	}
	if targetID == d.activeTargetID() {
		// Active target: close it first so its beforeunload dialog events
		// arrive on the still-attached session, then activate the replacement.
		// If beforeunload blocks the close, the active pointer is untouched and
		// the caller sees an honest dialog_blocked instead of a half-switched
		// tab. This also avoids chromedp.Cancel on the initial context, which
		// would gracefully close the whole browser.
		err := closeTab(opCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			return dispatchedError("target close failed", err)
		}
		if err := d.switchTarget(ctx, replacement); err != nil {
			return dispatchedError("activate replacement tab", err)
		}
		d.dropTarget(targetID)
		return nil
	}
	err = closeTab(opCtx)
	cancel()
	if err != nil {
		return dispatchedError("target close failed", err)
	}
	d.dropTarget(targetID)
	return nil
}

// dropTarget releases the cached context/cancel for a target that was closed
// by this driver (either explicitly or via tab close). The active pointer is
// never cleared here: the active-close path has already switched to the
// replacement and the non-active path never matches the active target.
func (d *driver) dropTarget(targetID string) {
	d.mu.Lock()
	if cancel := d.targetCancels[targetID]; cancel != nil {
		cancel()
	}
	delete(d.targetCancels, targetID)
	delete(d.targetContexts, targetID)
	d.mu.Unlock()
}

func dispatchedError(action string, err error) error {
	return &browser.DispatchError{Dispatched: true, Cause: fmt.Errorf("%s: %w", action, err)}
}

func replacementTab(tabs []browser.TabInfo, closing string) (string, error) {
	if len(tabs) <= 1 {
		return "", fmt.Errorf("cannot close last tab")
	}
	for _, tab := range tabs {
		if tab.ID != closing {
			return tab.ID, nil
		}
	}
	return "", fmt.Errorf("target %q not found", closing)
}

func validateNavigationURL(raw string) error {
	if raw == "about:blank" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() {
		return browser.NewError(browser.ErrInvalidURL, "URL must be an absolute HTTP/HTTPS URL", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return browser.NewError(browser.ErrUnsupportedScheme, "only HTTP and HTTPS navigation is supported", nil)
	}
	if parsed.Host == "" {
		return browser.NewError(browser.ErrInvalidURL, "URL must include a host", nil)
	}
	if parsed.User != nil {
		return browser.NewError(browser.ErrInvalidURL, "URL credentials are forbidden", nil)
	}
	return nil
}

func (d *driver) switchTarget(callCtx context.Context, targetID string) error {
	if err := callCtx.Err(); err != nil {
		return err
	}
	d.mu.RLock()
	baseCtx := d.browserBase
	d.mu.RUnlock()
	targetCtx, targetCancel := chromedp.NewContext(baseCtx, chromedp.WithTargetID(target.ID(targetID)))
	stop := context.AfterFunc(callCtx, targetCancel)
	err := chromedp.Run(targetCtx)
	stop()
	if err != nil {
		targetCancel()
		return err
	}
	if err := callCtx.Err(); err != nil {
		targetCancel()
		return err
	}
	d.mu.Lock()
	d.cdpCtx = targetCtx
	d.activeTarget = targetID
	if d.targetCancels == nil {
		d.targetCancels = make(map[string]context.CancelFunc)
	}
	d.targetCancels[targetID] = targetCancel
	d.targetContexts[targetID] = targetCtx
	d.mu.Unlock()
	d.listenTarget(targetCtx)
	return nil
}

func (d *driver) operationContext(callCtx context.Context) (context.Context, context.CancelFunc, error) {
	if d.closed.Load() {
		return nil, nil, fmt.Errorf("driver closed")
	}
	if callCtx == nil {
		return nil, nil, fmt.Errorf("nil action context")
	}
	if err := callCtx.Err(); err != nil {
		return nil, nil, err
	}
	base, _ := d.current()
	if base == nil {
		return nil, nil, fmt.Errorf("driver not started")
	}
	opCtx, cancel := context.WithCancel(base)
	stop := context.AfterFunc(callCtx, cancel)
	return opCtx, func() { stop(); cancel() }, nil
}

func (d *driver) operationContextForTarget(callCtx context.Context, targetID string) (context.Context, context.CancelFunc, error) {
	if targetID == "" || targetID == d.activeTargetID() {
		return d.operationContext(callCtx)
	}
	d.mu.RLock()
	cached := d.targetContexts[targetID]
	base := d.browserBase
	d.mu.RUnlock()
	if cached != nil {
		opCtx, opCancel := context.WithCancel(cached)
		stop := context.AfterFunc(callCtx, opCancel)
		return opCtx, func() { stop(); opCancel() }, nil
	}
	if base == nil {
		return nil, nil, fmt.Errorf("browser target context unavailable")
	}
	targetCtx, targetCancel := chromedp.NewContext(base, chromedp.WithTargetID(target.ID(targetID)))
	stop := context.AfterFunc(callCtx, targetCancel)
	if err := chromedp.Run(targetCtx); err != nil {
		stop()
		targetCancel()
		return nil, nil, fmt.Errorf("attach target %s: %w", targetID, err)
	}
	stop()
	d.mu.Lock()
	if d.targetContexts == nil {
		d.targetContexts = make(map[string]context.Context)
	}
	if d.targetCancels == nil {
		d.targetCancels = make(map[string]context.CancelFunc)
	}
	d.targetContexts[targetID] = targetCtx
	d.targetCancels[targetID] = targetCancel
	d.mu.Unlock()
	d.listenTarget(targetCtx)
	opCtx, opCancel := context.WithCancel(targetCtx)
	opStop := context.AfterFunc(callCtx, opCancel)
	return opCtx, func() { opStop(); opCancel() }, nil
}

func (d *driver) current() (context.Context, string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cdpCtx, d.activeTarget
}

func (d *driver) activeTargetID() string {
	_, targetID := d.current()
	return targetID
}

func (d *driver) Invalidations() <-chan browser.Invalidation { return d.invalCh }

func (d *driver) Close() error {
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		d.emit(browser.Invalidation{Kind: browser.InvalidationClosed, At: time.Now().UTC()})
		d.mu.Lock()
		// Drop any residual dialog policies: the driver is going away and no
		// operation may resolve them anymore.
		d.dialogs = nil
		d.mu.Unlock()
		d.mu.RLock()
		cancels := make([]context.CancelFunc, 0, len(d.targetCancels))
		for _, cancel := range d.targetCancels {
			cancels = append(cancels, cancel)
		}
		allocCancel := d.allocCancel
		rootCtx := d.rootCtx
		d.mu.RUnlock()
		if d.opts.OwnProcess && rootCtx != nil {
			closeCtx, closeCancel := context.WithTimeout(rootCtx, 10*time.Second)
			gracefulErr := chromedp.Cancel(closeCtx)
			closeCancel()
			d.mu.Lock()
			d.closeErr = gracefulErr
			d.closeErrPending = gracefulErr != nil
			d.mu.Unlock()
		}
		if !d.opts.OwnProcess {
			// chromedp context cancellation normally closes the attached target.
			// Clear each target pointer first so cancellation only disconnects this
			// client and leaves the shared browser and all of its tabs intact.
			d.detachTargets()
		}
		for _, cancel := range cancels {
			cancel()
		}
		if allocCancel != nil {
			allocCancel()
		}
		if d.eventCancel != nil {
			d.eventCancel()
		}
		<-d.eventDone
		if d.opts.OwnProcess {
			d.processExited.Store(true)
		}
	})
	return d.consumeCloseError()
}

// Kill forcibly reaps the browser process and then closes the driver. It is
// used only to reclaim a just-launched orphan when the launch itself fails
// endpoint validation; normal cleanup must call Close (detach only).
func (d *driver) Kill() error {
	d.mu.RLock()
	cmd := d.launchCmd
	done := d.launchDone
	d.mu.RUnlock()
	if cmd != nil {
		proc.KillTree(cmd)
		if done != nil {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
			}
		}
		d.processExited.Store(true)
	}
	return d.Close()
}

func (d *driver) detachTargets() {
	d.mu.RLock()
	contexts := make([]context.Context, 0, len(d.targetContexts)+1)
	if d.rootCtx != nil {
		contexts = append(contexts, d.rootCtx)
	}
	for _, ctx := range d.targetContexts {
		contexts = append(contexts, ctx)
	}
	d.mu.RUnlock()
	for _, ctx := range contexts {
		if c := chromedp.FromContext(ctx); c != nil {
			c.Target = nil
		}
	}
}

// ProcessID and ProcessExited are intentionally outside browser.Driver and
// BrowserInfo. They provide internal integration evidence without exposing PID
// through model-visible tool JSON.
func (d *driver) ProcessID() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.processID
}

func (d *driver) ProcessExited() bool { return d.processExited.Load() }

// DebugEndpoint returns the 127.0.0.1:<port> endpoint the browser actually
// listens on, or "" before a successful start. It is intentionally outside
// browser.Driver and BrowserInfo: it exists for lifecycle evidence and
// package-internal integration tests, never for model-visible output.
func (d *driver) DebugEndpoint() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.debugEndpoint
}

// CDPEndpoint satisfies browser.EndpointSource: the full loopback HTTP CDP
// endpoint (http://127.0.0.1:<port>) for the shared runtime record and the
// browser_attach tool.
func (d *driver) CDPEndpoint() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.debugEndpoint == "" {
		return ""
	}
	return "http://" + d.debugEndpoint
}

// consumeCloseError exposes a graceful-close error once. The process cleanup
// has already completed, so a later Manager retry may continue with profile
// release instead of being permanently pinned to the same stale error.
func (d *driver) consumeCloseError() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closeErrPending {
		return nil
	}
	d.closeErrPending = false
	return d.closeErr
}

func (d *driver) listenTarget(ctx context.Context) {
	targetID := ""
	if c := chromedp.FromContext(ctx); c != nil && c.Target != nil {
		targetID = string(c.Target.TargetID)
	}
	chromedp.ListenTarget(ctx, func(event any) { d.handleTargetEvent(targetID, event) })
}

// handleEvent routes browser-level events (target lifecycle). DOM/frame/dialog
// events arrive per target via handleTargetEvent.
func (d *driver) handleEvent(event any) {
	switch value := event.(type) {
	case *target.EventTargetCreated:
		if value.TargetInfo != nil {
			d.emit(browser.Invalidation{Kind: browser.InvalidationTarget, TargetID: string(value.TargetInfo.TargetID), At: time.Now().UTC()})
		}
	case *target.EventTargetInfoChanged:
		if value.TargetInfo != nil {
			d.emit(browser.Invalidation{Kind: browser.InvalidationTarget, TargetID: string(value.TargetInfo.TargetID), At: time.Now().UTC()})
		}
	case *target.EventTargetDestroyed:
		d.emit(browser.Invalidation{Kind: browser.InvalidationTarget, TargetID: string(value.TargetID), At: time.Now().UTC()})
	}
}

// handleTargetEvent routes events for one attached target. targetID comes from
// the context the listener was registered on, so dialog policy and
// invalidation are scoped to the right tab even with concurrent tabs.
func (d *driver) handleTargetEvent(targetID string, event any) {
	now := time.Now().UTC()
	switch value := event.(type) {
	case *dom.EventDocumentUpdated, *dom.EventAttributeModified, *dom.EventAttributeRemoved,
		*dom.EventCharacterDataModified, *dom.EventChildNodeCountUpdated,
		*dom.EventChildNodeInserted, *dom.EventChildNodeRemoved:
		d.emit(browser.Invalidation{Kind: browser.InvalidationDocument, TargetID: d.targetOrActive(targetID), At: now})
	case *page.EventFrameNavigated:
		frameID := ""
		if value.Frame != nil {
			frameID = string(value.Frame.ID)
		}
		d.emit(browser.Invalidation{Kind: browser.InvalidationFrame, TargetID: d.targetOrActive(targetID), FrameID: frameID, At: now})
	case *page.EventFrameStartedLoading:
		d.emit(browser.Invalidation{Kind: browser.InvalidationFrame, TargetID: d.targetOrActive(targetID), FrameID: string(value.FrameID), At: now})
	case *page.EventJavascriptDialogOpening:
		d.onDialogOpening(d.targetOrActive(targetID), value)
	case *page.EventJavascriptDialogClosed:
		// Resolution already happened on opening; nothing further to do.
	}
}

func (d *driver) targetOrActive(targetID string) string {
	if targetID != "" {
		return targetID
	}
	return d.activeTargetID()
}

func (d *driver) emit(event browser.Invalidation) {
	select {
	case d.events <- event:
	default:
		// Coalescing is safe: any invalidation makes the current snapshot stale.
	}
}

func (d *driver) runEventLoop(lifecycle context.Context) {
	defer close(d.eventDone)
	defer close(d.invalCh)
	for {
		select {
		case event := <-d.events:
			select {
			case d.invalCh <- event:
			default:
			}
		case <-lifecycle.Done():
			for {
				select {
				case event := <-d.events:
					select {
					case d.invalCh <- event:
					default:
					}
				default:
					return
				}
			}
		}
	}
}
