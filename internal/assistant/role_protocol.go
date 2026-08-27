package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"workground2/internal/permission"
)

// RoleModel runs one bounded, tool-free model completion for a role (Dispatcher,
// Reflector, or Ideator). Implementations MUST use a frozen read-only/deny
// permission policy and a stable system prompt; the entire role instruction and
// dynamic context is passed in the prompt (the user turn) so the system prompt
// prefix stays byte-stable. A returned error means the model could not run (for
// example the provider is unavailable); the caller persists a retryable state.
type RoleModel interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// RoleModelFunc adapts a plain function into a RoleModel.
type RoleModelFunc func(ctx context.Context, prompt string) (string, error)

func (f RoleModelFunc) Complete(ctx context.Context, prompt string) (string, error) {
	return f(ctx, prompt)
}

// RolePermissionPolicy is the frozen policy for role model calls: writers are
// denied, so a role call can never produce a tool side effect. Read-only tools
// remain harmless fallbacks.
func RolePermissionPolicy() permission.Policy {
	return permission.Policy{Mode: permission.Deny}
}

// roleBackoff returns a bounded exponential backoff for role-model retries. The
// first retry is one minute; it doubles up to a 30-minute ceiling.
func roleBackoff(attempt int) time.Duration {
	const base = time.Minute
	const max = 30 * time.Minute
	d := base
	for i := 1; i < attempt && d < max; i++ {
		d *= 2
		if d > max {
			d = max
		}
	}
	return d
}

// DispatcherOutput is the strict JSON contract the Dispatcher model must emit.
// No permission, workspace, credential, publish, or external-action fields are
// permitted; decoding rejects unknown fields so any such field fails closed.
type DispatcherOutput struct {
	Kind  DispatchKind    `json:"kind"`
	Reply string          `json:"reply"`
	Jobs  []DispatcherJob `json:"jobs,omitempty"`
}

type DispatcherJob struct {
	Name   string       `json:"name"`
	Kind   DispatchKind `json:"kind"`
	Target string       `json:"target,omitempty"`
	Prompt string       `json:"prompt"`
}

// ReflectorOutput is the strict JSON contract the Reflector model must emit. It
// maps 1:1 to ContextPackContent.
type ReflectorOutput struct {
	Conclusion    string   `json:"conclusion"`
	Evidence      []string `json:"evidence,omitempty"`
	Failures      []string `json:"failures,omitempty"`
	Strategies    []string `json:"strategies,omitempty"`
	OpenLoops     []string `json:"open_loops,omitempty"`
	RunnerContext string   `json:"runner_context,omitempty"`
}

// IdeatorOutput is the strict JSON contract the Ideator model must emit. It
// maps 1:1 to IdeaContent.
type IdeatorOutput struct {
	Summary        string `json:"summary"`
	Rationale      string `json:"rationale,omitempty"`
	StrategyMemory string `json:"strategy_memory,omitempty"`
	Responsibility string `json:"responsibility,omitempty"`
	Objective      string `json:"objective,omitempty"`
	DoneCriteria   string `json:"done_criteria,omitempty"`
	NextAction     string `json:"next_action,omitempty"`
}

const maxDispatcherJobs = 3

func ParseDispatcherOutput(text string) (Classification, error) {
	var out DispatcherOutput
	if err := parseRoleJSON(text, &out, roleForbiddenKeys); err != nil {
		return Classification{}, err
	}
	if err := validateDispatchKind(out.Kind); err != nil {
		return Classification{}, err
	}
	if strings.TrimSpace(out.Reply) == "" {
		return Classification{}, errors.New("assistant: dispatcher output requires a non-empty reply")
	}
	if len(out.Jobs) > maxDispatcherJobs {
		return Classification{}, fmt.Errorf("assistant: dispatcher output has %d jobs, max %d", len(out.Jobs), maxDispatcherJobs)
	}
	jobs := make([]JobSpec, 0, len(out.Jobs))
	seen := map[string]bool{}
	for i, raw := range out.Jobs {
		if err := validateDispatchKind(raw.Kind); err != nil {
			return Classification{}, err
		}
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			return Classification{}, fmt.Errorf("assistant: dispatcher job %d requires a name", i)
		}
		if seen[name] {
			return Classification{}, fmt.Errorf("assistant: dispatcher job name %q is not unique", name)
		}
		seen[name] = true
		jobs = append(jobs, JobSpec{
			Name: name, Kind: raw.Kind,
			Target: strings.TrimSpace(raw.Target), Prompt: strings.TrimSpace(raw.Prompt),
		})
	}
	return Classification{Kind: out.Kind, Reply: strings.TrimSpace(out.Reply), Jobs: jobs}, nil
}

func ParseReflectorOutput(text string) (ContextPackContent, error) {
	var out ReflectorOutput
	if err := parseRoleJSON(text, &out, roleForbiddenKeys); err != nil {
		return ContextPackContent{}, err
	}
	content := ContextPackContent{
		Conclusion:    strings.TrimSpace(out.Conclusion),
		Evidence:      trimList(out.Evidence),
		Failures:      trimList(out.Failures),
		Strategies:    trimList(out.Strategies),
		OpenLoops:     trimList(out.OpenLoops),
		RunnerContext: strings.TrimSpace(out.RunnerContext),
	}
	if err := validateContextPackContent(content); err != nil {
		return ContextPackContent{}, err
	}
	return content, nil
}

func ParseIdeatorOutput(text string) (IdeaContent, error) {
	var out IdeatorOutput
	if err := parseRoleJSON(text, &out, roleForbiddenKeys); err != nil {
		return IdeaContent{}, err
	}
	content := IdeaContent{
		Summary:        strings.TrimSpace(out.Summary),
		Rationale:      strings.TrimSpace(out.Rationale),
		StrategyMemory: strings.TrimSpace(out.StrategyMemory),
		Responsibility: strings.TrimSpace(out.Responsibility),
		Objective:      strings.TrimSpace(out.Objective),
		DoneCriteria:   strings.TrimSpace(out.DoneCriteria),
		NextAction:     strings.TrimSpace(out.NextAction),
	}
	if content.Summary == "" {
		return IdeaContent{}, errors.New("assistant: ideator output requires a non-empty summary")
	}
	if content.Responsibility != "" {
		if !validAlias(content.Responsibility) {
			return IdeaContent{}, errors.New("assistant: ideator responsibility must be a valid alias")
		}
		if content.Objective == "" {
			return IdeaContent{}, errors.New("assistant: ideator responsibility requires an objective")
		}
	}
	return content, nil
}

// roleForbiddenKeys are fields the model must never control. They are rejected
// before decoding so a clearer error is produced than a bare unknown-field error.
var roleForbiddenKeys = map[string]bool{
	"policy": true, "workspace": true, "workspace_root": true, "scope": true,
	"publish": true, "external_action": true, "action": true, "tool": true,
	"tool_call": true, "credential": true, "secret": true, "api_key": true,
	"mission": true, "lifecycle": true, "permission": true, "approval": true,
	"allow": true, "deny": true, "network": true, "local_write": true,
	"enabled": true, "schedule": true,
}

func parseRoleJSON(text string, out any, forbidden map[string]bool) error {
	raw := extractJSONObject(text)
	if raw == "" {
		return errors.New("assistant: role output contains no JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return fmt.Errorf("assistant: malformed role JSON: %w", err)
	}
	for key := range object {
		if forbidden[key] {
			return fmt.Errorf("assistant: role output must not contain field %q", key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("assistant: invalid role JSON: %w", err)
	}
	return nil
}

func extractJSONObject(text string) string {
	s := strings.TrimSpace(text)
	if start := strings.Index(s, "```"); start >= 0 {
		rest := s[start+3:]
		if newline := strings.Index(rest, "\n"); newline >= 0 {
			rest = rest[newline+1:]
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			s = rest[:end]
		}
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

const (
	roleMaxContextPacks   = 4
	roleMaxContextBytes   = 8000
	roleMaxRecentInputs   = 4
	roleMaxJobResultBytes = 16 * 1024
	roleMaxPlanBytes      = 8 * 1024
)

// DispatcherPrompt builds the bounded prompt the Dispatcher model receives:
// assistant mission, the raw user input, and a bounded slice of recent
// reflection context. Dynamic context lives only in this prompt.
func DispatcherPrompt(snapshot Snapshot, input string) string {
	var b strings.Builder
	b.WriteString("你是长期助手的 Dispatcher。对用户的直接输入做一次性分类，并严格按下面的 JSON 协议返回结果。\n\n")
	b.WriteString("助手使命：\n" + snapshot.Assistant.Mission + "\n\n")
	fmt.Fprintf(&b, "用户原文：\n%s\n\n", input)
	writeRoleContext(&b, snapshot)
	b.WriteString("分类只能是 task / question / feedback / improvement / correction / control 之一。\n")
	b.WriteString("reply 是给用户看的一级中文回复。jobs 是 0..3 个唯一命名的 Runner Job。task 或 improvement 通常需要 Job；feedback/correction/control 通常零 Job。question 若零 Job，reply 必须直接回答问题；需要查询、验证或执行才能回答时必须创建 Job，禁止只承诺稍后回答。\n\n")
	b.WriteString("只返回一个 JSON 对象，不要调用任何工具，不要输出解释或多余文本：\n")
	b.WriteString(`{"kind":"task","reply":"收到，我来处理。","jobs":[{"name":"execute","kind":"task","target":"","prompt":"…"}]}` + "\n")
	b.WriteString("禁止在 JSON 中携带 policy、workspace、scope、credential、publish、action 等任何权限/工作区/外部动作字段。")
	return b.String()
}

// ReflectorPrompt builds the bounded prompt the Reflector model receives: the
// dispatch classification and its terminal job results.
func ReflectorPrompt(snapshot Snapshot, dispatch Dispatch, jobs []RunnerJob) string {
	var b strings.Builder
	b.WriteString("你是长期助手的 Reflector。这个 Dispatch 的所有 Runner Job 已进入终态，把结果沉淀成一个有界 ContextPack，严格按 JSON 协议返回。\n\n")
	fmt.Fprintf(&b, "分类：%s\n用户原文：\n%s\nDispatcher 一级回复：%s\n\n", dispatch.Kind, dispatch.Input, dispatch.Reply)
	b.WriteString("各 Job 终态结果：\n")
	remaining := roleMaxJobResultBytes
	for _, job := range jobs {
		if remaining <= 0 {
			break
		}
		state := string(job.State)
		if job.Error != nil {
			state += "（错误：" + job.Error.Message + "）"
		}
		line := truncateRoleText(fmt.Sprintf("- [%s] %s：%s\n", job.Name, state, strings.TrimSpace(job.Summary)), remaining)
		b.WriteString(line)
		remaining -= len(line)
	}
	b.WriteString("\nconclusion 是给用户/后续 Job 看的有界结论；evidence/failures/strategies/open_loops 是可选列表；runner_context 是给后续 Runner Job 的简短上下文。\n\n")
	b.WriteString("只返回一个 JSON 对象，不要调用任何工具，不要输出解释或多余文本：\n")
	b.WriteString(`{"conclusion":"…","evidence":["…"],"failures":["…"],"strategies":["…"],"open_loops":["…"],"runner_context":"…"}` + "\n")
	b.WriteString("禁止在 JSON 中携带 policy、workspace、scope、credential、publish、action 等任何权限/工作区/外部动作字段。")
	return b.String()
}

// IdeatorPrompt builds the bounded prompt the Ideator model receives. It
// instructs the model to temporarily set aside existing strategy assumptions to
// re-examine mission/goals/path, while hard-forbidding any permission,
// workspace, safety, credential, or publish boundary crossing.
func IdeatorPrompt(snapshot Snapshot, trigger IdeaTrigger) string {
	var b strings.Builder
	b.WriteString("你是长期助手的 Ideator。暂时放下既有策略假设，重新审视使命、目标与路径，产出一个待人类确认的脑洞提案，严格按 JSON 协议返回。\n\n")
	b.WriteString("助手使命：\n" + snapshot.Assistant.Mission + "\n\n")
	if len(snapshot.Plan.Responsibilities) > 0 {
		b.WriteString("当前责任图：\n")
		remaining := roleMaxPlanBytes
		for _, resp := range snapshot.Plan.Responsibilities {
			if remaining <= 0 {
				break
			}
			line := truncateRoleText(fmt.Sprintf("- [%s] %s\n", resp.Alias, resp.Objective), remaining)
			b.WriteString(line)
			remaining -= len(line)
		}
		b.WriteString("\n")
	}
	writeRoleContext(&b, snapshot)
	b.WriteString("summary 是一句话提案；rationale 是理由；strategy_memory 可选，是接受后要写入的 strategy 记忆。\n")
	b.WriteString("responsibility/objective/done_criteria/next_action 可选，是接受后要创建的新责任候选。responsibility 是责任别名：不提出责任时留空；否则必须是 1..64 个 ASCII 字母、数字、下划线或连字符，例如 smoke-before-publish。\n\n")
	b.WriteString("硬性边界：绝不突破 Policy、Workspace、安全、凭据或发布边界；不直接改 Mission/Policy/Workspace/凭据/发布配置，不执行任何外部动作。\n\n")
	b.WriteString("只返回一个 JSON 对象，不要调用任何工具，不要输出解释或多余文本：\n")
	b.WriteString(`{"summary":"…","rationale":"…","strategy_memory":"…","responsibility":"…","objective":"…","done_criteria":"…","next_action":"…"}` + "\n")
	b.WriteString("禁止在 JSON 中携带 policy、workspace、scope、credential、publish、action 等任何权限/工作区/外部动作字段。")
	return b.String()
}

func writeRoleContext(b *strings.Builder, snapshot Snapshot) {
	packs := ApplicableContextPacks(snapshot.ContextPacks, snapshot.Assistant.ID, "", 0, roleMaxContextPacks, roleMaxContextBytes)
	if len(packs) > 0 {
		b.WriteString("近期反思结论（只作背景）：\n")
		for _, pack := range packs {
			fmt.Fprintf(b, "- %s\n", strings.TrimSpace(pack.Conclusion))
		}
		b.WriteString("\n")
	}
}

func truncateRoleText(value string, maxBytes int) string {
	if maxBytes <= 0 || value == "" {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	const suffix = "…"
	if maxBytes < len(suffix) {
		return ""
	}
	end := maxBytes - len(suffix)
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + suffix
}
