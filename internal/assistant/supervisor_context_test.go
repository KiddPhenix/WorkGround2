package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestBuildSupervisorContextIncludesBoundedSummary(t *testing.T) {
	in := SupervisorContextInput{
		Assistant: Assistant{
			Name: "helper", Mission: "keep the project healthy",
			Scope: ScopeWorkspace, WorkspaceRoot: "C:/proj", Policy: Policy{LocalWrite: AccessAllow, Network: AccessAllow},
		},
		Plan: Plan{Revision: 1, Responsibilities: []Responsibility{
			{ID: "r1", Alias: "scan", Objective: "scan changes", Status: RespReady},
			{ID: "r2", Alias: "fix", Objective: "fix tests", Status: RespBlocked, DependsOn: []string{"r1"}},
		}},
		RunningSessions:  []SupervisorSessionSummary{{ID: "s1", Title: "scan", Status: "running"}},
		FailedSessions:   []SupervisorSessionSummary{{ID: "s2", Title: "deploy", Status: "failed"}},
		PendingAttention: 1,
		Viewport:         ViewportSnapshot{WindowID: "w1", SelectedSessionID: "s1", VisibleSessionIDs: []string{"s1", "s2"}},
		Now:              time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC),
	}
	out := BuildSupervisorContext(in)
	for _, want := range []string{
		"使命：keep the project healthy",
		"权限：本地写入、网络",
		"可执行 [scan]",
		"等待 [fix]",
		"运行中的受管 Session",
		"最近失败的 Session",
		"待回答问题/审批：1",
		"用户当前视窗",
		"2026-08-17T08:00:00Z",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("context missing %q:\n%s", want, out)
		}
	}
}

func TestBuildSupervisorContextEmptyPlan(t *testing.T) {
	in := SupervisorContextInput{
		Assistant: Assistant{Mission: "m", Policy: DefaultPolicy()},
		Plan:      emptyPlan(),
		Now:       time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC),
	}
	out := BuildSupervisorContext(in)
	if strings.Contains(out, "当前计划") {
		t.Fatalf("empty plan should not render a plan section:\n%s", out)
	}
	if !strings.Contains(out, "权限：最小化") {
		t.Fatalf("default policy should render minimal permissions:\n%s", out)
	}
}

func TestBuildSupervisorContextIncludesControlPlaneState(t *testing.T) {
	in := SupervisorContextInput{
		Assistant: Assistant{Name: "helper", Mission: "m", Mode: ModeContinuous, Policy: DefaultPolicy()},
		Plan:      emptyPlan(),
		Memory: Memory{Revision: 7, Items: []MemoryItem{
			{ID: "m1", Kind: MemoryStrategy, Body: "直接发布前先跑一次本地验证", Revision: 1},
		}},
		WorkControl:                WorkControl{State: WorkRunning, Epoch: 2, Revision: 3},
		Routines:                   []Routine{{ID: "rt-1", Title: "每日构建检查", Enabled: true, Revision: 2}},
		RetryDue:                   1,
		ProjectConstraints:         "use tabs; no force push",
		ProjectConstraintsRevision: 5,
		Now:                        time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC),
	}
	out := BuildSupervisorContext(in)
	for _, want := range []string{
		"模式：continuous",
		"全局工作控制：running（epoch 2，revision 3）",
		"定时任务（1 条，1 条重试到期）",
		"每日构建检查",
		"项目约束（revision 5）：use tabs; no force push",
		"记忆（revision 7",
		"本地验证",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("context missing %q:\n%s", want, out)
		}
	}
}
