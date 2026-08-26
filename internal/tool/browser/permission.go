package browsertool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"workground2/internal/browser"
)

const (
	permissionOrdinary = "ordinary"
	permissionPublish  = "publish"
	permissionDelete   = "delete"
	permissionPayment  = "payment"
	permissionSecret   = "secret"
	permissionPrivate  = "private"
)

var (
	deleteWords = []string{
		"delete", "remove", "trash", "discard", "erase",
		"删除", "移除", "清空", "丢弃", "销毁",
	}
	paymentWords = []string{
		"buy", "purchase", "pay", "checkout", "subscribe", "place order", "donate", "tip",
		"购买", "支付", "付款", "结账", "订阅", "下单", "打赏", "捐赠",
	}
	publishWords = []string{
		"publish", "post", "reply", "comment", "submit", "send", "create topic", "create thread",
		"like", "upvote", "vote", "follow", "share",
		"发布", "发表", "发帖", "回复", "评论", "提交", "发送", "创建主题", "创建帖子",
		"点赞", "投票", "关注", "分享",
	}
	secretWords = []string{
		"password", "passwd", "passcode", "secret", "token", "api key", "apikey", "authorization", "otp", "verification code",
		"密码", "口令", "密钥", "令牌", "验证码",
	}
	privateWords = []string{
		"email", "e-mail", "phone", "mobile", "address", "id card", "identity", "real name",
		"邮箱", "邮件地址", "手机号", "手机号码", "电话", "地址", "身份证", "真实姓名",
	}
	searchWords = []string{"search", "find", "query", "搜索", "查找", "检索"}
)

func (t *clickTool) PermissionSubject(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Revision uint64 `json:"revision"`
		Index    int    `json:"index"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("decode browser_click permission target: %w", err)
	}
	element, err := permissionElement(ctx, t.svc, input.Revision, input.Index)
	if err != nil {
		return "", err
	}
	text := elementPermissionText(element)
	switch {
	case containsPermissionWord(text, deleteWords):
		return permissionDelete, nil
	case containsPermissionWord(text, paymentWords):
		return permissionPayment, nil
	case containsPermissionWord(text, publishWords), strings.EqualFold(strings.TrimSpace(element.InputType), "submit"):
		return permissionPublish, nil
	default:
		return permissionOrdinary, nil
	}
}

func (t *typeTool) PermissionSubject(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Revision   uint64 `json:"revision"`
		Index      int    `json:"index"`
		PressEnter bool   `json:"press_enter"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("decode browser_type permission target: %w", err)
	}
	element, err := permissionElement(ctx, t.svc, input.Revision, input.Index)
	if err != nil {
		return "", err
	}
	text := elementPermissionText(element)
	inputType := strings.ToLower(strings.TrimSpace(element.InputType))
	switch {
	case inputType == "password", containsPermissionWord(text, secretWords):
		return permissionSecret, nil
	case containsPermissionWord(text, privateWords):
		return permissionPrivate, nil
	case input.PressEnter && inputType != "search" && !containsPermissionWord(text, searchWords):
		return permissionPublish, nil
	default:
		return permissionOrdinary, nil
	}
}

func permissionElement(ctx context.Context, svc browser.Service, revision uint64, index int) (browser.Element, error) {
	if revision < 1 || index < 1 {
		return browser.Element{}, errors.New("browser permission target requires revision and index >= 1")
	}
	owner := ownerFromContext(ctx)
	if owner == "" {
		return browser.Element{}, errors.New("browser permission target has no parent session scope")
	}
	state, err := svc.State(ctx, owner, browser.StateRequest{Revision: &revision})
	if err != nil {
		return browser.Element{}, fmt.Errorf("read browser permission target: %w", err)
	}
	for _, element := range state.Elements {
		if element.Index == index {
			return element, nil
		}
	}
	return browser.Element{}, fmt.Errorf("browser permission target element %d not found at revision %d", index, revision)
}

func elementPermissionText(element browser.Element) string {
	return normalizePermissionText(strings.Join([]string{
		element.Role, element.Tag, element.InputType, element.Name, element.Placeholder,
	}, " "))
}

func normalizePermissionText(value string) string {
	return strings.ToLower(strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	}), " "))
}

func containsPermissionWord(value string, words []string) bool {
	for _, word := range words {
		if strings.Contains(value, normalizePermissionText(word)) {
			return true
		}
	}
	return false
}
