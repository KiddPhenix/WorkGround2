//go:build windows

package main

import "testing"

func TestWidgetRegionPointsMatchShell(t *testing.T) {
	points, err := widgetRegionPoints(widgetMinWidth, widgetMinHeight, 96)
	if err != nil {
		t.Fatal(err)
	}
	want := [8]w32Point{
		{17, 0}, {508, 0}, {520, 12}, {520, 147},
		{503, 160}, {17, 160}, {0, 145}, {0, 17},
	}
	if points != want {
		t.Fatalf("points = %#v, want %#v", points, want)
	}
}

func TestWidgetRegionPointsScaleForWindowDPI(t *testing.T) {
	points, err := widgetRegionPoints(764, 225, 120)
	if err != nil {
		t.Fatal(err)
	}
	want := [8]w32Point{
		{21, 0}, {940, 0}, {955, 15}, {955, 265},
		{934, 281}, {21, 281}, {0, 263}, {0, 21},
	}
	if points != want {
		t.Fatalf("points = %#v, want %#v", points, want)
	}
}

func TestWidgetRegionPointsRejectSmallWindow(t *testing.T) {
	if _, err := widgetRegionPoints(widgetMinWidth-1, widgetMinHeight, 96); err == nil {
		t.Fatal("expected width validation error")
	}
	if _, err := widgetRegionPoints(widgetMinWidth, widgetMinHeight-1, 96); err == nil {
		t.Fatal("expected height validation error")
	}
}
