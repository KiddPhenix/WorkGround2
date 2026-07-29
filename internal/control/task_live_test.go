package control

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"workground2/internal/event"
	"workground2/internal/work"
)

func TestTaskLiveSinkThrottlesAndPrefersAnswer(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	var updates []work.TaskLiveUpdate
	sink := newTaskLiveSink(func(update work.TaskLiveUpdate) error {
		updates = append(updates, update)
		return nil
	}, func() time.Time { return now }, taskLiveInterval)

	sink.Emit(event.Event{Kind: event.TurnStarted})
	sink.Emit(event.Event{Kind: event.Reasoning, Text: "正在分析"})
	now = now.Add(50 * time.Millisecond)
	sink.Emit(event.Event{Kind: event.Reasoning, Text: " 需求"})
	now = now.Add(taskLiveInterval)
	sink.Emit(event.Event{Kind: event.Reasoning, Text: " 并检查边界"})
	now = now.Add(taskLiveInterval)
	sink.Emit(event.Event{Kind: event.Text, Text: "开始实现"})
	sink.Emit(event.Event{Kind: event.Message, Text: "实现完成"})

	got := make([]string, 0, len(updates))
	for _, update := range updates {
		got = append(got, update.Output)
	}
	want := []string{"正在分析", "正在分析 需求 并检查边界", "开始实现", "实现完成"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updates = %#v, want %#v", got, want)
	}
}

func TestTaskLivePreviewKeepsBoundedTail(t *testing.T) {
	got := taskLivePreview(strings.Repeat("界", taskLiveRunes+20))
	if runes := []rune(got); len(runes) != taskLiveRunes || runes[0] != '…' {
		t.Fatalf("bounded preview = %q (%d runes)", got, len(runes))
	}
}
