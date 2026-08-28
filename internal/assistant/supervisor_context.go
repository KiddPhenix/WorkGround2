package assistant

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// SupervisorSessionSummary is the bounded view of one session injected into the
// supervisor's implicit context. Status is the derived lifecycle as a string so
// the Session subsystem stays the single source of truth while this package
// stays decoupled from the agent package.
type SupervisorSessionSummary struct {
	ID           string
	Title        string
	Status       string
	Purpose      string
	LastActivity string
	Preview      string
}

// SupervisorContextInput is the bounded input to the supervisor turn's implicit
// context. It deliberately excludes full history, full tool output, and large
// failure logs: the supervisor progressively loads those through read-only
// tools instead. Every authoritative part carries its revision/source so a
// decision is traceable to the exact state it observed; nothing here depends on
// UI-private state (the viewport is the only UI input, and it is a short-TTL
// observation of visible/selected Session IDs only).
type SupervisorContextInput struct {
	Assistant        Assistant
	Plan             Plan
	RunningSessions  []SupervisorSessionSummary
	RecentSessions   []SupervisorSessionSummary
	FailedSessions   []SupervisorSessionSummary
	PendingAttention int
	// Memory is the assistant's durable memory (facts/strategy/learning/metrics)
	// with its revision, bounded to a summary view by the renderer.
	Memory Memory
	// WorkControl is the global work gate (state/epoch/revision), so the
	// supervisor knows whether work is paused or recovering.
	WorkControl WorkControl
	// Routines are the durable scheduled tasks with their revisions, plus
	// RetryDue as the count of due retry timers.
	Routines []Routine
	RetryDue int
	// ProjectConstraints is the bounded summary of the authoritative project
	// constraints with its revision, supplied by the host (the Assistant never
	// keeps an overriding copy in Memory).
	ProjectConstraints         string
	ProjectConstraintsRevision int64
	// PendingEvents are the unconsumed durable wake-up reasons for this turn.
	PendingEvents []SupervisorEvent
	Viewport      ViewportSnapshot
	Now           time.Time
}

// BuildSupervisorContext renders the bounded implicit context injected into
// every supervisor reasoning turn. It mirrors design section 6: mission, mode,
// policy summary, executable/waiting/review responsibilities, running and recent
// sessions, failures, pending interactions, workspace, and the current viewport.
func BuildSupervisorContext(in SupervisorContextInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "使命：%s\n", strings.TrimSpace(in.Assistant.Mission))
	fmt.Fprintf(&b, "模式：%s", modeLabel(in.Assistant.Mode))
	fmt.Fprintf(&b, "（revision %d）", in.Plan.Revision)
	fmt.Fprintf(&b, "，作用域：%s", in.Assistant.Scope)
	if strings.TrimSpace(in.Assistant.WorkspaceRoot) != "" {
		fmt.Fprintf(&b, "（%s）", strings.TrimSpace(in.Assistant.WorkspaceRoot))
	}
	b.WriteString("\n")
	writePolicySummary(&b, in.Assistant.Policy)
	b.WriteString("\n")

	executable, waiting, review := planBuckets(in.Plan)
	if len(executable)+len(waiting)+len(review) > 0 {
		fmt.Fprintf(&b, "当前计划（revision %d）：\n", in.Plan.Revision)
		for _, r := range executable {
			fmt.Fprintf(&b, "- 可执行 [%s] %s\n", aliasOrID(r), strings.TrimSpace(r.Objective))
		}
		for _, r := range waiting {
			fmt.Fprintf(&b, "- 等待 [%s] %s（依赖：%s）\n", aliasOrID(r), strings.TrimSpace(r.Objective), strings.Join(r.DependsOn, ", "))
		}
		for _, r := range review {
			fmt.Fprintf(&b, "- 待验证 [%s] %s\n", aliasOrID(r), strings.TrimSpace(r.Objective))
		}
		b.WriteString("\n")
	}

	if len(in.RunningSessions) > 0 {
		b.WriteString("运行中的受管 Session：\n")
		for _, s := range in.RunningSessions {
			fmt.Fprintf(&b, "- %s [%s] %s\n", s.ID, s.Status, s.Title)
		}
		b.WriteString("\n")
	}
	if len(in.RecentSessions) > 0 {
		b.WriteString("最近相关 Session：\n")
		for _, s := range in.RecentSessions {
			fmt.Fprintf(&b, "- %s [%s] %s\n", s.ID, s.Status, s.Title)
		}
		b.WriteString("\n")
	}
	if len(in.FailedSessions) > 0 {
		b.WriteString("最近失败的 Session：\n")
		for _, s := range in.FailedSessions {
			fmt.Fprintf(&b, "- %s [%s] %s\n", s.ID, s.Status, s.Title)
		}
		b.WriteString("\n")
	}
	if in.PendingAttention > 0 {
		fmt.Fprintf(&b, "待回答问题/审批：%d\n\n", in.PendingAttention)
	}
	if wc := in.WorkControl; wc.State != "" {
		fmt.Fprintf(&b, "全局工作控制：%s（epoch %d，revision %d）\n",
			wc.State, wc.Epoch, wc.Revision)
		if wc.RestartIntent != RestartIntentNone {
			fmt.Fprintf(&b, "重启意图：%s\n", wc.RestartIntent)
		}
		b.WriteString("\n")
	}
	if len(in.Routines) > 0 {
		fmt.Fprintf(&b, "定时任务（%d 条，%d 条重试到期）：\n", len(in.Routines), in.RetryDue)
		for _, rt := range in.Routines {
			enabled := "启用"
			if !rt.Enabled {
				enabled = "停用"
			}
			fmt.Fprintf(&b, "- %s [%s] %s（revision %d）\n", rt.ID, enabled, rt.Title, rt.Revision)
		}
		b.WriteString("\n")
	}
	if strings.TrimSpace(in.ProjectConstraints) != "" {
		fmt.Fprintf(&b, "项目约束（revision %d）：%s\n\n", in.ProjectConstraintsRevision, strings.TrimSpace(in.ProjectConstraints))
	}
	memItems := in.Memory.Items
	if len(memItems) > 0 {
		b.WriteString(fmt.Sprintf("记忆（revision %d，共 %d 条，摘要）：\n", in.Memory.Revision, len(memItems)))
		for _, m := range memItems {
			fmt.Fprintf(&b, "- [%s] %s\n", m.Kind, truncateString(m.Body, 160))
		}
		b.WriteString("\n")
	}
	if len(in.PendingEvents) > 0 {
		b.WriteString("待处理事件（合并后）：\n")
		for _, ev := range in.PendingEvents {
			target := ev.SessionID
			if target == "" {
				target = orDash(ev.RequestID)
			}
			fmt.Fprintf(&b, "- %s %s（revision %d）\n", ev.Kind, target, ev.Revision)
		}
		b.WriteString("\n")
	}
	if in.Viewport.WindowID != "" {
		fmt.Fprintf(&b, "用户当前视窗：window=%s selected=%s visible=%s\n\n",
			in.Viewport.WindowID, orDash(in.Viewport.SelectedSessionID), strings.Join(in.Viewport.VisibleSessionIDs, ", "))
	}
	fmt.Fprintf(&b, "当前时间：%s\n", in.Now.UTC().Format(time.RFC3339))
	return strings.TrimRight(b.String(), "\n")
}

func planBuckets(plan Plan) (executable, waiting, review []Responsibility) {
	for _, r := range plan.Responsibilities {
		switch r.Status {
		case RespReady, RespActive:
			executable = append(executable, r)
		case RespBlocked:
			waiting = append(waiting, r)
		default:
			if r.Disposition == DispositionReview {
				review = append(review, r)
			}
		}
	}
	sort.Slice(executable, func(i, j int) bool { return executable[i].ID < executable[j].ID })
	return executable, waiting, review
}

func writePolicySummary(b *strings.Builder, p Policy) {
	parts := make([]string, 0, 4)
	if p.LocalWrite == AccessAllow {
		parts = append(parts, "本地写入")
	}
	if p.Network == AccessAllow {
		parts = append(parts, "网络")
	}
	if p.Publish == AccessAllow {
		parts = append(parts, "对外发言")
	}
	if len(parts) == 0 {
		b.WriteString("权限：最小化（仅只读/需逐次审批）")
		return
	}
	b.WriteString("权限：")
	b.WriteString(strings.Join(parts, "、"))
}

func aliasOrID(r Responsibility) string {
	if strings.TrimSpace(r.Alias) != "" {
		return r.Alias
	}
	return r.ID
}

// modeLabel renders the operating mode in the supervisor's language.
func modeLabel(m AssistantMode) string {
	switch m {
	case ModeFinite:
		return "finite（完成当前批次后停止/维护）"
	case ModeContinuous:
		return "continuous（完成后自动发现下一批）"
	case "":
		return "continuous（旧数据默认）"
	default:
		return string(m)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
