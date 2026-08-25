package control

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/event"
	"workground2/internal/permission"
)

func TestTrySubmitUserTurnAcceptsOneConcurrentCaller(t *testing.T) {
	session := agent.NewSession("sys")
	release := make(chan struct{})
	events := make(chan event.Event, 4)
	c := New(Options{
		Runner: blockingRunner{session: session, release: release},
		Sink:   event.FuncSink(func(e event.Event) { events <- e }),
	})

	const callers = 100
	start := make(chan struct{})
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if c.TrySubmitUserTurn("assistant task", "assistant task") {
				accepted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := accepted.Load(); got != 1 {
		t.Fatalf("accepted=%d, want 1", got)
	}
	close(release)
	select {
	case e := <-events:
		if e.Kind != event.TurnDone || e.Err != nil {
			t.Fatalf("turn completion=%+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("accepted turn did not finish")
	}
}

func TestSetPermissionPolicyRefreshesLiveGate(t *testing.T) {
	approvals := make(chan event.Approval, 2)
	c := New(Options{
		Policy: permission.New("ask", []string{"write_file"}, nil, nil),
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.ApprovalRequest {
				approvals <- e.Approval
			}
		}),
	})
	c.EnableInteractiveApproval()
	args := json.RawMessage(`{"path":"release.txt"}`)

	allow, _, err := c.permissionGate.Check(context.Background(), "write_file", args, false)
	if err != nil || !allow {
		t.Fatalf("initial allow=(%v,%v)", allow, err)
	}

	c.SetToolApprovalMode(ToolApprovalYolo)
	c.SetPermissionPolicy(permission.New("allow", nil, nil, []string{"write_file"}))
	allow, _, err = c.permissionGate.Check(context.Background(), "write_file", args, false)
	if err != nil || allow {
		t.Fatalf("deny under yolo=(%v,%v), want deny precedence", allow, err)
	}

	c.SetToolApprovalMode(ToolApprovalAsk)
	c.approval.grantSession("write_file", "release.txt")
	c.SetPermissionPolicy(permission.New("ask", nil, nil, nil))
	result := make(chan bool, 1)
	go func() {
		allowed, _, _ := c.permissionGate.Check(context.Background(), "write_file", args, false)
		result <- allowed
	}()
	select {
	case approval := <-approvals:
		c.Approve(approval.ID, false, false, false)
	case <-time.After(2 * time.Second):
		t.Fatal("ask policy reused a stale session grant")
	}
	if <-result {
		t.Fatal("declined ask was allowed")
	}
}

func TestSetPermissionPolicyConcurrentGateReads(t *testing.T) {
	c := New(Options{Policy: permission.New("allow", nil, nil, nil)})
	args := json.RawMessage(`{"path":"state.json"}`)
	allowPolicy := permission.New("allow", nil, nil, nil)
	denyPolicy := permission.New("allow", nil, nil, []string{"write_file"})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				c.SetPermissionPolicy(allowPolicy)
			} else {
				c.SetPermissionPolicy(denyPolicy)
			}
		}(i)
		go func() {
			defer wg.Done()
			_, _, _ = c.permissionGate.Check(context.Background(), "write_file", args, false)
		}()
	}
	wg.Wait()
}

func TestTrySubmitUserTurnWithPolicyBusyDoesNotMutateRuntime(t *testing.T) {
	session := agent.NewSession("sys")
	release := make(chan struct{})
	c := New(Options{
		Runner: blockingRunner{session: session, release: release},
		Policy: permission.New("ask", []string{"write_file"}, nil, nil),
	})
	c.EnableInteractiveApproval()
	c.SetToolApprovalMode(ToolApprovalAsk)
	c.approval.grantSession("write_file", "release.txt")
	if !c.TrySubmitUserTurn("user turn", "user turn") {
		t.Fatal("initial turn was not accepted")
	}

	accepted := c.TrySubmitUserTurnWithPolicy(
		"assistant turn", "assistant turn",
		permission.New("allow", nil, nil, []string{"write_file"}), ToolApprovalYolo,
		ToolGrant{Tool: "bash", Subject: "go test ./..."},
	)
	if accepted {
		t.Fatal("busy controller accepted assistant turn")
	}
	if got := c.ToolApprovalMode(); got != ToolApprovalAsk {
		t.Fatalf("busy submit changed tool mode to %q", got)
	}
	if decision := c.permissionPolicy().DecideSubject("write_file", false, "release.txt"); decision != permission.Allow {
		t.Fatalf("busy submit changed policy decision to %s", decision)
	}
	if !c.approval.preApproved("write_file", "release.txt") {
		t.Fatal("busy submit cleared existing session grant")
	}
	if c.approval.preApproved("bash", "go test ./...") {
		t.Fatal("busy submit installed one-shot grant")
	}
	close(release)
}

type permissionCheckingRunner struct {
	check  func() (bool, error)
	result chan permissionCheckResult
}

type permissionCheckResult struct {
	allow bool
	err   error
}

func (r permissionCheckingRunner) Run(context.Context, string) error {
	allow, err := r.check()
	r.result <- permissionCheckResult{allow: allow, err: err}
	return err
}

func TestTrySubmitUserTurnWithPolicyInstallsGateBeforeRun(t *testing.T) {
	result := make(chan permissionCheckResult, 1)
	var c *Controller
	runner := permissionCheckingRunner{
		result: result,
		check: func() (bool, error) {
			allow, _, err := c.permissionGate.Check(context.Background(), "write_file", json.RawMessage(`{"path":"release.txt"}`), false)
			return allow, err
		},
	}
	c = New(Options{Runner: runner, Policy: permission.New("allow", nil, nil, nil)})
	c.EnableInteractiveApproval()
	c.approval.grantSession("write_file", "release.txt")
	if !c.TrySubmitUserTurnWithPolicy(
		"assistant turn", "assistant turn",
		permission.New("allow", nil, nil, []string{"write_file"}), ToolApprovalAsk,
	) {
		t.Fatal("idle controller rejected assistant turn")
	}
	select {
	case got := <-result:
		if got.err != nil || got.allow {
			t.Fatalf("runner observed gate result allow=%v err=%v, want deny", got.allow, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not observe installed gate")
	}
	if c.approval.preApproved("write_file", "release.txt") {
		t.Fatal("accepted submit retained old session grant")
	}
}

func TestTrySubmitUserTurnWithPolicyRespectsExplicitMemoryAllow(t *testing.T) {
	for _, toolName := range []string{"remember", "forget"} {
		t.Run(toolName, func(t *testing.T) {
			result := make(chan permissionCheckResult, 1)
			approvals := make(chan event.Approval, 1)
			var c *Controller
			runner := permissionCheckingRunner{
				result: result,
				check: func() (bool, error) {
					allow, _, err := c.permissionGate.Check(context.Background(), toolName, json.RawMessage(`{"name":"assistant-note"}`), false)
					return allow, err
				},
			}
			c = New(Options{
				Runner: runner,
				Sink: event.FuncSink(func(e event.Event) {
					if e.Kind == event.ApprovalRequest {
						approvals <- e.Approval
					}
				}),
			})
			c.EnableInteractiveApproval()
			policy := permission.New("ask", []string{"memory", "remember", "forget"}, nil, nil)
			if !c.TrySubmitUserTurnWithPolicy("assistant turn", "assistant turn", policy, ToolApprovalAuto) {
				t.Fatal("idle controller rejected assistant turn")
			}
			select {
			case approval := <-approvals:
				c.Approve(approval.ID, false, false, false)
				t.Fatalf("explicitly allowed %s emitted approval: %+v", toolName, approval)
			case got := <-result:
				if got.err != nil || !got.allow {
					t.Fatalf("%s gate result allow=%v err=%v, want allow", toolName, got.allow, got.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s gate did not finish", toolName)
			}
		})
	}
}

func TestTrySubmitUserTurnWithPolicyUsesExactOneShotGrant(t *testing.T) {
	session := agent.NewSession("sys")
	release := make(chan struct{})
	c := New(Options{Runner: blockingRunner{session: session, release: release}})
	c.EnableInteractiveApproval()
	policy := permission.New("ask", nil, nil, nil)
	if !c.TrySubmitUserTurnWithPolicy(
		"assistant turn", "assistant turn", policy, ToolApprovalAsk,
		ToolGrant{Tool: "write_file", Subject: "release.txt"},
		ToolGrant{Tool: "write_file", Subject: "next-turn.txt"},
		ToolGrant{Tool: "report.publish", Subject: "release:42"},
		ToolGrant{Tool: "refresh_status"},
	) {
		t.Fatal("idle controller rejected assistant turn")
	}
	if !c.approval.preApproved("write_file", "release.txt") {
		t.Fatal("guardian peek did not observe exact one-shot grant")
	}
	if c.approval.preApproved("write_file", "other.txt") {
		t.Fatal("one-shot grant expanded to another subject")
	}
	if !c.approval.preApprovedForDecision("write_file", "release.txt", false, nil) {
		t.Fatal("exact one-shot grant did not approve first tool decision")
	}
	if c.approval.preApprovedForDecision("write_file", "release.txt", false, nil) {
		t.Fatal("one-shot grant approved the same tool decision twice")
	}
	if !c.approval.preApprovedForDecision("report.publish", "release:42", true, nil) {
		t.Fatal("exact one-shot grant did not approve a fresh decision")
	}
	if c.approval.preApprovedForDecision("report.publish", "release:42", true, nil) {
		t.Fatal("fresh decision reused a consumed one-shot grant")
	}
	if !c.approval.preApprovedForDecision("refresh_status", "", false, nil) {
		t.Fatal("empty-subject one-shot grant was not consumed")
	}
	if c.approval.preApprovedForDecision("refresh_status", "", false, nil) {
		t.Fatal("empty-subject one-shot grant approved twice")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for c.Running() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.Running() {
		t.Fatal("first turn did not finish")
	}

	if !c.TrySubmitUserTurnWithPolicy("next turn", "next turn", policy, ToolApprovalAsk) {
		t.Fatal("next turn was not accepted")
	}
	if c.approval.preApproved("write_file", "next-turn.txt") {
		t.Fatal("next turn retained an unused one-shot grant")
	}
}
