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
