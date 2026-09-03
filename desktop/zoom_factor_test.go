package main

import (
	"errors"
	"math"
	"testing"
)

func TestSetDesktopZoomFactorAppliesAndPersists(t *testing.T) {
	isolateDesktopUserDirs(t)
	var applied []float64
	app := &App{desktopZoomApply: func(factor float64) error {
		applied = append(applied, factor)
		return nil
	}}

	if err := app.SetDesktopZoomFactor(1.25); err != nil {
		t.Fatalf("SetDesktopZoomFactor: %v", err)
	}
	if len(applied) != 1 || applied[0] != 1.25 {
		t.Fatalf("applied = %v, want [1.25]", applied)
	}
	if got := app.GetDesktopZoomFactor(); got != 1.25 {
		t.Fatalf("GetDesktopZoomFactor = %v, want 1.25", got)
	}
}

func TestSetDesktopZoomFactorApplyFailureDoesNotPersist(t *testing.T) {
	isolateDesktopUserDirs(t)
	if err := saveZoomFactor(0.9); err != nil {
		t.Fatalf("seed zoom: %v", err)
	}
	applyErr := errors.New("controller unavailable")
	app := &App{desktopZoomApply: func(float64) error { return applyErr }}

	err := app.SetDesktopZoomFactor(1.4)
	if !errors.Is(err, applyErr) {
		t.Fatalf("SetDesktopZoomFactor error = %v", err)
	}
	if got := app.GetDesktopZoomFactor(); got != 0.9 {
		t.Fatalf("persisted zoom = %v, want previous 0.9", got)
	}
}

func TestSetDesktopZoomFactorPersistenceFailureRollsBack(t *testing.T) {
	isolateDesktopUserDirs(t)
	if err := saveZoomFactor(0.8); err != nil {
		t.Fatalf("seed zoom: %v", err)
	}
	var applied []float64
	app := &App{
		desktopZoomApply: func(factor float64) error {
			applied = append(applied, factor)
			return nil
		},
		desktopZoomSave: func(float64) error { return errors.New("disk full") },
	}

	err := app.SetDesktopZoomFactor(1.5)
	if err == nil || err.Error() != "persist desktop zoom: disk full" {
		t.Fatalf("SetDesktopZoomFactor error = %v", err)
	}
	if len(applied) != 2 || applied[0] != 1.5 || applied[1] != 0.8 {
		t.Fatalf("applied = %v, want live change followed by rollback", applied)
	}
	if got := app.GetDesktopZoomFactor(); got != 0.8 {
		t.Fatalf("persisted zoom = %v, want previous 0.8", got)
	}
}

func TestSetDesktopZoomFactorRejectsNonFiniteAndClampsRange(t *testing.T) {
	for _, invalid := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		app := &App{desktopZoomApply: func(float64) error {
			t.Fatal("invalid zoom reached live WebView")
			return nil
		}}
		if err := app.SetDesktopZoomFactor(invalid); err == nil {
			t.Fatalf("SetDesktopZoomFactor(%v) succeeded", invalid)
		}
	}

	var applied []float64
	app := &App{
		desktopZoomApply: func(factor float64) error {
			applied = append(applied, factor)
			return nil
		},
		desktopZoomSave: func(float64) error { return nil },
	}
	if err := app.SetDesktopZoomFactor(0.1); err != nil {
		t.Fatal(err)
	}
	if err := app.SetDesktopZoomFactor(3); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 || applied[0] != 0.5 || applied[1] != 2 {
		t.Fatalf("applied clamps = %v, want [0.5 2]", applied)
	}
}
