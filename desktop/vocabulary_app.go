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
