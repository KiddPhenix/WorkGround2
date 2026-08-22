package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"workground2/internal/fileutil"
)

const (
	desktopRoomPinsFile = "desktop-room-pins.json"
	desktopRoomPinLimit = 7
)

var desktopRoomPinsMu sync.Mutex

type desktopRoomPinState struct {
	TopicIDs []string          `json:"topicIds"`
	Icons    map[string]string `json:"icons,omitempty"`
}

func desktopRoomPinsPath() string {
	return filepath.Join(desktopConfigDir(), desktopRoomPinsFile)
}

func loadDesktopRoomPins() (desktopRoomPinState, error) {
	raw, err := readFileUTF8(desktopRoomPinsPath())
	if errors.Is(err, os.ErrNotExist) {
		return desktopRoomPinState{TopicIDs: []string{}, Icons: map[string]string{}}, nil
	}
	if err != nil {
		return desktopRoomPinState{}, fmt.Errorf("load desktop Room pins: %w", err)
	}
	var state desktopRoomPinState
	if err := json.Unmarshal(raw, &state); err != nil {
		return desktopRoomPinState{}, fmt.Errorf("load desktop Room pins: %w", err)
	}
	if err := validateDesktopRoomPins(state.TopicIDs); err != nil {
		return desktopRoomPinState{}, fmt.Errorf("load desktop Room pins: %w", err)
	}
	if err := validateDesktopRoomIcons(state.Icons); err != nil {
		return desktopRoomPinState{}, fmt.Errorf("load desktop Room pins: %w", err)
	}
	if state.TopicIDs == nil {
		state.TopicIDs = []string{}
	}
	if state.Icons == nil {
		state.Icons = map[string]string{}
	}
	return state, nil
}

func validateDesktopRoomPins(topicIDs []string) error {
	if len(topicIDs) > desktopRoomPinLimit {
		return fmt.Errorf("pin limit exceeded (%d)", desktopRoomPinLimit)
	}
	seen := make(map[string]struct{}, len(topicIDs))
	for _, topicID := range topicIDs {
		if topicID != strings.TrimSpace(topicID) || topicID == "" {
			return errors.New("Room pin topicID must be non-empty and trimmed")
		}
		if _, ok := seen[topicID]; ok {
			return fmt.Errorf("duplicate Room pin %q", topicID)
		}
		seen[topicID] = struct{}{}
	}
	return nil
}

func saveDesktopRoomPins(state desktopRoomPinState) error {
	if err := validateDesktopRoomPins(state.TopicIDs); err != nil {
		return err
	}
	if err := validateDesktopRoomIcons(state.Icons); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := fileutil.AtomicWriteFile(desktopRoomPinsPath(), raw, 0o644); err != nil {
		return fmt.Errorf("save desktop Room pins: %w", err)
	}
	return nil
}

// GetDesktopRoomPins returns the persisted desktop Room pin order. The returned
// slice is detached from storage state and is safe for callers to modify.
func (a *App) GetDesktopRoomPins() ([]string, error) {
	desktopRoomPinsMu.Lock()
	defer desktopRoomPinsMu.Unlock()
	state, err := loadDesktopRoomPins()
	if err != nil {
		return nil, err
	}
	pins := make([]string, len(state.TopicIDs))
	copy(pins, state.TopicIDs)
	return pins, nil
}

// GetDesktopRoomIcons returns the persisted per-topic Room icon preferences.
// Preferences are retained even while their project or topic is temporarily
// absent, so restoring the authoritative tree also restores its appearance.
func (a *App) GetDesktopRoomIcons() (map[string]string, error) {
	desktopRoomPinsMu.Lock()
	defer desktopRoomPinsMu.Unlock()
	state, err := loadDesktopRoomPins()
	if err != nil {
		return nil, err
	}
	return cloneDesktopRoomIcons(state.Icons), nil
}

// SetDesktopRoomPinned changes one desktop Room pin using an explicit target
// state. Repeating an already-applied target is a no-op. The next state is
// persisted atomically before the method returns, so a failed save leaves the
// prior file and the next retry sees the prior authoritative state.
func (a *App) SetDesktopRoomPinned(topicID string, pinned bool) error {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return errors.New("topicID is required")
	}
	desktopRoomPinsMu.Lock()
	defer desktopRoomPinsMu.Unlock()
	state, err := loadDesktopRoomPins()
	if err != nil {
		return err
	}
	_, err = applyDesktopRoomPin(state, topicID, pinned, saveDesktopRoomPins)
	return err
}

// SetDesktopRoomIcon changes one Room icon preference. Empty resets the Room
// to its default discussion glyph; unsupported values fail explicitly. The
// full pin/icon state is atomically saved, so failures are safe to retry.
func (a *App) SetDesktopRoomIcon(topicID, icon string) error {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return errors.New("topicID is required")
	}
	normalized, err := normalizeDesktopRoomIcon(icon)
	if err != nil {
		return err
	}
	desktopRoomPinsMu.Lock()
	defer desktopRoomPinsMu.Unlock()
	state, err := loadDesktopRoomPins()
	if err != nil {
		return err
	}
	_, err = applyDesktopRoomIcon(state, topicID, normalized, saveDesktopRoomPins)
	return err
}

func applyDesktopRoomPin(current desktopRoomPinState, topicID string, pinned bool, save func(desktopRoomPinState) error) (desktopRoomPinState, error) {
	nextPins, changed, err := mutateDesktopRoomPins(current.TopicIDs, topicID, pinned)
	if err != nil || !changed {
		return cloneDesktopRoomPinState(current), err
	}
	next := cloneDesktopRoomPinState(current)
	next.TopicIDs = nextPins
	if err := save(next); err != nil {
		return cloneDesktopRoomPinState(current), err
	}
	return next, nil
}

func applyDesktopRoomIcon(current desktopRoomPinState, topicID, icon string, save func(desktopRoomPinState) error) (desktopRoomPinState, error) {
	next := cloneDesktopRoomPinState(current)
	if icon == "" {
		if _, exists := next.Icons[topicID]; !exists {
			return next, nil
		}
		delete(next.Icons, topicID)
	} else {
		if next.Icons[topicID] == icon {
			return next, nil
		}
		next.Icons[topicID] = icon
	}
	if err := save(next); err != nil {
		return cloneDesktopRoomPinState(current), err
	}
	return next, nil
}

func normalizeDesktopRoomIcon(icon string) (string, error) {
	raw := strings.TrimSpace(strings.ToLower(icon))
	if raw == "" {
		return "", nil
	}
	normalized := normalizeProjectIcon(raw)
	if normalized == "" {
		return "", fmt.Errorf("unsupported Room icon %q", icon)
	}
	return normalized, nil
}

func validateDesktopRoomIcons(icons map[string]string) error {
	for topicID, icon := range icons {
		if topicID == "" || topicID != strings.TrimSpace(topicID) {
			return errors.New("Room icon topicID must be non-empty and trimmed")
		}
		normalized, err := normalizeDesktopRoomIcon(icon)
		if err != nil {
			return err
		}
		if normalized == "" || normalized != icon {
			return fmt.Errorf("Room icon for %q must be normalized", topicID)
		}
	}
	return nil
}

func cloneDesktopRoomPinState(state desktopRoomPinState) desktopRoomPinState {
	return desktopRoomPinState{TopicIDs: append([]string(nil), state.TopicIDs...), Icons: cloneDesktopRoomIcons(state.Icons)}
}

func cloneDesktopRoomIcons(icons map[string]string) map[string]string {
	cloned := make(map[string]string, len(icons))
	for topicID, icon := range icons {
		cloned[topicID] = icon
	}
	return cloned
}

func mutateDesktopRoomPins(current []string, topicID string, pinned bool) ([]string, bool, error) {
	if err := validateDesktopRoomPins(current); err != nil {
		return nil, false, err
	}
	index := -1
	for i, currentID := range current {
		if currentID == topicID {
			index = i
			break
		}
	}
	if pinned {
		if index >= 0 {
			return append([]string(nil), current...), false, nil
		}
		if len(current) >= desktopRoomPinLimit {
			return append([]string(nil), current...), false, fmt.Errorf("desktop Room pin limit reached (%d)", desktopRoomPinLimit)
		}
		next := make([]string, 0, len(current)+1)
		next = append(next, topicID)
		next = append(next, current...)
		return next, true, nil
	}
	if index < 0 {
		return append([]string(nil), current...), false, nil
	}
	next := make([]string, 0, len(current)-1)
	next = append(next, current[:index]...)
	next = append(next, current[index+1:]...)
	return next, true, nil
}
