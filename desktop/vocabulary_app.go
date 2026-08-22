package main

import (
	"fmt"
	"strings"

	"workground2/internal/vocabulary"
)

type vocabularyCompleter interface {
	CompleteVocabulary(prefix string, limit int) []vocabulary.Match
}

type vocabularyUseRecorder interface {
	RecordVocabularyUse(id, useID string) error
}

type vocabularySkillActivator interface {
	ActivateSkillVocabulary(name string) (vocabulary.RefreshResult, error)
}

// CompleteVocabulary returns completion candidates from the active tab's
// controller snapshot — the same data source the main Composer's vocabulary
// menu uses. The desktop widget has no tab of its own until a conversation is
// started, so it completes against the app's current session. An empty result
// (no active controller) simply closes the widget's suggestion list.
func (a *App) CompleteVocabulary(prefix string, limit int) ([]vocabulary.Match, error) {
	ctrl := a.activeCtrl()
	if ctrl == nil {
		return []vocabulary.Match{}, nil
	}
	completer, ok := ctrl.(vocabularyCompleter)
	if !ok {
		return []vocabulary.Match{}, nil
	}
	return completer.CompleteVocabulary(strings.TrimSpace(prefix), limit), nil
}

// RecordVocabularyUse records an accepted suggestion against the active tab.
// Unknown entries are treated as already satisfied so delayed/retried UI calls
// remain safe.
func (a *App) RecordVocabularyUse(id, useID string) error {
	ctrl := a.activeCtrl()
	if ctrl == nil {
		return nil
	}
	recorder, ok := ctrl.(vocabularyUseRecorder)
	if !ok {
		return nil
	}
	return recorder.RecordVocabularyUse(strings.TrimSpace(id), strings.TrimSpace(useID))
}

// CompleteVocabularyForTab returns completion candidates from the target tab's
// controller snapshot. The tab ID is mandatory so a delayed response from a
// previous workspace can never leak into the active composer.
func (a *App) CompleteVocabularyForTab(tabID, prefix string, limit int) ([]vocabulary.Match, error) {
	tabID = strings.TrimSpace(tabID)
	if tabID == "" {
		return []vocabulary.Match{}, fmt.Errorf("vocabulary: tab id is required")
	}
	_, ctrl := a.tabAndCtrlByID(tabID)
	if ctrl == nil {
		return []vocabulary.Match{}, nil
	}
	completer, ok := ctrl.(vocabularyCompleter)
	if !ok {
		return []vocabulary.Match{}, nil
	}
	return completer.CompleteVocabulary(prefix, limit), nil
}

// RecordVocabularyUseForTab records an accepted suggestion. Unknown entries are
// treated as already satisfied so delayed/retried UI calls remain safe.
func (a *App) RecordVocabularyUseForTab(tabID, id, useID string) error {
	tabID = strings.TrimSpace(tabID)
	if tabID == "" {
		return fmt.Errorf("vocabulary: tab id is required")
	}
	_, ctrl := a.tabAndCtrlByID(tabID)
	if ctrl == nil {
		return nil
	}
	recorder, ok := ctrl.(vocabularyUseRecorder)
	if !ok {
		return nil
	}
	return recorder.RecordVocabularyUse(strings.TrimSpace(id), strings.TrimSpace(useID))
}

// ActivateSkillVocabularyForTab hot-loads a selected Skill's vocabulary into
// the target Session before the slash command is submitted.
func (a *App) ActivateSkillVocabularyForTab(tabID, name string) (vocabulary.RefreshResult, error) {
	tabID = strings.TrimSpace(tabID)
	if tabID == "" {
		return vocabulary.RefreshResult{Warnings: []string{}}, fmt.Errorf("vocabulary: tab id is required")
	}
	_, ctrl := a.tabAndCtrlByID(tabID)
	if ctrl == nil {
		return vocabulary.RefreshResult{Warnings: []string{}}, nil
	}
	activator, ok := ctrl.(vocabularySkillActivator)
	if !ok {
		return vocabulary.RefreshResult{Warnings: []string{}}, nil
	}
	return activator.ActivateSkillVocabulary(strings.TrimSpace(name))
}
