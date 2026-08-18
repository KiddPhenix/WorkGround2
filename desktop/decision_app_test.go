package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/bot"
	"workground2/internal/decision"
)

func TestDecisionStateUsesEmptyArrays(t *testing.T) {
	broker, err := decision.Open("")
	if err != nil {
		t.Fatal(err)
	}
	state := (&App{decisionBroker: broker}).DecisionState()
	if state.Queue == nil || state.Deferred == nil || state.History == nil || state.Channels == nil {
		t.Fatalf("empty collections must be arrays: %+v", state)
	}
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"queue":[]`, `"deferred":[]`, `"history":[]`, `"channels":[]`} {
		if !strings.Contains(string(payload), field) {
			t.Fatalf("state JSON missing %s: %s", field, payload)
		}
	}
}

func TestInstallDecisionSkillIsIdempotentAndBacksUpChanges(t *testing.T) {
	root := t.TempDir()
	first, err := installDecisionSkill(root)
	if err != nil || !first.OK {
		t.Fatalf("first install=%+v err=%v", first, err)
	}
	skillFile := filepath.Join(first.SkillPath, "SKILL.md")
	original, err := os.ReadFile(skillFile)
	if err != nil || !strings.Contains(string(original), "Ask WorkGround2 Owner") {
		t.Fatalf("installed skill invalid: %v", err)
	}
	notifyFile := filepath.Join(root, "skills", "notify-me", "SKILL.md")
	notifyBody, err := os.ReadFile(notifyFile)
	if err != nil || !strings.Contains(string(notifyBody), "# Notify Me") {
		t.Fatalf("installed notify-me skill invalid: %v", err)
	}
	second, err := installDecisionSkill(root)
	if err != nil || len(second.Backups) != 0 {
		t.Fatalf("idempotent install=%+v err=%v", second, err)
	}
	if err := os.WriteFile(skillFile, []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := installDecisionSkill(root)
	if err != nil || len(third.Backups) != 1 {
		t.Fatalf("modified install=%+v err=%v", third, err)
	}
}

func TestDecisionInboundFirstAnswerWins(t *testing.T) {
	broker, err := decision.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.UpsertChannel(decision.Channel{ID: "owner-weixin", Name: "主人", Kind: "weixin", Enabled: true, ConnectionID: "wx-main", Domain: "weixin", ChatID: "owner", ChatType: "dm"}); err != nil {
		t.Fatal(err)
	}
	created, err := broker.Create(decision.CreateRequest{
		IdempotencyKey: "agent:hero",
		Origin:         decision.Origin{Kind: "agent", AgentID: "codex"},
		Presentation: decision.Presentation{
			Title: "确定主角图", TaskSummary: "正在制作活动页视觉", WhyNow: "批量生成前需要锁定角色策略", NoAnswerPolicy: "保持暂停",
			Questions: []decision.Question{{ID: "hero", Prompt: "复用还是新建？", Options: []decision.Option{{Label: "复用", Impact: "一致性高"}, {Label: "新建", Impact: "创意空间大"}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{decisionBroker: broker}
	msg := bot.InboundMessage{Platform: bot.PlatformWeixin, ConnectionID: "wx-main", Domain: "weixin", ChatType: bot.ChatDM, ChatID: "owner", UserID: "u1", UserName: "主人", Text: "/answer " + created.Decision.ID + " 2"}
	response, handled, err := app.handleDecisionInbound(msg)
	if err != nil || !handled || response != "✅ 已收到，你的选择是：「新建」。" {
		t.Fatalf("first reply = (%q, %v, %v)", response, handled, err)
	}
	value, ok := broker.Get(created.Decision.ID)
	if !ok || value.Status != decision.StatusDecided || decisionAnswerText(value) != "新建" {
		t.Fatalf("decision = %+v", value)
	}
	msg.UserID = "u2"
	msg.UserName = "第二个人"
	msg.Text = "/answer " + created.Decision.ID + " 1"
	response, handled, err = app.handleDecisionInbound(msg)
	if err != nil || !handled || response != "这个问题已经回答过，当前采用：「新建」。" {
		t.Fatalf("duplicate reply = (%q, %v, %v)", response, handled, err)
	}
	value, _ = broker.Get(created.Decision.ID)
	if decisionAnswerText(value) != "新建" {
		t.Fatalf("frozen answer changed: %+v", value.Answer)
	}
}

func TestDecisionMessagesHideProtocolAndSeparateOptions(t *testing.T) {
	value := decision.Decision{
		ID: "D-INTERNAL", Kind: decision.KindAsk,
		Presentation: decision.Presentation{
			Title: "选择验收时间", TaskSummary: "修复版已经完成构建。", WhyNow: "需要确定是否立即切换。", NoAnswerPolicy: "保持现状。",
			Questions: []decision.Question{{ID: "when", Prompt: "什么时候验收？", Options: []decision.Option{{Label: "现在验收", Impact: "立即切换"}, {Label: "稍后验收", Impact: "保持当前版本"}}}},
		},
	}
	message := renderDecisionDelivery(value, decision.DeliveryPresented)
	if strings.Contains(message, value.ID) || strings.Contains(message, "/answer") {
		t.Fatalf("message leaked internal protocol: %q", message)
	}
	if !strings.Contains(message, "1) 现在验收\n影响：立即切换\n\n2) 稍后验收") {
		t.Fatalf("options are not separated for IM rendering: %q", message)
	}
}

func TestNotificationMessageNeedsNoReply(t *testing.T) {
	value := decision.Decision{Kind: decision.KindNotify, Presentation: decision.Presentation{Title: "处理完成", TaskSummary: "修复版已经构建并通过检查。", WhyNow: "可以开始验收。"}}
	message := renderDecisionDelivery(value, decision.DeliveryPresented)
	if message != "【通知｜处理完成】\n\n修复版已经构建并通过检查。\n\n可以开始验收。" || strings.Contains(message, "回复") {
		t.Fatalf("notification = %q", message)
	}
	if decisionNeedsResolvedDelivery(value, "owner-weixin") {
		t.Fatal("notification must not enqueue a resolved follow-up")
	}
}

func TestResponderEndpointDoesNotReceiveDuplicateResolvedMessage(t *testing.T) {
	value := decision.Decision{Kind: decision.KindAsk, Responder: &decision.Responder{EndpointID: "owner-weixin"}}
	if decisionNeedsResolvedDelivery(value, "owner-weixin") {
		t.Fatal("answering endpoint should receive only the direct acknowledgement")
	}
	if !decisionNeedsResolvedDelivery(value, "desktop-owner") {
		t.Fatal("other presented endpoints should receive the resolved state")
	}
}

func TestInboundResponderLabelDoesNotExposeRemoteID(t *testing.T) {
	broker, _ := decision.Open("")
	_, _ = broker.UpsertChannel(decision.Channel{ID: "owner-weixin", Name: "主人", Kind: "weixin", Enabled: true, ConnectionID: "wx-main", Domain: "weixin", ChatID: "owner", ChatType: "dm"})
	created, _ := broker.Create(decision.CreateRequest{
		IdempotencyKey: "friendly-label",
		Presentation:   decision.Presentation{Title: "选择", TaskSummary: "正在处理任务", WhyNow: "需要继续", NoAnswerPolicy: "保持暂停", Questions: []decision.Question{{ID: "q", Prompt: "继续吗？", Options: []decision.Option{{Label: "继续", Impact: "继续处理"}, {Label: "暂停", Impact: "保持暂停"}}}}},
	})
	app := &App{decisionBroker: broker}
	_, _, err := app.handleDecisionInbound(bot.InboundMessage{Platform: bot.PlatformWeixin, ConnectionID: "wx-main", Domain: "weixin", ChatType: bot.ChatDM, ChatID: "owner", UserID: "raw-remote-id", UserName: "raw-remote-id@im.wechat", Text: "/answer " + created.Decision.ID + " 1"})
	if err != nil {
		t.Fatal(err)
	}
	value, _ := broker.Get(created.Decision.ID)
	if value.Responder == nil || value.Responder.Label != "微信用户" || value.Responder.ID != "raw-remote-id" {
		t.Fatalf("responder = %+v", value.Responder)
	}
}

func TestDecisionInboundIgnoresUnconfiguredChat(t *testing.T) {
	broker, _ := decision.Open("")
	app := &App{decisionBroker: broker}
	_, handled, err := app.handleDecisionInbound(bot.InboundMessage{Platform: bot.PlatformWeixin, ChatType: bot.ChatDM, ChatID: "other", Text: "1"})
	if err != nil || handled {
		t.Fatalf("unconfigured reply handled=%v err=%v", handled, err)
	}
}

func TestSaveDecisionChannelRetryIsIdempotent(t *testing.T) {
	broker, _ := decision.Open("")
	app := &App{decisionBroker: broker}
	input := DecisionChannelInput{Name: "主人", Kind: "weixin", Enabled: true, ConnectionID: "wx", Domain: "weixin", ChatID: "owner", ChatType: "dm"}
	if _, err := app.SaveDecisionChannel(input); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveDecisionChannel(input); err != nil {
		t.Fatal(err)
	}
	if got := len(broker.Channels()); got != 1 {
		t.Fatalf("channels = %d, want 1", got)
	}
}

func TestDecisionAnswerFromChoicesSupportsOrderedMultiQuestion(t *testing.T) {
	value := decision.Decision{Presentation: decision.Presentation{Questions: []decision.Question{
		{ID: "one", Options: []decision.Option{{Label: "A"}, {Label: "B"}}},
		{ID: "two", MultiSelect: true, Options: []decision.Option{{Label: "X"}, {Label: "Y"}}},
	}}}
	answer, err := decisionAnswerFromChoices(value, []string{"2", "1+2"})
	if err != nil || strings.Join(answer.Selections[0].Selected, ",") != "B" || strings.Join(answer.Selections[1].Selected, ",") != "X,Y" {
		t.Fatalf("answer=%+v err=%v", answer, err)
	}
}

func TestDecisionInboundAcceptsTextReply(t *testing.T) {
	newActiveDecision := func(t *testing.T) *App {
		t.Helper()
		broker, err := decision.Open("")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := broker.UpsertChannel(decision.Channel{ID: "owner-weixin", Name: "主人", Kind: "weixin", Enabled: true, ConnectionID: "wx-main", Domain: "weixin", ChatID: "owner", ChatType: "dm"}); err != nil {
			t.Fatal(err)
		}
		if _, err := broker.Create(decision.CreateRequest{
			IdempotencyKey: "agent:text-reply",
			Origin:         decision.Origin{Kind: "agent", AgentID: "codex"},
			Presentation: decision.Presentation{
				Title: "确定主角图", TaskSummary: "正在制作活动页视觉", WhyNow: "批量生成前需要锁定角色策略", NoAnswerPolicy: "保持暂停",
				Questions: []decision.Question{{ID: "hero", Prompt: "复用还是新建？", Options: []decision.Option{{Label: "复用", Impact: "一致性高"}, {Label: "新建", Impact: "创意空间大"}}}},
			},
		}); err != nil {
			t.Fatal(err)
		}
		return &App{decisionBroker: broker}
	}

	cases := []struct {
		name    string
		text    string
		handled bool
		want    string
	}{
		{name: "exact label", text: "复用", handled: true, want: "复用"},
		{name: "label with spaces", text: "选 复用", handled: true, want: "复用"},
		{name: "chinese numeral", text: "选第二个", handled: true, want: "新建"},
		{name: "option prefix", text: "选项2", handled: true, want: "新建"},
		{name: "full-width digits", text: "１", handled: true, want: "复用"},
		{name: "unrelated text", text: "随便吧", handled: false, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newActiveDecision(t)
			msg := bot.InboundMessage{Platform: bot.PlatformWeixin, ConnectionID: "wx-main", Domain: "weixin", ChatType: bot.ChatDM, ChatID: "owner", UserID: "u1", UserName: "主人", Text: tc.text}
			response, handled, err := app.handleDecisionInbound(msg)
			if handled != tc.handled {
				t.Fatalf("handled = %v, want %v (response=%q err=%v)", handled, tc.handled, response, err)
			}
			if !handled {
				return
			}
			if err != nil {
				t.Fatalf("reply error = %v", err)
			}
			want := "✅ 已收到，你的选择是：「" + tc.want + "」。"
			if response != want {
				t.Fatalf("response = %q, want %q", response, want)
			}
		})
	}
}

func TestMatchOptionReplyPrecedenceAndAmbiguity(t *testing.T) {
	question := decision.Question{Options: []decision.Option{{Label: "发送"}, {Label: "接收"}, {Label: "发送并接收"}}}
	// 精确匹配优先于包含匹配："发送" 不应被 "发送并接收" 拖入歧义。
	choices, handled, err := matchOptionReply(question, "发送")
	if !handled || err != nil || len(choices) != 1 || choices[0] != "1" {
		t.Fatalf("发送 = %v handled=%v err=%v", choices, handled, err)
	}
	// "接收" 同时被 "发送并接收" 包含，唯一精确命中仍应胜出。
	choices, handled, err = matchOptionReply(question, "接收")
	if !handled || err != nil || len(choices) != 1 || choices[0] != "2" {
		t.Fatalf("接收 = %v handled=%v err=%v", choices, handled, err)
	}
	// 包含匹配歧义：短文字命中多个选项时报错。
	choices, handled, err = matchOptionReply(question, "发送接收")
	if !handled || err == nil {
		t.Fatalf("ambiguous reply handled=%v err=%v choices=%v", handled, err, choices)
	}
	// 包含匹配唯一命中。
	choices, handled, err = matchOptionReply(question, "发送到微信")
	if !handled || err != nil || len(choices) != 1 || choices[0] != "1" {
		t.Fatalf("发送到微信 = %v handled=%v err=%v", choices, handled, err)
	}
	// 完全不匹配：不是决策回答。
	choices, handled, err = matchOptionReply(question, "今天天气不错")
	if handled || err != nil {
		t.Fatalf("unrelated reply handled=%v err=%v choices=%v", handled, err, choices)
	}
	// 序号形式。
	choices, handled, err = matchOptionReply(question, "第2个")
	if !handled || err != nil || len(choices) != 1 || choices[0] != "2" {
		t.Fatalf("第2个 = %v handled=%v err=%v", choices, handled, err)
	}
}
