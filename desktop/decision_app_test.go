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
	if err != nil || !handled || !strings.Contains(response, "已记录") {
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
	if err != nil || !handled || !strings.Contains(response, "已由主人回答") {
		t.Fatalf("duplicate reply = (%q, %v, %v)", response, handled, err)
	}
	value, _ = broker.Get(created.Decision.ID)
	if decisionAnswerText(value) != "新建" {
		t.Fatalf("frozen answer changed: %+v", value.Answer)
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
