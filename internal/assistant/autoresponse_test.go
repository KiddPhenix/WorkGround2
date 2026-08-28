package assistant

import (
	"testing"
	"time"
)

func TestClassifyHardGate(t *testing.T) {
	cases := []struct {
		name   string
		action string
		prompt string
		policy Policy
		want   HardGateReason
		gate   bool
	}{
		{
			name:   "ordinary question is not a hard gate",
			prompt: "which channel should I post to next?",
			policy: DefaultPolicy(),
			gate:   false,
		},
		{
			name:   "payment with approve is a funds gate",
			prompt: "确认付款 99 元订阅高级版",
			policy: Policy{Payment: AccessApprove},
			want:   HardGateFundsLegalIdentity,
			gate:   true,
		},
		{
			name:   "secrets with approve is a credentials gate",
			prompt: "请输入 API 密码以便登录",
			policy: Policy{Secrets: AccessApprove},
			want:   HardGateCredentials,
			gate:   true,
		},
		{
			name:   "destructive with approve is an irreversible gate",
			prompt: "永久删除所有旧渠道数据",
			policy: Policy{Delete: AccessApprove},
			want:   HardGateIrreversible,
			gate:   true,
		},
		{
			name:   "user demands confirmation",
			prompt: "这类操作必须总是问我再执行",
			policy: DefaultPolicy(),
			want:   HardGateUserRequiresConfirm,
			gate:   true,
		},
		{
			name:   "payment without approve is not auto-blocked by keyword",
			prompt: "确认付款 99 元",
			policy: DefaultPolicy(), // Payment = AccessApprove by default, but this is the keyword fallback path
			want:   HardGateFundsLegalIdentity,
			gate:   true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, gate := ClassifyHardGate(c.action, c.prompt, c.policy)
			if gate != c.gate || (c.gate && reason != c.want) {
				t.Fatalf("ClassifyHardGate = %q, %v; want %q, %v", reason, gate, c.want, c.gate)
			}
		})
	}
}

func TestAutoAnswerDueAt(t *testing.T) {
	now := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	due := AutoAnswerDueAt(now)
	if due.Before(now) || due.Sub(now) <= 0 {
		t.Fatalf("due = %v, want strictly after %v", due, now)
	}
	// Idempotent deadline: same input yields the same due time.
	if got := AutoAnswerDueAt(now); !got.Equal(due) {
		t.Fatalf("deadline not deterministic: %v vs %v", got, due)
	}
}

func TestRouteInteractionDecisionOrder(t *testing.T) {
	now := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   RouteInteractionInput
		want DecisionSource
		gate HardGateReason
	}{
		{
			name: "hard gate waits for user",
			in:   RouteInteractionInput{Action: "", Prompt: "确认付款 99 元", Policy: Policy{Payment: AccessApprove}, Now: now},
			want: DecisionUser, gate: HardGateFundsLegalIdentity,
		},
		{
			name: "confident reversible is inferred",
			in:   RouteInteractionInput{Prompt: "选哪个渠道", Policy: DefaultPolicy(), Confidence: 0.9, Candidates: []string{"A"}, Reversible: true, Now: now},
			want: DecisionInfer,
		},
		{
			name: "low-confidence reversible with isolation is an experiment",
			in:   RouteInteractionInput{Prompt: "选哪个渠道", Policy: DefaultPolicy(), Confidence: 0.5, Candidates: []string{"A", "B"}, Reversible: true, CanIsolate: true, Now: now},
			want: DecisionExperiment,
		},
		{
			name: "low-confidence reversible without isolation infers most reversible",
			in:   RouteInteractionInput{Prompt: "选哪个渠道", Policy: DefaultPolicy(), Confidence: 0.5, Candidates: []string{"A"}, Reversible: true, Now: now},
			want: DecisionInfer,
		},
		{
			name: "low-confidence irreversible waits for user",
			in:   RouteInteractionInput{Prompt: "覆盖生产配置", Policy: DefaultPolicy(), Confidence: 0.5, Reversible: false, Now: now},
			want: DecisionUser, gate: HardGateIrreversible,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RouteInteraction(c.in)
			if got.Source != c.want {
				t.Fatalf("Source = %q, want %q (%+v)", got.Source, c.want, got)
			}
			if c.gate != "" && got.HardGate != c.gate {
				t.Fatalf("HardGate = %q, want %q", got.HardGate, c.gate)
			}
		})
	}
}
