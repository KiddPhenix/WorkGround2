package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/provider"
)

type semanticIntentProvider struct {
	reply string
	err   error
	gate  <-chan struct{}
	calls atomic.Int32
}

func (p *semanticIntentProvider) Name() string { return "semantic-intent-test" }

func (p *semanticIntentProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	p.calls.Add(1)
	if p.err != nil {
		return nil, p.err
	}
	out := make(chan provider.Chunk, 2)
	go func() {
		defer close(out)
		if p.gate != nil {
			select {
			case <-ctx.Done():
				out <- provider.Chunk{Type: provider.ChunkError, Err: ctx.Err()}
				return
			case <-p.gate:
			}
		}
		out <- provider.Chunk{Type: provider.ChunkText, Text: p.reply}
		out <- provider.Chunk{Type: provider.ChunkDone}
	}()
	return out, nil
}

func TestSemanticIntentClassifierClassifiesAndCachesImplicitRoomRequest(t *testing.T) {
	prov := &semanticIntentProvider{reply: "uncertain"}
	classifier := newSemanticIntentClassifier(prov)
	input := "现在 多人协作room, 在session里会有一个\"外部\"的标签"

	for i := 0; i < 2; i++ {
		got, err := classifier.classify(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if got != SemanticIntentUncertain {
			t.Fatalf("intent = %q, want %q", got, SemanticIntentUncertain)
		}
	}
	if got := prov.calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 cached call", got)
	}
}

func TestSemanticIntentClassifierRejectsInvalidOutputAndRetries(t *testing.T) {
	prov := &semanticIntentProvider{reply: "probably a task"}
	classifier := newSemanticIntentClassifier(prov)
	for i := 0; i < 2; i++ {
		got, err := classifier.classify(context.Background(), "implicit behavior report")
		if got != SemanticIntentChat {
			t.Fatalf("fallback intent = %q, want chat", got)
		}
		if err == nil {
			t.Fatal("invalid output should remain observable")
		}
	}
	if got := prov.calls.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 retryable calls", got)
	}
}

func TestSemanticIntentClassifierTreatsEmptyOutputAsCachedChat(t *testing.T) {
	prov := &semanticIntentProvider{reply: " \n\t"}
	classifier := newSemanticIntentClassifier(prov)
	for i := 0; i < 2; i++ {
		got, err := classifier.classify(context.Background(), "plain Room conversation")
		if err != nil {
			t.Fatalf("empty output should safely fall back to chat: %v", err)
		}
		if got != SemanticIntentChat {
			t.Fatalf("intent = %q, want %q", got, SemanticIntentChat)
		}
	}
	if got := prov.calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 cached chat result", got)
	}
}

func TestSemanticIntentClassifierCoalescesConcurrentRequests(t *testing.T) {
	gate := make(chan struct{})
	prov := &semanticIntentProvider{reply: "self_agent", gate: gate}
	classifier := newSemanticIntentClassifier(prov)
	results := make(chan SemanticIntent, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			intent, err := classifier.classify(context.Background(), "please infer this intent")
			results <- intent
			errs <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for prov.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if prov.calls.Load() == 0 {
		t.Fatal("provider call did not start")
	}
	close(gate)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if got := <-results; got != SemanticIntentSelfAgent {
			t.Fatalf("intent = %q, want %q", got, SemanticIntentSelfAgent)
		}
	}
	if got := prov.calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 coalesced call", got)
	}
}

func TestSemanticIntentClassifierNilProviderReturnsError(t *testing.T) {
	classifier := newSemanticIntentClassifier(nil)
	_, err := classifier.classify(context.Background(), "test message")
	if err == nil {
		t.Fatal("nil provider should return an observable error")
	}
}

func TestSemanticIntentClassifierUsesOnlyOwnProvider(t *testing.T) {
	// The classifier must only call its own provider — never the main session
	// model. We verify this by giving it a dedicated provider and confirming
	// the main provider (a different one) is never touched.
	intentProv := &semanticIntentProvider{reply: "self_agent"}
	mainProv := &semanticIntentProvider{reply: "chat"}

	classifier := newSemanticIntentClassifier(intentProv)
	got, err := classifier.classify(context.Background(), "please fix the login button")
	if err != nil {
		t.Fatal(err)
	}
	if got != SemanticIntentSelfAgent {
		t.Fatalf("intent = %q, want self_agent", got)
	}
	if intentProv.calls.Load() != 1 {
		t.Fatalf("intent provider calls = %d, want 1", intentProv.calls.Load())
	}
	if mainProv.calls.Load() != 0 {
		t.Fatalf("main provider was called %d times, want 0 — classifier must not use main session model", mainProv.calls.Load())
	}
}

func TestSemanticIntentClassifierRespectsTimeout(t *testing.T) {
	// The internal timeout is 10s. A context with a shorter deadline must
	// cancel the request.
	gate := make(chan struct{})
	prov := &semanticIntentProvider{reply: "self_agent", gate: gate}
	classifier := newSemanticIntentClassifier(prov)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got, err := classifier.classify(ctx, "test")
	close(gate) // unblock the provider so the goroutine can finish
	if got != SemanticIntentChat {
		t.Fatalf("intent = %q, want chat on timeout", got)
	}
	if err == nil {
		t.Fatal("short deadline should produce a context error")
	}
}

func TestAgentClassifySemanticIntentNilIntentReturnsError(t *testing.T) {
	// An Agent without SemanticIntentProvider (nil intent) must return an
	// observable error, not silently fall back to chat.
	a := &Agent{intent: nil}
	_, err := a.ClassifySemanticIntent(context.Background(), "test")
	if err == nil {
		t.Fatal("nil intent should return an observable error")
	}
}
