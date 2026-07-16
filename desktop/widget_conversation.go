package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"workground2/internal/control"
	"workground2/internal/provider"
)

const (
	widgetPromptLimit = 4000
	widgetReadyWait   = 20 * time.Second

	widgetWorkspaceAuto    = "auto"
	widgetWorkspaceGlobal  = "global"
	widgetWorkspaceProject = "project"
)

// WidgetWorkspaceOption is one selectable workspace for the widget dropdown.
type WidgetWorkspaceOption struct {
	Scope string `json:"scope"`          // "auto", "project", "global"
	Name  string `json:"name"`           // display name
	Root  string `json:"root,omitempty"` // project root, set only for scope="project"
}

// WidgetConversationInput starts a normal controller turn without leaving the
// compact surface. RequestID makes a send safe to retry after an IPC failure.
// Workspace selects the target: "auto" (default), "global", or "project:<root>".
type WidgetConversationInput struct {
	Prompt    string `json:"prompt"`
	RequestID string `json:"requestId"`
	Workspace string `json:"workspace,omitempty"`
}

// WidgetConversationResult reports the chosen workspace and an explicit,
// retryable outcome so the widget never has to infer whether a send succeeded.
type WidgetConversationResult struct {
	Status        string         `json:"status"`
	Error         string         `json:"error,omitempty"`
	TabID         string         `json:"tabId,omitempty"`
	WorkspaceRoot string         `json:"workspaceRoot,omitempty"`
	WorkspaceName string         `json:"workspaceName,omitempty"`
	RouteReason   string         `json:"routeReason,omitempty"`
	Snapshot      WidgetSnapshot `json:"snapshot"`
}

type widgetConversationReceipt struct {
	RequestID          string `json:"requestId"`
	PromptHash         string `json:"promptHash"`
	WorkspaceSelection string `json:"workspaceSelection,omitempty"`
	Scope              string `json:"scope"`
	WorkspaceRoot      string `json:"workspaceRoot,omitempty"`
	WorkspaceName      string `json:"workspaceName"`
	RouteReason        string `json:"routeReason"`
	TabID              string `json:"tabId,omitempty"`
	Status             string `json:"status"`
	Error              string `json:"error,omitempty"`
	UpdatedAt          int64  `json:"updatedAt"`
}

type widgetWorkspaceCandidate struct {
	Scope     string
	Root      string
	Name      string
	Aliases   []string
	Topics    []string
	Active    bool
	Transient bool
	Order     int
}

type widgetWorkspaceRoute struct {
	Scope  string
	Root   string
	Name   string
	Reason string
}

// StartWidgetConversation routes, creates and submits one new conversation.
// Only starts are serialized; snapshot polling and existing task actions remain
// responsive while a workspace controller boots.
func (a *App) StartWidgetConversation(input WidgetConversationInput) WidgetConversationResult {
	a.widgetConversationMu.Lock()
	defer a.widgetConversationMu.Unlock()

	prompt := strings.TrimSpace(input.Prompt)
	requestID := strings.TrimSpace(input.RequestID)
	wsSelection := strings.TrimSpace(input.Workspace)
	if wsSelection == "" {
		wsSelection = widgetWorkspaceAuto
	}
	if requestID == "" {
		return a.widgetConversationResult("invalid", errors.New("requestId is required"), widgetConversationReceipt{})
	}
	if prompt == "" {
		return a.widgetConversationResult("invalid", errors.New("请输入对话内容"), widgetConversationReceipt{})
	}
	if len([]rune(prompt)) > widgetPromptLimit {
		return a.widgetConversationResult("invalid", fmt.Errorf("对话内容最多 %d 个字符", widgetPromptLimit), widgetConversationReceipt{})
	}
	promptHash := fmt.Sprintf("%x", sha256.Sum256([]byte(prompt)))

	receipt, found, err := a.widgetConversationReceipt(requestID)
	if err != nil {
		return a.widgetConversationResult("retryable_error", err, receipt)
	}
	receiptSelection := strings.TrimSpace(receipt.WorkspaceSelection)
	if receiptSelection == "" {
		receiptSelection = widgetWorkspaceAuto
	}
	if found && (receipt.PromptHash != promptHash || receiptSelection != wsSelection) {
		return a.widgetConversationResult("invalid", errors.New("同一 requestId 不能发送不同内容或切换工作区"), receipt)
	}
	if found && receipt.Status == "submitted" {
		return a.widgetConversationResult("already_applied", nil, receipt)
	}
	if !found {
		candidates := a.widgetWorkspaceCandidates()
		route, routeErr := resolveWidgetWorkspace(wsSelection, prompt, candidates)
		if routeErr != nil {
			return a.widgetConversationResult("invalid", routeErr, widgetConversationReceipt{})
		}
		receipt = widgetConversationReceipt{
			RequestID: requestID, PromptHash: promptHash,
			WorkspaceSelection: wsSelection,
			Scope:              route.Scope, WorkspaceRoot: route.Root, WorkspaceName: route.Name,
			RouteReason: route.Reason, Status: "routing", UpdatedAt: time.Now().UnixMilli(),
		}
		if err := a.saveWidgetConversationReceipt(receipt); err != nil {
			return a.widgetConversationResult("retryable_error", fmt.Errorf("保存新对话路由: %w", err), receipt)
		}
	}

	if receipt.TabID == "" {
		meta, err := a.EnsureBlankTab(receipt.Scope, receipt.WorkspaceRoot)
		if err != nil {
			receipt.Error = err.Error()
			_ = a.saveWidgetConversationReceipt(receipt)
			return a.widgetConversationResult("retryable_error", fmt.Errorf("创建新对话: %w", err), receipt)
		}
		receipt.TabID = meta.ID
		receipt.Status = "created"
		receipt.Error = ""
		if err := a.saveWidgetConversationReceipt(receipt); err != nil {
			return a.widgetConversationResult("retryable_error", fmt.Errorf("保存新对话状态: %w", err), receipt)
		}
	}

	ctrl, err := a.waitWidgetTabReady(receipt.TabID, widgetReadyWait)
	if err != nil {
		receipt.Status = "created"
		receipt.Error = err.Error()
		_ = a.saveWidgetConversationReceipt(receipt)
		return a.widgetConversationResult("retryable_error", err, receipt)
	}
	if receipt.Status == "submitting" && widgetHistoryHasPrompt(ctrl.History(), prompt) {
		receipt.Status = "submitted"
		receipt.Error = ""
		if err := a.saveWidgetConversationReceipt(receipt); err != nil {
			return a.widgetConversationResult("retryable_error", fmt.Errorf("确认已发送状态: %w", err), receipt)
		}
		return a.widgetConversationResult("already_applied", nil, receipt)
	}

	receipt.Status = "submitting"
	receipt.Error = ""
	if err := a.saveWidgetConversationReceipt(receipt); err != nil {
		return a.widgetConversationResult("retryable_error", fmt.Errorf("保存发送状态: %w", err), receipt)
	}
	if err := a.SubmitToTab(receipt.TabID, prompt); err != nil {
		receipt.Status = "created"
		receipt.Error = err.Error()
		_ = a.saveWidgetConversationReceipt(receipt)
		return a.widgetConversationResult("retryable_error", fmt.Errorf("发送新对话: %w", err), receipt)
	}
	receipt.Status = "submitted"
	receipt.Error = ""
	if err := a.saveWidgetConversationReceipt(receipt); err != nil {
		return a.widgetConversationResult("retryable_error", fmt.Errorf("保存发送回执: %w", err), receipt)
	}
	return a.widgetConversationResult("accepted", nil, receipt)
}

func (a *App) widgetConversationResult(status string, err error, receipt widgetConversationReceipt) WidgetConversationResult {
	result := WidgetConversationResult{
		Status: status, TabID: receipt.TabID, WorkspaceRoot: receipt.WorkspaceRoot,
		WorkspaceName: receipt.WorkspaceName, RouteReason: receipt.RouteReason,
		Snapshot: a.GetWidgetSnapshot(),
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func (a *App) widgetConversationReceipt(requestID string) (widgetConversationReceipt, bool, error) {
	a.widgetActionMu.Lock()
	defer a.widgetActionMu.Unlock()
	a.loadWidgetStateLocked()
	for _, receipt := range a.widgetState.Conversations {
		if receipt.RequestID == requestID {
			return receipt, true, nil
		}
	}
	return widgetConversationReceipt{}, false, nil
}

func (a *App) saveWidgetConversationReceipt(receipt widgetConversationReceipt) error {
	a.widgetActionMu.Lock()
	defer a.widgetActionMu.Unlock()
	a.loadWidgetStateLocked()
	receipt.UpdatedAt = time.Now().UnixMilli()
	found := false
	for i := range a.widgetState.Conversations {
		if a.widgetState.Conversations[i].RequestID == receipt.RequestID {
			a.widgetState.Conversations[i] = receipt
			found = true
			break
		}
	}
	if !found {
		a.widgetState.Conversations = append(a.widgetState.Conversations, receipt)
	}
	if len(a.widgetState.Conversations) > widgetActionLimit {
		a.widgetState.Conversations = a.widgetState.Conversations[len(a.widgetState.Conversations)-widgetActionLimit:]
	}
	return a.saveWidgetStateLocked()
}

func (a *App) waitWidgetTabReady(tabID string, timeout time.Duration) (control.SessionAPI, error) {
	deadline := time.Now().Add(timeout)
	for {
		a.mu.RLock()
		tab := a.tabByIDLocked(tabID)
		if tab == nil {
			a.mu.RUnlock()
			return nil, errors.New("新对话已不存在，可以重试")
		}
		ctrl := tab.Ctrl
		startupErr := strings.TrimSpace(tab.StartupErr)
		a.mu.RUnlock()
		if startupErr != "" {
			return nil, fmt.Errorf("工作区启动失败: %s", startupErr)
		}
		if ctrl != nil {
			return ctrl, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("工作区启动超时，可以重试")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func widgetHistoryHasPrompt(messages []provider.Message, prompt string) bool {
	prompt = strings.TrimSpace(prompt)
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == provider.RoleUser && strings.TrimSpace(messages[i].Content) == prompt {
			return true
		}
	}
	return false
}

func (a *App) widgetWorkspaceCandidates() []widgetWorkspaceCandidate {
	projects := loadProjectsFile()
	a.mu.RLock()
	active := a.activeTabLocked()
	activeRoot := ""
	if active != nil && active.Scope == "project" {
		activeRoot = normalizeProjectRoot(active.WorkspaceRoot)
	}
	a.mu.RUnlock()

	candidates := make([]widgetWorkspaceCandidate, 0, len(projects.Projects))
	for order, project := range projects.Projects {
		root := normalizeProjectRoot(project.Root)
		if root == "" {
			continue
		}
		name := strings.TrimSpace(project.Title)
		if name == "" {
			name = workspaceName(root)
		}
		titles := loadTopicTitles(root)
		topics := make([]string, 0, min(8, len(project.Topics)))
		for _, id := range project.Topics {
			if title := strings.TrimSpace(titles[id]); title != "" {
				topics = append(topics, title)
				if len(topics) == 8 {
					break
				}
			}
		}
		candidates = append(candidates, widgetWorkspaceCandidate{
			Scope: "project", Root: root, Name: name,
			Aliases: uniqueWidgetStrings(name, filepath.Base(root)), Topics: topics,
			Active: root == activeRoot, Order: order,
			Transient: widgetIsTransientRoot(root, name),
		})
	}
	return candidates
}

func chooseWidgetWorkspace(prompt string, candidates []widgetWorkspaceCandidate) widgetWorkspaceRoute {
	if len(candidates) == 0 {
		return widgetWorkspaceRoute{Scope: "global", Name: globalProjectTitle(), Reason: "Global 兜底"}
	}
	normalizedPrompt := normalizeWidgetMatchText(prompt)
	bestIndex, bestScore, bestReason := -1, -1, ""
	for i, candidate := range candidates {
		score, reason := 0, "最近使用"
		for _, alias := range candidate.Aliases {
			normalizedAlias := normalizeWidgetMatchText(alias)
			if len([]rune(normalizedAlias)) >= 2 && strings.Contains(normalizedPrompt, normalizedAlias) {
				next := 100 + len([]rune(normalizedAlias))
				if next > score {
					score, reason = next, "名称匹配"
				}
			}
		}
		if score < 100 {
			for _, topic := range candidate.Topics {
				if overlap := widgetTextOverlap(normalizedPrompt, normalizeWidgetMatchText(topic)); overlap >= 2 {
					next := 40 + overlap
					if next > score {
						score, reason = next, "历史上下文"
					}
				}
			}
		}
		if candidate.Active && score < 10 {
			score, reason = 10, "当前工作区"
		}
		if score > bestScore || (score == bestScore && bestIndex >= 0 && candidate.Order < candidates[bestIndex].Order) {
			bestIndex, bestScore, bestReason = i, score, reason
		}
	}
	best := candidates[bestIndex]
	// Transient workspaces remain addressable by an explicit full name, but they
	// never become the implicit destination of a compact conversation.
	if best.Transient && bestReason != "名称匹配" {
		if stable, ok := stableWidgetFamily(best, candidates); ok {
			best, bestReason = stable, "主工作区"
		} else {
			for _, candidate := range candidates {
				if !candidate.Transient {
					best, bestReason = candidate, "最近使用"
					break
				}
			}
		}
	}
	return widgetWorkspaceRoute{Scope: best.Scope, Root: best.Root, Name: best.Name, Reason: bestReason}
}

// ListWidgetWorkspaces returns the selectable workspace options for the widget
// dropdown: "auto", every non-transient project, and Global.
func (a *App) ListWidgetWorkspaces() []WidgetWorkspaceOption {
	candidates := a.widgetWorkspaceCandidates()
	out := []WidgetWorkspaceOption{{Scope: widgetWorkspaceAuto, Name: "自动"}}
	for _, c := range candidates {
		if c.Transient {
			continue
		}
		out = append(out, WidgetWorkspaceOption{Scope: widgetWorkspaceProject, Name: c.Name, Root: c.Root})
	}
	out = append(out, WidgetWorkspaceOption{Scope: widgetWorkspaceGlobal, Name: globalProjectTitle()})
	return out
}

// resolveWidgetWorkspace turns a workspace selection into a concrete route.
// "auto" delegates to the existing smart routing; "global" goes to Global;
// "project:<root>" validates the root is a known non-transient candidate.
func resolveWidgetWorkspace(selection, prompt string, candidates []widgetWorkspaceCandidate) (widgetWorkspaceRoute, error) {
	switch {
	case selection == widgetWorkspaceAuto:
		return chooseWidgetWorkspace(prompt, candidates), nil
	case selection == widgetWorkspaceGlobal:
		return widgetWorkspaceRoute{Scope: widgetWorkspaceGlobal, Name: globalProjectTitle(), Reason: "手动选择"}, nil
	case strings.HasPrefix(selection, widgetWorkspaceProject+":"):
		root := strings.TrimSpace(selection[len(widgetWorkspaceProject+":"):])
		if root == "" {
			return widgetWorkspaceRoute{}, errors.New("工作区 root 不能为空")
		}
		root = normalizeProjectRoot(root)
		for _, c := range candidates {
			if c.Root == root {
				if c.Transient {
					return widgetWorkspaceRoute{}, fmt.Errorf("临时工作区 %q 不可手动选择", c.Name)
				}
				return widgetWorkspaceRoute{Scope: c.Scope, Root: c.Root, Name: c.Name, Reason: "手动选择"}, nil
			}
		}
		return widgetWorkspaceRoute{}, fmt.Errorf("工作区 %q 不在当前候选列表中或已过期", root)
	default:
		return widgetWorkspaceRoute{}, fmt.Errorf("不支持的工作区选择 %q", selection)
	}
}

func stableWidgetFamily(transient widgetWorkspaceCandidate, candidates []widgetWorkspaceCandidate) (widgetWorkspaceCandidate, bool) {
	for _, candidate := range candidates {
		if !candidate.Transient && widgetWorkspaceVariantOf(transient.Root, candidate.Root) {
			return candidate, true
		}
	}
	return widgetWorkspaceCandidate{}, false
}

func widgetWorkspaceVariantOf(variantRoot, stableRoot string) bool {
	variantRoot = filepath.Clean(variantRoot)
	stableRoot = filepath.Clean(stableRoot)
	if !strings.EqualFold(filepath.Dir(variantRoot), filepath.Dir(stableRoot)) {
		return false
	}
	variant := strings.ToLower(filepath.Base(variantRoot))
	stable := strings.ToLower(filepath.Base(stableRoot))
	if len([]rune(stable)) < 2 || len(variant) <= len(stable) {
		return false
	}
	if !strings.HasPrefix(variant, stable) {
		return false
	}
	separator := variant[len(stable)]
	return separator == '-' || separator == '_' || separator == '.' || separator == ' '
}

func normalizeWidgetMatchText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, text)
}

func widgetTextOverlap(left, right string) int {
	lr, rr := []rune(left), []rune(right)
	best := 0
	for i := range lr {
		for j := range rr {
			length := 0
			for i+length < len(lr) && j+length < len(rr) && lr[i+length] == rr[j+length] {
				length++
			}
			if length > best {
				best = length
			}
		}
	}
	return best
}

func uniqueWidgetStrings(values ...string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value != "" && !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	return out
}

// widgetIsTransientRoot detects CI gate shells, linked worktrees, and other
// temporary workspaces that should not be selected by default.
func widgetIsTransientRoot(root, name string) bool {
	lower := strings.ToLower(name)
	for _, pattern := range []string{"-ci-gate", "-ci-", "-temp-", "-tmp-"} {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	// Check for linked worktree: .git is a file (not a directory) in worktrees.
	gitPath := filepath.Join(root, ".git")
	if info, err := os.Stat(gitPath); err == nil && !info.IsDir() {
		return true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !strings.EqualFold(entry.Name(), ".WorkGround2") {
			return false
		}
	}
	return true
}
