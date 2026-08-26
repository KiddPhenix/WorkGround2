package assistant

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func channelFixture(t *testing.T) (*Store, Snapshot, ChannelBinding) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "assistants"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Create(CreateInput{RequestID: "create-channel-helper", Assistant: Assistant{ID: "assistant-channel", Name: "Promo", Mission: "promote", Scope: ScopeGlobal, Lifecycle: LifecycleActive, Policy: DefaultPolicy()}, Now: testEpoch})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := store.PutChannel(PutChannelInput{RequestID: "put-channel", Channel: ChannelBinding{ID: "channel-forum", AssistantID: snapshot.Assistant.ID, Name: "Forum", Kind: ChannelDiscourse, BaseURL: "https://community.example.com", Username: "bot", CredentialKey: "ASSISTANT_CHANNEL_TEST_KEY", CollectIntervalSeconds: 3600, Enabled: true}, Now: testEpoch})
	if err != nil {
		t.Fatal(err)
	}
	return store, snapshot, channel
}

func TestStoreChannelActionIsDurablyIdempotentAndMetricsUseDeltas(t *testing.T) {
	store, snapshot, channel := channelFixture(t)
	in := BeginChannelActionInput{AssistantID: snapshot.Assistant.ID, ChannelID: channel.ID, RequestID: "publish-topic-1", Kind: ChannelCreateTopic, Title: "Hello", Body: "Body", Now: testEpoch.Add(time.Minute)}
	action, created, err := store.BeginChannelAction(in)
	if err != nil || !created {
		t.Fatalf("begin = %+v created=%v err=%v", action, created, err)
	}
	replayed, created, err := store.BeginChannelAction(in)
	if err != nil || created || replayed.ID != action.ID {
		t.Fatalf("replay = %+v created=%v err=%v", replayed, created, err)
	}
	changed := in
	changed.Body = "different"
	if _, _, err := store.BeginChannelAction(changed); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("changed replay err=%v", err)
	}
	finished, err := store.FinishChannelAction(FinishChannelActionInput{AssistantID: snapshot.Assistant.ID, ActionID: action.ID, RequestID: "finish-topic-1", ExpectedRevision: action.Revision, State: ChannelActionSucceeded, ExternalTopicID: 42, ExternalPostID: 7, URL: "https://community.example.com/t/42", Now: testEpoch.Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.RecordChannelMetric(RecordChannelMetricInput{AssistantID: snapshot.Assistant.ID, ActionID: action.ID, RequestID: "metric-1", WindowKey: "2026-08-26T01", Views: 10, Likes: 2, Replies: 1, Now: testEpoch.Add(8 * time.Minute)})
	if err != nil || first.ViewsDelta != 0 {
		t.Fatalf("first metric=%+v err=%v", first, err)
	}
	second, err := store.RecordChannelMetric(RecordChannelMetricInput{AssistantID: snapshot.Assistant.ID, ActionID: action.ID, RequestID: "metric-2", WindowKey: "2026-08-26T02", Views: 25, Likes: 5, Replies: 4, Now: testEpoch.Add(70 * time.Minute)})
	if err != nil || second.ViewsDelta != 15 || second.LikesDelta != 3 || second.ReplyDelta != 3 {
		t.Fatalf("second metric=%+v err=%v", second, err)
	}
	if finished.State != ChannelActionSucceeded {
		t.Fatalf("finish=%+v", finished)
	}
}

func TestStoreDueChannelCollectsAndBackoff(t *testing.T) {
	store, snapshot, channel := channelFixture(t)
	action, _, _ := store.BeginChannelAction(BeginChannelActionInput{AssistantID: snapshot.Assistant.ID, ChannelID: channel.ID, RequestID: "publish-topic-due", Kind: ChannelCreateTopic, Title: "Due", Body: "Body", Now: testEpoch})
	finished, err := store.FinishChannelAction(FinishChannelActionInput{AssistantID: snapshot.Assistant.ID, ActionID: action.ID, RequestID: "finish-topic-due", ExpectedRevision: action.Revision, State: ChannelActionSucceeded, ExternalTopicID: 9, Now: testEpoch})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := store.DueChannelCollects(finished.NextCollectAt)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	deferred, err := store.DeferChannelCollect(snapshot.Assistant.ID, action.ID, "defer-1", "timeout", finished.NextCollectAt)
	if err != nil || !deferred.NextCollectAt.After(finished.NextCollectAt) || deferred.CollectFailures != 1 {
		t.Fatalf("deferred=%+v err=%v", deferred, err)
	}
}
