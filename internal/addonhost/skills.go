package addonhost

import (
	"context"
	"fmt"
)

// ── Skills API ──────────────────────────────────────────────────────────────

// SkillRunner is the interface for invoking a host skill.
type SkillRunner interface {
	Run(ctx context.Context, name, input string) (string, error)
}

// SkillsRun invokes a host skill by name with the given input.
func (h *Host) SkillsRun(ctx context.Context, name, input string) (string, error) {
	if h.skills == nil {
		return "", fmt.Errorf("%w: skill runner not configured", ErrBadRequest)
	}
	return h.skills.Run(ctx, name, input)
}

// Ensure context is used.
var _ context.Context
