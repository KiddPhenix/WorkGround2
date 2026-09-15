package main

import (
	"testing"
	"workground2/internal/agent"
)

func TestHistoryStopCapabilityOnlyAttachesToLiveTail(t *testing.T) {
	progress := agent.ProgressSnapshot{ActiveTools: []agent.ActiveToolInfo{{CallID: "same", StopID: "live"}}}
	messages := []HistoryMessage{
		{Role: "user", Content: "old"},
		{Role: "assistant", ToolCalls: []HistoryToolCall{{ID: "same", Name: "wait"}}},
		{Role: "tool", ToolCallID: "same", Content: "old result"},
		{Role: "user", Content: "new"},
		{Role: "assistant", ToolCalls: []HistoryToolCall{{ID: "same", Name: "wait"}}},
	}
	attachActiveTools(messages, progress)
	if messages[1].ToolCalls[0].StopID != "" || messages[4].ToolCalls[0].StopID != "live" {
		t.Fatalf("bad restoration: %+v", messages)
	}
	messages[4].ToolCalls[0].StopID = ""
	messages = append(messages, HistoryMessage{Role: "tool", ToolCallID: "same", Content: "just finished"})
	attachActiveTools(messages, progress)
	if messages[4].ToolCalls[0].StopID != "" {
		t.Fatal("stale progress decorated completed history")
	}
	messages = append(messages, HistoryMessage{Role: "user", Content: "next"})
	attachActiveTools(messages, progress)
	if messages[1].ToolCalls[0].StopID != "" {
		t.Fatal("capability leaked into older turn")
	}
}
