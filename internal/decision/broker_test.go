package decision

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func validRequest(key, title string) CreateRequest {
	return CreateRequest{
		IdempotencyKey: key,
		Origin:         Origin{Kind: "desktop", WorkspaceRoot: "C:/workspace", SessionID: key},
		Presentation: Presentation{
			Title:          title,
			TaskSummary:    "正在准备角色镜头",
			WhyNow:         "需要确定素材策略后才能继续",
			NoAnswerPolicy: "暂停任务，不自动选择",
			Questions: []Question{{
				ID: "q1", Header: "素材", Prompt: "如何处理主角图？",
				Options: []Option{{Label: "复用", Impact: "保持角色一致"}, {Label: "新生成", Impact: "更匹配镜头但可能变脸"}},
			}},
			Recommendation: &Recommendation{QuestionID: "q1", Option: "复用", Reason: "一致性优先"},
		},
	}
}

func answer(label string) Answer {
	return Answer{Selections: []Selection{{QuestionID: "q1", Selected: []string{label}}}}
}

func TestBrokerSerializesAndResolvesFirstAnswer(t *testing.T) {
	b, err := Open(filepath.Join(t.TempDir(), "decision.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := b.Create(validRequest("one", "One"))
	if err != nil || first.Decision.Status != StatusPresented {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	second, err := b.Create(validRequest("two", "Two"))
	if err != nil || second.Decision.Status != StatusQueued {
		t.Fatalf("second = %+v err=%v", second, err)
	}
	resolved, err := b.Resolve(first.Decision.ID, answer("复用"), Responder{Kind: "desktop", Label: "主人"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Decision.Status != StatusDecided || resolved.Promoted == nil || resolved.Promoted.ID != second.Decision.ID {
		t.Fatalf("resolve = %+v", resolved)
	}
	duplicate, err := b.Resolve(first.Decision.ID, answer("新生成"), Responder{Kind: "weixin"})
	if err != nil || !duplicate.AlreadyResolved || duplicate.Decision.Answer.Selections[0].Selected[0] != "复用" {
		t.Fatalf("duplicate resolve = %+v err=%v", duplicate, err)
	}
}

func TestBrokerCreateIsIdempotentAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decision.json")
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := b.Create(validRequest("stable", "Stable"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := b.Create(validRequest("stable", "Different"))
	if err != nil || !again.Duplicate || again.Decision.ID != first.Decision.ID || again.Decision.Presentation.Title != "Stable" {
		t.Fatalf("duplicate = %+v err=%v", again, err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	active, ok := reopened.Active()
	if !ok || active.ID != first.Decision.ID {
		t.Fatalf("active after reopen = %+v ok=%v", active, ok)
	}
}

func TestBrokerNotificationIsDurableAndDoesNotOccupyQueue(t *testing.T) {
	b, err := Open(filepath.Join(t.TempDir(), "decision.json"))
	if err != nil {
		t.Fatal(err)
	}
	notice, err := b.Create(CreateRequest{
		IdempotencyKey: "build-ready",
		Kind:           KindNotify,
		Origin:         Origin{Kind: "agent", AgentID: "codex"},
		Presentation:   Presentation{Title: "构建完成", TaskSummary: "修复版已经完成构建，可以开始验收。"},
	})
	if err != nil || notice.Decision.Status != StatusApplied || notice.Decision.Kind != KindNotify || notice.Decision.AppliedAt == nil {
		t.Fatalf("notification = %+v err=%v", notice, err)
	}
	if _, ok := b.Active(); ok {
		t.Fatal("notification must not occupy the active decision slot")
	}
	asked, err := b.Create(validRequest("after-notice", "需要选择"))
	if err != nil || asked.Decision.Status != StatusPresented {
		t.Fatalf("decision after notification = %+v err=%v", asked, err)
	}
	again, err := b.Create(CreateRequest{IdempotencyKey: "build-ready", Kind: KindNotify, Presentation: Presentation{Title: "重复", TaskSummary: "重复"}})
	if err != nil || !again.Duplicate || again.Decision.ID != notice.Decision.ID {
		t.Fatalf("duplicate notification = %+v err=%v", again, err)
	}
}

func TestBrokerRejectsNotificationQuestions(t *testing.T) {
	b, _ := Open("")
	req := validRequest("bad-notice", "Bad")
	req.Kind = KindNotify
	if _, err := b.Create(req); err == nil {
		t.Fatal("notification with questions should fail")
	}
}

func TestBrokerDeferReleasesAttentionWithoutAnswering(t *testing.T) {
	b, _ := Open("")
	first, _ := b.Create(validRequest("one", "One"))
	second, _ := b.Create(validRequest("two", "Two"))
	transition, err := b.Defer(first.Decision.ID)
	if err != nil || transition.Decision.Status != StatusDeferred || transition.Promoted == nil || transition.Promoted.ID != second.Decision.ID {
		t.Fatalf("defer = %+v err=%v", transition, err)
	}
	if _, err := b.Resolve(first.Decision.ID, answer("复用"), Responder{Kind: "weixin"}); err != nil {
		t.Fatalf("deferred decision remains answerable: %v", err)
	}
}

func TestBrokerRejectsUnreadableQuestion(t *testing.T) {
	b, _ := Open("")
	req := validRequest("bad", "Bad")
	req.Presentation.TaskSummary = ""
	if _, err := b.Create(req); err == nil {
		t.Fatal("missing human context should fail")
	}
	req = validRequest("bad-option", "Bad")
	req.Presentation.Questions[0].Options[0].Impact = ""
	if _, err := b.Create(req); err == nil {
		t.Fatal("missing option impact should fail")
	}
}

func TestBrokerOrdersEndpointDeliveryAndRetries(t *testing.T) {
	b, _ := Open("")
	created, _ := b.Create(validRequest("delivery", "Delivery"))
	first, duplicate, err := b.EnqueueDelivery("weixin", created.Decision.ID, DeliveryPresented)
	if err != nil || duplicate || first.Sequence != 1 {
		t.Fatalf("first delivery = %+v duplicate=%v err=%v", first, duplicate, err)
	}
	again, duplicate, err := b.EnqueueDelivery("weixin", created.Decision.ID, DeliveryPresented)
	if err != nil || !duplicate || again.ID != first.ID {
		t.Fatalf("duplicate delivery = %+v duplicate=%v err=%v", again, duplicate, err)
	}
	retryAt := time.Now().Add(time.Minute)
	failed, err := b.CompleteDelivery(first.ID, "", errors.New("network"), retryAt)
	if err != nil || failed.Status != DeliveryFailed || failed.Attempts != 1 {
		t.Fatalf("failed delivery = %+v err=%v", failed, err)
	}
	if _, ok := b.NextDelivery("weixin", time.Now()); ok {
		t.Fatal("delivery should wait for retry time")
	}
	if next, ok := b.NextDelivery("weixin", retryAt.Add(time.Second)); !ok || next.ID != first.ID {
		t.Fatalf("retry delivery = %+v ok=%v", next, ok)
	}
}

func TestExternalDeliveryModes(t *testing.T) {
	b, _ := Open("")
	now := time.Now().UTC()
	presented := now.Add(-time.Minute)
	if !b.ExternalDeliveryEnabled(now, true, &presented) {
		t.Fatal("smart grace elapsed should deliver")
	}
	until := now.Add(time.Hour)
	if _, err := b.SetSettings(Settings{ExternalMode: ExternalLocalOnly, LocalOnlyUntil: &until}); err != nil {
		t.Fatal(err)
	}
	if b.ExternalDeliveryEnabled(now, false, &presented) {
		t.Fatal("local-only should suppress before deadline")
	}
	if !b.ExternalDeliveryEnabled(until.Add(time.Second), false, &presented) {
		t.Fatal("local-only should auto-expire")
	}
}

func TestLocalOnlyWithoutDeadlineStaysDisabled(t *testing.T) {
	b, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetSettings(Settings{ExternalMode: ExternalLocalOnly, SmartGrace: time.Second}); err != nil {
		t.Fatal(err)
	}
	if b.ExternalDeliveryEnabled(time.Now().Add(24*time.Hour), false, nil) {
		t.Fatal("local-only without a deadline must stay disabled")
	}
}

func TestAuditRecordsDecisionAndDeliveryFailures(t *testing.T) {
	b, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	created, err := b.Create(validRequest("audit-key", "Audit"))
	if err != nil {
		t.Fatal(err)
	}
	delivery, _, err := b.EnqueueDelivery("owner", created.Decision.ID, DeliveryPresented)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.CompleteDelivery(delivery.ID, "", errors.New("network down"), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Resolve(created.Decision.ID, answer("复用"), Responder{Kind: "desktop", Label: "owner"}); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, entry := range b.Snapshot().Audit {
		kinds[entry.Kind] = true
	}
	for _, want := range []string{"created", "delivery_queued", "delivery_failed", "resolved"} {
		if !kinds[want] {
			t.Fatalf("audit kinds = %#v, missing %q", kinds, want)
		}
	}
}
