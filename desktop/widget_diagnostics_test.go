package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testDiagnosticsEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WorkGround2_STATE_HOME", home)
	return home
}

func readDiagnosticsLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read diagnostics log: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func TestDesktopIconDiagnosticsPathUnderUserStateDir(t *testing.T) {
	home := testDiagnosticsEnv(t)
	app := &App{}
	got := app.DesktopIconDiagnosticsPath()
	want := filepath.Join(home, desktopIconDiagnosticsFile)
	if got != want {
		t.Fatalf("diagnostics path = %q, want %q", got, want)
	}
}

func TestWriteDesktopIconDiagnosticsAppendsNDJSON(t *testing.T) {
	testDiagnosticsEnv(t)
	app := &App{}
	start := DesktopIconDiagnosticsInput{Kind: "hover_start", TraceID: "icon:abc-123", TargetKind: "icon", IdleMs: 12000, TS: 1700000000000, T0: 5000, Visibility: "visible", Focus: true, ViewportW: 1080, ViewportH: 720, DPR: 1.5, IconCount: 7, Revision: "r1"}
	recover := DesktopIconDiagnosticsInput{Kind: "hover_recovery", TraceID: "icon:abc-123", Frames: 60, WorstFrameGapMS: 120, AvgFrameGapMS: 16, LongTasks: 1, LongTasksMaxMS: 90, LongTasksTotalMS: 90, VisibilityChanges: 1, DOMMutations: 3, LayoutShifts: 2, EndedBy: "healthy", DurationMS: 1000}
	if err := app.WriteDesktopIconDiagnostics(start); err != nil {
		t.Fatalf("write start: %v", err)
	}
	if err := app.WriteDesktopIconDiagnostics(recover); err != nil {
		t.Fatalf("write recovery: %v", err)
	}
	lines := readDiagnosticsLines(t, app.DesktopIconDiagnosticsPath())
	if len(lines) != 2 {
		t.Fatalf("log lines = %d, want 2", len(lines))
	}
	var parsed DesktopIconDiagnosticsInput
	if err := json.Unmarshal([]byte(lines[0]), &parsed); err != nil {
		t.Fatalf("line 1 is not valid JSON: %v", err)
	}
	if parsed.Kind != "hover_start" || parsed.TraceID != "icon:abc-123" || parsed.IdleMs != 12000 || parsed.IconCount != 7 || parsed.DPR != 1.5 {
		t.Fatalf("line 1 record = %+v", parsed)
	}
	if err := json.Unmarshal([]byte(lines[1]), &parsed); err != nil {
		t.Fatalf("line 2 is not valid JSON: %v", err)
	}
	if parsed.Kind != "hover_recovery" || parsed.EndedBy != "healthy" || parsed.Frames != 60 || parsed.DOMMutations != 3 {
		t.Fatalf("line 2 record = %+v", parsed)
	}
}

func TestWriteDesktopIconDiagnosticsValidatesExplicitly(t *testing.T) {
	testDiagnosticsEnv(t)
	app := &App{}
	cases := []struct {
		name  string
		input DesktopIconDiagnosticsInput
	}{
		{"unknown kind", DesktopIconDiagnosticsInput{Kind: "hover_middle", TraceID: "icon:a"}},
		{"empty trace id", DesktopIconDiagnosticsInput{Kind: "hover_start", TraceID: ""}},
		{"oversized trace id", DesktopIconDiagnosticsInput{Kind: "hover_start", TraceID: strings.Repeat("a", 65)}},
		{"trace id free-form content", DesktopIconDiagnosticsInput{Kind: "hover_start", TraceID: "icon:user prompt with spaces"}},
		{"bad target kind", DesktopIconDiagnosticsInput{Kind: "hover_start", TraceID: "icon:a", TargetKind: "popup"}},
		{"bad visibility", DesktopIconDiagnosticsInput{Kind: "hover_start", TraceID: "icon:a", Visibility: "blurred"}},
		{"oversized revision", DesktopIconDiagnosticsInput{Kind: "hover_start", TraceID: "icon:a", Revision: strings.Repeat("r", 257)}},
		{"negative idle", DesktopIconDiagnosticsInput{Kind: "hover_start", TraceID: "icon:a", IdleMs: -1}},
		{"absurd icon count", DesktopIconDiagnosticsInput{Kind: "hover_start", TraceID: "icon:a", IconCount: 5000}},
		{"negative frames", DesktopIconDiagnosticsInput{Kind: "hover_recovery", TraceID: "icon:a", Frames: -3}},
		{"bad endedBy", DesktopIconDiagnosticsInput{Kind: "hover_recovery", TraceID: "icon:a", EndedBy: "forever"}},
	}
	for _, tc := range cases {
		if err := app.WriteDesktopIconDiagnostics(tc.input); err == nil {
			t.Errorf("%s: expected explicit error", tc.name)
		}
	}
	// A fully rejected batch must leave no log file behind.
	if _, err := os.Stat(app.DesktopIconDiagnosticsPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected writes must not create the log file, stat err = %v", err)
	}
}

func TestWriteDesktopIconDiagnosticsAcceptsMaxSizeFields(t *testing.T) {
	testDiagnosticsEnv(t)
	app := &App{}
	// Every field at its validation cap must still pass and land on disk as a
	// single NDJSON line (the 8 KiB line limit is defense-in-depth: today no
	// schema-valid record can reach it).
	input := DesktopIconDiagnosticsInput{
		Kind: "hover_start", TraceID: strings.Repeat("a", 64),
		Visibility: "visible", Revision: strings.Repeat("r", 256),
		IdleMs: 24 * 60 * 60 * 1000, ViewportW: 32768, ViewportH: 32768,
	}
	if err := app.WriteDesktopIconDiagnostics(input); err != nil {
		t.Fatalf("max-size fields should pass validation: %v", err)
	}
	lines := readDiagnosticsLines(t, app.DesktopIconDiagnosticsPath())
	if len(lines) != 1 {
		t.Fatalf("log lines = %d, want 1", len(lines))
	}
}

func TestAppendDesktopIconDiagnosticsLineRotatesOnOverflow(t *testing.T) {
	home := testDiagnosticsEnv(t)
	path := filepath.Join(home, desktopIconDiagnosticsFile)
	previousMax := desktopIconDiagnosticsMaxBytes
	desktopIconDiagnosticsMaxBytes = 200
	t.Cleanup(func() { desktopIconDiagnosticsMaxBytes = previousMax })

	// Each gen1 line is 66 bytes ("{...}x15 + \n"); three fit in the 200-byte
	// cap (198), the fourth would overflow and rotate instead.
	gen1Line := []byte(`{"kind":"hover_start","traceId":"icon:gen1","n":` + strings.Repeat("x", 15) + `}`)
	for i := 0; i < 3; i++ {
		if err := appendDesktopIconDiagnosticsLine(path, gen1Line); err != nil {
			t.Fatalf("append gen1 #%d: %v", i, err)
		}
	}
	gen1 := readDiagnosticsLines(t, path)
	if len(gen1) != 3 {
		t.Fatalf("gen1 lines = %d, want 3 (before rotation)", len(gen1))
	}

	// Overflowing append rotates gen1 to .1 and starts a fresh active file.
	gen2Line := []byte(`{"kind":"hover_start","traceId":"icon:gen2","n":` + strings.Repeat("x", 120) + `}`)
	if err := appendDesktopIconDiagnosticsLine(path, gen2Line); err != nil {
		t.Fatalf("append gen2: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated previous generation missing: %v", err)
	}
	active := readDiagnosticsLines(t, path)
	if len(active) != 1 || !strings.Contains(active[0], "gen2") {
		t.Fatalf("active file after rotation = %v, want only gen2", active)
	}
	prev := readDiagnosticsLines(t, path+".1")
	if len(prev) != 3 {
		t.Fatalf("previous generation lines = %d, want 3", len(prev))
	}

	// A second rotation replaces .1, so the log stays bounded to two files.
	if err := appendDesktopIconDiagnosticsLine(path, gen2Line); err != nil {
		t.Fatalf("append gen3: %v", err)
	}
	prev = readDiagnosticsLines(t, path+".1")
	if len(prev) != 1 || !strings.Contains(prev[0], "gen2") {
		t.Fatalf("second rotation should replace .1 with gen2, got %v", prev)
	}
}

func TestAppendDesktopIconDiagnosticsLineConcurrentAppendsKeepWholeLines(t *testing.T) {
	home := testDiagnosticsEnv(t)
	path := filepath.Join(home, desktopIconDiagnosticsFile)
	const workers = 8
	const perWorker = 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				line, err := json.Marshal(DesktopIconDiagnosticsInput{Kind: "hover_recovery", TraceID: "icon:concurrent", Frames: w*1000 + i})
				if err != nil {
					t.Errorf("marshal: %v", err)
					return
				}
				if err := appendDesktopIconDiagnosticsLine(path, line); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	// This exercises the OS O_APPEND whole-line atomicity on the raw append
	// helper; the iconDiagMu serialization of the bound method is covered by
	// TestDesktopIconDiagnosticsBoundMethodSerializesWrites below.
	lines := readDiagnosticsLines(t, path)
	if len(lines) != workers*perWorker {
		t.Fatalf("log lines = %d, want %d (no interleaved/lost lines)", len(lines), workers*perWorker)
	}
	seen := map[int]bool{}
	for _, line := range lines {
		var parsed DesktopIconDiagnosticsInput
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Fatalf("interleaved or corrupt line %q: %v", line, err)
		}
		key := parsed.Frames
		if seen[key] {
			t.Fatalf("duplicate record key %d — concurrent appends collided", key)
		}
		seen[key] = true
	}
}

func TestDesktopIconDiagnosticsBoundMethodSerializesWrites(t *testing.T) {
	testDiagnosticsEnv(t)
	app := &App{}
	input := DesktopIconDiagnosticsInput{Kind: "hover_start", TraceID: "icon:mutex", IdleMs: 6000}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := app.WriteDesktopIconDiagnostics(input); err != nil {
					t.Errorf("bound write: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	lines := readDiagnosticsLines(t, app.DesktopIconDiagnosticsPath())
	if len(lines) != 8*20 {
		t.Fatalf("bound method log lines = %d, want %d (iconDiagMu must serialize every append)", len(lines), 8*20)
	}
}
