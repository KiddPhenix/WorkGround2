package main

// 完成/失败通知的“新闻体”短摘要：独立于原会话的轻量 LLM 生成。
//
// 摘要以“task + 完成类型 + 完成 revision”为稳定 key，单飞异步生成、落盘缓存；
// 生成期间与失败降级都使用机械短摘要，绝不阻塞图标快照；迟到的生成结果在
// 写回前校验任务当前完成指纹，发现任务已进入新一轮状态时直接丢弃。

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"workground2/internal/agent"
	"workground2/internal/boot"
	"workground2/internal/config"
	"workground2/internal/provider"
)

const (
	completionSummaryReady  = "ready"
	completionSummaryFailed = "failed"

	// completionSummaryCacheLimit caps the persisted summary cache so long-
	// lived users cannot grow desktopIconState.json without bound. Oldest
	// entries are trimmed first; a trimmed task simply regenerates on demand.
	completionSummaryCacheLimit = 128

	// completionSummaryTimeout caps one summary generation so a hung model
	// stream can never pin a widget snapshot or an in-flight slot forever.
	completionSummaryTimeout = 30 * time.Second
	// completionSummaryRetryAfter is the backoff before a failed generation is
	// retried from the next snapshot.
	completionSummaryRetryAfter = 5 * time.Minute
	// completionSummaryMaxRunes matches the largest result body that remains
	// comfortably readable in the enlarged desktop-icon popup.
	completionSummaryMaxRunes = 260
)

// desktopIconCompletionSummary is one persisted cache entry for a completion
// notice's news-style summary. Status is either "ready" (Text is the LLM
// summary) or "failed" (Text is empty; the mechanical fallback keeps showing
// and generation retries after RetryAfter).
type desktopIconCompletionSummary struct {
	Revision    string `json:"revision"`        // completion fingerprint at request time
	Kind        string `json:"kind"`            // completed | failed
	Text        string `json:"text,omitempty"`  // ready 时有效
	Status      string `json:"status"`          // ready | failed
	Error       string `json:"error,omitempty"` // failed 时的显式原因（可观察、可重试）
	GeneratedAt int64  `json:"generatedAt,omitempty"`
	RetryAfter  int64  `json:"retryAfter,omitempty"`
}

// desktopIconCompletionSummaryRequest carries every input a generation needs.
// It is captured from the snapshot sources so the async job never has to reach
// back into App locks for its material.
type desktopIconCompletionSummaryRequest struct {
	Key           string // desktopIconCompletionKey result
	TaskID        string
	Kind          string // completed | failed
	Revision      string // completion fingerprint at request time
	Title         string
	Request       string
	Result        string
	WorkspaceRoot string
}

// completionSummaryGenerator is the injection seam for async generation. The
// default implementation calls a configured provider through boot.NewProvider;
// tests inject a fake so snapshot generation never touches the network.
type completionSummaryGenerator interface {
	Generate(ctx context.Context, req desktopIconCompletionSummaryRequest) (string, error)
}

// completionSummaryCall is one in-flight generation slot (singleflight).
type completionSummaryCall struct {
	done chan struct{}
}

// desktopIconCompletionKey derives the stable cache key from the task id, the
// completion kind and the completion revision (NeedsAttentionAt timestamp). A
// later completion of the same task yields a different key, so a stale
// generation can never overwrite or shadow a newer summary.
func desktopIconCompletionKey(taskID, kind string, at int64) string {
	return widgetRevision(taskID, kind, strconv.FormatInt(at, 10))
}

func desktopIconFailureKey(taskID, result string) string {
	return widgetRevision(taskID, "failed", strings.TrimSpace(result))
}

// desktopIconCompletionSummaryFor projects a cached summary onto a notice body.
// It returns the mechanical fallback while generating (no cache entry or still
// pending) and on failure, plus the status the frontend may surface.
func desktopIconCompletionSummaryFor(summaries map[string]desktopIconCompletionSummary, key, fallback string) (body, status string) {
	entry, ok := summaries[key]
	if !ok {
		return fallback, ""
	}
	switch entry.Status {
	case completionSummaryReady:
		if text := strings.TrimSpace(entry.Text); text != "" {
			return text, completionSummaryReady
		}
		return fallback, ""
	case completionSummaryFailed:
		return fallback, completionSummaryFailed
	default:
		return fallback, ""
	}
}

// summaryGenerationDue reports whether this completion needs a (re)generation:
// never generated, or failed long enough ago that the retry backoff expired.
func summaryGenerationDue(entry desktopIconCompletionSummary, now int64) bool {
	switch entry.Status {
	case completionSummaryReady:
		return false
	case completionSummaryFailed:
		return now >= entry.RetryAfter
	default:
		return true
	}
}

const completionSummarySystemPrompt = "你是 WorkGround2 桌面小组件的新闻撰稿人。把下面的任务信息改写成一段中文“新闻体”短摘要：结论先行、事实明确，不带 Markdown 标题、代码块、文件路径或命令清单。最多 260 字。只输出摘要正文，不要引号包裹，不要任何解释。"

// buildCompletionSummaryPrompt assembles the user-side material for the
// one-shot prompt: the task title, the user request and the final assistant
// result. All inputs are bounded so the prompt stays small and cheap. The
// system message carries the news-writer instructions.
func buildCompletionSummaryPrompt(req desktopIconCompletionSummaryRequest) string {
	return "任务标题：" + conciseWidgetText(req.Title, 80) +
		"\n用户请求：" + conciseWidgetText(req.Request, 600) +
		"\n任务结果：" + conciseWidgetText(req.Result, 900)
}

var (
	completionCodeFenceRe  = regexp.MustCompile("```[^`]*```")
	completionInlineCodeRe = regexp.MustCompile("`[^`]*`")
	completionHeadingRe    = regexp.MustCompile("(?m)^#{1,6}\\s*")
	completionListMarkerRe = regexp.MustCompile("(?m)^\\s*[-*+]\\s+")
)

// cleanCompletionSummary strips markdown scaffolding and quoting from model
// output, collapses whitespace and bounds the length for the popup. An empty
// result after cleaning is an explicit error so the caller can degrade.
func cleanCompletionSummary(text string) (string, error) {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "\"'“”‘’")
	text = completionCodeFenceRe.ReplaceAllString(text, " ")
	text = completionInlineCodeRe.ReplaceAllString(text, " ")
	text = completionHeadingRe.ReplaceAllString(text, "")
	text = completionListMarkerRe.ReplaceAllString(text, "")
	text = strings.Join(strings.Fields(text), " ")
	text = strings.Trim(text, " \t\r\n-–—·:")
	if text == "" {
		return "", errors.New("completion summary is empty after cleaning")
	}
	runes := []rune(text)
	if len(runes) > completionSummaryMaxRunes {
		text = strings.TrimSpace(string(runes[:completionSummaryMaxRunes-1])) + "…"
	}
	return text, nil
}

// completionSummaryRequestsLocked collects every visible completion/failure
// that still needs (or may retry) a summary. It reads the persisted cache and
// the in-flight slots and therefore must run while iconWidgetMu is held.
func (a *App) completionSummaryRequestsLocked(sources []widgetSource) []desktopIconCompletionSummaryRequest {
	if len(sources) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	out := make([]desktopIconCompletionSummaryRequest, 0, len(sources))
	for _, source := range sources {
		kind, result, at := "", "", source.meta.NeedsAttentionAt
		switch {
		case source.meta.NeedsAttention && strings.TrimSpace(source.resultText) != "":
			kind, result = "completed", source.resultText
		case strings.TrimSpace(source.meta.StartupErr) != "":
			kind, result = "failed", source.meta.StartupErr
		default:
			continue
		}
		key := desktopIconCompletionKey(source.meta.ID, kind, at)
		revision := desktopIconCompletionFingerprint(kind, at)
		if kind == "failed" {
			key = desktopIconFailureKey(source.meta.ID, result)
			revision = desktopIconFailureFingerprint(result)
		}
		if entry, ok := a.iconWidgetState.CompletionSummaries[key]; ok && !summaryGenerationDue(entry, now) {
			continue
		}
		if a.completionSummaryInFlight[key] != nil {
			continue
		}
		out = append(out, desktopIconCompletionSummaryRequest{
			Key:           key,
			TaskID:        source.meta.ID,
			Kind:          kind,
			Revision:      revision,
			Title:         firstNonEmpty(strings.TrimSpace(source.meta.SessionDisplayTitle), strings.TrimSpace(source.meta.TopicTitle), "当前任务"),
			Request:       source.requestText,
			Result:        result,
			WorkspaceRoot: source.meta.WorkspaceRoot,
		})
	}
	return out
}

// desktopIconCompletionFingerprint is the stable completion identity captured
// at request time and re-checked before writing a late result back.
func desktopIconCompletionFingerprint(kind string, at int64) string {
	return "completed:" + strconv.FormatInt(at, 10)
}

func desktopIconFailureFingerprint(result string) string {
	return "failed:" + widgetRevision(strings.TrimSpace(result))
}

// maybeGenerateCompletionSummariesLocked starts one async generation per
// request (singleflight). It only starts goroutines while holding
// iconWidgetMu; every network call happens inside the goroutine, never under
// an App or widget mutex.
func (a *App) maybeGenerateCompletionSummariesLocked(requests []desktopIconCompletionSummaryRequest) {
	if len(requests) == 0 {
		return
	}
	if a.completionSummaryInFlight == nil {
		a.completionSummaryInFlight = map[string]*completionSummaryCall{}
	}
	for _, req := range requests {
		call := &completionSummaryCall{done: make(chan struct{})}
		a.completionSummaryInFlight[req.Key] = call
		go a.runCompletionSummary(req, call)
	}
}

func (a *App) runCompletionSummary(req desktopIconCompletionSummaryRequest, call *completionSummaryCall) {
	defer func() {
		a.iconWidgetMu.Lock()
		delete(a.completionSummaryInFlight, req.Key)
		close(call.done)
		a.iconWidgetMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), completionSummaryTimeout)
	defer cancel()
	var text string
	var genErr error
	if a.completionSummaryGen != nil {
		text, genErr = a.completionSummaryGen.Generate(ctx, req)
	} else {
		text, genErr = a.generateCompletionSummary(ctx, req)
	}
	if genErr == nil {
		text, genErr = cleanCompletionSummary(text)
	}

	// Late-result guard: if the task already moved to a newer completion (or
	// lost its completion state) while we were generating, drop the result so
	// it can never leak into the next round.
	if current, ok := a.completionRevisionForTask(req.TaskID); !ok || current != req.Revision {
		return
	}

	entry := desktopIconCompletionSummary{Revision: req.Revision, Kind: req.Kind, GeneratedAt: time.Now().UnixMilli()}
	if genErr != nil {
		entry.Status = completionSummaryFailed
		entry.Error = genErr.Error()
		entry.RetryAfter = entry.GeneratedAt + int64(completionSummaryRetryAfter/time.Millisecond)
	} else {
		entry.Status = completionSummaryReady
		entry.Text = text
	}
	a.iconWidgetMu.Lock()
	a.iconWidgetState.CompletionSummaries[req.Key] = entry
	a.trimCompletionSummariesLocked()
	if err := a.saveDesktopIconStateLocked(); err != nil {
		a.iconWidgetStateErr = fmt.Errorf("save completion summary: %w", err)
	}
	a.iconWidgetMu.Unlock()
}

// trimCompletionSummariesLocked keeps the persisted summary cache bounded by
// dropping the oldest generated entries. It must run while iconWidgetMu is
// held and is a no-op when the cache is within the limit.
func (a *App) trimCompletionSummariesLocked() {
	if len(a.iconWidgetState.CompletionSummaries) <= completionSummaryCacheLimit {
		return
	}
	type agedSummary struct {
		key string
		at  int64
	}
	aged := make([]agedSummary, 0, len(a.iconWidgetState.CompletionSummaries))
	for key, entry := range a.iconWidgetState.CompletionSummaries {
		aged = append(aged, agedSummary{key: key, at: entry.GeneratedAt})
	}
	sort.Slice(aged, func(i, j int) bool { return aged[i].at < aged[j].at })
	excess := len(aged) - completionSummaryCacheLimit
	for i := 0; i < excess; i++ {
		delete(a.iconWidgetState.CompletionSummaries, aged[i].key)
	}
}

// completionRevisionForTask returns the task's current completion fingerprint.
// It is the write-back guard for late generations: a fingerprint mismatch
// means the task started a new round and the pending result must be dropped.
// The fingerprint uses the same "min(branch meta, pending)" effective
// attention timestamp as tabMeta, so the guard and the cache key always agree.
func (a *App) completionRevisionForTask(taskID string) (string, bool) {
	a.mu.RLock()
	tab := a.tabByIDLocked(taskID)
	if tab == nil {
		a.mu.RUnlock()
		return "", false
	}
	startupErr := strings.TrimSpace(tab.StartupErr)
	tab.saveMu.Lock()
	pendingAt := tab.pendingAttentionAt
	tab.saveMu.Unlock()
	path := strings.TrimSpace(tab.currentSessionPath())
	a.mu.RUnlock()

	if startupErr != "" {
		return desktopIconFailureFingerprint(startupErr), true
	}
	// The BranchMeta read happens after RUnlock so file I/O never runs under
	// the App main lock.
	metaAttentionAt := int64(0)
	if path != "" {
		if meta, _, err := agent.LoadBranchMeta(path); err == nil && meta.NeedsAttention {
			metaAttentionAt = meta.NeedsAttentionAt
		}
	}
	effective := pendingAt
	if effective == 0 || (metaAttentionAt != 0 && metaAttentionAt < effective) {
		effective = metaAttentionAt
	}
	if effective != 0 {
		return "completed:" + strconv.FormatInt(effective, 10), true
	}
	return "", false
}

// generateCompletionSummary is the default LLM backend: it reuses the
// repository's provider build (boot.NewProvider) plus the plain chat stream,
// so no session history is touched and no tool is ever triggered.
func (a *App) generateCompletionSummary(ctx context.Context, req desktopIconCompletionSummaryRequest) (string, error) {
	cfg, err := config.LoadForRoot(req.WorkspaceRoot)
	if err != nil {
		return "", fmt.Errorf("completion summary: load config: %w", err)
	}
	entry := completionSummaryProvider(cfg)
	if entry == nil {
		return "", errors.New("completion summary: no configured provider")
	}
	prov, err := boot.NewProvider(entry)
	if err != nil {
		return "", fmt.Errorf("completion summary: build provider %s: %w", entry.Name, err)
	}
	chunks, err := prov.Stream(ctx, provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: completionSummarySystemPrompt},
			{Role: provider.RoleUser, Content: buildCompletionSummaryPrompt(req)},
		},
		Temperature: 0.3,
		MaxTokens:   300,
	})
	if err != nil {
		return "", fmt.Errorf("completion summary: %w", err)
	}
	var out strings.Builder
	for chunk := range chunks {
		switch chunk.Type {
		case provider.ChunkText:
			out.WriteString(chunk.Text)
		case provider.ChunkError:
			if chunk.Err != nil {
				return "", fmt.Errorf("completion summary: %w", chunk.Err)
			}
		}
	}
	return out.String(), nil
}

func completionSummaryProvider(cfg *config.Config) *config.ProviderEntry {
	if cfg == nil {
		return nil
	}
	if entry, ok := cfg.ResolveModel(cfg.DefaultModel); ok && entry.Configured() {
		return entry
	}
	for i := range cfg.Providers {
		candidate := &cfg.Providers[i]
		if candidate.Configured() {
			copy := *candidate
			copy.Model = candidate.DefaultModel()
			return &copy
		}
	}
	return nil
}
