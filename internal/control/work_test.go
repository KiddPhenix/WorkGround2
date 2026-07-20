package control

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/work"
)

// --- Options nil safety ---

func TestOptionsNilWorkDefaultSafe(t *testing.T) {
	// Zero-value Options must produce a Controller that handles nil Work/WorKViews.
	c := New(Options{})
	if c == nil {
		t.Fatal("New with zero Options returned nil")
	}
	// WorkControl must return a non-panicking port.
	wc := c.WorkControl()
	if wc == nil {
		t.Fatal("WorkControl returned nil interface")
	}
	_, err := wc.CreateWork(context.Background(), work.CreateWorkInput{})
	if err == nil {
		t.Fatal("CreateWork should return error when Work is disabled")
	}

	// WorkViews returns nil when disabled.
	if v := c.WorkViews(); v != nil {
		t.Fatal("WorkViews should return nil when Work is disabled")
	}
}

func TestOptionsTypedNilWorkSafe(t *testing.T) {
	var svc *work.Service
	c := New(Options{Work: svc})
	if c == nil {
		t.Fatal("New with typed nil Work returned nil")
	}
	wc := c.WorkControl()
	_, err := wc.GetWork(context.Background(), "w-1")
	if err == nil {
		t.Fatal("GetWork should return error with typed nil service")
	}
	if err.Error() == "" {
		t.Fatal("error message should not be empty")
	}
}

// --- WorkViewBroadcaster ---

func TestBroadcasterSubscribeUnsubscribe(t *testing.T) {
	b := NewWorkViewBroadcaster()
	if b.SubscriberCount() != 0 {
		t.Fatal("initial subscriber count should be 0")
	}

	id1, ch1 := b.Subscribe(8)
	if id1 == "" {
		t.Fatal("subscribe returned empty id")
	}
	if b.SubscriberCount() != 1 {
		t.Fatalf("subscriber count = %d, want 1", b.SubscriberCount())
	}

	id2, ch2 := b.Subscribe(4)
	if b.SubscriberCount() != 2 {
		t.Fatalf("subscriber count = %d, want 2", b.SubscriberCount())
	}

	b.Unsubscribe(id1)
	if b.SubscriberCount() != 1 {
		t.Fatalf("subscriber count after unsubscribe = %d, want 1", b.SubscriberCount())
	}
	if _, ok := <-ch1; ok {
		t.Fatal("unsubscribe must close the subscriber channel")
	}

	// Idempotent unsubscribe.
	b.Unsubscribe(id1)
	b.Unsubscribe("nonexistent")
	if b.SubscriberCount() != 1 {
		t.Fatalf("subscriber count after idempotent unsubscribe = %d, want 1", b.SubscriberCount())
	}

	b.Unsubscribe(id2)
	if b.SubscriberCount() != 0 {
		t.Fatalf("subscriber count after all removed = %d, want 0", b.SubscriberCount())
	}
	if _, ok := <-ch2; ok {
		t.Fatal("unsubscribe must close the second subscriber channel")
	}
}

func TestBroadcasterNilAndZeroValueSafe(t *testing.T) {
	var nilBroadcaster *WorkViewBroadcaster
	if id, ch := nilBroadcaster.Subscribe(1); id != "" {
		t.Fatalf("nil broadcaster subscription ID = %q, want empty", id)
	} else if _, ok := <-ch; ok {
		t.Fatal("nil broadcaster must return a closed channel")
	}
	nilBroadcaster.EmitWorkView(work.WorkViewEvent{})
	nilBroadcaster.Unsubscribe("missing")
	if nilBroadcaster.SubscriberCount() != 0 || nilBroadcaster.OverflowCount() != 0 {
		t.Fatal("nil broadcaster observability must use zero values")
	}

	var zero WorkViewBroadcaster
	id, ch := zero.Subscribe(1)
	zero.EmitWorkView(work.WorkViewEvent{WorkID: "w-zero"})
	if got := <-ch; got.WorkID != "w-zero" {
		t.Fatalf("zero-value broadcaster WorkID = %q", got.WorkID)
	}
	zero.Unsubscribe(id)
	if _, ok := <-ch; ok {
		t.Fatal("zero-value broadcaster cancel must close the channel")
	}
}

func TestBroadcasterEmitFansOut(t *testing.T) {
	b := NewWorkViewBroadcaster()
	id1, ch1 := b.Subscribe(16)
	id2, ch2 := b.Subscribe(16)
	defer b.Unsubscribe(id1)
	defer b.Unsubscribe(id2)

	ev := work.WorkViewEvent{WorkID: "w-1", EventID: "e-1"}
	b.EmitWorkView(ev)

	// Both subscribers should receive.
	select {
	case got := <-ch1:
		if got.WorkID != "w-1" {
			t.Fatalf("ch1: WorkID = %q, want %q", got.WorkID, "w-1")
		}
	case <-time.After(time.Second):
		t.Fatal("ch1: timeout waiting for event")
	}
	select {
	case got := <-ch2:
		if got.WorkID != "w-1" {
			t.Fatalf("ch2: WorkID = %q, want %q", got.WorkID, "w-1")
		}
	case <-time.After(time.Second):
		t.Fatal("ch2: timeout waiting for event")
	}
}

func TestBroadcasterSlowSubscriberDoesNotBlock(t *testing.T) {
	b := NewWorkViewBroadcaster()
	// Subscriber with tiny buffer that we don't drain.
	idSlow, chSlow := b.Subscribe(1)
	defer b.Unsubscribe(idSlow)

	// Fill the buffer.
	b.EmitWorkView(work.WorkViewEvent{WorkID: "w-1", EventID: "e-1"})
	// This second emit should overflow the slow subscriber.
	b.EmitWorkView(work.WorkViewEvent{WorkID: "w-2", EventID: "e-2"})

	if b.OverflowCount() < 1 {
		t.Fatalf("OverflowCount = %d, want >= 1", b.OverflowCount())
	}
	if b.SubscriberDrops(idSlow) < 1 {
		t.Fatalf("SubscriberDrops = %d, want >= 1", b.SubscriberDrops(idSlow))
	}

	// A normal subscriber should still receive both events without blocking.
	idFast, chFast := b.Subscribe(16)
	defer b.Unsubscribe(idFast)

	b.EmitWorkView(work.WorkViewEvent{WorkID: "w-3", EventID: "e-3"})
	select {
	case got := <-chFast:
		if got.WorkID != "w-3" {
			t.Fatalf("fast subscriber got WorkID = %q, want %q", got.WorkID, "w-3")
		}
	case <-time.After(time.Second):
		t.Fatal("fast subscriber timeout")
	}

	_ = chSlow
}

func TestBroadcasterConcurrencySafe(t *testing.T) {
	b := NewWorkViewBroadcaster()
	var wg sync.WaitGroup
	n := 50
	stop := make(chan struct{})

	// Concurrent subscribers with drain goroutines.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id, ch := b.Subscribe(8)
			defer b.Unsubscribe(id)
			// Drain events until stop.
			go func() {
				for {
					select {
					case <-ch:
					case <-stop:
						return
					}
				}
			}()
			time.Sleep(time.Millisecond)
		}(i)
	}

	// Concurrent emitters.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			b.EmitWorkView(work.WorkViewEvent{WorkID: "w", EventID: "e"})
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent operations timed out")
	}
	close(stop)
}

func TestBroadcasterSubscriberDropsUnknown(t *testing.T) {
	b := NewWorkViewBroadcaster()
	if d := b.SubscriberDrops("nonexistent"); d != 0 {
		t.Fatalf("SubscriberDrops for unknown = %d, want 0", d)
	}
}

// --- Work events absent from Agent sink ---

func TestWorkEventsAbsentFromEventSink(t *testing.T) {
	// Verify that work.ViewSink and event.Sink are separate types and WorkViewEvent
	// cannot be emitted through event.Sink.
	var es event.Sink = event.Discard
	var ws work.ViewSink = NewWorkViewBroadcaster()

	// Compile-time check: these are different interfaces.
	_ = es
	_ = ws

	// Runtime check: event.Sink.Emit accepts event.Event, not work.WorkViewEvent.
	// This is a type-system guarantee — if someone tried to put a WorkViewEvent
	// through event.Sink it wouldn't compile.
}

// --- Controller nil receiver safety ---

func TestControllerNilWorkControl(t *testing.T) {
	var c *Controller
	wc := c.WorkControl()
	if wc == nil {
		t.Fatal("nil Controller WorkControl must return non-nil (nil-safe port)")
	}
	_, err := wc.CreateWork(context.Background(), work.CreateWorkInput{})
	if err == nil {
		t.Fatal("nil Controller CreateWork must return error")
	}
}

func TestControllerNilWorkViews(t *testing.T) {
	var c *Controller
	if v := c.WorkViews(); v != nil {
		t.Fatal("nil Controller WorkViews must return nil")
	}
}

func TestControllerLookupSessionAdapter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	session := agent.NewSession("system")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "inspect this Session"})
	if err := session.SaveSnapshot(path); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	c := New(Options{SessionDir: dir, SessionPath: path, ModelRef: "provider/model"})
	ref, ok, err := c.LookupSession(context.Background(), path)
	if err != nil {
		t.Fatalf("LookupSession: %v", err)
	}
	if !ok {
		t.Fatal("LookupSession did not find the persisted Session")
	}
	if ref.SessionPath != path || ref.BranchID != agent.BranchID(path) {
		t.Fatalf("Session identity = %+v", ref)
	}
	if ref.ModelRef != "provider/model" || ref.TurnCount != 1 || !strings.Contains(ref.Preview, "inspect this Session") {
		t.Fatalf("Session projection = %+v", ref)
	}
	if ref.StartedAt.IsZero() {
		t.Fatal("Session StartedAt must be observable")
	}

	missing, found, err := c.LookupSession(context.Background(), filepath.Join(dir, "missing.jsonl"))
	if err != nil || found || missing != (work.SessionRef{}) {
		t.Fatalf("missing Session = (%+v, %v, %v)", missing, found, err)
	}
}

func TestControllerLookupSessionRejectsOutsideDir(t *testing.T) {
	dir := t.TempDir()
	outsideDir := t.TempDir()
	existing := filepath.Join(outsideDir, "outside.jsonl")
	if err := agent.NewSession("system").SaveSnapshot(existing); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	c := New(Options{SessionDir: dir})
	for _, path := range []string{existing, filepath.Join(outsideDir, "missing.jsonl")} {
		ref, found, err := c.LookupSession(context.Background(), path)
		if err == nil || !strings.Contains(err.Error(), "outside SessionDir") {
			t.Fatalf("outside lookup %q error = %v", path, err)
		}
		if found || ref != (work.SessionRef{}) {
			t.Fatalf("outside lookup %q = (%+v, %v), want rejected zero result", path, ref, found)
		}
	}
}

func TestControllerLookupSessionMissingInsideDir(t *testing.T) {
	dir := t.TempDir()
	c := New(Options{SessionDir: dir})
	ref, found, err := c.LookupSession(context.Background(), filepath.Join(dir, "missing.jsonl"))
	if err != nil || found || ref != (work.SessionRef{}) {
		t.Fatalf("inside missing lookup = (%+v, %v, %v)", ref, found, err)
	}
}

func TestControllerLookupSessionCanceledContextWins(t *testing.T) {
	dir := t.TempDir()
	c := New(Options{SessionDir: dir})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ref, found, err := c.LookupSession(ctx, filepath.Join(t.TempDir(), "outside.jsonl"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup error = %v, want context.Canceled", err)
	}
	if found || ref != (work.SessionRef{}) {
		t.Fatalf("canceled lookup = (%+v, %v), want zero result", ref, found)
	}
}
