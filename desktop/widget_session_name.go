package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"workground2/internal/boot"
	"workground2/internal/config"
	"workground2/internal/provider"
)

const (
	widgetSessionNameChineseRunes = 5
	widgetSessionNameEnglishRunes = 10
	widgetSessionNameTimeout      = 30 * time.Second
)

const widgetSessionNameSystemPrompt = `你是 WorkGround2 的会话命名器。根据用户任务生成一个一眼可识别的短名：中文最多 5 个字，不含中文时最多 10 个字符。避免空泛的“新会话”“新任务”，且不能与现场名称重复。只输出名称本身，不要 Markdown、引号、标点或解释。`

type widgetSessionNameRequest struct {
	Prompt         string
	WorkspaceRoot  string
	Model          string
	ExistingTitles []string
}

type widgetSessionNameGenerator interface {
	Generate(context.Context, widgetSessionNameRequest) (string, error)
}

func (a *App) generateUniqueWidgetSessionName(prompt, workspaceRoot, model string, optimisticTitles []string) (string, error) {
	existing := widgetSessionIconTitles(a.GetDesktopIconSnapshot())
	existing = mergeWidgetSessionTitles(existing, optimisticTitles)
	req := widgetSessionNameRequest{
		Prompt:         prompt,
		WorkspaceRoot:  workspaceRoot,
		Model:          model,
		ExistingTitles: existing,
	}
	ctx, cancel := context.WithTimeout(a.bootContext(), widgetSessionNameTimeout)
	defer cancel()

	var (
		raw string
		err error
	)
	if a.widgetSessionNameGen != nil {
		raw, err = a.widgetSessionNameGen.Generate(ctx, req)
	} else {
		raw, err = a.generateWidgetSessionName(ctx, req)
	}
	if err != nil {
		return "", fmt.Errorf("生成会话名称: %w", err)
	}
	candidate, err := cleanWidgetSessionName(raw)
	if err != nil {
		return "", fmt.Errorf("生成会话名称: %w", err)
	}
	return uniqueWidgetSessionName(candidate, existing)
}

func mergeWidgetSessionTitles(groups ...[]string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, group := range groups {
		for _, title := range group {
			title = strings.TrimSpace(title)
			key := widgetSessionNameKey(title)
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, title)
		}
	}
	sort.Slice(out, func(i, j int) bool { return widgetSessionNameKey(out[i]) < widgetSessionNameKey(out[j]) })
	return out
}

func widgetSessionIconTitles(snapshot DesktopIconSnapshot) []string {
	titles := make([]string, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		titles = append(titles, item.Title)
	}
	return mergeWidgetSessionTitles(titles)
}

func buildWidgetSessionNamePrompt(req widgetSessionNameRequest) string {
	titles := req.ExistingTitles
	if len(titles) > 40 {
		titles = titles[:40]
	}
	existing := "（无）"
	if len(titles) > 0 {
		bounded := make([]string, 0, len(titles))
		for _, title := range titles {
			bounded = append(bounded, conciseWidgetText(title, 30))
		}
		existing = strings.Join(bounded, "、")
	}
	return "用户任务：" + conciseWidgetText(req.Prompt, 800) + "\n现场已有名称：" + existing
}

func cleanWidgetSessionName(raw string) (string, error) {
	raw = strings.ReplaceAll(raw, "`", "")
	line := ""
	for _, candidate := range strings.Split(raw, "\n") {
		if strings.TrimSpace(candidate) != "" {
			line = candidate
			break
		}
	}
	line = strings.TrimSpace(line)
	for _, prefix := range []string{"名称：", "名称:", "Name:", "name:"} {
		line = strings.TrimSpace(strings.TrimPrefix(line, prefix))
	}
	line = strings.Join(strings.Fields(line), " ")
	line = strings.Trim(line, " \t\r\n，。！？；：、,.!?;:\"'“”‘’()[]{}【】-_")
	if line == "" {
		return "", errors.New("模型返回了空名称")
	}
	limit := widgetSessionNameEnglishRunes
	if containsHan(line) {
		limit = widgetSessionNameChineseRunes
	}
	runes := []rune(line)
	if len(runes) > limit {
		line = strings.TrimSpace(string(runes[:limit]))
	}
	if line == "" {
		return "", errors.New("模型返回的名称无效")
	}
	return line, nil
}

func uniqueWidgetSessionName(candidate string, existing []string) (string, error) {
	used := make(map[string]struct{}, len(existing))
	for _, title := range existing {
		if key := widgetSessionNameKey(title); key != "" {
			used[key] = struct{}{}
		}
	}
	if _, collision := used[widgetSessionNameKey(candidate)]; !collision {
		return candidate, nil
	}
	limit := widgetSessionNameEnglishRunes
	if containsHan(candidate) {
		limit = widgetSessionNameChineseRunes
	}
	for suffix := 2; suffix <= 999; suffix++ {
		tail := fmt.Sprint(suffix)
		prefixLimit := limit - len([]rune(tail))
		if prefixLimit <= 0 {
			break
		}
		prefix := []rune(candidate)
		if len(prefix) > prefixLimit {
			prefix = prefix[:prefixLimit]
		}
		next := strings.TrimSpace(string(prefix)) + tail
		if _, collision := used[widgetSessionNameKey(next)]; !collision {
			return next, nil
		}
	}
	return "", errors.New("无法在短名长度限制内生成唯一名称")
}

func widgetSessionNameKey(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), ""))
}

func containsHan(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
}

func (a *App) generateWidgetSessionName(ctx context.Context, req widgetSessionNameRequest) (string, error) {
	cfg, err := config.LoadForRoot(req.WorkspaceRoot)
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	entry, ok := cfg.ResolveModel(strings.TrimSpace(req.Model))
	if !ok || entry == nil || !entry.Configured() {
		return "", fmt.Errorf("模型 %q 未配置", req.Model)
	}
	prov, err := boot.NewProvider(entry)
	if err != nil {
		return "", fmt.Errorf("build provider %s: %w", entry.Name, err)
	}
	chunks, err := prov.Stream(ctx, provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: widgetSessionNameSystemPrompt},
			{Role: provider.RoleUser, Content: buildWidgetSessionNamePrompt(req)},
		},
		Temperature: 0.3,
		MaxTokens:   1024,
	})
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for chunk := range chunks {
		switch chunk.Type {
		case provider.ChunkText:
			out.WriteString(chunk.Text)
		case provider.ChunkError:
			if chunk.Err != nil {
				return "", chunk.Err
			}
		}
	}
	return out.String(), nil
}

func (a *App) applyWidgetSessionName(tabID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("会话名称为空")
	}
	a.mu.RLock()
	tab := a.tabs[strings.TrimSpace(tabID)]
	if tab == nil {
		a.mu.RUnlock()
		return errors.New("新会话不存在")
	}
	scope, workspaceRoot, topicID := tab.Scope, tab.WorkspaceRoot, tab.TopicID
	sessionPath := tab.currentSessionPath()
	a.mu.RUnlock()
	if strings.TrimSpace(topicID) == "" || strings.TrimSpace(sessionPath) == "" {
		return errors.New("新会话缺少 topic 或 session path")
	}
	titleRoot := topicTitleRoot(scope, workspaceRoot)
	if err := setTopicTitleWithSource(titleRoot, topicID, name, topicTitleSourceAuto); err != nil {
		return err
	}
	if err := setSessionTitle(filepath.Dir(sessionPath), sessionPath, name); err != nil {
		return err
	}

	a.mu.Lock()
	if current := a.tabs[tabID]; current != nil && current.TopicID == topicID && current.currentSessionPath() == sessionPath {
		current.TopicTitle = name
		a.saveTabsLocked()
	}
	a.mu.Unlock()
	a.updateTopicSessionTitles(topicID, name)
	a.emitProjectTreeChanged()
	return nil
}
