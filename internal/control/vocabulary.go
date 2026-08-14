package control

import (
	"fmt"
	"strings"

	"workground2/internal/event"
	"workground2/internal/vocabulary"
)

// CompleteVocabulary exposes the controller-owned workspace snapshot without
// making a frontend infer project or Skill/Agent scope.
func (c *Controller) CompleteVocabulary(prefix string, limit int) []vocabulary.Match {
	if c == nil || c.vocabulary == nil {
		return []vocabulary.Match{}
	}
	return c.vocabulary.Complete(prefix, limit)
}

// RecordVocabularyUse is idempotent for stale/unknown completion IDs. A write
// failure is returned so Desktop can expose it instead of silently losing rank.
func (c *Controller) RecordVocabularyUse(id, useID string) error {
	if c == nil || c.vocabulary == nil {
		return nil
	}
	return c.vocabulary.RecordUse(id, useID)
}

func (c *Controller) observeVocabulary(index int, role, text string) {
	if c == nil || c.vocabulary == nil || strings.TrimSpace(text) == "" {
		return
	}
	eventID := fmt.Sprintf("%s:%d:%s", c.SessionPath(), index, role)
	if err := c.vocabulary.Observe(eventID, text, role); err != nil {
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "vocabulary: " + err.Error()})
	}
}
