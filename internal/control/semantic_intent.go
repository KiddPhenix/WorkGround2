package control

import (
	"context"
	"fmt"

	"workground2/internal/agent"
)

// SemanticIntentClassifier is an optional read-only capability used by
// frontends that need a small model classification without starting a turn.
type SemanticIntentClassifier interface {
	ClassifySemanticIntent(ctx context.Context, input string) (agent.SemanticIntent, error)
}

// ClassifySemanticIntent uses the current Session model without mutating its
// transcript, run state, or cache-stable system prompt.
func (c *Controller) ClassifySemanticIntent(ctx context.Context, input string) (agent.SemanticIntent, error) {
	if c == nil {
		return agent.SemanticIntentChat, fmt.Errorf("session controller is unavailable")
	}
	if c.executor != nil {
		return c.executor.ClassifySemanticIntent(ctx, input)
	}
	if classifier, ok := c.runner.(SemanticIntentClassifier); ok {
		return classifier.ClassifySemanticIntent(ctx, input)
	}
	return agent.SemanticIntentChat, fmt.Errorf("session model does not support semantic intent classification")
}

var _ SemanticIntentClassifier = (*Controller)(nil)
