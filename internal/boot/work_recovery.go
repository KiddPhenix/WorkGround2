package boot

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"workground2/internal/event"
	"workground2/internal/work"
)

var activeWorkRecoveries = struct {
	sync.Mutex
	keys map[string]struct{}
}{keys: make(map[string]struct{})}

func workRecoveryKey(workDir string) string {
	key, err := filepath.Abs(strings.TrimSpace(workDir))
	if err != nil {
		key = filepath.Clean(strings.TrimSpace(workDir))
	}
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}

func claimWorkRecovery(workDir string) (string, bool) {
	key := workRecoveryKey(workDir)
	if key == "" || key == "." {
		return "", false
	}
	activeWorkRecoveries.Lock()
	defer activeWorkRecoveries.Unlock()
	if _, exists := activeWorkRecoveries.keys[key]; exists {
		return key, false
	}
	activeWorkRecoveries.keys[key] = struct{}{}
	return key, true
}

func releaseWorkRecovery(key string) {
	activeWorkRecoveries.Lock()
	delete(activeWorkRecoveries.keys, key)
	activeWorkRecoveries.Unlock()
}

func workRecoveryRunning(workDir string) bool {
	key := workRecoveryKey(workDir)
	activeWorkRecoveries.Lock()
	_, exists := activeWorkRecoveries.keys[key]
	activeWorkRecoveries.Unlock()
	return exists
}

// startBackgroundWorkRecovery keeps durable Work self-healing out of the
// controller construction path. One slow provider or retryable historical task
// must not prevent a new Session from becoming usable.
func startBackgroundWorkRecovery(ctx context.Context, workDir string, svc *work.Service, sink event.Sink) bool {
	if svc == nil {
		return false
	}
	key, claimed := claimWorkRecovery(workDir)
	if !claimed {
		slog.Debug("work: background recovery already active", "work_dir", workDir)
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if sink == nil {
		sink = event.Discard
	}
	go func() {
		defer releaseWorkRecovery(key)
		defer func() {
			if value := recover(); value != nil {
				slog.Error("work: background recovery panicked", "work_dir", workDir, "panic", value)
				sink.Emit(event.Event{
					Kind:  event.Notice,
					Level: event.LevelWarn,
					Text:  fmt.Sprintf("work: background recovery failed: %v", value),
				})
			}
		}()

		slog.Info("work: background recovery started", "work_dir", workDir)
		report := svc.RecoverAllV2Scheduling(ctx)
		for _, failure := range report.Failures {
			slog.Warn("work: background recovery remains retryable", "work_dir", workDir, "work", failure.WorkID, "err", failure.Error)
			sink.Emit(event.Event{
				Kind:  event.Notice,
				Level: event.LevelWarn,
				Text:  fmt.Sprintf("work: background recovery remains retryable · work=%s · %s", failure.WorkID, failure.Error),
			})
		}
		slog.Info("work: background recovery finished",
			"work_dir", workDir,
			"recovered", report.Recovered,
			"scanned", report.Scanned,
			"failures", len(report.Failures),
		)
		if report.Recovered > 0 {
			sink.Emit(event.Event{
				Kind:  event.Notice,
				Level: event.LevelInfo,
				Text:  fmt.Sprintf("work: background recovery finished · recovered=%d scanned=%d", report.Recovered, report.Scanned),
			})
		}
	}()
	return true
}
