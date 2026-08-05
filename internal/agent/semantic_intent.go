package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"workground2/internal/provider"
)

// SemanticIntent is a model-classified user intent that was not covered by a
// caller's deterministic rules.
type SemanticIntent string

const (
	SemanticIntentChat      SemanticIntent = "chat"
	SemanticIntentUncertain SemanticIntent = "uncertain"
	SemanticIntentSelfAgent SemanticIntent = "self_agent"
)

const semanticIntentPrompt = `You classify a message written by a person in a multi-human, multi-agent engineering Room.
Decide whether the author intends their own local Agent to act.

self_agent: an explicit request or instruction for the author's Agent to inspect, change, run, verify, or otherwise act.
uncertain: an implicit request, product/code behavior observation, or problem statement that probably invites the author's Agent to investigate, but does not explicitly ask.
chat: information sharing, status updates, acknowledgements, greetings, completed-work reports, or messages directed at another person.

Examples:
- 现在多人协作 room，在 session 里会有一个“外部”的标签 -> uncertain
- the login button stays disabled after sign-in -> uncertain
- please inspect the external label -> self_agent
- I already fixed the external label -> chat
- thanks, I see it now -> chat

Reply with exactly one token: self_agent, uncertain, or chat.`

const (
	semanticIntentTimeout  = 10 * time.Second
	semanticIntentCacheTTL = 5 * time.Minute
	semanticIntentCacheMax = 100
	semanticIntentMaxRunes = 4_000
)

type semanticIntentEntry struct {
	intent SemanticIntent
	at     time.Time
}

type semanticIntentFlight struct {
	done   chan struct{}
	intent SemanticIntent
	err    error
}

type semanticIntentClassifier struct {
	provider provider.Provider
	mu       sync.Mutex
	cache    map[string]semanticIntentEntry
	flights  map[string]*semanticIntentFlight
}

func newSemanticIntentClassifier(prov provider.Provider) *semanticIntentClassifier {
	return &semanticIntentClassifier{
		provider: prov,
		cache:    make(map[string]semanticIntentEntry),
		flights:  make(map[string]*semanticIntentFlight),
	}
}

// ClassifySemanticIntent asks the current model to classify an otherwise
// uncovered message without mutating the Agent session.
func (a *Agent) ClassifySemanticIntent(ctx context.Context, input string) (SemanticIntent, error) {
	if a == nil || a.intent == nil {
		return SemanticIntentChat, fmt.Errorf("semantic intent model is unavailable")
	}
	return a.intent.classify(ctx, input)
}

func (c *semanticIntentClassifier) classify(ctx context.Context, input string) (SemanticIntent, error) {
	key := normalizeInputForCache(input)
	if key == "" {
		return SemanticIntentChat, nil
	}

	c.mu.Lock()
	if entry, ok := c.cache[key]; ok && time.Since(entry.at) <= semanticIntentCacheTTL {
		c.mu.Unlock()
		return entry.intent, nil
	}
	if flight := c.flights[key]; flight != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return SemanticIntentChat, ctx.Err()
		case <-flight.done:
			return flight.intent, flight.err
		}
	}
	flight := &semanticIntentFlight{done: make(chan struct{})}
	c.flights[key] = flight
	c.mu.Unlock()

	intent, err := c.request(ctx, input)

	c.mu.Lock()
	flight.intent, flight.err = intent, err
	delete(c.flights, key)
	if err == nil {
		if len(c.cache) >= semanticIntentCacheMax {
			c.evictOldestLocked()
		}
		c.cache[key] = semanticIntentEntry{intent: intent, at: time.Now()}
	}
	close(flight.done)
	c.mu.Unlock()
	return intent, err
}

func (c *semanticIntentClassifier) request(ctx context.Context, input string) (SemanticIntent, error) {
	if c.provider == nil {
		return SemanticIntentChat, fmt.Errorf("semantic intent classifier provider is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, semanticIntentTimeout)
	defer cancel()

	input = truncateIntentInput(strings.TrimSpace(input), semanticIntentMaxRunes)
	stream, err := c.provider.Stream(ctx, provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: semanticIntentPrompt},
			{Role: provider.RoleUser, Content: input},
		},
		MaxTokens:   8,
		Temperature: 0,
	})
	if err != nil {
		return SemanticIntentChat, fmt.Errorf("semantic intent request: %w", err)
	}
	var response strings.Builder
	for chunk := range stream {
		if chunk.Err != nil {
			return SemanticIntentChat, fmt.Errorf("semantic intent stream: %w", chunk.Err)
		}
		if chunk.Type == provider.ChunkText {
			response.WriteString(chunk.Text)
		}
	}
	raw := strings.TrimSpace(response.String())
	if raw == "" {
		return SemanticIntentChat, nil
	}
	intent := SemanticIntent(strings.ToLower(raw))
	switch intent {
	case SemanticIntentChat, SemanticIntentUncertain, SemanticIntentSelfAgent:
		return intent, nil
	default:
		return SemanticIntentChat, fmt.Errorf("semantic intent returned invalid value %q", response.String())
	}
}

func (c *semanticIntentClassifier) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range c.cache {
		if oldestKey == "" || entry.at.Before(oldest) {
			oldestKey, oldest = key, entry.at
		}
	}
	delete(c.cache, oldestKey)
}

func truncateIntentInput(input string, maxRunes int) string {
	if utf8.RuneCountInString(input) <= maxRunes {
		return input
	}
	runes := []rune(input)
	return string(runes[:maxRunes])
}
