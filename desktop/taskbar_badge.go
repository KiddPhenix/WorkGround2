package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func taskbarBadgeLabel(count int) string {
	if count <= 0 {
		return ""
	}
	if count > 99 {
		return "99+"
	}
	return strconv.Itoa(count)
}

// scheduleUnreadBadge coalesces concurrent unread changes and serializes native
// taskbar writes so an older update can never overwrite a newer total.
func (a *App) scheduleUnreadBadge(total int) {
	if a == nil || a.ctx == nil {
		return
	}
	if total < 0 {
		total = 0
	}
	a.unreadBadgeMu.Lock()
	a.unreadBadgeTarget = total
	if a.unreadBadgeRunning {
		a.unreadBadgeMu.Unlock()
		return
	}
	a.unreadBadgeRunning = true
	a.unreadBadgeMu.Unlock()

	a.goSafe("syncUnreadBadge", func() {
		for {
			a.unreadBadgeMu.Lock()
			target := a.unreadBadgeTarget
			a.unreadBadgeMu.Unlock()

			var err error
			for attempt := 0; attempt < 3; attempt++ {
				err = setTaskbarBadge(target)
				if err == nil {
					break
				}
				time.Sleep(time.Duration(attempt+1) * 150 * time.Millisecond)
				a.unreadBadgeMu.Lock()
				changed := a.unreadBadgeTarget != target
				a.unreadBadgeMu.Unlock()
				if changed {
					break
				}
			}

			a.unreadBadgeMu.Lock()
			if a.unreadBadgeTarget != target {
				a.unreadBadgeMu.Unlock()
				continue
			}
			a.unreadBadgeRunning = false
			a.unreadBadgeMu.Unlock()
			if err != nil {
				runtime.LogWarning(a.ctx, fmt.Sprintf("update taskbar unread badge: %v", err))
			}
			return
		}
	})
}
