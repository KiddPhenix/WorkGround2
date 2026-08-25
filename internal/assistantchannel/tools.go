package assistantchannel

import (
	"context"
	"encoding/json"
	"strings"

	"workground2/internal/assistant"
	"workground2/internal/tool"
)

type channelTool struct {
	service            *Service
	assistantID, runID string
	kind               assistant.ChannelActionKind
}

func Tools(service *Service, assistantID, runID string) []tool.Tool {
	if service == nil || strings.TrimSpace(assistantID) == "" {
		return nil
	}
	return []tool.Tool{&channelTool{service: service, assistantID: assistantID, runID: runID, kind: assistant.ChannelCreateTopic}, &channelTool{service: service, assistantID: assistantID, runID: runID, kind: assistant.ChannelReplyTopic}, &metricsTool{service: service, assistantID: assistantID}}
}

func (t *channelTool) Name() string {
	if t.kind == assistant.ChannelReplyTopic {
		return "assistant_channel_reply"
	}
	return "assistant_channel_publish"
}
func (t *channelTool) Description() string {
	if t.kind == assistant.ChannelReplyTopic {
		return "Reply to a configured community topic. This always requires per-action human approval and is durably deduplicated."
	}
	return "Publish a topic to a configured Assistant community channel. This always requires per-action human approval and is durably deduplicated."
}
func (t *channelTool) Schema() json.RawMessage {
	if t.kind == assistant.ChannelReplyTopic {
		return json.RawMessage(`{"type":"object","properties":{"channel_id":{"type":"string"},"topic_id":{"type":"integer","minimum":1},"body":{"type":"string"}},"required":["channel_id","topic_id","body"],"additionalProperties":false}`)
	}
	return json.RawMessage(`{"type":"object","properties":{"channel_id":{"type":"string"},"title":{"type":"string"},"body":{"type":"string"}},"required":["channel_id","title","body"],"additionalProperties":false}`)
}
func (*channelTool) ReadOnly() bool            { return false }
func (*channelTool) SideEffectingDefaultSnip() {}
func (t *channelTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		ChannelID string `json:"channel_id"`
		TopicID   int    `json:"topic_id"`
		Title     string `json:"title"`
		Body      string `json:"body"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", err
	}
	action, err := t.service.Publish(ctx, t.assistantID, t.runID, in.ChannelID, t.kind, in.Title, in.Body, in.TopicID)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(struct {
		State    assistant.ChannelActionState `json:"state"`
		ActionID string                       `json:"action_id"`
		TopicID  int                          `json:"topic_id"`
		PostID   int                          `json:"post_id"`
		URL      string                       `json:"url,omitempty"`
	}{action.State, action.ID, action.ExternalTopicID, action.ExternalPostID, action.URL})
	return string(out), nil
}

type metricsTool struct {
	service     *Service
	assistantID string
}

func (*metricsTool) Name() string { return "assistant_channel_metrics" }
func (*metricsTool) Description() string {
	return "Read durable promotion metrics already collected for this Assistant. Metrics are authoritative channel observations."
}
func (*metricsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"channel_id":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":100}},"additionalProperties":false}`)
}
func (*metricsTool) ReadOnly() bool { return true }
func (t *metricsTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		ChannelID string `json:"channel_id"`
		Limit     int    `json:"limit"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return "", err
		}
	}
	metrics, err := t.service.Metrics(t.assistantID, in.ChannelID)
	if err != nil {
		return "", err
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	if len(metrics) > in.Limit {
		metrics = metrics[:in.Limit]
	}
	out, err := json.Marshal(metrics)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

var _ tool.Tool = (*channelTool)(nil)
var _ tool.Tool = (*metricsTool)(nil)
