//go:build !windows

package main

import (
	"errors"
	"time"
)

func platformSystemIdleDuration() (time.Duration, error) {
	return 0, errors.New("system-wide AFK detection is unavailable on this platform")
}
