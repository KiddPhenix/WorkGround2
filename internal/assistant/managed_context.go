package assistant

import (
	"fmt"
	"sort"
	"strings"
)

const (
	managedPromptMaxBytes = 24 * 1024
	managedTaskMaxBytes   = 8 * 1024
	managedMissionBytes   = 2 * 1024
	managedIdentityBytes  = 512
	managedWorkspaceBytes = 2 * 1024
	managedItemMaxBytes   = 1200
	managedPlanItems      = 8
	managedMemoryItems    = 8
	managedContextPacks   = 4
	managedArtifacts      = 6
	managedPromptIntro    = "你正在执行一个长期 Assistant 委派的受管 Session。以下上下文在创建时冻结，仅作定位线索；开工后必须通过工具核对当前状态。"
	managedTaskSection    = "\n原始任务：\n"
	managedContractStart  = "\n\n执行契约：\n1. 工作区任务先调用 project_status 和 project_constraints_get"
)

// ManagedSessionPrompt builds the bounded execution envelope passed to every
// Assistant-managed Session. The snapshot is a creation-time context hint, not
// a second source of truth: the Session must re-read current project, memory and
// Session state through tools before acting.
func ManagedSessionPrompt(snapshot Snapshot, task string) string {
	var b strings.Builder
	b.WriteString(managedPromptIntro + "\n\n")
	fmt.Fprintf(&b, "Assistant：%s（%s）\n使命：%s\n范围：%s\n",
		truncateRoleText(strings.TrimSpace(snapshot.Assistant.Name), managedIdentityBytes),
		truncateRoleText(strings.TrimSpace(snapshot.Assistant.ID), managedIdentityBytes),
		truncateRoleText(strings.TrimSpace(snapshot.Assistant.Mission), managedMissionBytes),
		snapshot.Assistant.Scope,
	)
	if workspace := strings.TrimSpace(snapshot.Assistant.WorkspaceRoot); workspace != "" {
		fmt.Fprintf(&b, "工作区：%s\n", truncateRoleText(workspace, managedWorkspaceBytes))
	}
	fmt.Fprintf(&b, "上下文版本：assistant=%d memory=%d plan=%d aggregate=%d\n\n", snapshot.Assistant.Revision, snapshot.Memory.Revision, snapshot.Plan.Revision, snapshot.Revision)
	b.WriteString(strings.TrimPrefix(managedTaskSection, "\n"))
	b.WriteString(truncateRoleText(strings.TrimSpace(task), managedTaskMaxBytes))
	b.WriteString("\n\n执行契约：\n")
	b.WriteString("1. 工作区任务先调用 project_status 和 project_constraints_get，再用 ls/glob/grep/read_file 实际检查相关目录与文件；不得凭标题、常识或旧摘要猜测。\n")
	b.WriteString("2. 决策前用任务关键词调用 memory_search。需要复用既有成果时，使用 session_list/session_status/session_read 找到来源 Session，并核对成果路径与证据。\n")
	b.WriteString("3. 用户明确要求长期保留的约束或偏好、跨任务可复用的事实或策略、未闭环事项、成果位置与证据，使用 memory_remember 幂等写入；携带稳定 request_id、source 和 evidence。修订冲突时重新查询后安全重试。临时日志、猜测和整段转录不得进入长期记忆。\n")
	b.WriteString("4. 最终答复必须给出结果、验证证据和产物位置；失败要显式说明可重试条件。涉及责任进度时，继续用 <assistant-progress> 协议回写计划。\n\n")
	b.WriteString("创建时上下文快照：\n")

	w := managedPromptWriter{b: &b, remaining: managedPromptMaxBytes - b.Len()}
	w.writePlan(snapshot.Plan.Responsibilities)
	w.writeMemory(snapshot.Memory.Items)
	w.writePacks(snapshot.ContextPacks)
	w.writeArtifacts(snapshot.Artifacts)
	return b.String()
}

// ManagedSessionIntent recovers the raw task from the legacy managed-context
// envelope written before IntentPrompt was stored as the transcript display.
// It deliberately accepts only the exact generated prelude and contract start,
// so ordinary user messages containing a similar heading remain untouched.
func ManagedSessionIntent(prompt string) (string, bool) {
	prompt = strings.TrimSpace(prompt)
	if !strings.HasPrefix(prompt, managedPromptIntro+"\n\nAssistant：") {
		return "", false
	}
	start := strings.Index(prompt, managedTaskSection)
	if start < 0 {
		return "", false
	}
	taskAndRest := prompt[start+len(managedTaskSection):]
	end := strings.Index(taskAndRest, managedContractStart)
	if end < 0 {
		return "", false
	}
	intent := strings.TrimSpace(taskAndRest[:end])
	return intent, intent != ""
}

type managedPromptWriter struct {
	b         *strings.Builder
	remaining int
}

func (w *managedPromptWriter) write(text string) bool {
	if w.remaining <= 0 || text == "" {
		return false
	}
	text = truncateRoleText(text, w.remaining)
	if text == "" {
		return false
	}
	w.b.WriteString(text)
	w.remaining -= len(text)
	return true
}

func (w *managedPromptWriter) writePlan(items []Responsibility) {
	if len(items) == 0 || !w.write("\n责任图：\n") {
		return
	}
	for i, item := range items {
		if i >= managedPlanItems {
			break
		}
		line := fmt.Sprintf("- [%s/%s] %s；完成标准：%s；下一步：%s；依赖：%s\n", item.Alias, item.Status, item.Objective, item.DoneCriteria, item.NextAction, strings.Join(item.DependsOn, ","))
		if !w.write(truncateRoleText(line, managedItemMaxBytes)) {
			return
		}
	}
}

func (w *managedPromptWriter) writeMemory(items []MemoryItem) {
	if len(items) == 0 {
		return
	}
	items = append([]MemoryItem(nil), items...)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Locked != items[j].Locked {
			return items[i].Locked
		}
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		}
		return items[i].ID < items[j].ID
	})
	if !w.write("\n长期记忆：\n") {
		return
	}
	for i, item := range items {
		if i >= managedMemoryItems {
			break
		}
		line := fmt.Sprintf("- [%s/%s] %s；来源：%s；证据：%s\n", item.Kind, item.ID, item.Body, item.SourceRun, item.Evidence)
		if !w.write(truncateRoleText(line, managedItemMaxBytes)) {
			return
		}
	}
}

func (w *managedPromptWriter) writePacks(items []ContextPack) {
	if len(items) == 0 {
		return
	}
	items = append([]ContextPack(nil), items...)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Revision != items[j].Revision {
			return items[i].Revision > items[j].Revision
		}
		return items[i].ID < items[j].ID
	})
	if !w.write("\n近期执行结论：\n") {
		return
	}
	for i, item := range items {
		if i >= managedContextPacks {
			break
		}
		line := fmt.Sprintf("- [dispatch=%s/rev=%d] %s；证据：%s；未闭环：%s\n", item.DispatchID, item.Revision, item.Conclusion, strings.Join(item.Evidence, "；"), strings.Join(item.OpenLoops, "；"))
		if !w.write(truncateRoleText(line, managedItemMaxBytes)) {
			return
		}
	}
}

func (w *managedPromptWriter) writeArtifacts(items []Artifact) {
	if len(items) == 0 {
		return
	}
	items = append([]Artifact(nil), items...)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Revision != items[j].Revision {
			return items[i].Revision > items[j].Revision
		}
		return items[i].ID < items[j].ID
	})
	if !w.write("\n已有成果：\n") {
		return
	}
	for i, item := range items {
		if i >= managedArtifacts {
			break
		}
		line := fmt.Sprintf("- [%s/%s] %s；内容或位置：%s；证据：%s\n", item.Kind, item.ID, item.Title, item.Content, item.Evidence)
		if !w.write(truncateRoleText(line, managedItemMaxBytes)) {
			return
		}
	}
}
