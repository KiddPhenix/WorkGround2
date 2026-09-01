package agent

import (
	"context"
	"strings"
	"testing"

	"workground2/internal/agent/testutil"
	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

func TestSteerText(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		wantOK  bool
	}{
		{
			name:    "happy path: prefix + newline + text",
			content: MidTurnSteerPrefix + "\nplease use smaller diffs",
			want:    "please use smaller diffs",
			wantOK:  true,
		},
		{
			name:    "prefix only, no user text",
			content: MidTurnSteerPrefix,
			want:    "",
			wantOK:  true,
		},
		{
			name:    "prefix with trailing whitespace only",
			content: MidTurnSteerPrefix + "\n  ",
			want:    "  ",
			wantOK:  true,
		},
		{
			name:    "round-trip through midTurnSteerMessage",
			content: midTurnSteerMessage("stop using such large diffs"),
			want:    "stop using such large diffs",
			wantOK:  true,
		},
		{
			name:    "user text with leading/trailing spaces preserved (matches live event)",
			content: MidTurnSteerPrefix + "\n   keep going but use read_file first   ",
			want:    "   keep going but use read_file first   ",
			wantOK:  true,
		},
		{
			name:    "regular user message, not steer",
			content: "please use smaller diffs",
			want:    "",
			wantOK:  false,
		},
		{
			name:    "empty string",
			content: "",
			want:    "",
			wantOK:  false,
		},
		{
			name:    "whitespace only",
			content: "   ",
			want:    "",
			wantOK:  false,
		},
		{
			name:    "prefix-like but truncated (no closing bracket)",
			content: "[Mid-turn steer queued by the user. Do not treat this as a new task\nplease go on",
			want:    "",
			wantOK:  false,
		},
		{
			name:    "prefix appears mid-message, not at start",
			content: "hey model " + MidTurnSteerPrefix + "\nuse smaller diffs",
			want:    "",
			wantOK:  false,
		},
		{
			name:    "multiline steer text preserved",
			content: MidTurnSteerPrefix + "\nline one\nline two",
			want:    "line one\nline two",
			wantOK:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SteerText(tt.content)
			if ok != tt.wantOK {
				t.Errorf("SteerText() ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("SteerText() text = %q, want %q", got, tt.want)
			}
			// Sanity: when ok is true the result must never contain the prefix.
			if ok && strings.Contains(got, MidTurnSteerPrefix) {
				t.Errorf("SteerText() returned text still contains the prefix: %q", got)
			}
		})
	}
}

func TestSteerEmitsVisibleConfirmationImmediately(t *testing.T) {
	var events []event.Event
	sink := event.FuncSink(func(e event.Event) { events = append(events, e) })
	a := New(testutil.NewMock("m", testutil.Turn{Text: "done"}), tool.NewRegistry(), NewSession("sys"), Options{}, sink)

	a.Steer("please use smaller diffs")

	steers := collectSteers(events)
	if len(steers) != 1 {
		t.Fatalf("Steer() emitted %d Steer events, want 1", len(steers))
	}
	if steers[0].Text != "please use smaller diffs" {
		t.Errorf("Steer() text = %q, want %q", steers[0].Text, "please use smaller diffs")
	}
}

func TestSteerEmitsOncePerEnqueue(t *testing.T) {
	var events []event.Event
	sink := event.FuncSink(func(e event.Event) { events = append(events, e) })
	a := New(testutil.NewMock("m", testutil.Turn{Text: "done"}), tool.NewRegistry(), NewSession("sys"), Options{}, sink)

	a.Steer("one")
	a.Steer("two")
	a.Steer("three")

	steers := collectSteers(events)
	if len(steers) != 3 {
		t.Fatalf("Steer() emitted %d Steer events, want 3", len(steers))
	}
	want := []string{"one", "two", "three"}
	for i, w := range want {
		if steers[i].Text != w {
			t.Errorf("Steer() event %d text = %q, want %q", i, steers[i].Text, w)
		}
	}
}

func TestRunDoesNotReemitSteer(t *testing.T) {
	var events []event.Event
	sink := event.FuncSink(func(e event.Event) { events = append(events, e) })
	a := New(testutil.NewMock("m", testutil.Turn{Text: "done"}), tool.NewRegistry(), NewSession("sys"), Options{}, sink)

	a.Steer("guidance")
	if err := a.Run(context.Background(), "work"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	steers := collectSteers(events)
	if len(steers) != 1 {
		t.Fatalf("got %d Steer events after Steer+Run, want exactly the 1 enqueue confirmation", len(steers))
	}
	if !sessionHasSteer(a.session.Snapshot(), "guidance") {
		t.Fatal("steer was not persisted to the session on consume")
	}
}

func TestSteerInjectedInOrderOnePerStep(t *testing.T) {
	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "r1", Name: "read_file", Arguments: `{"path":"a.go"}`}}},
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "r2", Name: "read_file", Arguments: `{"path":"b.go"}`}}},
		testutil.Turn{Text: "done"},
	)
	reg := tool.NewRegistry()
	reg.Add(anchoredFakeTool{name: "read_file"})
	var events []event.Event
	sink := event.FuncSink(func(e event.Event) { events = append(events, e) })
	a := New(mp, reg, NewSession("sys"), Options{}, sink)

	a.Steer("first")
	a.Steer("second")
	if err := a.Run(context.Background(), "work"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if steers := collectSteers(events); len(steers) != 2 {
		t.Fatalf("got %d Steer events, want 2 (one per enqueue)", len(steers))
	}

	got := sessionSteerTexts(a.session.Snapshot())
	want := []string{"first", "second"}
	if len(got) != len(want) {
		t.Fatalf("session steer texts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("session steer %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func collectSteers(events []event.Event) []event.Event {
	var out []event.Event
	for _, e := range events {
		if e.Kind == event.Steer {
			out = append(out, e)
		}
	}
	return out
}

func sessionHasSteer(msgs []provider.Message, text string) bool {
	for _, m := range msgs {
		if t, ok := SteerText(m.Content); ok && t == text {
			return true
		}
	}
	return false
}

func sessionSteerTexts(msgs []provider.Message) []string {
	var out []string
	for _, m := range msgs {
		if t, ok := SteerText(m.Content); ok {
			out = append(out, t)
		}
	}
	return out
}

func TestMidTurnSteerMessageRoundTrip(t *testing.T) {
	inputs := []string{
		"stop",
		"use read_file instead of cat",
		"",
		"  keep going  ",
	}
	for _, in := range inputs {
		msg := midTurnSteerMessage(in)
		got, ok := SteerText(msg)
		if !ok {
			t.Errorf("SteerText(midTurnSteerMessage(%q)): not recognized as steer", in)
			continue
		}
		if got != in {
			t.Errorf("SteerText(midTurnSteerMessage(%q)) = %q, want %q", in, got, in)
		}
	}
}
