package assistant

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type ChannelKind string

const ChannelDiscourse ChannelKind = "discourse"

type ChannelBinding struct {
	ID                     string      `json:"id"`
	AssistantID            string      `json:"assistant_id"`
	Name                   string      `json:"name"`
	Kind                   ChannelKind `json:"kind"`
	BaseURL                string      `json:"base_url"`
	Username               string      `json:"username"`
	CredentialKey          string      `json:"credential_key"`
	CategoryID             int         `json:"category_id,omitempty"`
	CollectIntervalSeconds int64       `json:"collect_interval_seconds"`
	Enabled                bool        `json:"enabled"`
	Revision               int64       `json:"revision"`
	CreatedAt              time.Time   `json:"created_at" ts_type:"string"`
	UpdatedAt              time.Time   `json:"updated_at" ts_type:"string"`
}

type ChannelActionKind string

const (
	ChannelCreateTopic ChannelActionKind = "create_topic"
	ChannelReplyTopic  ChannelActionKind = "reply_topic"
)

type ChannelActionState string

const (
	ChannelActionExecuting ChannelActionState = "executing"
	ChannelActionSucceeded ChannelActionState = "succeeded"
	ChannelActionFailed    ChannelActionState = "failed"
	ChannelActionUnknown   ChannelActionState = "unknown"
)

// ChannelAction is the durable external-effect ledger. An executing record is
// committed before HTTP starts; after a crash it stays unknown instead of
// being replayed and accidentally publishing twice.
type ChannelAction struct {
	ID              string             `json:"id"`
	AssistantID     string             `json:"assistant_id"`
	ChannelID       string             `json:"channel_id"`
	RunID           string             `json:"run_id,omitempty"`
	RequestID       string             `json:"request_id"`
	Fingerprint     string             `json:"fingerprint"`
	Kind            ChannelActionKind  `json:"kind"`
	Title           string             `json:"title,omitempty"`
	Body            string             `json:"body"`
	TargetTopicID   int                `json:"target_topic_id,omitempty"`
	ExternalTopicID int                `json:"external_topic_id,omitempty"`
	ExternalPostID  int                `json:"external_post_id,omitempty"`
	URL             string             `json:"url,omitempty"`
	State           ChannelActionState `json:"state"`
	Error           string             `json:"error,omitempty"`
	CollectFailures int                `json:"collect_failures"`
	CollectError    string             `json:"collect_error,omitempty"`
	NextCollectAt   time.Time          `json:"next_collect_at,omitempty" ts_type:"string"`
	Revision        int64              `json:"revision"`
	CreatedAt       time.Time          `json:"created_at" ts_type:"string"`
	UpdatedAt       time.Time          `json:"updated_at" ts_type:"string"`
}

type ChannelMetric struct {
	ID          string    `json:"id"`
	AssistantID string    `json:"assistant_id"`
	ChannelID   string    `json:"channel_id"`
	ActionID    string    `json:"action_id"`
	TopicID     int       `json:"topic_id"`
	WindowKey   string    `json:"window_key"`
	Views       int64     `json:"views"`
	Likes       int64     `json:"likes"`
	Replies     int64     `json:"replies"`
	ViewsDelta  int64     `json:"views_delta"`
	LikesDelta  int64     `json:"likes_delta"`
	ReplyDelta  int64     `json:"reply_delta"`
	CollectedAt time.Time `json:"collected_at" ts_type:"string"`
}

type PutChannelInput struct {
	RequestID        string
	ExpectedRevision int64
	Channel          ChannelBinding
	Now              time.Time
}

type BeginChannelActionInput struct {
	AssistantID   string
	ChannelID     string
	RunID         string
	RequestID     string
	Kind          ChannelActionKind
	Title         string
	Body          string
	TargetTopicID int
	Now           time.Time
}

type FinishChannelActionInput struct {
	AssistantID      string
	ActionID         string
	RequestID        string
	ExpectedRevision int64
	State            ChannelActionState
	ExternalTopicID  int
	ExternalPostID   int
	URL              string
	Error            string
	Now              time.Time
}

type RecordChannelMetricInput struct {
	AssistantID string
	ActionID    string
	RequestID   string
	WindowKey   string
	Views       int64
	Likes       int64
	Replies     int64
	Now         time.Time
}

type ChannelCollectJob struct {
	Assistant Assistant
	Channel   ChannelBinding
	Action    ChannelAction
}

var credentialKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,127}$`)

func validateChannel(c ChannelBinding) error {
	if err := validateID("channel", c.ID); err != nil {
		return err
	}
	if err := validateID("assistant", c.AssistantID); err != nil {
		return err
	}
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("assistant: channel name is required")
	}
	if c.Kind != ChannelDiscourse {
		return fmt.Errorf("assistant: unsupported channel kind %q", c.Kind)
	}
	u, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("assistant: channel base URL must be an HTTPS origin without credentials, query, or fragment")
	}
	if strings.TrimSpace(c.Username) == "" {
		return errors.New("assistant: channel username is required")
	}
	if !credentialKeyPattern.MatchString(strings.TrimSpace(c.CredentialKey)) {
		return errors.New("assistant: invalid channel credential key")
	}
	if c.CategoryID < 0 {
		return errors.New("assistant: channel category ID cannot be negative")
	}
	if c.CollectIntervalSeconds < 300 || c.CollectIntervalSeconds > 7*24*3600 {
		return errors.New("assistant: channel collection interval must be between 5 minutes and 7 days")
	}
	if c.Revision < 1 || c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		return errors.New("assistant: channel revision and timestamps are required")
	}
	return nil
}

func validateChannelAction(a ChannelAction) error {
	if err := validateID("channel action", a.ID); err != nil {
		return err
	}
	if err := validateID("assistant", a.AssistantID); err != nil {
		return err
	}
	if err := validateID("channel", a.ChannelID); err != nil {
		return err
	}
	if err := validateRequestID(a.RequestID); err != nil {
		return err
	}
	if a.Fingerprint == "" || strings.TrimSpace(a.Body) == "" {
		return errors.New("assistant: channel action fingerprint and body are required")
	}
	switch a.Kind {
	case ChannelCreateTopic:
		if strings.TrimSpace(a.Title) == "" {
			return errors.New("assistant: topic title is required")
		}
	case ChannelReplyTopic:
		if a.TargetTopicID < 1 {
			return errors.New("assistant: reply target topic ID is required")
		}
	default:
		return fmt.Errorf("assistant: invalid channel action kind %q", a.Kind)
	}
	switch a.State {
	case ChannelActionExecuting, ChannelActionSucceeded, ChannelActionFailed, ChannelActionUnknown:
	default:
		return fmt.Errorf("assistant: invalid channel action state %q", a.State)
	}
	if a.Revision < 1 || a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() {
		return errors.New("assistant: channel action revision and timestamps are required")
	}
	return nil
}

func validateChannelMetric(m ChannelMetric) error {
	if err := validateID("channel metric", m.ID); err != nil {
		return err
	}
	if err := validateID("assistant", m.AssistantID); err != nil {
		return err
	}
	if err := validateID("channel", m.ChannelID); err != nil {
		return err
	}
	if err := validateID("channel action", m.ActionID); err != nil {
		return err
	}
	if m.TopicID < 1 || m.WindowKey == "" || m.CollectedAt.IsZero() {
		return errors.New("assistant: channel metric topic, window, and time are required")
	}
	if m.Views < 0 || m.Likes < 0 || m.Replies < 0 {
		return errors.New("assistant: channel metrics cannot be negative")
	}
	return nil
}
