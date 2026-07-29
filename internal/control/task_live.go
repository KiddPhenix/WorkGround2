package control

import (
	"log/slog"
	"strings"
	"time"

	"workground2/internal/event"
	"workground2/internal/work"
)

const (
	taskLiveInterval = 500 * time.Millisecond
	taskLiveRunes    = 360
)

// NewTaskLiveSink converts the Controller event stream into bounded,
// throttled Work Task previews. It keeps visible answer text authoritative and
// uses reasoning only until the answer starts.
func NewTaskLiveSink(report work.TaskLiveReporter) event.Sink {
	if report == nil {
		return event.Discard
	}
	return newTaskLiveSink(report, time.Now, taskLiveInterval)
}

type taskLiveSink struct {
	report    work.TaskLiveReporter
	now       func() time.Time
	interval  time.Duration
	reasoning string
	answer    string
	last      string
	lastAt    time.Time
}

func newTaskLiveSink(report work.TaskLiveReporter, now func() time.Time, interval time.Duration) *taskLiveSink {
	return &taskLiveSink{report: report, now: now, interval: interval}
}

func (s *taskLiveSink) Emit(value event.Event) {
	if s == nil || s.report == nil {
		return
	}
	switch value.Kind {
	case event.TurnStarted:
		s.reasoning = ""
		s.answer = ""
		s.last = ""
		s.lastAt = time.Time{}
	case event.Reasoning:
		s.reasoning += value.Text
		if s.answer == "" {
			s.publish(false)
		}
	case event.Text:
		s.answer += value.Text
		s.publish(false)
	case event.Message:
		if strings.TrimSpace(value.Text) != "" {
			s.answer = value.Text
		}
		s.publish(true)
	}
}

func (s *taskLiveSink) publish(force bool) {
	output := taskLivePreview(s.answer)
	if output == "" {
		output = taskLivePreview(s.reasoning)
	}
	if output == "" || output == s.last {
		return
	}
	now := s.now()
	if !force && !s.lastAt.IsZero() && now.Sub(s.lastAt) < s.interval {
		return
	}
	if err := s.report(work.TaskLiveUpdate{Output: output}); err != nil {
		slog.Warn("work: publish task live output failed", "error", err)
		return
	}
	s.last = output
	s.lastAt = now
}

func taskLivePreview(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= taskLiveRunes {
		return value
	}
	return "…" + string(runes[len(runes)-taskLiveRunes+1:])
}
