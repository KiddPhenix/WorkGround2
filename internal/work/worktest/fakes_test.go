package worktest

import (
	"errors"
	"testing"

	"workground2/internal/work"
)

func TestSinkReturnsCopy(t *testing.T) {
	sink := &Sink{}
	sink.EmitWorkView(work.WorkViewEvent{EventID: "event-1"})
	events := sink.Events()
	events[0].EventID = "mutated"
	if sink.Events()[0].EventID != "event-1" {
		t.Fatal("Events exposed mutable recorder state")
	}
}

func TestMissingBehaviorIsExplicit(t *testing.T) {
	store := &Store{}
	if _, err := store.LoadProjection("work-1"); !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("LoadProjection error = %v", err)
	}
	if _, err := store.CommitEvents("work-1", nil); !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("CommitEvents error = %v", err)
	}
}

func TestCommitEventsDelegatesWholeBatch(t *testing.T) {
	events := []work.WorkEvent{{ID: "e1"}, {ID: "e2"}}
	store := &Store{CommitEventsFunc: func(workID string, got []work.WorkEvent) ([]int64, error) {
		if workID != "work-1" || len(got) != 2 || got[0].ID != "e1" || got[1].ID != "e2" {
			t.Fatalf("CommitEvents args = (%q, %+v)", workID, got)
		}
		return []int64{4, 5}, nil
	}}
	revisions, err := store.CommitEvents("work-1", events)
	if err != nil || len(revisions) != 2 || revisions[0] != 4 || revisions[1] != 5 {
		t.Fatalf("CommitEvents = (%v, %v)", revisions, err)
	}
}
