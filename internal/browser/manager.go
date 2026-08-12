package browser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const profileCleanupTimeout = 10 * time.Second
const driverCloseAttempts = 2

// Manager implements Service, managing per-owner browser sessions.
type Manager struct {
	opts     Options
	profiles ProfileProvider
	creds    CredentialProvider
	factory  DriverFactory

	mu       sync.Mutex
	sessions map[string]*Session // ownerID -> Session
	closeMu  sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed atomic.Bool
}

// NewManager creates a new Manager. The Factory must be non-nil; otherwise an
// error is returned. IdleTimeout of 0 disables the idle reaper (browsers are
// only closed explicitly or by lifecycle cleanup); a negative IdleTimeout is a
// config error. The Manager does not start any browser process — that is
// deferred until the first browser_open call.
func NewManager(ctx context.Context, opts Options) (*Manager, error) {
	if opts.Factory == nil {
		return nil, NewError(ErrConfig, "DriverFactory is required; nil means no CDP backend is wired", nil)
	}
	if opts.IdleTimeout < 0 {
		return nil, NewError(ErrConfig, "Options.IdleTimeout must be >= 0; 0 disables the idle reaper", nil)
	}
	if opts.ActionTimeout <= 0 {
		opts.ActionTimeout = 30 * time.Second
	}
	if opts.StateTimeout <= 0 {
		opts.StateTimeout = 15 * time.Second
	}
	if opts.SettleWindow <= 0 {
		opts.SettleWindow = 300 * time.Millisecond
	}
	if opts.MaxTextChars <= 0 {
		opts.MaxTextChars = 20000
	}
	if opts.MaxElements <= 0 {
		opts.MaxElements = 400
	}
	if opts.Profiles == nil {
		opts.Profiles = NewEphemeralProfileProvider(opts.ProfileRoot)
	}
	if opts.Credentials == nil {
		opts.Credentials = NewDisabledCredentialProvider()
	}

	ctx, cancel := context.WithCancel(ctx)
	m := &Manager{
		opts:     opts,
		profiles: opts.Profiles,
		creds:    opts.Credentials,
		factory:  opts.Factory,
		sessions: make(map[string]*Session),
		ctx:      ctx,
		cancel:   cancel,
	}

	// Start idle reaper only when a positive timeout is configured. Zero means
	// the browser is never auto-closed for idleness; explicit Close/CloseSession
	// and lifecycle cleanup remain the only close paths.
	if m.opts.IdleTimeout > 0 {
		m.wg.Add(1)
		go m.reapLoop()
	}

	return m, nil
}

// getOrCreateSession returns the session for ownerID, creating one if needed.
func (m *Manager) getOrCreateSession(ownerID string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed.Load() {
		return nil, NewError(ErrBrowserDisconnected, "browser manager is closed", nil)
	}

	if s, ok := m.sessions[ownerID]; ok {
		return s, nil
	}
	s := newSession(ownerID, m.opts)
	m.sessions[ownerID] = s
	return s, nil
}

// getSession returns the session for ownerID, or nil.
func (m *Manager) getSession(ownerID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[ownerID]
}

// removeSession removes a session from the map.
func (m *Manager) removeSession(ownerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, ownerID)
}

// ensureDriver starts the browser if not already started.
func (m *Manager) ensureDriver(ctx context.Context, s *Session) (Driver, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if s.driver != nil && s.State() == SessionReady {
		return s.driver, nil
	}

	if s.driver != nil {
		// A broken/closing driver must never be handed back as healthy. Tear it
		// down under the operation lock, then create a fresh process.
		s.mu.Lock()
		if s.watchCancel != nil {
			s.watchCancel()
			s.watchCancel = nil
		}
		s.mu.Unlock()
		if err := s.driver.Close(); err != nil {
			return nil, NewError(ErrBrowserDisconnected, "failed to close broken browser before restart", err)
		}
		s.driver = nil
		if s.hasLease {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), profileCleanupTimeout)
			err := m.profiles.Release(cleanupCtx, s.lease)
			cancel()
			if err != nil {
				return nil, NewError(ErrProfileUnavailable, "failed to release broken browser profile", err)
			}
			s.lease = ProfileLease{}
			s.hasLease = false
		}
	}
	if s.hasLease {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), profileCleanupTimeout)
		err := m.profiles.Release(cleanupCtx, s.lease)
		cancel()
		if err != nil {
			return nil, NewError(ErrProfileUnavailable, "failed to release previous browser profile before retry", err)
		}
		s.lease = ProfileLease{}
		s.hasLease = false
	}

	// Acquire profile.
	acquireCtx, acquireCancel := context.WithTimeout(m.ctx, profileCleanupTimeout)
	defer acquireCancel()
	lease, err := m.profiles.Acquire(acquireCtx, ProfileRequest{
		OwnerID:  s.ownerID,
		Kind:     m.opts.BrowserKind,
		Headless: m.opts.Headless,
	})
	if err != nil {
		s.setState(SessionBroken)
		slog.Error("browser profile acquire failed", "owner", s.ownerID, "err", err)
		return nil, NewError(ErrProfileUnavailable, "failed to acquire profile", err)
	}

	// The Driver owns a long-lived browser process. Never derive its lifetime
	// from a single tool-call context.
	driver, err := m.factory.New(m.ctx, DriverOptions{
		BrowserKind:    m.opts.BrowserKind,
		ExecutablePath: m.opts.ExecutablePath,
		Headless:       m.opts.Headless,
		UserDataDir:    lease.UserDataDir,
		ProfileName:    lease.ProfileName,
		DebugURL:       lease.DebugURL,
		OwnProcess:     lease.OwnProcess,
		DenyDownloads:  true,
		SettleWindow:   m.opts.SettleWindow,
	})
	if err != nil {
		// Keep the lease attached to the Session until release succeeds. A failed
		// launch must remain recoverable through CloseSession/Manager.Close.
		s.lease = lease
		s.hasLease = true
		cleanupCtx, cancel := context.WithTimeout(context.Background(), profileCleanupTimeout)
		releaseErr := m.profiles.Release(cleanupCtx, lease)
		cancel()
		if releaseErr == nil {
			s.lease = ProfileLease{}
			s.hasLease = false
		}
		s.setState(SessionBroken)
		slog.Error("browser launch failed", "owner", s.ownerID, "err", err)
		if releaseErr != nil {
			return nil, NewError(ErrBrowserLaunchFailed, "failed to launch browser and release acquired profile", errors.Join(err, releaseErr))
		}
		return nil, NewError(ErrBrowserLaunchFailed, "failed to launch browser", err)
	}

	s.driver = driver
	s.lease = lease
	s.hasLease = true
	s.setState(SessionReady)

	// Start invalidation watcher.
	s.listenInvalidations(m.ctx)

	slog.Info("browser started",
		"session", s.id,
		"owner", s.ownerID,
		"kind", driver.Info().Kind,
		"version", driver.Info().Version,
	)
	return driver, nil
}

func (m *Manager) actionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, m.opts.ActionTimeout)
}

func (m *Manager) stateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, m.opts.StateTimeout)
}

func (m *Manager) observeAndPublish(ctx context.Context, s *Session, driver Driver, opts ObserveOptions, forceBump bool) (Observation, uint64, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		generation := s.Generation()
		obs, err := driver.Observe(ctx, opts)
		if err != nil {
			return Observation{}, generation, err
		}
		var revision uint64
		var accepted bool
		if forceBump {
			revision, accepted = s.publishSnapshotAfterWrite(s.id, obs, generation)
		} else {
			revision, accepted = s.publishSnapshot(s.id, obs, generation)
		}
		if accepted {
			return obs, revision, nil
		}
		lastErr = fmt.Errorf("page changed between observation and snapshot publish (attempt %d)", attempt+1)
	}
	return Observation{}, s.Generation(), lastErr
}

func (m *Manager) settle(ctx context.Context, s *Session) error {
	return s.waitForQuiet(ctx, m.opts.SettleWindow)
}

func beginAction(ctx context.Context, s *Session, requestID, sig string) (*RequestRecord, ActionResult, bool, error) {
	rec, leader, err := s.requests.Begin(requestID, sig)
	if err != nil {
		return nil, ActionResult{}, false, err
	}
	if leader {
		return rec, ActionResult{}, true, nil
	}
	v, err := rec.Wait(ctx)
	if err != nil {
		return nil, ActionResult{}, false, err
	}
	result, ok := v.(ActionResult)
	if !ok {
		return nil, ActionResult{}, false, NewError(ErrConfig, "cached browser action has invalid result type", nil)
	}
	return nil, result, false, nil
}

func completeAction(s *Session, rec *RequestRecord, result ActionResult, err error) (ActionResult, error) {
	s.requests.Complete(rec, result, err)
	return result, err
}

func completeDriverError(s *Session, rec *RequestRecord, action string, err error) (ActionResult, error) {
	if outcomeUnknown(err) {
		s.bumpGeneration()
		return completeAction(s, rec, ActionResult{}, NewError(ErrOutcomeUnknown, action+" may have been dispatched before failure", err))
	}
	return completeAction(s, rec, ActionResult{}, NewError(ErrElementNotInteractable, action+" failed", err))
}

func beginOpen(ctx context.Context, s *Session, requestID, sig string) (*RequestRecord, OpenResult, bool, error) {
	rec, leader, err := s.requests.Begin(requestID, sig)
	if err != nil {
		return nil, OpenResult{}, false, err
	}
	if leader {
		return rec, OpenResult{}, true, nil
	}
	v, err := rec.Wait(ctx)
	if err != nil {
		return nil, OpenResult{}, false, err
	}
	result, ok := v.(OpenResult)
	if !ok {
		return nil, OpenResult{}, false, NewError(ErrConfig, "cached browser open has invalid result type", nil)
	}
	return nil, result, false, nil
}

func completeOpen(s *Session, rec *RequestRecord, result OpenResult, err error) (OpenResult, error) {
	s.requests.Complete(rec, result, err)
	return result, err
}

// closeResources closes the process before releasing its profile. Both steps
// are retryable: a failed close leaves the session in the manager map.
func (m *Manager) closeResources(s *Session) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.setState(SessionClosing)
	s.mu.Lock()
	if s.watchCancel != nil {
		s.watchCancel()
		s.watchCancel = nil
	}
	s.mu.Unlock()
	if s.driver != nil {
		var closeErr error
		for attempt := 0; attempt < driverCloseAttempts; attempt++ {
			closeErr = s.driver.Close()
			if closeErr == nil {
				break
			}
		}
		if closeErr != nil {
			s.setState(SessionBroken)
			return fmt.Errorf("browser driver close failed after %d attempts: %w", driverCloseAttempts, closeErr)
		}
		s.driver = nil
	}
	if s.hasLease {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), profileCleanupTimeout)
		err := m.profiles.Release(cleanupCtx, s.lease)
		cancel()
		if err != nil {
			s.setState(SessionBroken)
			return err
		}
		s.lease = ProfileLease{}
		s.hasLease = false
	}
	s.requests.Clear()
	s.setState(SessionClosed)
	return nil
}

// ── Service implementation ──────────────────────────────────────────────────

// Open creates or reuses a browser session for the owner.
func (m *Manager) Open(ctx context.Context, ownerID string, req OpenRequest) (OpenResult, error) {
	if ownerID == "" {
		return OpenResult{}, NewError(ErrMissingSessionScope, "no parent session scope", nil)
	}
	if req.RequestID == "" {
		return OpenResult{}, NewError(ErrConfig, "request_id is required for open", nil)
	}
	if m.closed.Load() {
		return OpenResult{}, NewError(ErrBrowserDisconnected, "browser manager is closed", nil)
	}
	navURL := req.URL
	if navURL == "" {
		navURL = "about:blank"
	}
	if err := validateURL(navURL); err != nil {
		return OpenResult{}, err
	}

	s, err := m.getOrCreateSession(ownerID)
	if err != nil {
		return OpenResult{}, err
	}
	s.touch()
	defer s.touch()
	rec, cached, leader, err := beginOpen(ctx, s, req.RequestID, requestSignature("browser_open", map[string]any{"url": navURL}))
	if err != nil || !leader {
		return cached, err
	}

	driver, err := m.ensureDriver(ctx, s)
	if err != nil {
		return completeOpen(s, rec, OpenResult{}, err)
	}
	callCtx, cancel := m.actionContext(ctx)
	defer cancel()

	// Lock for the observe + initial navigation.
	s.opMu.Lock()
	defer s.opMu.Unlock()

	created := s.Snapshot() == nil

	// Navigate if URL is provided (and not empty and not about:blank which is default).
	if created || navURL != "about:blank" {
		if navURL == "about:blank" {
			// For about:blank of a fresh session, navigate to blank.
			if err := driver.Navigate(callCtx, navURL); err != nil {
				return completeOpen(s, rec, OpenResult{}, NewError(ErrNavigationTimeout, "navigation failed", err))
			}
		} else {
			if err := driver.Navigate(callCtx, navURL); err != nil {
				return completeOpen(s, rec, OpenResult{}, NewError(ErrNavigationTimeout, "navigation failed", err))
			}
		}
	}

	// Observe.
	if err := m.settle(callCtx, s); err != nil {
		return completeOpen(s, rec, OpenResult{}, NewError(ErrNavigationTimeout, "navigation settle canceled", err))
	}
	obs, rev, err := m.observeAndPublish(callCtx, s, driver, ObserveOptions{
		MaxTextChars: m.opts.MaxTextChars,
		MaxElements:  m.opts.MaxElements,
	}, false)
	if err != nil {
		s.markBroken()
		return completeOpen(s, rec, OpenResult{}, NewError(ErrStateTimeout, "initial observation failed", err))
	}

	result := OpenResult{
		SessionID: s.id,
		Created:   created,
		Revision:  rev,
		URL:       obs.URL,
		Title:     obs.Title,
		Browser:   driver.Info(),
	}
	return completeOpen(s, rec, result, nil)
}

// Navigate navigates the active tab.
func (m *Manager) Navigate(ctx context.Context, ownerID string, req NavigateRequest) (ActionResult, error) {
	if ownerID == "" {
		return ActionResult{}, NewError(ErrMissingSessionScope, "no parent session scope", nil)
	}
	if req.RequestID == "" {
		return ActionResult{}, NewError(ErrInvalidURL, "request_id is required for navigate", nil)
	}
	if err := validateURL(req.URL); err != nil {
		return ActionResult{}, err
	}

	s := m.getSession(ownerID)
	if s == nil || s.State() != SessionReady {
		return ActionResult{}, NewError(ErrBrowserNotOpen, "browser not open", nil)
	}
	s.touch()
	defer s.touch()

	sig := requestSignature("browser_navigate", map[string]any{"url": req.URL})
	rec, cached, leader, err := beginAction(ctx, s, req.RequestID, sig)
	if err != nil || !leader {
		return cached, err
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	beforeRev := s.Revision()

	driver := s.driver
	if driver == nil {
		return completeAction(s, rec, ActionResult{}, NewError(ErrBrowserDisconnected, "driver disconnected", nil))
	}
	callCtx, cancel := m.actionContext(ctx)
	defer cancel()

	if err := driver.Navigate(callCtx, req.URL); err != nil {
		return completeAction(s, rec, ActionResult{}, NewError(ErrNavigationTimeout, "navigation failed", err))
	}

	// Re-observe after action.
	if err := m.settle(callCtx, s); err != nil {
		s.bumpGeneration()
		return completeAction(s, rec, ActionResult{}, NewError(ErrOutcomeUnknown, "navigation dispatched but settle was canceled", err))
	}
	obs, afterRev, err := m.observeAndPublish(callCtx, s, driver, ObserveOptions{
		MaxTextChars: m.opts.MaxTextChars,
		MaxElements:  m.opts.MaxElements,
	}, true)
	if err != nil {
		s.bumpGeneration()
		return completeAction(s, rec, ActionResult{}, NewError(ErrOutcomeUnknown, "navigation succeeded but re-observation failed", err))
	}

	result := ActionResult{
		SessionID:      s.id,
		RequestID:      req.RequestID,
		BeforeRevision: beforeRev,
		AfterRevision:  afterRev,
		Changed:        beforeRev != afterRev,
		Method:         "navigate",
		URL:            obs.URL,
		Title:          obs.Title,
	}
	return completeAction(s, rec, result, nil)
}

// State returns the current page state.
func (m *Manager) State(ctx context.Context, ownerID string, req StateRequest) (PageState, error) {
	if ownerID == "" {
		return PageState{}, NewError(ErrMissingSessionScope, "no parent session scope", nil)
	}

	s := m.getSession(ownerID)
	if s == nil {
		return PageState{}, NewError(ErrBrowserNotOpen, "browser not open", nil)
	}
	s.touch()
	defer s.touch()

	if !req.Refresh {
		snap := s.Snapshot()
		if snap != nil {
			return snap.State, nil
		}
		// No snapshot, fall through to refresh.
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()
	callCtx, cancel := m.stateContext(ctx)
	defer cancel()

	driver := s.driver
	if driver == nil {
		return PageState{}, NewError(ErrBrowserDisconnected, "driver disconnected", nil)
	}

	maxChars := req.MaxChars
	if maxChars <= 0 || maxChars > m.opts.MaxTextChars {
		maxChars = m.opts.MaxTextChars
	}

	_, _, err := m.observeAndPublish(callCtx, s, driver, ObserveOptions{
		MaxTextChars: maxChars,
		MaxElements:  m.opts.MaxElements,
	}, false)
	if err != nil {
		s.markBroken()
		return PageState{}, NewError(ErrStateTimeout, "observation failed", err)
	}

	snap := s.Snapshot()
	if snap == nil {
		return PageState{}, NewError(ErrStateTimeout, "no snapshot after publish", nil)
	}
	return snap.State, nil
}

// Click clicks an element identified by revision and index.
func (m *Manager) Click(ctx context.Context, ownerID string, req ClickRequest) (ActionResult, error) {
	if ownerID == "" {
		return ActionResult{}, NewError(ErrMissingSessionScope, "no parent session scope", nil)
	}
	if req.RequestID == "" {
		return ActionResult{}, NewError(ErrStaleState, "request_id is required for click", nil)
	}

	s := m.getSession(ownerID)
	if s == nil || s.State() != SessionReady {
		return ActionResult{}, NewError(ErrBrowserNotOpen, "browser not open", nil)
	}
	s.touch()
	defer s.touch()

	// Idempotency check.
	sig := requestSignature("browser_click", map[string]any{"revision": req.Revision, "index": req.Index})
	rec, cached, leader, err := beginAction(ctx, s, req.RequestID, sig)
	if err != nil || !leader {
		return cached, err
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	beforeRev := s.Revision()

	// Validate revision.
	snap, err := s.validateRevision(req.Revision)
	if err != nil {
		return completeAction(s, rec, ActionResult{}, err)
	}

	ref, ok := snap.Nodes[req.Index]
	if !ok {
		return completeAction(s, rec, ActionResult{}, NewError(ErrElementNotFound, fmt.Sprintf("element %d not found", req.Index), nil))
	}

	driver := s.driver
	if driver == nil {
		return completeAction(s, rec, ActionResult{}, NewError(ErrBrowserDisconnected, "driver disconnected", nil))
	}
	callCtx, cancel := m.actionContext(ctx)
	defer cancel()

	// Dispatch the click.
	dispatchErr := driver.Click(callCtx, ref)

	if dispatchErr != nil {
		return completeDriverError(s, rec, "click", dispatchErr)
	}

	// Re-observe. If this fails, the click was dispatched but we don't know the result.
	if err := m.settle(callCtx, s); err != nil {
		s.bumpGeneration()
		return completeAction(s, rec, ActionResult{}, NewError(ErrOutcomeUnknown, "click dispatched but settle was canceled", err))
	}
	obs, afterRev, err := m.observeAndPublish(callCtx, s, driver, ObserveOptions{
		MaxTextChars: m.opts.MaxTextChars,
		MaxElements:  m.opts.MaxElements,
	}, true)
	if err != nil {
		s.bumpGeneration()
		return completeAction(s, rec, ActionResult{}, NewError(ErrOutcomeUnknown, "click dispatched but re-observation failed", err))
	}

	result := ActionResult{
		SessionID:      s.id,
		RequestID:      req.RequestID,
		BeforeRevision: beforeRev,
		AfterRevision:  afterRev,
		Changed:        beforeRev != afterRev,
		Method:         "click",
		URL:            obs.URL,
		Title:          obs.Title,
	}
	return completeAction(s, rec, result, nil)
}

// Type types text into an editable element.
func (m *Manager) Type(ctx context.Context, ownerID string, req TypeRequest) (ActionResult, error) {
	if ownerID == "" {
		return ActionResult{}, NewError(ErrMissingSessionScope, "no parent session scope", nil)
	}
	if req.RequestID == "" {
		return ActionResult{}, NewError(ErrStaleState, "request_id is required for type", nil)
	}

	s := m.getSession(ownerID)
	if s == nil || s.State() != SessionReady {
		return ActionResult{}, NewError(ErrBrowserNotOpen, "browser not open", nil)
	}
	s.touch()
	defer s.touch()

	// Idempotency check.
	sig := requestSignature("browser_type", map[string]any{
		"revision":    req.Revision,
		"index":       req.Index,
		"text":        req.Text,
		"clear":       req.Clear,
		"press_enter": req.PressEnter,
	})
	rec, cached, leader, err := beginAction(ctx, s, req.RequestID, sig)
	if err != nil || !leader {
		return cached, err
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	beforeRev := s.Revision()

	snap, err := s.validateRevision(req.Revision)
	if err != nil {
		return completeAction(s, rec, ActionResult{}, err)
	}

	ref, ok := snap.Nodes[req.Index]
	if !ok {
		return completeAction(s, rec, ActionResult{}, NewError(ErrElementNotFound, fmt.Sprintf("element %d not found", req.Index), nil))
	}
	inputType := ""
	editable := false
	disabled := false
	for _, element := range snap.State.Elements {
		if element.Index == req.Index {
			inputType = element.InputType
			editable = element.Editable
			disabled = element.Disabled
			break
		}
	}
	inputType = strings.ToLower(strings.TrimSpace(inputType))
	if inputType == "password" || inputType == "file" {
		return completeAction(s, rec, ActionResult{}, NewError(ErrSensitiveInputBlocked, "browser_type refuses password and file inputs", nil))
	}
	if !editable || disabled {
		return completeAction(s, rec, ActionResult{}, NewError(ErrElementNotInteractable, "target element is not editable", nil))
	}

	driver := s.driver
	if driver == nil {
		return completeAction(s, rec, ActionResult{}, NewError(ErrBrowserDisconnected, "driver disconnected", nil))
	}
	callCtx, cancel := m.actionContext(ctx)
	defer cancel()

	dispatchErr := driver.Type(callCtx, ref, TypeInput{
		Text:       req.Text,
		Clear:      req.Clear,
		PressEnter: req.PressEnter,
	})
	if dispatchErr != nil {
		var browserErr *Error
		if errors.As(dispatchErr, &browserErr) && browserErr.Code == ErrSensitiveInputBlocked {
			return completeAction(s, rec, ActionResult{}, browserErr)
		}
		return completeDriverError(s, rec, "type", dispatchErr)
	}

	if err := m.settle(callCtx, s); err != nil {
		s.bumpGeneration()
		return completeAction(s, rec, ActionResult{}, NewError(ErrOutcomeUnknown, "type dispatched but settle was canceled", err))
	}
	obs, afterRev, err := m.observeAndPublish(callCtx, s, driver, ObserveOptions{
		MaxTextChars: m.opts.MaxTextChars,
		MaxElements:  m.opts.MaxElements,
	}, true)
	if err != nil {
		s.bumpGeneration()
		return completeAction(s, rec, ActionResult{}, NewError(ErrOutcomeUnknown, "type dispatched but re-observation failed", err))
	}

	result := ActionResult{
		SessionID:      s.id,
		RequestID:      req.RequestID,
		BeforeRevision: beforeRev,
		AfterRevision:  afterRev,
		Changed:        beforeRev != afterRev,
		Method:         "type",
		URL:            obs.URL,
		Title:          obs.Title,
	}
	return completeAction(s, rec, result, nil)
}

// Scroll scrolls the page or an element.
func (m *Manager) Scroll(ctx context.Context, ownerID string, req ScrollRequest) (ActionResult, error) {
	if ownerID == "" {
		return ActionResult{}, NewError(ErrMissingSessionScope, "no parent session scope", nil)
	}
	if req.RequestID == "" {
		return ActionResult{}, NewError(ErrStaleState, "request_id is required for scroll", nil)
	}
	if req.DeltaY == 0 || req.DeltaY < -4000 || req.DeltaY > 4000 {
		return ActionResult{}, NewError(ErrInvalidURL, "delta_y must be in [-4000,-1] or [1,4000]", nil)
	}

	s := m.getSession(ownerID)
	if s == nil || s.State() != SessionReady {
		return ActionResult{}, NewError(ErrBrowserNotOpen, "browser not open", nil)
	}
	s.touch()
	defer s.touch()

	sig := requestSignature("browser_scroll", map[string]any{
		"revision": req.Revision,
		"index":    req.Index,
		"delta_y":  req.DeltaY,
	})
	rec, cached, leader, err := beginAction(ctx, s, req.RequestID, sig)
	if err != nil || !leader {
		return cached, err
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	beforeRev := s.Revision()

	snap, err := s.validateRevision(req.Revision)
	if err != nil {
		return completeAction(s, rec, ActionResult{}, err)
	}

	var nodeRef *NodeRef
	if req.Index > 0 {
		ref, ok := snap.Nodes[req.Index]
		if !ok {
			return completeAction(s, rec, ActionResult{}, NewError(ErrElementNotFound, fmt.Sprintf("element %d not found", req.Index), nil))
		}
		nodeRef = &ref
	}

	driver := s.driver
	if driver == nil {
		return completeAction(s, rec, ActionResult{}, NewError(ErrBrowserDisconnected, "driver disconnected", nil))
	}
	callCtx, cancel := m.actionContext(ctx)
	defer cancel()

	dispatchErr := driver.Scroll(callCtx, ScrollInput{
		Node:   nodeRef,
		DeltaY: req.DeltaY,
	})
	if dispatchErr != nil {
		return completeDriverError(s, rec, "scroll", dispatchErr)
	}

	if err := m.settle(callCtx, s); err != nil {
		s.bumpGeneration()
		return completeAction(s, rec, ActionResult{}, NewError(ErrOutcomeUnknown, "scroll dispatched but settle was canceled", err))
	}
	obs, afterRev, err := m.observeAndPublish(callCtx, s, driver, ObserveOptions{
		MaxTextChars: m.opts.MaxTextChars,
		MaxElements:  m.opts.MaxElements,
	}, true)
	if err != nil {
		s.bumpGeneration()
		return completeAction(s, rec, ActionResult{}, NewError(ErrOutcomeUnknown, "scroll dispatched but re-observation failed", err))
	}

	result := ActionResult{
		SessionID:      s.id,
		RequestID:      req.RequestID,
		BeforeRevision: beforeRev,
		AfterRevision:  afterRev,
		Changed:        beforeRev != afterRev,
		Method:         "scroll",
		URL:            obs.URL,
		Title:          obs.Title,
	}
	return completeAction(s, rec, result, nil)
}

// Tab manipulates browser tabs.
func (m *Manager) Tab(ctx context.Context, ownerID string, req TabRequest) (ActionResult, error) {
	if ownerID == "" {
		return ActionResult{}, NewError(ErrMissingSessionScope, "no parent session scope", nil)
	}
	if req.RequestID == "" {
		return ActionResult{}, NewError(ErrStaleState, "request_id is required for tab", nil)
	}

	s := m.getSession(ownerID)
	if s == nil || s.State() != SessionReady {
		return ActionResult{}, NewError(ErrBrowserNotOpen, "browser not open", nil)
	}
	s.touch()
	defer s.touch()

	sig := requestSignature("browser_tab", map[string]any{
		"revision": req.Revision,
		"action":   string(req.Action),
		"tab_id":   req.TabID,
		"url":      req.URL,
	})
	rec, cached, leader, err := beginAction(ctx, s, req.RequestID, sig)
	if err != nil || !leader {
		return cached, err
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	beforeRev := s.Revision()

	// Validate revision (tab actions depend on current tabs state).
	_, err = s.validateRevision(req.Revision)
	if err != nil {
		return completeAction(s, rec, ActionResult{}, err)
	}

	driver := s.driver
	if driver == nil {
		return completeAction(s, rec, ActionResult{}, NewError(ErrBrowserDisconnected, "driver disconnected", nil))
	}
	callCtx, cancel := m.actionContext(ctx)
	defer cancel()

	switch req.Action {
	case TabNew:
		navURL := req.URL
		if navURL == "" {
			navURL = "about:blank"
		}
		if navURL != "about:blank" {
			if err := validateURL(navURL); err != nil {
				return completeAction(s, rec, ActionResult{}, err)
			}
		}
		_, err := driver.NewTab(callCtx, navURL)
		if err != nil {
			if outcomeUnknown(err) {
				s.bumpGeneration()
				return completeAction(s, rec, ActionResult{}, NewError(ErrOutcomeUnknown, "tab creation outcome is unknown", err))
			}
			return completeAction(s, rec, ActionResult{}, NewError(ErrTargetClosed, "failed to create tab", err))
		}

	case TabActivate:
		if req.TabID == "" {
			return completeAction(s, rec, ActionResult{}, NewError(ErrInvalidURL, "tab_id is required for activate", nil))
		}
		if err := driver.ActivateTab(callCtx, req.TabID); err != nil {
			if outcomeUnknown(err) {
				s.bumpGeneration()
				return completeAction(s, rec, ActionResult{}, NewError(ErrOutcomeUnknown, "tab activation outcome is unknown", err))
			}
			return completeAction(s, rec, ActionResult{}, NewError(ErrTargetClosed, "failed to activate tab", err))
		}

	case TabClose:
		if req.TabID == "" {
			return completeAction(s, rec, ActionResult{}, NewError(ErrInvalidURL, "tab_id is required for close", nil))
		}
		// Reject closing the last tab.
		snap := s.Snapshot()
		if snap != nil && len(snap.State.Tabs) <= 1 {
			return completeAction(s, rec, ActionResult{}, NewError(ErrLastTab, "cannot close the last tab; use browser_close", nil))
		}
		if err := driver.CloseTab(callCtx, req.TabID); err != nil {
			if outcomeUnknown(err) {
				s.bumpGeneration()
				return completeAction(s, rec, ActionResult{}, NewError(ErrOutcomeUnknown, "tab close outcome is unknown", err))
			}
			return completeAction(s, rec, ActionResult{}, NewError(ErrTargetClosed, "failed to close tab", err))
		}
	default:
		return completeAction(s, rec, ActionResult{}, NewError(ErrInvalidURL, fmt.Sprintf("unknown tab action: %s", req.Action), nil))
	}

	// Re-observe.
	if err := m.settle(callCtx, s); err != nil {
		s.bumpGeneration()
		return completeAction(s, rec, ActionResult{}, NewError(ErrOutcomeUnknown, "tab action dispatched but settle was canceled", err))
	}
	obs, afterRev, err := m.observeAndPublish(callCtx, s, driver, ObserveOptions{
		MaxTextChars: m.opts.MaxTextChars,
		MaxElements:  m.opts.MaxElements,
	}, true)
	if err != nil {
		s.bumpGeneration()
		return completeAction(s, rec, ActionResult{}, NewError(ErrOutcomeUnknown, "tab action succeeded but re-observation failed", err))
	}

	result := ActionResult{
		SessionID:      s.id,
		RequestID:      req.RequestID,
		BeforeRevision: beforeRev,
		AfterRevision:  afterRev,
		Changed:        beforeRev != afterRev,
		Method:         "tab_" + string(req.Action),
		URL:            obs.URL,
		Title:          obs.Title,
	}
	return completeAction(s, rec, result, nil)
}

// CloseSession closes the browser session for an owner.
func (m *Manager) CloseSession(ctx context.Context, ownerID string) (CloseResult, error) {
	m.closeMu.Lock()
	defer m.closeMu.Unlock()
	if ownerID == "" {
		return CloseResult{}, NewError(ErrMissingSessionScope, "no parent session scope", nil)
	}

	s := m.getSession(ownerID)
	if s == nil {
		return CloseResult{SessionID: "", Closed: false}, nil
	}

	if err := m.closeResources(s); err != nil {
		slog.Warn("browser session close error", "owner", ownerID, "err", err)
		return CloseResult{SessionID: s.id, Closed: false}, err
	}
	m.removeSession(ownerID)

	slog.Info("browser session closed", "owner", ownerID, "session", s.id)
	return CloseResult{SessionID: s.id, Closed: true}, nil
}

// Close closes all sessions and shuts down the idle reaper.
func (m *Manager) Close() error {
	// Serialize full shutdown so Close and retrying Close calls cannot cancel
	// the Driver lifecycle while another caller is still closing resources.
	m.closeMu.Lock()
	defer m.closeMu.Unlock()
	m.closed.Store(true)

	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	var errs []error
	for _, s := range sessions {
		if err := m.closeResources(s); err != nil {
			errs = append(errs, err)
			continue
		}
		m.removeSession(s.ownerID)
	}

	// Driver graceful shutdown requires the Manager lifecycle to remain alive.
	// Cancel the reaper/lifecycle only after every close attempt completed.
	m.cancel()
	m.wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("browser manager close: %d sessions had errors: %w", len(errs), errs[0])
	}
	return nil
}

// ── Idle reaper ─────────────────────────────────────────────────────────────

func (m *Manager) reapLoop() {
	defer m.wg.Done()

	interval := m.opts.IdleTimeout / 4
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.reapIdle()
		}
	}
}

func (m *Manager) reapIdle() {
	m.mu.Lock()
	var toReap []*Session
	for _, s := range m.sessions {
		state := s.State()
		if state == SessionReady || state == SessionBroken {
			if s.idleDuration() > m.opts.IdleTimeout {
				// Try to acquire opMu to ensure no active operation.
				if s.opMu.TryLock() {
					s.setState(SessionClosing)
					toReap = append(toReap, s)
					s.opMu.Unlock()
				}
			}
		}
	}
	m.mu.Unlock()

	for _, s := range toReap {
		slog.Info("reaping idle browser session", "session", s.id, "owner", s.ownerID)
		if err := m.closeResources(s); err != nil {
			slog.Warn("idle session close error", "session", s.id, "err", err)
			continue
		}
		m.removeSession(s.ownerID)
	}
}

// ── URL validation ──────────────────────────────────────────────────────────

func validateURL(raw string) error {
	if raw == "about:blank" {
		return nil
	}
	if len(raw) > 8192 {
		return NewError(ErrInvalidURL, "url exceeds 8192 characters", nil)
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return NewError(ErrInvalidURL, "url must be an absolute HTTP/HTTPS URL", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return NewError(ErrUnsupportedScheme, "only absolute http/https urls are allowed", nil)
	}
	if u.User != nil {
		return NewError(ErrInvalidURL, "url must not contain embedded credentials", nil)
	}
	return nil
}

// Compile-time check.
var _ Service = (*Manager)(nil)
