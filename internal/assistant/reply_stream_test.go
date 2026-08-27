package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

func streamTestNow() time.Time {
	return time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
}

func feedAll(d *replyStreamDecoder, chunks ...string) (string, bool) {
	var last string
	anyChanged := false
	for _, chunk := range chunks {
		var changed bool
		last, changed = d.Feed(chunk)
		anyChanged = anyChanged || changed
	}
	return last, anyChanged
}

func TestReplyStreamDecoderEscapes(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		reply string
	}{
		{"plain", `{"kind":"task","reply":"收到，我来处理。","jobs":[]}`, "收到，我来处理。"},
		{"escaped quote", `{"kind":"question","reply":"他说\"你好\"","jobs":[]}`, `他说"你好"`},
		{"escaped backslash", `{"kind":"task","reply":"路径是 C:\\work","jobs":[]}`, `路径是 C:\work`},
		{"escaped newline", `{"kind":"task","reply":"第一行\n第二行","jobs":[]}`, "第一行\n第二行"},
		{"unicode bmp", `{"kind":"task","reply":"\u4f60\u597d","jobs":[]}`, "你好"},
		{"unicode surrogate pair", `{"kind":"task","reply":"\ud83d\ude00","jobs":[]}`, "😀"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := feedAll(&replyStreamDecoder{}, tc.raw)
			if !changed || got != tc.reply {
				t.Fatalf("Feed(%q) = %q changed=%v, want %q", tc.raw, got, changed, tc.reply)
			}
		})
	}
}

func TestReplyStreamDecoderChunked(t *testing.T) {
	raw := `{"kind":"task","reply":"第一行\n他说\"好\"，路径 C:\\x，\u4f60\u597d\ud83d\ude00","jobs":[{"name":"a","kind":"task","prompt":"x"}]}`
	want := "第一行\n他说\"好\"，路径 C:\\x，你好😀"

	// Split at every position to exercise every possible chunk boundary,
	// including inside escapes and UTF-8 sequences.
	for cut := 0; cut <= len(raw); cut++ {
		d := &replyStreamDecoder{}
		got, changed := feedAll(d, raw[:cut], raw[cut:])
		if !changed || got != want {
			t.Fatalf("chunked at %d: got %q changed=%v, want %q", cut, got, changed, want)
		}
	}
}

func TestReplyStreamDecoderIgnoresJobReplyMention(t *testing.T) {
	// The first "reply" occurrence is the field key; the literal inside a job
	// prompt must not be treated as the value.
	raw := `{"kind":"task","reply":"已收到","jobs":[{"name":"a","kind":"task","prompt":"不要写 reply 这个词"}]}`
	got, changed := feedAll(&replyStreamDecoder{}, raw)
	if !changed || got != "已收到" {
		t.Fatalf("got %q changed=%v, want 已收到", got, changed)
	}
}

func TestReplyStreamDecoderNoReply(t *testing.T) {
	got, changed := feedAll(&replyStreamDecoder{}, `{"kind":"task"}`)
	if changed || got != "" {
		t.Fatalf("got %q changed=%v, want empty", got, changed)
	}
}

type streamRoleModelFunc func(ctx context.Context, prompt string, onDelta func(string)) (string, error)

func (f streamRoleModelFunc) Complete(ctx context.Context, prompt string) (string, error) {
	return f(ctx, prompt, nil)
}

func (f streamRoleModelFunc) CompleteStream(ctx context.Context, prompt string, onDelta func(string)) (string, error) {
	return f(ctx, prompt, onDelta)
}

func TestDispatcherStreamsReplyPreview(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	created, err := store.Create(CreateInput{
		RequestID: "create-stream",
		Assistant: Assistant{ID: "helper-stream", Name: "Helper", Mission: "keep healthy", Scope: ScopeGlobal, Lifecycle: LifecycleActive, Policy: DefaultPolicy()},
		Now:       streamTestNow(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	final := `{"kind":"task","reply":"收到，我来处理。","jobs":[{"name":"execute","kind":"task","prompt":"scan"}]}`
	model := streamRoleModelFunc(func(_ context.Context, _ string, onDelta func(string)) (string, error) {
		if onDelta == nil {
			return final, nil
		}
		// Stream in awkward chunks that split a rune and an escape.
		for _, chunk := range []string{`{"kind":"task","re`, `ply":"收到`, `，我来处`, `理。"}`, `,`} {
			onDelta(chunk)
		}
		return final, nil
	})
	dispatcher, err := NewDispatcher(store, model)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	var previews []string
	dispatcher.SetReplyObserver(func(p ReplyPreview) {
		if p.AssistantID != created.Assistant.ID || p.RequestID != "submit-stream" {
			t.Fatalf("preview carries wrong identity: %+v", p)
		}
		previews = append(previews, p.Reply)
	})

	dispatch, err := dispatcher.Dispatch(context.Background(), OpenDispatchInput{
		AssistantID: created.Assistant.ID, RequestID: "submit-stream", Input: "scan it", Now: streamTestNow(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if dispatch.State != DispatchClassified || dispatch.Reply != "收到，我来处理。" {
		t.Fatalf("final dispatch not authoritative: %+v", dispatch)
	}
	if len(previews) == 0 {
		t.Fatal("expected at least one streamed preview")
	}
	// Previews are cumulative and must never show raw JSON or partial escapes.
	for _, preview := range previews {
		if strings.Contains(preview, `\`) || strings.Contains(preview, `{"kind"`) {
			t.Fatalf("preview leaked raw JSON/escapes: %q", preview)
		}
	}
	if previews[len(previews)-1] != "收到，我来处理。" {
		t.Fatalf("final preview %q != authoritative reply", previews[len(previews)-1])
	}
}
