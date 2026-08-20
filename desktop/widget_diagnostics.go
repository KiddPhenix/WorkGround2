package main

// 桌面图标小组件的低开销悬停性能诊断日志：NDJSON 逐行追加、按大小轮转。
//
// 前端在“空闲后首次悬停”时写一条 hover_start（即时），有界恢复采样结束后写
// 一条 hover_recovery（汇总），因此每次合格 trace 恰好两次写入、无高频后端调用。
// 记录只携带测量数值与稳定状态标记，绝不包含任务内容、提示词、图标标题或用户
// 路径；输入逐字段显式校验，非法或超限输入直接报错，日志失败不阻断图标交互。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"workground2/internal/config"
)

const (
	// desktopIconDiagnosticsFile is the stable per-user NDJSON diagnostics log.
	desktopIconDiagnosticsFile = "desktop-icon-diagnostics.ndjson"
	// desktopIconDiagnosticsLineLimit rejects a single oversized record so a
	// corrupted or hostile frontend can never balloon the log with one write.
	desktopIconDiagnosticsLineLimit = 8 << 10

	desktopIconDiagnosticsKindStart   = "hover_start"
	desktopIconDiagnosticsKindRecover = "hover_recovery"

	desktopIconDiagnosticsEndedHealthy = "healthy"
	desktopIconDiagnosticsEndedTimeout = "timeout"
	desktopIconDiagnosticsEndedAborted = "aborted"

	desktopIconDiagnosticsTraceIDLimit  = 64
	desktopIconDiagnosticsStringLimit   = 128
	desktopIconDiagnosticsRevisionLimit = 256
	// desktopIconDiagnosticsTimeLimit is a generous sanity bound shared by wall
	// clock ms (epoch timestamps) and monotonic ms (long-lived sessions): it
	// rejects negatives and absurd values without rejecting real clocks.
	desktopIconDiagnosticsTimeLimit int64 = 1 << 46
)

// desktopIconDiagnosticsMaxBytes caps the active log. When the next append
// would overflow, the current file rotates to <file>.1 (one previous
// generation is kept, older generations are replaced). It is a package var so
// rotation tests can lower the bound without touching real user files.
var desktopIconDiagnosticsMaxBytes int64 = 2 << 20

// DesktopIconDiagnosticsInput is one typed NDJSON record appended by the icon
// widget frontend. Every field is a measurement or a stable widget state
// marker; free-form strings are length-bounded and validated, so the log can
// never carry conversation content, prompts, icon titles or user paths.
type DesktopIconDiagnosticsInput struct {
	Kind       string `json:"kind"`                 // hover_start | hover_recovery
	TraceID    string `json:"traceId"`              // unique per trace, safe charset
	TargetKind string `json:"targetKind,omitempty"` // icon | anchor

	// hover_start context captured at the moment the trace opens.
	IdleMs     int64   `json:"idleMs,omitempty"`     // measured pointer-idle before the hover
	TS         int64   `json:"ts,omitempty"`         // wall clock ms
	T0         int64   `json:"t0,omitempty"`         // monotonic clock ms
	Visibility string  `json:"visibility,omitempty"` // document.visibilityState
	Focus      bool    `json:"focus,omitempty"`      // document.hasFocus()
	ViewportW  int     `json:"viewportW,omitempty"`
	ViewportH  int     `json:"viewportH,omitempty"`
	DPR        float64 `json:"dpr,omitempty"`
	IconCount  int     `json:"iconCount,omitempty"` // rendered icon count
	Revision   string  `json:"revision,omitempty"`  // snapshot state revision

	// hover_recovery aggregates over the bounded sampling window.
	Frames            int    `json:"frames,omitempty"`
	WorstFrameGapMS   int64  `json:"worstFrameGapMs,omitempty"`
	AvgFrameGapMS     int64  `json:"avgFrameGapMs,omitempty"`
	LongTasks         int    `json:"longTasks,omitempty"`
	LongTasksMaxMS    int64  `json:"longTasksMaxMs,omitempty"`
	LongTasksTotalMS  int64  `json:"longTasksTotalMs,omitempty"`
	VisibilityChanges int    `json:"visibilityChanges,omitempty"`
	DOMMutations      int    `json:"domMutations,omitempty"`
	LayoutShifts      int    `json:"layoutShifts,omitempty"`
	EndedBy           string `json:"endedBy,omitempty"` // healthy | timeout | aborted
	DurationMS        int64  `json:"durationMs,omitempty"`
}

// desktopIconDiagnosticsPath returns the stable per-user diagnostics file.
func desktopIconDiagnosticsPath() string {
	return filepath.Join(config.MemoryUserDir(), desktopIconDiagnosticsFile)
}

// DesktopIconDiagnosticsPath exposes the exact log path so the user can
// retrieve the diagnostics file.
func (a *App) DesktopIconDiagnosticsPath() string {
	return desktopIconDiagnosticsPath()
}

// WriteDesktopIconDiagnostics validates and appends one NDJSON record to the
// per-user diagnostics log. Invalid or oversized input fails explicitly;
// concurrent calls are serialized by iconDiagMu. The write is fire-and-forget
// from the frontend's perspective and must never break widget interaction.
func (a *App) WriteDesktopIconDiagnostics(input DesktopIconDiagnosticsInput) error {
	if err := validateDesktopIconDiagnostics(input); err != nil {
		return err
	}
	line, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode icon widget diagnostics: %w", err)
	}
	if len(line) > desktopIconDiagnosticsLineLimit {
		return fmt.Errorf("icon widget diagnostics record exceeds %d bytes", desktopIconDiagnosticsLineLimit)
	}
	a.iconDiagMu.Lock()
	defer a.iconDiagMu.Unlock()
	return appendDesktopIconDiagnosticsLine(desktopIconDiagnosticsPath(), line)
}

// validateDesktopIconDiagnostics rejects records that are structurally invalid
// or carry unexpected content, before anything touches the log file.
func validateDesktopIconDiagnostics(input DesktopIconDiagnosticsInput) error {
	switch input.Kind {
	case desktopIconDiagnosticsKindStart, desktopIconDiagnosticsKindRecover:
	default:
		return fmt.Errorf("invalid icon widget diagnostics kind %q", input.Kind)
	}
	if !validDesktopIconTraceID(input.TraceID) {
		return errors.New("invalid icon widget diagnostics traceId")
	}
	if input.TargetKind != "" && input.TargetKind != "icon" && input.TargetKind != "anchor" {
		return fmt.Errorf("invalid icon widget diagnostics targetKind %q", input.TargetKind)
	}
	if len(input.Revision) > desktopIconDiagnosticsRevisionLimit {
		return fmt.Errorf("icon widget diagnostics revision exceeds %d bytes", desktopIconDiagnosticsRevisionLimit)
	}
	if input.Visibility != "" && input.Visibility != "visible" && input.Visibility != "hidden" && input.Visibility != "prerender" && input.Visibility != "unloaded" {
		return fmt.Errorf("invalid icon widget diagnostics visibility %q", input.Visibility)
	}
	if len(input.Visibility) > desktopIconDiagnosticsStringLimit {
		return fmt.Errorf("icon widget diagnostics visibility exceeds %d bytes", desktopIconDiagnosticsStringLimit)
	}
	if input.EndedBy != "" && input.EndedBy != desktopIconDiagnosticsEndedHealthy && input.EndedBy != desktopIconDiagnosticsEndedTimeout && input.EndedBy != desktopIconDiagnosticsEndedAborted {
		return fmt.Errorf("invalid icon widget diagnostics endedBy %q", input.EndedBy)
	}
	if len(input.EndedBy) > desktopIconDiagnosticsStringLimit {
		return fmt.Errorf("icon widget diagnostics endedBy exceeds %d bytes", desktopIconDiagnosticsStringLimit)
	}
	// Sanity bounds so a corrupted frontend cannot persist absurd measurements.
	if input.IconCount < 0 || input.IconCount > 4096 {
		return errors.New("invalid icon widget diagnostics iconCount")
	}
	if input.ViewportW < 0 || input.ViewportW > 32768 || input.ViewportH < 0 || input.ViewportH > 32768 {
		return errors.New("invalid icon widget diagnostics viewport")
	}
	if input.DPR < 0 || input.DPR > 16 {
		return errors.New("invalid icon widget diagnostics dpr")
	}
	if input.Frames < 0 || input.Frames > 1_000_000 {
		return errors.New("invalid icon widget diagnostics frames")
	}
	if input.LongTasks < 0 || input.LongTasks > 1_000_000 || input.VisibilityChanges < 0 || input.VisibilityChanges > 1_000_000 ||
		input.DOMMutations < 0 || input.DOMMutations > 1_000_000 || input.LayoutShifts < 0 || input.LayoutShifts > 1_000_000 {
		return errors.New("invalid icon widget diagnostics aggregate count")
	}
	for _, ms := range []int64{input.IdleMs, input.TS, input.T0, input.WorstFrameGapMS, input.AvgFrameGapMS, input.LongTasksMaxMS, input.LongTasksTotalMS, input.DurationMS} {
		if ms < 0 || ms > desktopIconDiagnosticsTimeLimit {
			return errors.New("invalid icon widget diagnostics timestamp/duration")
		}
	}
	return nil
}

// validDesktopIconTraceID bounds the trace identifier to a safe charset so the
// log line can never carry free-form frontend content.
func validDesktopIconTraceID(id string) bool {
	if id == "" || len(id) > desktopIconDiagnosticsTraceIDLimit {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == ':' || r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// appendDesktopIconDiagnosticsLine appends one NDJSON line, rotating the log
// when the active file would exceed the size bound. Rotation keeps exactly one
// previous generation (<file>.1); on Windows the old .1 must be removed before
// rename can replace it.
func appendDesktopIconDiagnosticsLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create icon widget diagnostics dir: %w", err)
	}
	if fi, err := os.Stat(path); err == nil {
		if fi.Size() > 0 && fi.Size()+int64(len(line))+1 > desktopIconDiagnosticsMaxBytes {
			if err := rotateDesktopIconDiagnostics(path); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat icon widget diagnostics: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open icon widget diagnostics: %w", err)
	}
	defer file.Close()
	line = append(line, '\n')
	if _, err := file.Write(line); err != nil {
		return fmt.Errorf("append icon widget diagnostics: %w", err)
	}
	return nil
}

func rotateDesktopIconDiagnostics(path string) error {
	previous := path + ".1"
	// Windows cannot rename over an existing file; removing the previous
	// generation first keeps the rotation bounded to at most two files.
	_ = os.Remove(previous)
	if err := os.Rename(path, previous); err != nil {
		return fmt.Errorf("rotate icon widget diagnostics: %w", err)
	}
	return nil
}
