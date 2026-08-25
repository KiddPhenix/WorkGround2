package assistant

import (
	"fmt"
	"regexp"
	"strings"
)

// Capability is a deterministic, typed requirement derived from a frozen run's
// mission and routine prompt. It is the contract the runner must evidence with
// successful tool results before accepting the run as successful.
type Capability string

const (
	// CapabilityLiveWeb requires a successful result from a live Web/browser
	// tool. A dispatch alone, a failed result, or a local-only tool never counts.
	CapabilityLiveWeb Capability = "live_web"
)

// liveWebTools are the tools whose successful result satisfies CapabilityLiveWeb.
// The set is intentionally explicit: navigation/state/fetch/search tools that
// actually observe live web content. Teardown and upload tools are excluded.
var liveWebTools = map[string]bool{
	"browser_open":     true,
	"browser_navigate": true,
	"browser_state":    true,
	"browser_click":    true,
	"browser_scroll":   true,
	"web_fetch":        true,
	"web_search":       true,
}

// LiveWebTool reports whether a successful result from name satisfies
// CapabilityLiveWeb.
func LiveWebTool(name string) bool {
	return liveWebTools[strings.TrimSpace(name)]
}

// urlPattern matches an explicit URL or a domain-like token (host ending in a
// known TLD). It avoids bare single labels ("com") and bare host names.
var urlPattern = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[a-z0-9][^\s]*\b|\b[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*\.(?:com|cn|org|net|io|dev|app|ai|co|me|info|biz|xyz|site|top|club|cc|tv)\b`)

// websiteKeywords unambiguously name a website or web page.
var websiteKeywords = []string{
	"website", "webpage", "web page", "web site",
	"网站", "网页", "官网", "网页内容",
}

// onlineActionKeywords name an explicit inspect/browse/check intent.
var onlineActionKeywords = []string{"inspect", "browse", "检查", "浏览", "查看"}

// onlineMarkerKeywords name an online/live context marker.
var onlineMarkerKeywords = []string{"online", "在线"}

// RequiredCapabilities derives the deterministic capability set from the frozen
// mission and routine prompt. At minimum, a task naming a URL/domain or
// explicitly asking to inspect/browse an online website requires live_web.
func RequiredCapabilities(mission, prompt string) []Capability {
	text := strings.TrimSpace(mission) + "\n" + strings.TrimSpace(prompt)
	if !requiresLiveWeb(text) {
		return nil
	}
	return []Capability{CapabilityLiveWeb}
}

func requiresLiveWeb(text string) bool {
	if urlPattern.MatchString(text) {
		return true
	}
	lower := strings.ToLower(text)
	action := containsAny(lower, onlineActionKeywords...)
	online := containsAny(lower, onlineMarkerKeywords...)
	website := containsAny(lower, websiteKeywords...)
	return action && (online || website)
}

func containsAny(text string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// Evidence accumulates the successful tool results observed for a single run. It
// is the only source the runner consults when validating required capabilities.
type Evidence struct {
	liveWeb bool
}

// RecordToolResult records a finished tool result. ok is true only when the call
// completed without error; a failed, blocked, or denied result records nothing.
func (e *Evidence) RecordToolResult(name string, ok bool) {
	if ok && LiveWebTool(name) {
		e.liveWeb = true
	}
}

// Satisfies reports whether the accumulated evidence covers capability c.
func (e Evidence) Satisfies(c Capability) bool {
	switch c {
	case CapabilityLiveWeb:
		return e.liveWeb
	default:
		return false
	}
}

// Missing returns the required capabilities that have no successful evidence.
func (e Evidence) Missing(required []Capability) []Capability {
	var out []Capability
	for _, c := range required {
		if !e.Satisfies(c) {
			out = append(out, c)
		}
	}
	return out
}

// EvidenceFailure builds the recoverable failure used when a required capability
// has no successful tool-result evidence. Callers must set Now and RetryAfter.
func EvidenceFailure(missing []Capability) Failure {
	codes := make([]string, 0, len(missing))
	for _, c := range missing {
		codes = append(codes, string(c))
	}
	return Failure{
		Code:         "evidence_missing",
		Message:      fmt.Sprintf("本次运行未取得必需能力的成功工具证据（%s）；浏览器/Web 工具可能未调用、不可用或执行失败，运行将按策略重试", strings.Join(codes, ", ")),
		Retryable:    true,
		OutcomeKnown: true,
	}
}

const freshCycleDirective = "当前责任图已全部完成。保留已完成的职责作为历史，不要重开或修改它们；基于当前冻结的使命与任务，声明一个新的 2–4 项责任周期（使用新的 alias），并把新周期所需的硬性能力写进对应责任的 objective/done_criteria/next_action。选择其中一项 ready 责任开始执行，用 <assistant-progress> 声明这些责任。"

const emptyPlanDirective = "当前责任图为空：先根据使命推导一个 2–4 项的小型责任图，为每项写清 objective、done_criteria、next_action 和依赖，选择其中一项 ready 责任开始执行，并用 <assistant-progress> 声明这些责任。"

// FreshCycleDirective returns the bounded instruction for opening a new
// responsibility cycle. needed is true when the graph is empty or every
// responsibility is done and no ready/active item exists. Completed items are
// preserved as history by applyProgress; this directive only drives the model
// to declare fresh responsibilities under new aliases.
func FreshCycleDirective(plan Plan) (directive string, needed bool) {
	if len(plan.Responsibilities) == 0 {
		return emptyPlanDirective, true
	}
	for _, r := range plan.Responsibilities {
		if r.Status != RespDone {
			return "", false
		}
	}
	return freshCycleDirective, true
}
