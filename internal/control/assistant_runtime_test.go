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
