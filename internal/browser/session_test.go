package browser

import (
	"context"
	"errors"
	"testing"
	"time"
)

func testObservation(fingerprint string, backendID int64) Observation {
	return Observation{Fingerprint: fingerprint, Nodes: []ObservedNode{{
		Ref: NodeRef{BackendNodeID: backendID, TargetID: "tab", Bounds: Rect{Width: 10, Height: 10}},
		Tag: "button", Role: "button",
	}}}
}

func TestPublishRejectsObservationCapturedBeforeInvalidation(t *testing.T) {
	s := newSession("owner", Options{})
	startGeneration := s.Generation()
	s.bumpGeneration()
	if revision, accepted := s.publishSnapshot("session", testObservation("old", 11), startGeneration); accepted {
		t.Fatalf("stale observation published at revision %d", revision)
	}
	if snapshot := s.Snapshot(); snapshot != nil {
		t.Fatalf("stale index revived: %+v", snapshot.Nodes)
	}
	currentGeneration := s.Generation()
	if _, accepted := s.publishSnapshot("session", testObservation("new", 22), currentGeneration); !accepted {
		t.Fatal("current observation rejected")
	}
	if snapshot := s.Snapshot(); snapshot == nil || snapshot.Nodes[1].BackendNodeID != 22 {
		t.Fatalf("current node map missing: %+v", snapshot)
	}
}

func TestWaitForQuietResetsOnInvalidation(t *testing.T) {
	s := newSession("owner", Options{})
	s.bumpGeneration()
	window := 40 * time.Millisecond
	started := time.Now()
	go func() {
		time.Sleep(25 * time.Millisecond)
		s.bumpGeneration()
	}()
	if err := s.waitForQuiet(context.Background(), window); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 60*time.Millisecond {
		t.Fatalf("quiet window did not reset, elapsed=%s", elapsed)
	}
}

func TestWaitForQuietReturnsImmediatelyWithoutInvalidationAndHonorsCancel(t *testing.T) {
	s := newSession("owner", Options{})
	started := time.Now()
	if err := s.waitForQuiet(context.Background(), time.Second); err != nil || time.Since(started) > 20*time.Millisecond {
		t.Fatalf("quiet page waited unnecessarily: elapsed=%s err=%v", time.Since(started), err)
	}
	s.bumpGeneration()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.waitForQuiet(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}
