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

// ActivateSkillVocabulary adds one live Skill's glossary to this Controller.
// It intentionally leaves the stable system prompt unchanged.
func (c *Controller) ActivateSkillVocabulary(name string) (vocabulary.RefreshResult, error) {
	if c == nil || c.vocabulary == nil {
		return vocabulary.RefreshResult{Warnings: []string{}}, nil
	}
	name = strings.TrimSpace(strings.TrimPrefix(name, "/"))
	canonical := ""
	for _, candidate := range c.Skills() {
		if strings.EqualFold(candidate.Name, name) {
			canonical = candidate.Name
			break
		}
	}
	if canonical == "" {
		return vocabulary.RefreshResult{Warnings: []string{}}, fmt.Errorf("unknown or disabled skill: %s", name)
	}
	sk, ok := c.skills.resolve(canonical)
	if !ok || sk.Protected {
		return vocabulary.RefreshResult{Warnings: []string{}}, fmt.Errorf("skill vocabulary is unavailable: %s", canonical)
	}
	return c.vocabulary.ActivateSkill(vocabulary.SkillSource{Name: sk.Name, Path: sk.Path, Terms: sk.Vocabulary}), nil
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
