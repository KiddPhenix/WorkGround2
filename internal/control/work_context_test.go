package control

import (
	"strings"
	"testing"
	"unicode/utf8"

	"workground2/internal/agent"
	"workground2/internal/event"
	"workground2/internal/work"
)

func TestBuildWorkChatContext_NilView(t *testing.T) {
	if got := BuildWorkChatContext(nil); got != "" {
		t.Errorf("expected empty for nil view, got %q", got)
	}
}

func TestBuildWorkChatContext_NilWork(t *testing.T) {
	view := &work.WorkView{}
	if got := BuildWorkChatContext(view); got != "" {
		t.Errorf("expected empty for view with nil Work, got %q", got)
	}
}

func TestBuildWorkChatContext_BasicIdentity(t *testing.T) {
	view := &work.WorkView{
		Revision: 7,
		Work: &work.Work{
			ID:                "work-abc",
			Name:              "Fix login bug",
			State:             work.WorkRunning,
			Prompt:            "Investigate and fix the login timeout issue",
			V2CurrentRevision: 3,
		},
	}
	ctx := BuildWorkChatContext(view)
	checks := []string{
		"work-abc",
		"Fix login bug",
		string(work.WorkRunning),
		"Investigate and fix the login timeout issue",
	}
	for _, c := range checks {
		if !strings.Contains(ctx, c) {
			t.Errorf("context missing %q", c)
		}
	}
	if !strings.Contains(ctx, "7") {
		t.Errorf("context missing revision 7")
	}
	if !strings.Contains(ctx, "3") {
		t.Errorf("context missing definition rev 3")
	}
}

func TestBuildWorkChatContext_DefinitionNodes(t *testing.T) {
	view := &work.WorkView{
		Revision: 1,
		Work: &work.Work{
			ID:    "work-def",
			Name:  "Test definition",
			State: work.WorkRunning,
		},
		Definition: &work.WorkDefinitionRevision{
			Goal:   "Build a REST API endpoint",
			Status: work.DefActive,
			Nodes: []work.NodeDef{
				{ID: "node-1", Title: "Design schema", Description: "Design the database schema"},
				{ID: "node-2", Title: "Implement handler"},
			},
		},
	}
	ctx := BuildWorkChatContext(view)
	if !strings.Contains(ctx, "Build a REST API endpoint") {
		t.Errorf("context missing goal")
	}
	if !strings.Contains(ctx, "node-1") {
		t.Errorf("context missing node-1")
	}
	if !strings.Contains(ctx, "Design schema") {
		t.Errorf("context missing node-1 title")
	}
	if !strings.Contains(ctx, "node-2") {
		t.Errorf("context missing node-2")
	}
}

func TestBuildWorkChatContext_FailedTask(t *testing.T) {
	view := &work.WorkView{
		Revision: 1,
		Work: &work.Work{
			ID:    "work-err",
			Name:  "Test errors",
			State: work.WorkFailed,
		},
		Tasks: []work.TaskV2View{
			{
				ID:    "task-1",
				Title: "Compile code",
				State: work.TaskFailedRetryable,
				Error: "syntax error in main.go:42",
			},
		},
	}
	ctx := BuildWorkChatContext(view)
	if !strings.Contains(ctx, "task-1") {
		t.Errorf("context missing task-1")
	}
	if !strings.Contains(ctx, "syntax error in main.go:42") {
		t.Errorf("context missing error detail")
	}
	if !strings.Contains(ctx, string(work.TaskFailedRetryable)) {
		t.Errorf("context missing failed state")
	}
}

func TestBuildWorkChatContext_WaitingTask(t *testing.T) {
	view := &work.WorkView{
		Revision: 1,
		Work: &work.Work{
			ID:    "work-wait",
			Name:  "Test waiting",
			State: work.WorkWaitingUser,
		},
		Tasks: []work.TaskV2View{
			{
				ID:              "task-wait",
				Title:           "Needs approval",
				State:           work.TaskWaitingInput,
				WaitingInputIDs: []string{"input-1", "input-2"},
			},
		},
	}
	ctx := BuildWorkChatContext(view)
	if !strings.Contains(ctx, "task-wait") {
		t.Errorf("context missing waiting task")
	}
	if !strings.Contains(ctx, "input-1") {
		t.Errorf("context missing waiting input IDs")
	}
}

func TestBuildWorkChatContext_V2TaskRuntimes(t *testing.T) {
	view := &work.WorkView{
		Revision: 1,
		Work: &work.Work{
			ID:    "work-v2",
			Name:  "Test V2 runtimes",
			State: work.WorkRunning,
			V2TaskRuntimes: map[string]*work.V2TaskRuntime{
				"rt-1": {
					TaskID: "rt-1",
					State:  work.TaskFailedRetryable,
					Error:  "connection refused",
					Attempts: []work.V2Attempt{
						{Index: 1, State: work.TaskFailedRetryable, Error: "timeout after 30s"},
					},
				},
				"rt-2": {
					TaskID: "rt-2",
					State:  work.TaskCompleted,
				},
			},
		},
	}
	ctx := BuildWorkChatContext(view)
	if !strings.Contains(ctx, "rt-1") {
		t.Errorf("context missing runtime rt-1")
	}
	if !strings.Contains(ctx, "connection refused") {
		t.Errorf("context missing runtime error")
	}
	if !strings.Contains(ctx, "rt-2") {
		t.Errorf("context missing completed runtime")
	}
	if !strings.Contains(ctx, "Latest attempt error") {
		t.Errorf("context missing attempt error")
	}
}

func TestBuildWorkChatContext_Inputs(t *testing.T) {
	view := &work.WorkView{
		Revision: 1,
		Work: &work.Work{
			ID:    "work-in",
			Name:  "Test inputs",
			State: work.WorkRunning,
		},
		Inputs: []work.WorkInput{
			{ID: "input-1", State: work.InputSubmitted, Value: []byte(`"user-provided-text"`)},
			{ID: "input-2", State: work.InputRequested},
		},
	}
	ctx := BuildWorkChatContext(view)
	if !strings.Contains(ctx, "input-1") {
		t.Errorf("context missing input-1")
	}
	if !strings.Contains(ctx, "input-2") {
		t.Errorf("context missing input-2")
	}
	if !strings.Contains(ctx, string(work.InputSubmitted)) {
		t.Errorf("context missing submitted state")
	}
	if !strings.Contains(ctx, string(work.InputRequested)) {
		t.Errorf("context missing requested state")
	}
}

func TestBuildWorkChatContext_RunBlock(t *testing.T) {
	view := &work.WorkView{
		Revision: 1,
		Work: &work.Work{
			ID:    "work-block",
			Name:  "Test blocked",
			State: work.WorkDraft,
		},
		RunBlock: &work.RunBlockReason{
			Blocked: true,
			Items: []work.RunBlockItem{
				{Code: work.RunBlockBlobMissing, Detail: "Missing configuration file"},
			},
		},
	}
	ctx := BuildWorkChatContext(view)
	if !strings.Contains(ctx, string(work.RunBlockBlobMissing)) {
		t.Errorf("context missing run block code")
	}
	if !strings.Contains(ctx, "Missing configuration file") {
		t.Errorf("context missing run block detail")
	}
}

func TestBuildWorkChatContext_Truncation(t *testing.T) {
	nodes := make([]work.NodeDef, 40)
	for i := range nodes {
		nodes[i] = work.NodeDef{
			ID:          "node-" + strings.Repeat("界", 20),
			Title:       "节点",
			Description: strings.Repeat("很长的中文说明", 100),
		}
	}
	view := &work.WorkView{
		Revision: 9,
		Work: &work.Work{
			ID:     "work-large",
			Name:   "大任务",
			State:  work.WorkFailed,
			Prompt: strings.Repeat("任务背景", 1000),
			V2TaskRuntimes: map[string]*work.V2TaskRuntime{
				"task-b": {State: work.TaskCompleted},
				"task-a": {State: work.TaskFailedRetryable, Error: "关键运行错误"},
			},
		},
		Definition: &work.WorkDefinitionRevision{
			Goal:  strings.Repeat("目标", 1000),
			Nodes: nodes,
		},
	}
	first := BuildWorkChatContext(view)
	second := BuildWorkChatContext(view)
	if first != second {
		t.Fatal("context must be deterministic across map iteration")
	}
	if len(first) > workChatContextMaxBytes {
		t.Fatalf("context bytes = %d, want <= %d", len(first), workChatContextMaxBytes)
	}
	if !utf8.ValidString(first) {
		t.Fatal("truncated context is not valid UTF-8")
	}
	if !strings.Contains(first, "关键运行错误") {
		t.Fatal("actionable failure was lost before lower-priority definition details")
	}
	if !strings.Contains(first, "[Context truncated]") {
		t.Fatal("large context did not expose truncation")
	}
}

func TestBuildWorkChatContext_EmptyFieldsSafe(t *testing.T) {
	view := &work.WorkView{
		Revision: 1,
		Work: &work.Work{
			ID:    "work-empty",
			Name:  "",
			State: work.WorkDraft,
		},
	}
	ctx := BuildWorkChatContext(view)
	if !strings.Contains(ctx, "work-empty") {
		t.Errorf("context missing work ID")
	}
	// Empty name should not produce KV line.
	if strings.Contains(ctx, "**Name**:") {
		t.Errorf("context has empty Name KV")
	}
}

func TestSubmitWorkChatInjectsContextButRecordsOriginalDisplay(t *testing.T) {
	session := agent.NewSession("system")
	executor := agent.New(nil, nil, session, agent.Options{}, event.Discard)
	events := make(chan event.Event, 4)
	controller := New(Options{
		AutoPlan: "off",
		Runner:   handoffRunner{session: session},
		Executor: executor,
		Sink: event.FuncSink(func(value event.Event) {
			events <- value
		}),
	})
	var recordedContent, recordedDisplay string
	controller.SetDisplayRecorder(func(content, display string) {
		recordedContent = content
		recordedDisplay = display
	})

	controller.SubmitWorkChat("这个运行错误", "定位当前失败", "## Current Work\n- ID: work-1\n- Error: timeout")
	waitForTurnDone(t, events)

	if recordedDisplay != "这个运行错误" {
		t.Fatalf("display = %q, want original user text", recordedDisplay)
	}
	if strings.Contains(recordedDisplay, "work-1") {
		t.Fatalf("display leaked Work context: %q", recordedDisplay)
	}
	for _, expected := range []string{"<work_context>", "work-1", "timeout", "定位当前失败"} {
		if !strings.Contains(recordedContent, expected) {
			t.Fatalf("model content missing %q:\n%s", expected, recordedContent)
		}
	}
}
