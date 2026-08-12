package browser_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/browser"
)

// fakeDriver implements browser.Driver for testing without a real browser.
type fakeDriver struct {
	info             browser.BrowserInfo
	url              string
	title            string
	nodes            []browser.ObservedNode
	invalCh          chan browser.Invalidation
	closed           atomic.Bool
	clicked          []browser.NodeRef
	typed            []typeRecord
	uploaded         []uploadRecord
	scrolled         []browser.ScrollInput
	tabs             []browser.TabInfo
	activeTab        string
	navigateCalls    atomic.Int32
	observeCalls     atomic.Int32
	clickCalls       atomic.Int32
	typeCalls        atomic.Int32
	uploadCalls      atomic.Int32
	closeCalls       atomic.Int32
	observeErr       error
	blockClick       chan struct{}
	blockNavigate    chan struct{}
	clickErr         error
	uploadErr        error
	closeErrOnce     atomic.Bool
	closeFailures    atomic.Int32
	lifecycleCtx     context.Context
	closeSawCanceled atomic.Bool
	newTabErr        error
	newTabCalls      atomic.Int32

	mu sync.Mutex
}

type typeRecord struct {
	ref   browser.NodeRef
	input browser.TypeInput
}

type uploadRecord struct {
	ref   browser.NodeRef
	files []string
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{
		info: browser.BrowserInfo{
			Kind:    browser.BrowserChrome,
			Product: "Chrome",
			Version: "999.0",
		},
		url:       "about:blank",
		title:     "Test Page",
		invalCh:   make(chan browser.Invalidation, 16),
		tabs:      []browser.TabInfo{{ID: "tab-1", URL: "about:blank", Title: "Test Page", Active: true}},
		activeTab: "tab-1",
		nodes: []browser.ObservedNode{
			{
				Ref:  browser.NodeRef{BackendNodeID: 1, TargetID: "tab-1", Bounds: browser.Rect{X: 10, Y: 10, Width: 100, Height: 30}},
				Role: "button", Tag: "button", Name: "Click me",
			},
			{
				Ref:  browser.NodeRef{BackendNodeID: 2, TargetID: "tab-1", Bounds: browser.Rect{X: 10, Y: 50, Width: 200, Height: 30}},
				Role: "textbox", Tag: "input", Name: "Search", Editable: true,
				Placeholder: "Type here...",
			},
		},
	}
}

func (d *fakeDriver) Info() browser.BrowserInfo { return d.info }
func (d *fakeDriver) Close() error {
	d.closeCalls.Add(1)
	if d.lifecycleCtx != nil && d.lifecycleCtx.Err() != nil {
		d.closeSawCanceled.Store(true)
	}
	if d.closeErrOnce.CompareAndSwap(true, false) {
		return fmt.Errorf("injected close failure")
	}
	if d.closeFailures.Load() > 0 {
		d.closeFailures.Add(-1)
		return fmt.Errorf("injected close failure")
	}
	d.closed.Store(true)
	return nil
}

func (d *fakeDriver) Navigate(ctx context.Context, url string) error {
	d.navigateCalls.Add(1)
	if d.blockNavigate != nil {
		select {
		case <-d.blockNavigate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.url = url
	d.title = "Navigated: " + url
	return nil
}

func (d *fakeDriver) Observe(ctx context.Context, opts browser.ObserveOptions) (browser.Observation, error) {
	d.observeCalls.Add(1)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.observeErr != nil {
		err := d.observeErr
		d.observeErr = nil
		return browser.Observation{}, err
	}
	nodes := make([]browser.ObservedNode, len(d.nodes))
	copy(nodes, d.nodes)
	return browser.Observation{
		URL:         d.url,
		Title:       d.title,
		ActiveTab:   d.activeTab,
		Tabs:        d.tabs,
		Text:        "This is a test page with interactive elements.",
		Nodes:       nodes,
		Fingerprint: fmt.Sprintf("fp-%s-%d", d.url, len(d.nodes)),
	}, nil
}

func (d *fakeDriver) Click(ctx context.Context, ref browser.NodeRef) error {
	d.clickCalls.Add(1)
	if d.blockClick != nil {
		select {
		case <-d.blockClick:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if d.clickErr != nil {
		return d.clickErr
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.clicked = append(d.clicked, ref)
	d.title = "Clicked!"
	return nil
}

func (d *fakeDriver) Type(ctx context.Context, ref browser.NodeRef, input browser.TypeInput) error {
	d.typeCalls.Add(1)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.typed = append(d.typed, typeRecord{ref: ref, input: input})
	d.title = "Typed!"
	return nil
}

func (d *fakeDriver) Upload(ctx context.Context, ref browser.NodeRef, files []string) error {
	d.uploadCalls.Add(1)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.uploadErr != nil {
		return d.uploadErr
	}
	d.uploaded = append(d.uploaded, uploadRecord{ref: ref, files: append([]string(nil), files...)})
	d.title = "Uploaded!"
	return nil
}

func (d *fakeDriver) Scroll(ctx context.Context, input browser.ScrollInput) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.scrolled = append(d.scrolled, input)
	d.title = "Scrolled!"
	return nil
}

func (d *fakeDriver) NewTab(ctx context.Context, url string) (string, error) {
	d.newTabCalls.Add(1)
	if d.newTabErr != nil {
		return "tab-created", d.newTabErr
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	id := fmt.Sprintf("tab-%d", len(d.tabs)+1)
	d.tabs = append(d.tabs, browser.TabInfo{ID: id, URL: url, Title: "New Tab", Active: false})
	return id, nil
}

func (d *fakeDriver) ActivateTab(ctx context.Context, targetID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.activeTab = targetID
	return nil
}

func (d *fakeDriver) CloseTab(ctx context.Context, targetID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, t := range d.tabs {
		if t.ID == targetID {
			d.tabs = append(d.tabs[:i], d.tabs[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("tab not found")
}

func (d *fakeDriver) Invalidations() <-chan browser.Invalidation {
	return d.invalCh
}

// fakeFactory implements browser.DriverFactory.
type fakeFactory struct {
	drivers   map[string]*fakeDriver
	mu        sync.Mutex
	created   []*fakeDriver
	configure func(*fakeDriver)
	opts      []browser.DriverOptions
	newCtx    context.Context
	newErr    error
}

func newFakeFactory() *fakeFactory {
	return &fakeFactory{drivers: make(map[string]*fakeDriver)}
}

func (f *fakeFactory) New(ctx context.Context, opts browser.DriverOptions) (browser.Driver, error) {
	if f.newErr != nil {
		return nil, f.newErr
	}
	d := newFakeDriver()
	d.lifecycleCtx = ctx
	if f.configure != nil {
		f.configure(d)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drivers[opts.UserDataDir] = d
	f.created = append(f.created, d)
	f.opts = append(f.opts, opts)
	f.newCtx = ctx
	return d, nil
}

func (f *fakeFactory) only(t *testing.T) *fakeDriver {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.created) != 1 {
		t.Fatalf("expected one driver, got %d", len(f.created))
	}
	return f.created[0]
}

// ── Core Tests ──────────────────────────────────────────────────────────────

func TestManagerFactoryNil(t *testing.T) {
	_, err := browser.NewManager(context.Background(), browser.Options{Factory: nil})
	if err == nil {
		t.Fatal("expected error for nil Factory")
	}
	be, ok := err.(*browser.Error)
	if !ok || be.Code != browser.ErrConfig {
		t.Fatalf("expected ErrConfig, got %v", err)
	}
}

func TestOpenCreatesSession(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.Close()

	result, err := mgr.Open(context.Background(), "owner-1", browser.OpenRequest{
		URL:       "https://example.com",
		RequestID: "req-1",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if result.SessionID == "" {
		t.Fatal("expected non-empty session ID")
	}
	if !result.Created {
		t.Fatal("expected Created=true for first open")
	}
	if result.Revision != 1 {
		t.Fatalf("expected revision 1, got %d", result.Revision)
	}
	if result.URL != "https://example.com" {
		t.Fatalf("expected url https://example.com, got %s", result.URL)
	}
}

func TestOpenSameOwnerReturnsSameSession(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.Close()

	r1, err := mgr.Open(context.Background(), "owner-1", browser.OpenRequest{RequestID: "req-1"})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	r2, err := mgr.Open(context.Background(), "owner-1", browser.OpenRequest{RequestID: "req-2"})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if r1.SessionID != r2.SessionID {
		t.Fatalf("expected same session ID: %s != %s", r1.SessionID, r2.SessionID)
	}
	if r2.Created {
		t.Fatal("expected Created=false for second open")
	}
}

func TestConcurrentOpenSameOwnerCreatesOneDriverAndNavigatesOnce(t *testing.T) {
	factory := newFakeFactory()
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	const callers = 8
	results := make(chan browser.OpenResult, callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			result, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{URL: "https://example.com", RequestID: "same-open"})
			results <- result
			errs <- err
		}()
	}
	var sessionID string
	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Open: %v", err)
		}
		result := <-results
		if sessionID == "" {
			sessionID = result.SessionID
		} else if result.SessionID != sessionID {
			t.Fatalf("multiple sessions: %s and %s", sessionID, result.SessionID)
		}
	}
	factory.mu.Lock()
	created := len(factory.created)
	factory.mu.Unlock()
	if created != 1 || factory.only(t).navigateCalls.Load() != 1 {
		t.Fatalf("created drivers=%d navigate calls=%d, want 1/1", created, factory.only(t).navigateCalls.Load())
	}
}

func TestDifferentOwnersAreIsolated(t *testing.T) {
	factory := newFakeFactory()
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	r1, _ := mgr.Open(context.Background(), "owner-1", browser.OpenRequest{RequestID: "req-1"})
	r2, _ := mgr.Open(context.Background(), "owner-2", browser.OpenRequest{RequestID: "req-2"})

	if r1.SessionID == r2.SessionID {
		t.Fatal("different owners must have different sessions")
	}
	if _, err := mgr.Click(context.Background(), "owner-1", browser.ClickRequest{Revision: r1.Revision, Index: 1, RequestID: "owner-1-click"}); err != nil {
		t.Fatal(err)
	}
	owner2, err := mgr.State(context.Background(), "owner-2", browser.StateRequest{Refresh: false})
	if err != nil {
		t.Fatal(err)
	}
	if owner2.Title == "Clicked!" || owner2.Revision != r2.Revision {
		t.Fatalf("owner-1 action leaked into owner-2: %+v", owner2)
	}
}

func TestMissingOwnerFails(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.Close()

	_, err := mgr.Open(context.Background(), "", browser.OpenRequest{RequestID: "req-1"})
	if err == nil {
		t.Fatal("expected error for empty owner")
	}
	be, ok := err.(*browser.Error)
	if !ok || be.Code != browser.ErrMissingSessionScope {
		t.Fatalf("expected ErrMissingSessionScope, got %v", err)
	}
}

func TestRevisionIncrementsOnChange(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.Close()

	ctx := context.Background()
	// First open.
	r1, _ := mgr.Open(ctx, "owner-1", browser.OpenRequest{URL: "https://example.com", RequestID: "req-1"})
	if r1.Revision != 1 {
		t.Fatalf("expected rev 1, got %d", r1.Revision)
	}

	// Navigate to a different URL.
	r2, err := mgr.Navigate(ctx, "owner-1", browser.NavigateRequest{
		URL: "https://other.com", RequestID: "nav-1",
	})
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if r2.BeforeRevision != 1 {
		t.Fatalf("expected before revision 1, got %d", r2.BeforeRevision)
	}
	if r2.AfterRevision != 2 {
		t.Fatalf("expected after revision 2, got %d", r2.AfterRevision)
	}
}

func TestStaleRevisionFails(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.Close()

	ctx := context.Background()
	// Open and get initial revision.
	r1, _ := mgr.Open(ctx, "owner-1", browser.OpenRequest{URL: "https://example.com", RequestID: "req-1"})

	// Navigate to change revision.
	mgr.Navigate(ctx, "owner-1", browser.NavigateRequest{URL: "https://other.com", RequestID: "nav-1"})

	// Try to click with the old revision.
	_, err := mgr.Click(ctx, "owner-1", browser.ClickRequest{
		Revision: r1.Revision, Index: 1, RequestID: "click-1",
	})
	if err == nil {
		t.Fatal("expected stale state error")
	}
	be, ok := err.(*browser.Error)
	if !ok || be.Code != browser.ErrStaleState {
		t.Fatalf("expected ErrStaleState, got %v", err)
	}
}

func TestStaleRevisionDoesNotDispatch(t *testing.T) {
	factory := newFakeFactory()
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Navigate(context.Background(), "owner", browser.NavigateRequest{URL: "https://example.com", RequestID: "nav"}); err != nil {
		t.Fatal(err)
	}
	_, err = mgr.Click(context.Background(), "owner", browser.ClickRequest{Revision: opened.Revision, Index: 1, RequestID: "stale-click"})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrStaleState {
		t.Fatalf("expected stale_state, got %v", err)
	}
	if got := factory.only(t).clickCalls.Load(); got != 0 {
		t.Fatalf("stale click dispatched %d times", got)
	}
}

func TestDriverInvalidationRejectsOldRevision(t *testing.T) {
	factory := newFakeFactory()
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	driver := factory.only(t)
	driver.invalCh <- browser.Invalidation{Kind: browser.InvalidationDocument, At: time.Now()}
	time.Sleep(20 * time.Millisecond)
	_, err = mgr.Click(context.Background(), "owner", browser.ClickRequest{Revision: opened.Revision, Index: 1, RequestID: "after-invalidation"})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrStaleState {
		t.Fatalf("expected stale_state after driver invalidation, got %v", err)
	}
	if driver.clickCalls.Load() != 0 {
		t.Fatal("invalidated snapshot reached driver")
	}
}

func TestIdempotentRequestID(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.Close()

	ctx := context.Background()
	mgr.Open(ctx, "owner-1", browser.OpenRequest{URL: "https://example.com", RequestID: "req-1"})

	// First click.
	r1, err := mgr.Click(ctx, "owner-1", browser.ClickRequest{
		Revision: 1, Index: 1, RequestID: "click-idem-1",
	})
	if err != nil {
		t.Fatalf("first click: %v", err)
	}

	// Same request_id, same params — should return cached result.
	r2, err := mgr.Click(ctx, "owner-1", browser.ClickRequest{
		Revision: 1, Index: 1, RequestID: "click-idem-1",
	})
	if err != nil {
		t.Fatalf("second click: %v", err)
	}
	if r1.AfterRevision != r2.AfterRevision {
		t.Fatal("expected same cached result")
	}
}

func TestRequestIDConflict(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.Close()

	ctx := context.Background()
	mgr.Open(ctx, "owner-1", browser.OpenRequest{URL: "https://example.com", RequestID: "req-1"})

	// First click with request_id "conflict-1".
	mgr.Click(ctx, "owner-1", browser.ClickRequest{
		Revision: 1, Index: 1, RequestID: "conflict-1",
	})

	// Same request_id, different params — should fail.
	_, err := mgr.Click(ctx, "owner-1", browser.ClickRequest{
		Revision: 1, Index: 2, RequestID: "conflict-1",
	})
	if err == nil {
		t.Fatal("expected request_id_conflict error")
	}
	be, ok := err.(*browser.Error)
	if !ok || be.Code != browser.ErrRequestIDConflict {
		t.Fatalf("expected ErrRequestIDConflict, got %v", err)
	}
}

func TestCloseSessionIdempotent(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.Close()

	ctx := context.Background()
	mgr.Open(ctx, "owner-1", browser.OpenRequest{RequestID: "req-1"})

	r1, _ := mgr.CloseSession(ctx, "owner-1")
	if !r1.Closed {
		t.Fatal("expected Closed=true")
	}

	// Second close is safe.
	r2, _ := mgr.CloseSession(ctx, "owner-1")
	if r2.Closed {
		t.Fatal("expected Closed=false for already-closed session")
	}
}

func TestManagerCloseClosesAll(t *testing.T) {
	mgr := newTestManager(t)

	ctx := context.Background()
	mgr.Open(ctx, "owner-1", browser.OpenRequest{RequestID: "req-1"})
	mgr.Open(ctx, "owner-2", browser.OpenRequest{RequestID: "req-2"})

	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Sessions after close should return browser_not_open.
	_, err := mgr.State(ctx, "owner-1", browser.StateRequest{Refresh: true})
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestManagerCloseKeepsDriverLifecycleAliveUntilResourcesClose(t *testing.T) {
	factory := newFakeFactory()
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"}); err != nil {
		t.Fatal(err)
	}
	driver := factory.only(t)
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}
	if driver.closeSawCanceled.Load() {
		t.Fatal("Manager canceled Driver lifecycle before Driver.Close")
	}
	if driver.lifecycleCtx == nil || driver.lifecycleCtx.Err() == nil {
		t.Fatal("Manager lifecycle was not canceled after resources closed")
	}
}

func TestManagerCloseErrorCanRetryAndReleaseProfile(t *testing.T) {
	factory := newFakeFactory()
	factory.configure = func(d *fakeDriver) { d.closeErrOnce.Store(true) }
	profiles := &recordingProfiles{lease: browser.ProfileLease{ID: "lease", Mode: browser.ProfileEphemeral, OwnProcess: true}}
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory, Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"}); err != nil {
		t.Fatal(err)
	}
	driver := factory.only(t)
	if err := mgr.Close(); err != nil {
		t.Fatalf("single Manager.Close did not converge: %v", err)
	}
	if driver.closeSawCanceled.Load() {
		t.Fatal("first Driver.Close saw canceled lifecycle")
	}
	if profiles.releaseCalls.Load() != 1 || driver.closeCalls.Load() != 2 {
		t.Fatalf("single cleanup did not converge: releases=%d closes=%d", profiles.releaseCalls.Load(), driver.closeCalls.Load())
	}
}

func TestManagerCloseTwoFailuresRemainRetryable(t *testing.T) {
	factory := newFakeFactory()
	factory.configure = func(d *fakeDriver) { d.closeFailures.Store(2) }
	profiles := &recordingProfiles{lease: browser.ProfileLease{ID: "lease", Mode: browser.ProfileEphemeral, OwnProcess: true}}
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory, Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Close(); err == nil {
		t.Fatal("expected two-attempt close failure")
	}
	if profiles.releaseCalls.Load() != 0 || factory.only(t).closeCalls.Load() != 2 {
		t.Fatalf("failed close lost session resources: releases=%d closes=%d", profiles.releaseCalls.Load(), factory.only(t).closeCalls.Load())
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("later retry did not converge: %v", err)
	}
	if profiles.releaseCalls.Load() != 1 || factory.only(t).closeCalls.Load() != 3 {
		t.Fatalf("retry counts release=%d close=%d", profiles.releaseCalls.Load(), factory.only(t).closeCalls.Load())
	}
}

func TestConcurrentManagerAndSessionCloseReleaseOnce(t *testing.T) {
	factory := newFakeFactory()
	profiles := &recordingProfiles{lease: browser.ProfileLease{ID: "lease", Mode: browser.ProfileEphemeral, OwnProcess: true}}
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory, Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"}); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() { errs <- mgr.Close() }()
	go func() { _, err := mgr.CloseSession(context.Background(), "owner"); errs <- err }()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent close: %v", err)
		}
	}
	if profiles.releaseCalls.Load() != 1 || factory.only(t).closeCalls.Load() != 1 {
		t.Fatalf("resources closed more than once: releases=%d closes=%d", profiles.releaseCalls.Load(), factory.only(t).closeCalls.Load())
	}
}

func TestStateReturnsSnapshot(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.Close()

	ctx := context.Background()
	mgr.Open(ctx, "owner-1", browser.OpenRequest{URL: "https://example.com", RequestID: "req-1"})

	state, err := mgr.State(ctx, "owner-1", browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.URL != "https://example.com" {
		t.Fatalf("expected url, got %s", state.URL)
	}
	if len(state.Elements) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(state.Elements))
	}
	if state.Elements[0].Index != 1 {
		t.Fatalf("expected 1-based indexing, got %d", state.Elements[0].Index)
	}
}

func TestStateReturnsDeepCopy(t *testing.T) {
	factory := newFakeFactory()
	factory.configure = func(d *fakeDriver) {
		checked := true
		d.nodes[0].Checked = &checked
	}
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if _, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"}); err != nil {
		t.Fatal(err)
	}
	first, err := mgr.State(context.Background(), "owner", browser.StateRequest{Refresh: false})
	if err != nil {
		t.Fatal(err)
	}
	first.Elements[0].Name = "mutated"
	*first.Elements[0].Checked = false
	first.Tabs[0].Title = "mutated"
	second, err := mgr.State(context.Background(), "owner", browser.StateRequest{Refresh: false})
	if err != nil {
		t.Fatal(err)
	}
	if second.Elements[0].Name == "mutated" || second.Elements[0].Checked == nil || !*second.Elements[0].Checked || second.Tabs[0].Title == "mutated" {
		t.Fatalf("caller mutation polluted snapshot: %+v", second)
	}
}

func TestTabOperations(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.Close()

	ctx := context.Background()
	mgr.Open(ctx, "owner-1", browser.OpenRequest{RequestID: "req-1"})

	// Create new tab.
	r1, err := mgr.Tab(ctx, "owner-1", browser.TabRequest{
		Revision: 1, Action: browser.TabNew, URL: "https://newtab.com", RequestID: "tab-1",
	})
	if err != nil {
		t.Fatalf("Tab new: %v", err)
	}
	if r1.AfterRevision != 2 {
		t.Fatalf("expected revision bump, got %d", r1.AfterRevision)
	}

	// Check state shows both tabs.
	state, _ := mgr.State(ctx, "owner-1", browser.StateRequest{Refresh: true})
	if len(state.Tabs) < 2 {
		t.Fatalf("expected at least 2 tabs, got %d", len(state.Tabs))
	}
}

func TestInvalidURLRejected(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.Close()

	ctx := context.Background()
	_, err := mgr.Navigate(ctx, "owner-1", browser.NavigateRequest{
		URL: "javascript:alert(1)", RequestID: "nav-bad",
	})
	if err == nil {
		t.Fatal("expected error for javascript: URL")
	}
}

func TestURLEmbeddedCredentialsRejected(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.Close()

	ctx := context.Background()
	_, err := mgr.Navigate(ctx, "owner-1", browser.NavigateRequest{
		URL: "https://user:pass@example.com", RequestID: "nav-creds",
	})
	if err == nil {
		t.Fatal("expected error for URL with embedded credentials")
	}
}

func newTestManager(t *testing.T) *browser.Manager {
	t.Helper()
	mgr, err := browser.NewManager(context.Background(), browser.Options{
		Factory:      newFakeFactory(),
		IdleTimeout:  10 * time.Minute,
		MaxTextChars: 20000,
		MaxElements:  400,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

func TestTypeRejectsFileInputBeforeDriverDispatch(t *testing.T) {
	factory := newFakeFactory()
	factory.configure = func(d *fakeDriver) {
		d.nodes[1].InputType = "file"
	}
	mgr, err := browser.NewManager(context.Background(), browser.Options{
		Factory: factory, AllowPasswordInput: true, AllowFileUpload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.Type(context.Background(), "owner", browser.TypeRequest{
		Revision: opened.Revision, Index: 2, Text: "must-not-dispatch", RequestID: "type",
	})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrSensitiveInputBlocked {
		t.Fatalf("expected sensitive_input_blocked, got %v", err)
	}
	if got := factory.only(t).typeCalls.Load(); got != 0 {
		t.Fatalf("driver Type call count = %d, want 0", got)
	}
}

func TestTypePasswordFollowsAllowPasswordInputSwitch(t *testing.T) {
	t.Run("allowed by default", func(t *testing.T) {
		factory := newFakeFactory()
		factory.configure = func(d *fakeDriver) {
			d.nodes[1].InputType = "password"
		}
		mgr, err := browser.NewManager(context.Background(), browser.Options{
			Factory: factory, AllowPasswordInput: true, AllowFileUpload: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer mgr.Close()
		opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := mgr.Type(context.Background(), "owner", browser.TypeRequest{
			Revision: opened.Revision, Index: 2, Text: "secret", RequestID: "type-pw",
		})
		if err != nil {
			t.Fatalf("password type with switch enabled: %v", err)
		}
		if result.Method != "type" || factory.only(t).typeCalls.Load() != 1 {
			t.Fatalf("password type result=%+v calls=%d", result, factory.only(t).typeCalls.Load())
		}
	})
	t.Run("rejected when disabled", func(t *testing.T) {
		factory := newFakeFactory()
		factory.configure = func(d *fakeDriver) {
			d.nodes[1].InputType = "password"
		}
		mgr, err := browser.NewManager(context.Background(), browser.Options{
			Factory: factory, AllowPasswordInput: false, AllowFileUpload: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer mgr.Close()
		opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = mgr.Type(context.Background(), "owner", browser.TypeRequest{
			Revision: opened.Revision, Index: 2, Text: "must-not-dispatch", RequestID: "type-pw",
		})
		var browserErr *browser.Error
		if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrSensitiveInputBlocked {
			t.Fatalf("expected sensitive_input_blocked, got %v", err)
		}
		if got := factory.only(t).typeCalls.Load(); got != 0 {
			t.Fatalf("driver Type call count = %d, want 0", got)
		}
	})
}

// ── Upload ─────────────────────────────────────────────────────────────────

func newUploadManager(t *testing.T, configure func(*browser.Options)) (*browser.Manager, *fakeFactory) {
	t.Helper()
	factory := newFakeFactory()
	opts := browser.Options{Factory: factory, AllowPasswordInput: true, AllowFileUpload: true}
	if configure != nil {
		configure(&opts)
	}
	effective := factory
	if f, ok := opts.Factory.(*fakeFactory); ok {
		effective = f
	}
	mgr, err := browser.NewManager(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return mgr, effective
}

// newFileInputManager builds a manager whose index-2 element is an enabled
// input[type=file], so path/target validation paths are reached.
func newFileInputManager(t *testing.T) (*browser.Manager, *fakeFactory) {
	t.Helper()
	return newUploadManager(t, func(opts *browser.Options) {
		fileFactory := newFakeFactory()
		fileFactory.configure = func(d *fakeDriver) { d.nodes[1].InputType = "file" }
		opts.Factory = fileFactory
	})
}

func fileInputFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	good := filepath.Join(root, "payload.txt")
	if err := os.WriteFile(good, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, good
}

func TestUploadSetsFilesOnFileInput(t *testing.T) {
	_, good := fileInputFixture(t)
	mgr, factory := newUploadManager(t, nil)
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	_ = opened
	factory.only(t).nodes[1].InputType = "file"
	state, err := mgr.State(context.Background(), "owner", browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := mgr.Upload(context.Background(), "owner", browser.UploadRequest{
		Revision: state.Revision, Index: 2, Files: []string{good}, RequestID: "upload-1",
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if result.Method != "upload" || result.BeforeRevision >= result.AfterRevision {
		t.Fatalf("upload result=%+v", result)
	}
	driver := factory.only(t)
	if got := driver.uploadCalls.Load(); got != 1 {
		t.Fatalf("driver Upload call count = %d, want 1", got)
	}
	driver.mu.Lock()
	uploaded := append([]uploadRecord(nil), driver.uploaded...)
	driver.mu.Unlock()
	if len(uploaded) != 1 || len(uploaded[0].files) != 1 || uploaded[0].files[0] != good {
		t.Fatalf("uploaded records = %+v, want [%s]", uploaded, good)
	}
}

func TestUploadRejectsWhenSwitchDisabled(t *testing.T) {
	_, good := fileInputFixture(t)
	mgr, factory := newUploadManager(t, func(opts *browser.Options) { opts.AllowFileUpload = false })
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.Upload(context.Background(), "owner", browser.UploadRequest{
		Revision: opened.Revision, Index: 2, Files: []string{good}, RequestID: "upload-deny",
	})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrSensitiveInputBlocked {
		t.Fatalf("expected sensitive_input_blocked, got %v", err)
	}
	if got := factory.only(t).uploadCalls.Load(); got != 0 {
		t.Fatalf("driver Upload call count = %d, want 0", got)
	}
}

func TestUploadRejectsNonFileTarget(t *testing.T) {
	_, good := fileInputFixture(t)
	mgr, factory := newUploadManager(t, nil)
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	// Index 1 is a button, not a file input.
	_, err = mgr.Upload(context.Background(), "owner", browser.UploadRequest{
		Revision: opened.Revision, Index: 1, Files: []string{good}, RequestID: "upload-wrong",
	})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrElementNotInteractable {
		t.Fatalf("expected element_not_interactable, got %v", err)
	}
	if got := factory.only(t).uploadCalls.Load(); got != 0 {
		t.Fatalf("driver Upload call count = %d, want 0", got)
	}
}

func TestUploadRejectsMissingPathBeforeDispatch(t *testing.T) {
	mgr, factory := newFileInputManager(t)
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "nope.txt")
	_, err = mgr.Upload(context.Background(), "owner", browser.UploadRequest{
		Revision: opened.Revision, Index: 2, Files: []string{missing}, RequestID: "upload-missing",
	})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrInvalidArguments {
		t.Fatalf("expected invalid_arguments for missing file, got %v", err)
	}
	if got := factory.only(t).uploadCalls.Load(); got != 0 {
		t.Fatalf("driver Upload call count = %d, want 0", got)
	}
}

func TestUploadRejectsDirectoryAndEmptyPath(t *testing.T) {
	root, good := fileInputFixture(t)
	mgr, factory := newFileInputManager(t)
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	for name, files := range map[string][]string{
		"directory": {root},
		"empty":     {""},
		"mixed":     {good, ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := mgr.Upload(context.Background(), "owner", browser.UploadRequest{
				Revision: opened.Revision, Index: 2, Files: files, RequestID: "upload-" + name,
			})
			var browserErr *browser.Error
			if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrInvalidArguments {
				t.Fatalf("expected invalid_arguments for %v, got %v", files, err)
			}
			if got := factory.only(t).uploadCalls.Load(); got != 0 {
				t.Fatalf("driver Upload call count = %d, want 0", got)
			}
		})
	}
}

func TestUploadRejectsMoreThanTwentyFiles(t *testing.T) {
	root := t.TempDir()
	files := make([]string, 0, 21)
	for i := 0; i < 21; i++ {
		p := filepath.Join(root, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	mgr, factory := newFileInputManager(t)
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.Upload(context.Background(), "owner", browser.UploadRequest{
		Revision: opened.Revision, Index: 2, Files: files, RequestID: "upload-many",
	})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrInvalidArguments {
		t.Fatalf("expected invalid_arguments for 21 files, got %v", err)
	}
	if got := factory.only(t).uploadCalls.Load(); got != 0 {
		t.Fatalf("driver Upload call count = %d, want 0", got)
	}
}

func TestUploadStaleRevisionNoDispatch(t *testing.T) {
	_, good := fileInputFixture(t)
	mgr, factory := newFileInputManager(t)
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	// Bump the revision with a successful click so the earlier revision is stale.
	if _, err := mgr.Click(context.Background(), "owner", browser.ClickRequest{Revision: opened.Revision, Index: 1, RequestID: "click-bump"}); err != nil {
		t.Fatal(err)
	}
	_, err = mgr.Upload(context.Background(), "owner", browser.UploadRequest{
		Revision: opened.Revision, Index: 2, Files: []string{good}, RequestID: "upload-stale",
	})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrStaleState {
		t.Fatalf("expected stale_state for old revision, got %v", err)
	}
	if got := factory.only(t).uploadCalls.Load(); got != 0 {
		t.Fatalf("driver Upload call count = %d, want 0", got)
	}
}

func TestUploadRequestReplayReturnsCachedResult(t *testing.T) {
	_, good := fileInputFixture(t)
	mgr, factory := newUploadManager(t, nil)
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	_ = opened
	factory.only(t).nodes[1].InputType = "file"
	state, err := mgr.State(context.Background(), "owner", browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	req := browser.UploadRequest{Revision: state.Revision, Index: 2, Files: []string{good}, RequestID: "upload-replay"}
	first, err := mgr.Upload(context.Background(), "owner", req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := mgr.Upload(context.Background(), "owner", req)
	if err != nil {
		t.Fatal(err)
	}
	if first.AfterRevision != second.AfterRevision || first.RequestID != second.RequestID {
		t.Fatalf("replay mismatch: %+v vs %+v", first, second)
	}
	if got := factory.only(t).uploadCalls.Load(); got != 1 {
		t.Fatalf("driver Upload call count = %d, want 1", got)
	}
}

func TestUploadRequestConflict(t *testing.T) {
	_, good := fileInputFixture(t)
	mgr, factory := newFileInputManager(t)
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.Upload(context.Background(), "owner", browser.UploadRequest{
		Revision: opened.Revision, Index: 2, Files: []string{good}, RequestID: "upload-conflict",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.Upload(context.Background(), "owner", browser.UploadRequest{
		Revision: opened.Revision, Index: 2, Files: []string{good, good}, RequestID: "upload-conflict",
	})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrRequestIDConflict {
		t.Fatalf("expected request_id_conflict, got %v", err)
	}
	if got := factory.only(t).uploadCalls.Load(); got != 1 {
		t.Fatalf("driver Upload call count = %d, want 1", got)
	}
}

func TestUploadConcurrentSameRequestDispatchesOnce(t *testing.T) {
	_, good := fileInputFixture(t)
	mgr, factory := newUploadManager(t, nil)
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	_ = opened
	factory.only(t).nodes[1].InputType = "file"
	state, err := mgr.State(context.Background(), "owner", browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	req := browser.UploadRequest{Revision: state.Revision, Index: 2, Files: []string{good}, RequestID: "upload-concurrent"}
	const callers = 8
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			_, err := mgr.Upload(context.Background(), "owner", req)
			errs <- err
		}()
	}
	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent upload error: %v", err)
		}
	}
	if got := factory.only(t).uploadCalls.Load(); got != 1 {
		t.Fatalf("driver Upload call count = %d, want 1", got)
	}
}

func TestUploadDriverRejectionIsPropagatedBeforeDispatch(t *testing.T) {
	_, good := fileInputFixture(t)
	mgr, factory := newUploadManager(t, nil)
	defer mgr.Close()
	if _, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"}); err != nil {
		t.Fatal(err)
	}
	driver := factory.only(t)
	driver.nodes[1].InputType = "file"
	state, err := mgr.State(context.Background(), "owner", browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	driver.mu.Lock()
	driver.uploadErr = browser.NewError(browser.ErrInvalidArguments, "target file input lacks the multiple attribute for multiple files", nil)
	driver.mu.Unlock()
	_, err = mgr.Upload(context.Background(), "owner", browser.UploadRequest{
		Revision: state.Revision, Index: 2, Files: []string{good, good}, RequestID: "upload-multiple",
	})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrInvalidArguments {
		t.Fatalf("driver multiple rejection must propagate as invalid_arguments, got %v", err)
	}
	// The Driver-level validation error is outcome-known and dispatched once.
	if got := driver.uploadCalls.Load(); got != 1 {
		t.Fatalf("driver Upload call count = %d, want 1", got)
	}
}

func TestUploadOutcomeUnknownDispatchesOnceAndReplays(t *testing.T) {
	_, good := fileInputFixture(t)
	mgr, factory := newUploadManager(t, nil)
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	_ = opened
	driver := factory.only(t)
	driver.nodes[1].InputType = "file"
	state, err := mgr.State(context.Background(), "owner", browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	driver.mu.Lock()
	driver.observeErr = fmt.Errorf("injected observe failure")
	driver.mu.Unlock()
	req := browser.UploadRequest{Revision: state.Revision, Index: 2, Files: []string{good}, RequestID: "upload-unknown"}
	for i := 0; i < 2; i++ {
		_, err := mgr.Upload(context.Background(), "owner", req)
		var browserErr *browser.Error
		if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrOutcomeUnknown || browserErr.OutcomeKnown {
			t.Fatalf("attempt %d: expected outcome_unknown, got %v", i+1, err)
		}
	}
	if got := driver.uploadCalls.Load(); got != 1 {
		t.Fatalf("driver Upload call count = %d, want 1", got)
	}
}

func TestTypeRejectsNonEditableElementBeforeDriverDispatch(t *testing.T) {
	factory := newFakeFactory()
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.Type(context.Background(), "owner", browser.TypeRequest{
		Revision: opened.Revision, Index: 1, Text: "no", RequestID: "type-button",
	})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrElementNotInteractable {
		t.Fatalf("expected element_not_interactable, got %v", err)
	}
	if factory.only(t).typeCalls.Load() != 0 {
		t.Fatal("non-editable type reached driver")
	}
}

func TestOutcomeUnknownIsReplayedWithoutRedispatch(t *testing.T) {
	factory := newFakeFactory()
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	driver := factory.only(t)
	driver.mu.Lock()
	driver.observeErr = fmt.Errorf("injected observe failure")
	driver.mu.Unlock()
	req := browser.ClickRequest{Revision: opened.Revision, Index: 1, RequestID: "click-once"}
	for i := 0; i < 2; i++ {
		_, err = mgr.Click(context.Background(), "owner", req)
		var browserErr *browser.Error
		if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrOutcomeUnknown || browserErr.OutcomeKnown {
			t.Fatalf("attempt %d: expected outcome_unknown, got %v", i+1, err)
		}
	}
	if got := driver.clickCalls.Load(); got != 1 {
		t.Fatalf("click dispatch count = %d, want 1", got)
	}
	_, err = mgr.Click(context.Background(), "owner", browser.ClickRequest{Revision: opened.Revision, Index: 1, RequestID: "new-click"})
	var stale *browser.Error
	if !errors.As(err, &stale) || stale.Code != browser.ErrBrowserNotOpen && stale.Code != browser.ErrStaleState {
		t.Fatalf("snapshot must be invalid after unknown outcome, got %v", err)
	}
}

func TestPartialDispatchErrorBecomesOutcomeUnknown(t *testing.T) {
	factory := newFakeFactory()
	factory.configure = func(d *fakeDriver) {
		d.clickErr = &browser.DispatchError{Dispatched: true, Cause: fmt.Errorf("mouse released failed")}
	}
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.Click(context.Background(), "owner", browser.ClickRequest{Revision: opened.Revision, Index: 1, RequestID: "partial"})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrOutcomeUnknown || browserErr.OutcomeKnown {
		t.Fatalf("expected outcome_unknown, got %v", err)
	}
	if factory.only(t).clickCalls.Load() != 1 {
		t.Fatal("partial action must be dispatched exactly once")
	}
}

func TestPartialTabDispatchIsOutcomeUnknownAndNotRepeated(t *testing.T) {
	factory := newFakeFactory()
	factory.configure = func(d *fakeDriver) {
		d.newTabErr = &browser.DispatchError{Dispatched: true, Cause: fmt.Errorf("target created but attach failed")}
	}
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	req := browser.TabRequest{Revision: opened.Revision, Action: browser.TabNew, URL: "https://example.com", RequestID: "partial-tab"}
	for i := 0; i < 2; i++ {
		_, err = mgr.Tab(context.Background(), "owner", req)
		var browserErr *browser.Error
		if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrOutcomeUnknown {
			t.Fatalf("attempt %d: expected outcome_unknown, got %v", i+1, err)
		}
	}
	if factory.only(t).newTabCalls.Load() != 1 {
		t.Fatalf("partial tab action repeated %d times", factory.only(t).newTabCalls.Load())
	}
}

func TestConcurrentDuplicateRequestDispatchesOnce(t *testing.T) {
	factory := newFakeFactory()
	gate := make(chan struct{})
	factory.configure = func(d *fakeDriver) { d.blockClick = gate }
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	req := browser.ClickRequest{Revision: opened.Revision, Index: 1, RequestID: "same"}
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := mgr.Click(context.Background(), "owner", req); errCh <- err }()
	}
	deadline := time.After(time.Second)
	for factory.only(t).clickCalls.Load() != 1 {
		select {
		case <-deadline:
			t.Fatal("click was not dispatched")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(gate)
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("duplicate call failed: %v", err)
		}
	}
	if got := factory.only(t).clickCalls.Load(); got != 1 {
		t.Fatalf("click dispatch count = %d, want 1", got)
	}
}

func TestOpenRequestIsIdempotentAndDriverOutlivesCaller(t *testing.T) {
	factory := newFakeFactory()
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	callCtx, cancel := context.WithCancel(context.Background())
	first, err := mgr.Open(callCtx, "owner", browser.OpenRequest{URL: "https://example.com", RequestID: "open-same"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	second, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{URL: "https://example.com", RequestID: "open-same"})
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID != second.SessionID || factory.only(t).navigateCalls.Load() != 1 {
		t.Fatalf("open was redispatched: first=%+v second=%+v calls=%d", first, second, factory.only(t).navigateCalls.Load())
	}
	factory.mu.Lock()
	longCtx := factory.newCtx
	factory.mu.Unlock()
	if err := longCtx.Err(); err != nil {
		t.Fatalf("driver lifetime inherited canceled call context: %v", err)
	}
	if _, err := mgr.State(context.Background(), "owner", browser.StateRequest{Refresh: true}); err != nil {
		t.Fatalf("driver did not survive caller cancellation: %v", err)
	}
}

func TestActionHonorsCallCancellationWithoutKillingSession(t *testing.T) {
	factory := newFakeFactory()
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory, ActionTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if _, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"}); err != nil {
		t.Fatal(err)
	}
	driver := factory.only(t)
	driver.blockNavigate = make(chan struct{})
	callCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := mgr.Navigate(callCtx, "owner", browser.NavigateRequest{URL: "https://example.com", RequestID: "cancel-nav"})
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for driver.navigateCalls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled navigate unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("navigate ignored call cancellation")
	}
	close(driver.blockNavigate)
	driver.blockNavigate = nil
	if _, err := mgr.State(context.Background(), "owner", browser.StateRequest{Refresh: true}); err != nil {
		t.Fatalf("call cancellation killed browser session: %v", err)
	}
}

type recordingProfiles struct {
	lease          browser.ProfileLease
	releaseCalls   atomic.Int32
	releaseCtxOK   atomic.Bool
	releaseErrOnce atomic.Bool
}

func (p *recordingProfiles) Acquire(context.Context, browser.ProfileRequest) (browser.ProfileLease, error) {
	return p.lease, nil
}
func (p *recordingProfiles) Release(ctx context.Context, lease browser.ProfileLease) error {
	p.releaseCalls.Add(1)
	p.releaseCtxOK.Store(ctx.Err() == nil)
	if lease != p.lease {
		return fmt.Errorf("wrong lease: %+v", lease)
	}
	if p.releaseErrOnce.CompareAndSwap(true, false) {
		return fmt.Errorf("injected release failure")
	}
	return nil
}

func TestProfileLeasePreservedAndReleasedWithIndependentContext(t *testing.T) {
	for _, mode := range []browser.ProfileMode{browser.ProfileManaged, browser.ProfileAttach} {
		t.Run(string(mode), func(t *testing.T) {
			profiles := &recordingProfiles{lease: browser.ProfileLease{
				ID: "lease", Mode: mode, UserDataDir: "managed-dir", DebugURL: "http://127.0.0.1:9222",
				OwnProcess: mode != browser.ProfileAttach, Persistent: true,
			}}
			factory := newFakeFactory()
			mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory, Profiles: profiles})
			if err != nil {
				t.Fatal(err)
			}
			callCtx, cancel := context.WithCancel(context.Background())
			if _, err := mgr.Open(callCtx, "owner", browser.OpenRequest{RequestID: "open"}); err != nil {
				t.Fatal(err)
			}
			cancel()
			if _, err := mgr.CloseSession(callCtx, "owner"); err != nil {
				t.Fatal(err)
			}
			factory.mu.Lock()
			opts := factory.opts[0]
			factory.mu.Unlock()
			if opts.DebugURL != profiles.lease.DebugURL || opts.OwnProcess != profiles.lease.OwnProcess || opts.UserDataDir != profiles.lease.UserDataDir {
				t.Fatalf("lease semantics degraded: opts=%+v lease=%+v", opts, profiles.lease)
			}
			if profiles.releaseCalls.Load() != 1 || !profiles.releaseCtxOK.Load() {
				t.Fatalf("release calls=%d independent_ctx=%v", profiles.releaseCalls.Load(), profiles.releaseCtxOK.Load())
			}
			if err := mgr.Close(); err != nil {
				t.Fatal(err)
			}
			if profiles.releaseCalls.Load() != 1 {
				t.Fatalf("release must be idempotent, calls=%d", profiles.releaseCalls.Load())
			}
		})
	}
}

func TestLaunchFailureKeepsLeaseForRetryWhenReleaseFails(t *testing.T) {
	factory := newFakeFactory()
	factory.newErr = fmt.Errorf("injected launch failure")
	profiles := &recordingProfiles{lease: browser.ProfileLease{ID: "lease", Mode: browser.ProfileEphemeral, OwnProcess: true}}
	profiles.releaseErrOnce.Store(true)
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory, Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrBrowserLaunchFailed || !strings.Contains(err.Error(), "release") {
		t.Fatalf("launch+release error not explicit: %v", err)
	}
	if profiles.releaseCalls.Load() != 1 {
		t.Fatalf("initial release calls=%d", profiles.releaseCalls.Load())
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("retry cleanup failed: %v", err)
	}
	if profiles.releaseCalls.Load() != 2 {
		t.Fatalf("lease was not retried: calls=%d", profiles.releaseCalls.Load())
	}
}

func TestEphemeralProfileOnlyDeletesOwnedDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-yet-created", "profiles")
	provider := browser.NewEphemeralProfileProvider(root)
	lease, err := provider.Acquire(context.Background(), browser.ProfileRequest{OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lease.UserDataDir); err != nil {
		t.Fatalf("owned profile missing: %v", err)
	}
	foreign := filepath.Join(t.TempDir(), "foreign")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := provider.Release(context.Background(), browser.ProfileLease{UserDataDir: foreign}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign directory was deleted: %v", err)
	}
	if err := provider.Release(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if err := provider.Release(context.Background(), lease); err != nil {
		t.Fatalf("second release must be safe: %v", err)
	}
	if _, err := os.Stat(lease.UserDataDir); !os.IsNotExist(err) {
		t.Fatalf("owned directory still exists or unexpected error: %v", err)
	}
}

func TestCloseFailureCanBeRetried(t *testing.T) {
	factory := newFakeFactory()
	factory.configure = func(d *fakeDriver) { d.closeErrOnce.Store(true) }
	profiles := &recordingProfiles{lease: browser.ProfileLease{ID: "lease", Mode: browser.ProfileEphemeral, OwnProcess: true}}
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory, Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"}); err != nil {
		t.Fatal(err)
	}
	result, err := mgr.CloseSession(context.Background(), "owner")
	if err != nil || !result.Closed {
		t.Fatalf("single CloseSession did not converge: result=%+v err=%v", result, err)
	}
	if profiles.releaseCalls.Load() != 1 || factory.only(t).closeCalls.Load() != 2 {
		t.Fatalf("unexpected retry counts: release=%d close=%d", profiles.releaseCalls.Load(), factory.only(t).closeCalls.Load())
	}
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseSessionTwoFailuresRemainRetryable(t *testing.T) {
	factory := newFakeFactory()
	factory.configure = func(d *fakeDriver) { d.closeFailures.Store(2) }
	profiles := &recordingProfiles{lease: browser.ProfileLease{ID: "lease", Mode: browser.ProfileEphemeral, OwnProcess: true}}
	mgr, err := browser.NewManager(context.Background(), browser.Options{Factory: factory, Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if _, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"}); err != nil {
		t.Fatal(err)
	}
	result, err := mgr.CloseSession(context.Background(), "owner")
	if err == nil || result.Closed {
		t.Fatalf("two close failures unexpectedly succeeded: result=%+v err=%v", result, err)
	}
	if profiles.releaseCalls.Load() != 0 {
		t.Fatal("profile released while Driver remained failed")
	}
	result, err = mgr.CloseSession(context.Background(), "owner")
	if err != nil || !result.Closed {
		t.Fatalf("CloseSession retry did not converge: result=%+v err=%v", result, err)
	}
	if profiles.releaseCalls.Load() != 1 || factory.only(t).closeCalls.Load() != 3 {
		t.Fatalf("retry counts release=%d close=%d", profiles.releaseCalls.Load(), factory.only(t).closeCalls.Load())
	}
}

func TestIdleReaperClosesAndReleasesSession(t *testing.T) {
	factory := newFakeFactory()
	profiles := &recordingProfiles{lease: browser.ProfileLease{ID: "lease", Mode: browser.ProfileEphemeral, OwnProcess: true}}
	mgr, err := browser.NewManager(context.Background(), browser.Options{
		Factory: factory, Profiles: profiles, IdleTimeout: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if _, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for (factory.only(t).closeCalls.Load() == 0 || profiles.releaseCalls.Load() == 0) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if factory.only(t).closeCalls.Load() != 1 || profiles.releaseCalls.Load() != 1 {
		t.Fatalf("idle cleanup missing: close=%d release=%d", factory.only(t).closeCalls.Load(), profiles.releaseCalls.Load())
	}
	_, err = mgr.State(context.Background(), "owner", browser.StateRequest{})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrBrowserNotOpen {
		t.Fatalf("reaped session still reachable: %v", err)
	}
}

func TestIdleReaperDoesNotCloseActiveOperation(t *testing.T) {
	factory := newFakeFactory()
	gate := make(chan struct{})
	factory.configure = func(d *fakeDriver) { d.blockClick = gate }
	profiles := &recordingProfiles{lease: browser.ProfileLease{ID: "lease", Mode: browser.ProfileEphemeral, OwnProcess: true}}
	mgr, err := browser.NewManager(context.Background(), browser.Options{
		Factory: factory, Profiles: profiles, IdleTimeout: 40 * time.Millisecond, ActionTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	opened, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := mgr.Click(context.Background(), "owner", browser.ClickRequest{Revision: opened.Revision, Index: 1, RequestID: "slow-click"})
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for factory.only(t).clickCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if factory.only(t).closeCalls.Load() != 0 || profiles.releaseCalls.Load() != 0 {
		t.Fatalf("active operation was reaped: close=%d release=%d", factory.only(t).closeCalls.Load(), profiles.releaseCalls.Load())
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("active operation failed: %v", err)
	}
}

func TestNewManagerNegativeIdleTimeoutIsConfigError(t *testing.T) {
	factory := newFakeFactory()
	_, err := browser.NewManager(context.Background(), browser.Options{Factory: factory, IdleTimeout: -time.Second})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrConfig {
		t.Fatalf("negative IdleTimeout = %v, want structured config error", err)
	}
}

func TestIdleReaperDisabledAtZeroKeepsSessionUntilExplicitClose(t *testing.T) {
	factory := newFakeFactory()
	profiles := &recordingProfiles{lease: browser.ProfileLease{ID: "lease", Mode: browser.ProfileEphemeral, OwnProcess: true}}
	mgr, err := browser.NewManager(context.Background(), browser.Options{
		Factory: factory, Profiles: profiles, IdleTimeout: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if _, err := mgr.Open(context.Background(), "owner", browser.OpenRequest{RequestID: "open"}); err != nil {
		t.Fatal(err)
	}
	// Wait far beyond a short reaper window (a 40ms-timeout reaper would have
	// fired many times here). With IdleTimeout=0 no reaper goroutine runs, so
	// the session must stay alive and usable.
	time.Sleep(300 * time.Millisecond)
	if factory.only(t).closeCalls.Load() != 0 || profiles.releaseCalls.Load() != 0 {
		t.Fatalf("IdleTimeout=0 reaped the session: close=%d release=%d", factory.only(t).closeCalls.Load(), profiles.releaseCalls.Load())
	}
	if _, err := mgr.State(context.Background(), "owner", browser.StateRequest{}); err != nil {
		t.Fatalf("session not usable after idle window with IdleTimeout=0: %v", err)
	}
	// Explicit close still releases the driver and profile.
	result, err := mgr.CloseSession(context.Background(), "owner")
	if err != nil || !result.Closed {
		t.Fatalf("explicit CloseSession failed: result=%+v err=%v", result, err)
	}
	if factory.only(t).closeCalls.Load() != 1 || profiles.releaseCalls.Load() != 1 {
		t.Fatalf("explicit close missing cleanup: close=%d release=%d", factory.only(t).closeCalls.Load(), profiles.releaseCalls.Load())
	}
	_, err = mgr.State(context.Background(), "owner", browser.StateRequest{})
	var browserErr *browser.Error
	if !errors.As(err, &browserErr) || browserErr.Code != browser.ErrBrowserNotOpen {
		t.Fatalf("session reachable after explicit close: %v", err)
	}
}
