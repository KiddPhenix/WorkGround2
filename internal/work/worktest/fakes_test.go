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
}
