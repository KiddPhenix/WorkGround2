package assistantchannel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"workground2/internal/assistant"
)

type Discourse struct{ client *http.Client }

func NewDiscourse(client *http.Client) *Discourse {
	if client == nil {
		client = http.DefaultClient
	}
	return &Discourse{client: client}
}
func (*Discourse) Kind() assistant.ChannelKind { return assistant.ChannelDiscourse }

func (d *Discourse) Publish(ctx context.Context, channel assistant.ChannelBinding, secret string, action assistant.ChannelAction) (PublishResult, error) {
	values := url.Values{"raw": {action.Body}}
	if action.Kind == assistant.ChannelCreateTopic {
		values.Set("title", action.Title)
		if channel.CategoryID > 0 {
			values.Set("category", strconv.Itoa(channel.CategoryID))
		}
	} else {
		values.Set("topic_id", strconv.Itoa(action.TargetTopicID))
	}
	endpoint := strings.TrimRight(channel.BaseURL, "/") + "/posts.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return PublishResult{}, &DeliveryError{Err: err, OutcomeKnown: true}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	applyDiscourseAuth(req, channel.Username, secret)
	resp, err := d.client.Do(req)
	if err != nil {
		return PublishResult{}, &DeliveryError{Err: err, OutcomeKnown: false}
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return PublishResult{}, &DeliveryError{Err: readErr, OutcomeKnown: false}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not persist response bodies for write failures. A misconfigured or
		// hostile endpoint can echo request credentials or content in its error.
		return PublishResult{}, &DeliveryError{Err: fmt.Errorf("discourse publish returned %s", resp.Status), OutcomeKnown: true}
	}
	var payload struct {
		ID      int    `json:"id"`
		TopicID int    `json:"topic_id"`
		PostURL string `json:"post_url"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.ID < 1 || payload.TopicID < 1 {
		return PublishResult{}, &DeliveryError{Err: fmt.Errorf("discourse publish returned an invalid response"), OutcomeKnown: false}
	}
	postURL := payload.PostURL
	if strings.HasPrefix(postURL, "/") {
		postURL = strings.TrimRight(channel.BaseURL, "/") + postURL
	}
	if postURL == "" {
		postURL = fmt.Sprintf("%s/t/%d", strings.TrimRight(channel.BaseURL, "/"), payload.TopicID)
	}
	return PublishResult{TopicID: payload.TopicID, PostID: payload.ID, URL: postURL}, nil
}

func (d *Discourse) Collect(ctx context.Context, channel assistant.ChannelBinding, secret string, topicID int) (Metrics, error) {
	endpoint := fmt.Sprintf("%s/t/%d.json", strings.TrimRight(channel.BaseURL, "/"), topicID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Metrics{}, err
	}
	applyDiscourseAuth(req, channel.Username, secret)
	resp, err := d.client.Do(req)
	if err != nil {
		return Metrics{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Metrics{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Metrics{}, fmt.Errorf("discourse metrics returned %s: %s", resp.Status, safeBody(body))
	}
	var payload struct {
		Views      int64 `json:"views"`
		PostsCount int64 `json:"posts_count"`
		LikeCount  int64 `json:"like_count"`
		PostStream struct {
			Posts []struct {
				Actions []struct {
					ID    int   `json:"id"`
					Count int64 `json:"count"`
				} `json:"actions_summary"`
			} `json:"posts"`
		} `json:"post_stream"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Metrics{}, fmt.Errorf("decode discourse metrics: %w", err)
	}
	likes := payload.LikeCount
	if likes == 0 {
		for _, post := range payload.PostStream.Posts {
			for _, action := range post.Actions {
				if action.ID == 2 {
					likes += action.Count
				}
			}
		}
	}
	replies := payload.PostsCount - 1
	if replies < 0 {
		replies = 0
	}
	return Metrics{Views: payload.Views, Likes: likes, Replies: replies}, nil
}

func applyDiscourseAuth(req *http.Request, username, secret string) {
	req.Header.Set("Api-Username", username)
	req.Header.Set("Api-Key", secret)
	req.Header.Set("Accept", "application/json")
}
func safeBody(body []byte) string {
	value := strings.TrimSpace(string(body))
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}
