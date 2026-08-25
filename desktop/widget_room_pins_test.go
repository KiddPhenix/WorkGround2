package main

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestDesktopRoomPinsPersistAndEnforceGlobalLimit(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()

	pins, err := app.GetDesktopRoomPins()
	if err != nil {
		t.Fatalf("GetDesktopRoomPins empty: %v", err)
	}
	if len(pins) != 0 {
		t.Fatalf("initial pins = %v, want empty", pins)
	}

	for i := 0; i < desktopRoomPinLimit; i++ {
		if err := app.SetDesktopRoomPinned(fmt.Sprintf("room-%d", i), true); err != nil {
			t.Fatalf("pin room %d: %v", i, err)
		}
	}
	if err := app.SetDesktopRoomPinned("room-0", true); err != nil {
		t.Fatalf("repeat pin must be idempotent: %v", err)
	}
	if err := app.SetDesktopRoomPinned("room-overflow", true); err == nil || !strings.Contains(err.Error(), "pin limit") {
		t.Fatalf("eighth pin error = %v, want explicit pin limit", err)
	}

	want := []string{"room-6", "room-5", "room-4", "room-3", "room-2", "room-1", "room-0"}
	pins, err = NewApp().GetDesktopRoomPins()
	if err != nil {
		t.Fatalf("GetDesktopRoomPins reloaded: %v", err)
	}
	if !reflect.DeepEqual(pins, want) {
		t.Fatalf("reloaded pins = %v, want %v", pins, want)
	}

	if err := app.SetDesktopRoomPinned("room-3", false); err != nil {
		t.Fatalf("unpin while full: %v", err)
	}
	if err := app.SetDesktopRoomPinned("room-3", false); err != nil {
		t.Fatalf("repeat unpin must be idempotent: %v", err)
	}
	if err := app.SetDesktopRoomPinned("room-overflow", true); err != nil {
		t.Fatalf("pin after freeing slot: %v", err)
	}
}

func TestMutateDesktopRoomPinsLeavesPriorStateOnFailure(t *testing.T) {
	current := []string{"room-a", "room-b"}
	next, changed, err := mutateDesktopRoomPins(current, "room-a", true)
	if err != nil || changed || !reflect.DeepEqual(next, current) {
		t.Fatalf("repeat pin = (%v, %v, %v), want detached no-op", next, changed, err)
	}
	next[0] = "changed"
	if current[0] != "room-a" {
		t.Fatalf("no-op result aliases authoritative state: %v", current)
	}

	full := make([]string, desktopRoomPinLimit)
	for i := range full {
		full[i] = fmt.Sprintf("room-%d", i)
	}
	next, changed, err = mutateDesktopRoomPins(full, "overflow", true)
	if err == nil || changed || !reflect.DeepEqual(next, full) {
		t.Fatalf("overflow mutation = (%v, %v, %v), want unchanged explicit failure", next, changed, err)
	}

	saveErr := fmt.Errorf("disk busy")
	currentState := desktopRoomPinState{TopicIDs: current, Icons: map[string]string{"room-stale": "discussion"}}
	nextState, err := applyDesktopRoomPin(currentState, "room-c", true, func(desktopRoomPinState) error { return saveErr })
	if err != saveErr || !reflect.DeepEqual(nextState, currentState) {
		t.Fatalf("failed save = (%v, %v), want prior state and original error", nextState, err)
	}
}

func TestDesktopRoomIconsMigrateAndSurvivePinMutations(t *testing.T) {
	isolateDesktopUserDirs(t)
	if err := os.MkdirAll(desktopConfigDir(), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(desktopRoomPinsPath(), []byte(`{"topicIds":["room-old"]}`), 0o644); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}
	app := NewApp()
	icons, err := app.GetDesktopRoomIcons()
	if err != nil || len(icons) != 0 {
		t.Fatalf("legacy icons = (%v, %v), want empty compatible map", icons, err)
	}
	if err := app.SetDesktopRoomIcon("room-stale", " Discussion "); err != nil {
		t.Fatalf("set stale topic icon: %v", err)
	}
	if err := app.SetDesktopRoomPinned("room-new", true); err != nil {
		t.Fatalf("pin with existing icon preference: %v", err)
	}
	icons, err = NewApp().GetDesktopRoomIcons()
	if err != nil || icons["room-stale"] != "discussion" {
		t.Fatalf("icons after pin = (%v, %v), want stale preference retained", icons, err)
	}
	if err := app.SetDesktopRoomIcon("room-stale", ""); err != nil {
		t.Fatalf("reset Room icon: %v", err)
	}
	icons, err = app.GetDesktopRoomIcons()
	if err != nil || len(icons) != 0 {
		t.Fatalf("icons after reset = (%v, %v), want default/empty", icons, err)
	}
}

func TestDesktopRoomIconRejectsUnknownAndRollsBackFailedSave(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	if err := app.SetDesktopRoomIcon("room-a", "unknown-glyph"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unknown icon error = %v, want explicit unsupported error", err)
	}
	current := desktopRoomPinState{TopicIDs: []string{"room-a"}, Icons: map[string]string{"room-a": "discussion"}}
	saveErr := fmt.Errorf("disk busy")
	next, err := applyDesktopRoomIcon(current, "room-a", "python", func(desktopRoomPinState) error { return saveErr })
	if err != saveErr || !reflect.DeepEqual(next, current) {
		t.Fatalf("failed icon save = (%v, %v), want prior full state", next, err)
	}
	next.Icons["room-a"] = "changed"
	if current.Icons["room-a"] != "discussion" {
		t.Fatalf("rollback state aliases authoritative icons: %v", current.Icons)
	}
}

func TestDesktopRoomPinsRejectCorruptStateWithoutOverwriting(t *testing.T) {
	isolateDesktopUserDirs(t)
	path := desktopRoomPinsPath()
	if err := os.MkdirAll(desktopConfigDir(), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	raw := []byte(`{"topicIds":["duplicate","duplicate"]}`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write corrupt pins: %v", err)
	}

	if _, err := NewApp().GetDesktopRoomPins(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("GetDesktopRoomPins error = %v, want duplicate exposed", err)
	}
	if err := NewApp().SetDesktopRoomPinned("new-room", true); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("SetDesktopRoomPinned error = %v, want corrupt state exposed", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read corrupt pins after failed write: %v", err)
	}
	if !reflect.DeepEqual(got, raw) {
		t.Fatalf("failed mutation overwrote prior state: %s", got)
	}
}
