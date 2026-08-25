package assistantchannel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"workground2/internal/assistant"
)

var ErrOutcomeUnknown = errors.New("assistant channel: external outcome is unknown; reconcile before retrying")

type CredentialResolver func(string) string

type PublishResult struct {
	TopicID, PostID int
	URL             string
}
type Metrics struct{ Views, Likes, Replies int64 }

type Adapter interface {
	Kind() assistant.ChannelKind
	Publish(context.Context, assistant.ChannelBinding, string, assistant.ChannelAction) (PublishResult, error)
	Collect(context.Context, assistant.ChannelBinding, string, int) (Metrics, error)
}

type DeliveryError struct {
	Err          error
	OutcomeKnown bool
}

func (e *DeliveryError) Error() string { return e.Err.Error() }
func (e *DeliveryError) Unwrap() error { return e.Err }

type Service struct {
	store    *assistant.Store
	resolve  CredentialResolver
	adapters map[assistant.ChannelKind]Adapter
	now      func() time.Time
}

func New(store *assistant.Store, resolve CredentialResolver, adapters ...Adapter) (*Service, error) {
	if store == nil {
		return nil, errors.New("assistant channel: store is required")
	}
	if resolve == nil {
		return nil, errors.New("assistant channel: credential resolver is required")
	}
	s := &Service{store: store, resolve: resolve, adapters: map[assistant.ChannelKind]Adapter{}, now: time.Now}
	for _, adapter := range adapters {
		if adapter != nil {
			s.adapters[adapter.Kind()] = adapter
		}
	}
	return s, nil
}

func (s *Service) Publish(ctx context.Context, assistantID, runID, channelID string, kind assistant.ChannelActionKind, title, body string, topicID int) (assistant.ChannelAction, error) {
	intent := struct {
		AssistantID, RunID, ChannelID string
		Kind                          assistant.ChannelActionKind
		Title, Body                   string
		TopicID                       int
	}{assistantID, runID, channelID, kind, strings.TrimSpace(title), strings.TrimSpace(body), topicID}
	raw, _ := json.Marshal(intent)
	requestID := assistant.StableID("channel-request", string(raw))
	action, created, err := s.store.BeginChannelAction(assistant.BeginChannelActionInput{AssistantID: assistantID, ChannelID: channelID, RunID: runID, RequestID: requestID, Kind: kind, Title: title, Body: body, TargetTopicID: topicID, Now: s.now()})
	if err != nil {
		return assistant.ChannelAction{}, err
	}
	if !created {
		switch action.State {
		case assistant.ChannelActionExecuting:
			finished, finishErr := s.finish(action, assistant.ChannelActionUnknown, PublishResult{}, ErrOutcomeUnknown)
			return finished, errors.Join(ErrOutcomeUnknown, finishErr)
		case assistant.ChannelActionUnknown:
			return action, ErrOutcomeUnknown
		case assistant.ChannelActionFailed:
			if action.Error == "" {
				return action, errors.New("assistant channel: previous delivery failed")
			}
			return action, errors.New(action.Error)
		}
		return action, nil
	}
	snapshot, err := s.store.Get(assistantID)
	if err != nil {
		return s.finish(action, assistant.ChannelActionFailed, PublishResult{}, err)
	}
	var binding *assistant.ChannelBinding
	for i := range snapshot.Channels {
		if snapshot.Channels[i].ID == channelID {
			binding = &snapshot.Channels[i]
			break
		}
	}
	if binding == nil {
		return s.finish(action, assistant.ChannelActionFailed, PublishResult{}, assistant.ErrNotFound)
	}
	adapter := s.adapters[binding.Kind]
	if adapter == nil {
		return s.finish(action, assistant.ChannelActionFailed, PublishResult{}, fmt.Errorf("assistant channel: adapter %q is unavailable", binding.Kind))
	}
	secret := strings.TrimSpace(s.resolve(binding.CredentialKey))
	if secret == "" {
		return s.finish(action, assistant.ChannelActionFailed, PublishResult{}, fmt.Errorf("assistant channel: credential %s is not configured", binding.CredentialKey))
	}
	result, publishErr := adapter.Publish(ctx, *binding, secret, action)
	if publishErr != nil {
		state := assistant.ChannelActionUnknown
		var delivery *DeliveryError
		if errors.As(publishErr, &delivery) && delivery.OutcomeKnown {
			state = assistant.ChannelActionFailed
		}
		finished, finishErr := s.finish(action, state, PublishResult{}, publishErr)
		if finishErr != nil {
			return assistant.ChannelAction{}, errors.Join(publishErr, finishErr)
		}
		if state == assistant.ChannelActionUnknown {
			return finished, ErrOutcomeUnknown
		}
		return finished, publishErr
	}
	return s.finish(action, assistant.ChannelActionSucceeded, result, nil)
}

func (s *Service) finish(action assistant.ChannelAction, state assistant.ChannelActionState, result PublishResult, cause error) (assistant.ChannelAction, error) {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	requestID := assistant.StableID("channel-finish", action.ID+"/"+string(state))
	finished, err := s.store.FinishChannelAction(assistant.FinishChannelActionInput{AssistantID: action.AssistantID, ActionID: action.ID, RequestID: requestID, ExpectedRevision: action.Revision, State: state, ExternalTopicID: result.TopicID, ExternalPostID: result.PostID, URL: result.URL, Error: message, Now: s.now()})
	if err != nil {
		return assistant.ChannelAction{}, err
	}
	return finished, nil
}

func (s *Service) CollectDue(ctx context.Context) ([]assistant.ChannelMetric, error) {
	jobs, scanErr := s.store.DueChannelCollects(s.now())
	results := []assistant.ChannelMetric{}
	issues := []error{scanErr}
	for _, job := range jobs {
		adapter := s.adapters[job.Channel.Kind]
		secret := strings.TrimSpace(s.resolve(job.Channel.CredentialKey))
		if adapter == nil || secret == "" {
			reason := "adapter unavailable"
			if secret == "" {
				reason = "credential unavailable"
			}
			_, err := s.store.DeferChannelCollect(job.Assistant.ID, job.Action.ID, assistant.StableID("channel-collect-defer", job.Action.ID+"/"+job.Action.NextCollectAt.UTC().Format(time.RFC3339)), reason, s.now())
			issues = append(issues, err)
			continue
		}
		metric, err := adapter.Collect(ctx, job.Channel, secret, job.Action.ExternalTopicID)
		if err != nil {
			_, deferErr := s.store.DeferChannelCollect(job.Assistant.ID, job.Action.ID, assistant.StableID("channel-collect-defer", job.Action.ID+"/"+job.Action.NextCollectAt.UTC().Format(time.RFC3339)), err.Error(), s.now())
			issues = append(issues, err, deferErr)
			continue
		}
		window := job.Action.NextCollectAt.UTC().Format(time.RFC3339)
		recorded, err := s.store.RecordChannelMetric(assistant.RecordChannelMetricInput{AssistantID: job.Assistant.ID, ActionID: job.Action.ID, RequestID: assistant.StableID("channel-collect", job.Action.ID+"/"+window), WindowKey: window, Views: metric.Views, Likes: metric.Likes, Replies: metric.Replies, Now: s.now()})
		if err != nil {
			issues = append(issues, err)
			continue
		}
		results = append(results, recorded)
	}
	return results, errors.Join(compactErrors(issues)...)
}

func (s *Service) Metrics(assistantID, channelID string) ([]assistant.ChannelMetric, error) {
	snapshot, err := s.store.Get(assistantID)
	if err != nil {
		return nil, err
	}
	result := []assistant.ChannelMetric{}
	for _, metric := range snapshot.ChannelMetrics {
		if channelID == "" || metric.ChannelID == channelID {
			result = append(result, metric)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CollectedAt.After(result[j].CollectedAt) })
	return result, nil
}

func compactErrors(in []error) []error {
	out := []error{}
	for _, err := range in {
		if err != nil {
			out = append(out, err)
		}
	}
	return out
}
