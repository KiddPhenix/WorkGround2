package main

import (
	"reflect"
	"testing"
	"time"
)

func TestRetryWidgetConversationRetriesFiveTimesWithSameInput(t *testing.T) {
	input := WidgetConversationInput{Prompt: "fix it", RequestID: "req-1", Workspace: "global"}
	var calls []WidgetConversationInput
	var delays []time.Duration

	result := retryWidgetConversation(input, func(got WidgetConversationInput) WidgetConversationResult {
		calls = append(calls, got)
		return WidgetConversationResult{Status: "retryable_error", Error: "timeout"}
	}, func(delay time.Duration) {
		delays = append(delays, delay)
	})

	if result.Status != "retryable_error" || result.Error != "timeout" {
		t.Fatalf("result = %+v, want final retryable error", result)
	}
	if len(calls) != 6 {
		t.Fatalf("calls = %d, want 6 (initial + 5 retries)", len(calls))
	}
	for i, got := range calls {
		if got != input {
			t.Fatalf("call %d input = %+v, want %+v", i, got, input)
		}
	}
	wantDelays := []time.Duration{
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1600 * time.Millisecond,
		3200 * time.Millisecond,
	}
	if !reflect.DeepEqual(delays, wantDelays) {
		t.Fatalf("delays = %v, want %v", delays, wantDelays)
	}
}

func TestRetryWidgetConversationStopsAfterSuccess(t *testing.T) {
	calls := 0
	result := retryWidgetConversation(WidgetConversationInput{RequestID: "req-2"}, func(WidgetConversationInput) WidgetConversationResult {
		calls++
		if calls < 3 {
			return WidgetConversationResult{Status: "retryable_error"}
		}
		return WidgetConversationResult{Status: "accepted"}
	}, func(time.Duration) {})

	if result.Status != "accepted" || calls != 3 {
		t.Fatalf("result = %+v, calls = %d; want accepted after 3 calls", result, calls)
	}
}

func TestRetryWidgetConversationDoesNotRetryTerminalResult(t *testing.T) {
	for _, status := range []string{"accepted", "already_applied", "invalid", "unknown"} {
		t.Run(status, func(t *testing.T) {
			calls := 0
			result := retryWidgetConversation(WidgetConversationInput{}, func(WidgetConversationInput) WidgetConversationResult {
				calls++
				return WidgetConversationResult{Status: status}
			}, func(time.Duration) { t.Fatal("terminal result must not sleep") })
			if result.Status != status || calls != 1 {
				t.Fatalf("result = %+v, calls = %d; want one terminal call", result, calls)
			}
		})
	}
}
