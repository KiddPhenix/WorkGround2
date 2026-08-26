package assistantchannel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workground2/internal/assistant"
)

func TestDiscoursePublishesWithAuthAndCollectsMetrics(t *testing.T) {
	var posted url.Values
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Api-Key") != "secret" || r.Header.Get("Api-Username") != "promoter" {
			t.Errorf("auth headers missing")
		}
		switch r.URL.Path {
		case "/posts.json":
			_ = r.ParseForm()
			posted = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":7,"topic_id":42,"post_url":"/t/hello/42/1"}`))
		case "/t/42.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"views":120,"posts_count":5,"like_count":9}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	adapter := NewDiscourse(server.Client())
	channel := assistant.ChannelBinding{Kind: assistant.ChannelDiscourse, BaseURL: server.URL, Username: "promoter", CategoryID: 3}
	result, err := adapter.Publish(context.Background(), channel, "secret", assistant.ChannelAction{Kind: assistant.ChannelCreateTopic, Title: "Launch", Body: "Hello"})
	if err != nil || result.TopicID != 42 || posted.Get("category") != "3" || posted.Get("raw") != "Hello" {
		t.Fatalf("publish=%+v posted=%v err=%v", result, posted, err)
	}
	metric, err := adapter.Collect(context.Background(), channel, "secret", 42)
	if err != nil || metric.Views != 120 || metric.Likes != 9 || metric.Replies != 4 {
		t.Fatalf("metric=%+v err=%v", metric, err)
	}
}

func TestDiscourseClassifiesHTTPFailureKnownAndTransportFailureUnknown(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "denied", http.StatusForbidden) }))
	defer server.Close()
	channel := assistant.ChannelBinding{Kind: assistant.ChannelDiscourse, BaseURL: server.URL, Username: "bot"}
	_, err := NewDiscourse(server.Client()).Publish(context.Background(), channel, "secret", assistant.ChannelAction{Kind: assistant.ChannelCreateTopic, Title: "x", Body: "y"})
	var delivery *DeliveryError
	if !errors.As(err, &delivery) || !delivery.OutcomeKnown {
		t.Fatalf("HTTP error=%v", err)
	}
	server.Close()
	_, err = NewDiscourse(server.Client()).Publish(context.Background(), channel, "secret", assistant.ChannelAction{Kind: assistant.ChannelCreateTopic, Title: "x", Body: "y"})
	if !errors.As(err, &delivery) || delivery.OutcomeKnown {
		t.Fatalf("transport error=%v", err)
	}
}

func TestServicePersistsBeforePublishAndDoesNotReplayUnknown(t *testing.T) {
	store, err := assistant.NewStore(filepath.Join(t.TempDir(), "assistants"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	snapshot, err := store.Create(assistant.CreateInput{RequestID: "create", Assistant: assistant.Assistant{ID: "assistant-promo", Name: "Promo", Mission: "promote", Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy()}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijacker := w.(http.Hijacker)
		conn, _, _ := hijacker.Hijack()
		_ = conn.Close()
	}))
	defer server.Close()
	channel, err := store.PutChannel(assistant.PutChannelInput{RequestID: "put", Channel: assistant.ChannelBinding{ID: "channel-discourse", AssistantID: snapshot.Assistant.ID, Name: "Forum", Kind: assistant.ChannelDiscourse, BaseURL: server.URL, Username: "bot", CredentialKey: "ASSISTANT_CHANNEL_PROMO_KEY", CollectIntervalSeconds: 3600, Enabled: true}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	service, _ := New(store, func(string) string { return "secret" }, NewDiscourse(server.Client()))
	service.now = func() time.Time { return now }
	action, err := service.Publish(context.Background(), snapshot.Assistant.ID, "run-1", channel.ID, assistant.ChannelCreateTopic, "Title", "Body", 0)
	if !errors.Is(err, ErrOutcomeUnknown) || action.State != assistant.ChannelActionUnknown {
		t.Fatalf("first action=%+v err=%v", action, err)
	}
	replayed, err := service.Publish(context.Background(), snapshot.Assistant.ID, "run-1", channel.ID, assistant.ChannelCreateTopic, "Title", "Body", 0)
	if !errors.Is(err, ErrOutcomeUnknown) || replayed.ID != action.ID || replayed.State != assistant.ChannelActionUnknown {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	data, _ := store.Get(snapshot.Assistant.ID)
	if len(data.ChannelActions) != 1 || strings.Contains(data.ChannelActions[0].Error, "secret") {
		t.Fatalf("ledger=%+v", data.ChannelActions)
	}
}

func TestServiceConvertsInterruptedExecutingActionToUnknown(t *testing.T) {
	store, err := assistant.NewStore(filepath.Join(t.TempDir(), "assistants"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 2, 0, 0, 0, time.UTC)
	snapshot, err := store.Create(assistant.CreateInput{RequestID: "create", Assistant: assistant.Assistant{ID: "assistant-promo", Name: "Promo", Mission: "promote", Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy()}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := store.PutChannel(assistant.PutChannelInput{RequestID: "put", Channel: assistant.ChannelBinding{ID: "channel-discourse", AssistantID: snapshot.Assistant.ID, Name: "Forum", Kind: assistant.ChannelDiscourse, BaseURL: "https://forum.example.com", Username: "bot", CredentialKey: "ASSISTANT_CHANNEL_PROMO_KEY", CollectIntervalSeconds: 3600, Enabled: true}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	service, _ := New(store, func(string) string { return "secret" })
	service.now = func() time.Time { return now }
	intent := struct {
		AssistantID, RunID, ChannelID string
		Kind                          assistant.ChannelActionKind
		Title, Body                   string
		TopicID                       int
	}{snapshot.Assistant.ID, "run-1", channel.ID, assistant.ChannelCreateTopic, "Title", "Body", 0}
	raw, _ := json.Marshal(intent)
	requestID := assistant.StableID("channel-request", string(raw))
	action, created, err := store.BeginChannelAction(assistant.BeginChannelActionInput{AssistantID: snapshot.Assistant.ID, ChannelID: channel.ID, RunID: "run-1", RequestID: requestID, Kind: assistant.ChannelCreateTopic, Title: "Title", Body: "Body", Now: now})
	if err != nil || !created || action.State != assistant.ChannelActionExecuting {
		t.Fatalf("begin=%+v created=%v err=%v", action, created, err)
	}
	replayed, err := service.Publish(context.Background(), snapshot.Assistant.ID, "run-1", channel.ID, assistant.ChannelCreateTopic, "Title", "Body", 0)
	if !errors.Is(err, ErrOutcomeUnknown) || replayed.State != assistant.ChannelActionUnknown || replayed.Revision != action.Revision+1 {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
}
