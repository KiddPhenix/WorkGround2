package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"workground2/internal/bot"
	"workground2/internal/decision"
	"workground2/internal/event"
)

// TestOwnerDecisionFeatureDisabledByDefault: the master kill switch is off by
// default, and NewApp must not initialize the broker while disabled so no
// persisted broker/channel data is touched.
func TestOwnerDecisionFeatureDisabledByDefault(t *testing.T) {
	if ownerDecisionFeatureEnabled {
		t.Fatal("ownerDecisionFeatureEnabled must default to false (temporary kill switch)")
	}
	if (&App{}).ownerDecisionActive() {
		t.Fatal("nil/zero App must report owner decision feature as inactive")
	}
	app := NewApp()
	if app.ownerDecisionActive() {
		t.Fatal("NewApp must start with the owner decision feature disabled")
	}
	if app.decisionBroker != nil {
		t.Fatal("decision broker must not be initialized while the feature is disabled")
	}
}

// TestOwnerDecisionDisabledAskStaysInSession: with the gate off, an AskRequest
// must keep flowing through tabEventSink.Emit into the source Session instead
// of being captured by the decision broker (observeDecisionAsk returns true).
func TestOwnerDecisionDisabledAskStaysInSession(t *testing.T) {
	app := &App{ownerDecisionEnabled: false}
	sink := &tabEventSink{tabID: "tab-a", ctx: context.Background(), app: app}
	delivered := make(chan string, 2)
	sink.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		if name != eventChannel {
			return
		}
		if wire, ok := payload[0].(wireEventTab); ok {
			delivered <- wire.Kind
		}
	}
	ask := event.Ask{
		ID: "ask-1",
		Questions: []event.AskQuestion{{
			ID: "q1", Prompt: "继续？",
			Options: []event.AskOption{{Label: "A"}, {Label: "B"}},
		}},
	}
	sink.Emit(event.Event{Kind: event.AskRequest, Ask: ask})
	select {
	case kind := <-delivered:
		if kind != "ask_request" {
			t.Fatalf("delivered event kind = %s, want ask_request", kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AskRequest was swallowed and never reached the source Session")
	}
}

// TestOwnerDecisionDisabledAskNotCapturedEvenWithBroker: the gate is the
// authoritative switch — even a manually injected broker must not capture asks.
func TestOwnerDecisionDisabledAskNotCapturedEvenWithBroker(t *testing.T) {
	broker, err := decision.Open("")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{ownerDecisionEnabled: false, decisionBroker: broker}
	if !app.observeDecisionAsk("tab-a", event.Ask{ID: "ask-2"}) {
		t.Fatal("observeDecisionAsk must return true (keep ask in Session) while disabled")
	}
	if got := broker.List(decision.ListFilter{}); len(got) != 0 {
		t.Fatalf("disabled feature must not register asks in the broker, got %d", len(got))
	}
	state := app.DecisionState()
	if state.Available {
		t.Fatal("DecisionState must report unavailable while the feature is disabled")
	}
	if _, err := app.CreateDecision(DecisionCreateInput{Title: "x"}); err == nil {
		t.Fatal("CreateDecision must fail closed while the feature is disabled")
	}
	app.startDecisionRuntime()
	if app.decisionCancel != nil {
		t.Fatal("disabled feature must not start the decision runtime even with an injected broker")
	}
}

// TestOwnerDecisionDisabledRemoteRoutesUnavailable: with the gate off,
// /api/v1/decisions/* must not be registered (404), while ordinary session
// routes stay available.
func TestOwnerDecisionDisabledRemoteRoutesUnavailable(t *testing.T) {
	mux := http.NewServeMux()
	(&remoteAPI{app: &App{ownerDecisionEnabled: false}}).registerRoutes(mux)

	for _, path := range []string{
		"/api/v1/decisions/create",
		"/api/v1/decisions/get",
		"/api/v1/decisions/list",
		"/api/v1/decisions/wait",
		"/api/v1/decisions/cancel",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if _, pattern := mux.Handler(req); pattern != "" {
			t.Fatalf("decision route %s registered while disabled (pattern %q)", path, pattern)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("decision route %s status = %d, want 404", path, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/status", nil)
	if _, pattern := mux.Handler(req); pattern == "" {
		t.Fatal("ordinary session route disappeared while the decision feature is disabled")
	}
}

// TestOwnerDecisionEnabledRemoteRoutesRegistered: when the gate is on the
// decision routes are registered again (restoration path).
func TestOwnerDecisionEnabledRemoteRoutesRegistered(t *testing.T) {
	mux := http.NewServeMux()
	(&remoteAPI{app: &App{ownerDecisionEnabled: true}}).registerRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/decisions/create", nil)
	if _, pattern := mux.Handler(req); pattern == "" {
		t.Fatal("decision routes must be registered when the feature is enabled")
	}
}

// TestOwnerDecisionDisabledBotHandlerAbsent: with the gate off the bot gateway
// receives a nil decision handler, so /answer decision replies are not
// intercepted (gateway.go only intercepts when HandleDecision != nil).
func TestOwnerDecisionDisabledBotHandlerAbsent(t *testing.T) {
	app := &App{ownerDecisionEnabled: false}
	if handler := app.decisionInboundHandler(); handler != nil {
		t.Fatal("bot decision handler must be nil while the feature is disabled")
	}
	enabled := &App{ownerDecisionEnabled: true}
	if handler := enabled.decisionInboundHandler(); handler == nil {
		t.Fatal("bot decision handler must be wired when the feature is enabled")
	}
}

// TestOwnerDecisionDisabledNoAutoNotify: with the gate off, TurnDone must not
// create an automatic owner notify in the broker.
func TestOwnerDecisionDisabledNoAutoNotify(t *testing.T) {
	broker, err := decision.Open("")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{ownerDecisionEnabled: false, decisionBroker: broker}
	app.observeOwnerNotifyEvent(&tabEventSink{tabID: "tab-a"}, event.Event{Kind: event.TurnDone})
	if got := broker.List(decision.ListFilter{}); len(got) != 0 {
		t.Fatalf("disabled feature must not create owner notifications, got %d", len(got))
	}
}

// TestOwnerDecisionDisabledBotConversationPreserved: an ordinary IM inbound
// message stays a normal conversation — the decision handler being nil means
// the gateway never treats it as a decision reply, and handleDecisionInbound
// itself stays a no-op when the broker is absent.
func TestOwnerDecisionDisabledBotConversationPreserved(t *testing.T) {
	app := &App{ownerDecisionEnabled: false}
	response, handled, err := app.handleDecisionInbound(bot.InboundMessage{
		Platform: bot.PlatformWeixin, ConnectionID: "wx-main", Domain: "weixin",
		ChatType: bot.ChatDM, ChatID: "owner", Text: "/answer d-1 1",
	})
	if err != nil {
		t.Fatalf("ordinary bot message must not error: %v", err)
	}
	if handled {
		t.Fatalf("ordinary bot message must not be intercepted as a decision reply (response=%q)", response)
	}
}
