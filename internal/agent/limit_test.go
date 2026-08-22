package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

// limiterTool is a fake Tool implementing tool.OutputLimiter with a
// configurable LimitOutput result, for wiring and fallback tests.
type limiterTool struct {
	name     string
	readOnly bool
	out      string
	handled  bool
	limitFn  func(s string, maxBytes int) (string, bool)
}

func (l limiterTool) Name() string            { return l.name }
func (l limiterTool) Description() string     { return "" }
func (l limiterTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (l limiterTool) ReadOnly() bool          { return l.readOnly }
func (l limiterTool) Execute(context.Context, json.RawMessage) (string, error) {
	return strings.Repeat("x", 40*1024), nil // always over the cap
}
func (l limiterTool) LimitOutput(s string, maxBytes int) (string, bool) {
	if l.limitFn != nil {
		return l.limitFn(s, maxBytes)
	}
	return l.out, l.handled
}

// TestFitToolOutputUsesLimiter proves a tool's shape-aware output is fed to
// the model instead of the generic head/tail cut, with the byte elision
// reported truthfully.
func TestFitToolOutputUsesLimiter(t *testing.T) {
	big := strings.Repeat("x", 40*1024)
	lm := limiterTool{name: "shapey", out: `{"ok":true,"kept":3}`, handled: true}
	out, notice := fitToolOutput(lm, big)
	if out != `{"ok":true,"kept":3}` {
		t.Fatalf("limiter output not used: %q", out)
	}
	if !strings.Contains(notice, "truncated") {
		t.Fatalf("truncation notice missing: %q", notice)
	}
}

// TestFitToolOutputLimiterPassThrough keeps an untouched pass-through silent —
// no truncation, no notice.
func TestFitToolOutputLimiterPassThrough(t *testing.T) {
	small := `{"ok":true}`
	lm := limiterTool{name: "shapey", out: small, handled: true}
	out, notice := fitToolOutput(lm, small)
	if out != small {
		t.Fatalf("pass-through output changed: %q", out)
	}
	if notice != "" {
		t.Fatalf("pass-through must not emit a notice, got %q", notice)
	}
}

// TestFitToolOutputLimiterDeclinesFallsBackToGeneric: when the limiter
// declines, the generic head/tail truncation must apply unchanged.
func TestFitToolOutputLimiterDeclinesFallsBackToGeneric(t *testing.T) {
	big := strings.Repeat("x", 40*1024)
	lm := limiterTool{name: "shapey", out: "", handled: false}
	out, notice := fitToolOutput(lm, big)
	if !strings.Contains(out, "…[truncated") {
		t.Fatal("declined limiter must fall back to the generic marker")
	}
	if !strings.Contains(notice, "truncated") {
		t.Fatalf("generic notice missing: %q", notice)
	}
}

// TestFitToolOutputLimiterOverBudgetFallsBack: a limiter that claims to have
// handled the payload but still returns over-budget output is ignored in
// favour of the generic truncation.
func TestFitToolOutputLimiterOverBudgetFallsBack(t *testing.T) {
	big := strings.Repeat("x", 40*1024)
	lm := limiterTool{name: "shapey", out: big, handled: true}
	out, _ := fitToolOutput(lm, big)
	if !strings.Contains(out, "…[truncated") {
		t.Fatal("over-budget limiter output must fall back to the generic marker")
	}
}

// TestFitToolOutputLimiterInvalidUTF8FallsBack: a limiter that splits a rune
// is rejected in favour of the generic (rune-safe) truncation.
func TestFitToolOutputLimiterInvalidUTF8FallsBack(t *testing.T) {
	big := strings.Repeat("x", 40*1024)
	lm := limiterTool{name: "shapey", out: "\xff\xfe", handled: true}
	out, _ := fitToolOutput(lm, big)
	if !strings.Contains(out, "…[truncated") {
		t.Fatal("invalid-UTF-8 limiter output must fall back to the generic marker")
	}
}

// TestFitToolOutputNoLimiterUnchanged keeps tools without the capability on
// the generic path — the pre-existing behaviour must not shift.
func TestFitToolOutputNoLimiterUnchanged(t *testing.T) {
	big := strings.Repeat("x", 40*1024)
	out, notice := fitToolOutput(fakeTool{name: "plain", readOnly: true}, big)
	if !strings.Contains(out, "…[truncated") || !strings.Contains(notice, "truncated") {
		t.Fatal("tool without OutputLimiter must use the generic truncation")
	}
}

// TestExecuteOneUsesOutputLimiter wires the capability through the real
// execution path: the fitted payload lands in the tool outcome.
func TestExecuteOneUsesOutputLimiter(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(limiterTool{name: "shapey", readOnly: true, out: `{"ok":true,"kept":3}`, handled: true})
	a := New(nil, reg, NewSession(""), Options{}, event.Discard)

	out := a.executeOne(context.Background(), provider.ToolCall{Name: "shapey", Arguments: `{}`})
	if out.output != `{"ok":true,"kept":3}` {
		t.Fatalf("executeOne did not use the limiter output: %q", out.output)
	}
	if !out.truncated || out.truncMsg == "" {
		t.Fatal("executeOne must report the limiter truncation")
	}
}
