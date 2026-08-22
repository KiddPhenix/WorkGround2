package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

func StableID(prefix, key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return prefix + "-" + hex.EncodeToString(sum[:12])
}

func OccurrenceKey(assistantID, routineID string, scheduledFor time.Time) string {
	return fmt.Sprintf("%s/%s/%s", assistantID, routineID, scheduledFor.UTC().Format(time.RFC3339Nano))
}
