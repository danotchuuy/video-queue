// Package discord talks to the Discord bot REST API and syncs a suggestions
// channel into the store.
package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	defaultBaseURL = "https://discord.com/api/v10"
	userAgent      = "DiscordBot (https://github.com/danotchuuy/video-queue, 1.0)"
	maxErrBody     = 512
)

// Client is a minimal Discord bot REST client.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// NewClient returns a client authenticating as the given bot.
func NewClient(botToken string) *Client {
	return &Client{
		token:   botToken,
		baseURL: defaultBaseURL,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// MessageQuery holds the supported query parameters of GET /channels/{id}/messages.
type MessageQuery struct {
	After string // only messages with a larger ID; empty for none
	Limit int    // 1-100; 0 uses Discord's default
}

type Message struct {
	ID          string       `json:"id"`
	Type        int          `json:"type"`
	Content     string       `json:"content"`
	Timestamp   string       `json:"timestamp"`
	Author      Author       `json:"author"`
	Embeds      []Embed      `json:"embeds"`
	Attachments []Attachment `json:"attachments"`
	Reactions   []Reaction   `json:"reactions"`
}

type Author struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
	Bot        bool   `json:"bot"`
}

type Embed struct {
	URL string `json:"url"`
}

type Attachment struct {
	URL string `json:"url"`
}

type Reaction struct {
	Count int `json:"count"`
}

// ChannelMessages fetches messages from a channel, newest first.
func (c *Client) ChannelMessages(ctx context.Context, channelID string, q MessageQuery) ([]Message, error) {
	vals := url.Values{}
	if q.After != "" {
		vals.Set("after", q.After)
	}
	if q.Limit > 0 {
		vals.Set("limit", strconv.Itoa(q.Limit))
	}
	u := c.baseURL + "/channels/" + url.PathEscape(channelID) + "/messages"
	if len(vals) > 0 {
		u += "?" + vals.Encode()
	}

	body, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	var msgs []Message
	if err := json.Unmarshal(body, &msgs); err != nil {
		return nil, fmt.Errorf("discord: decode messages: %w", err)
	}
	return msgs, nil
}

// get performs an authenticated GET, retrying once after a 429.
func (c *Client) get(ctx context.Context, u string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		status, body, err := c.do(ctx, u)
		if err != nil {
			return nil, err
		}
		if status == http.StatusTooManyRequests && attempt == 0 {
			var rl struct {
				RetryAfter float64 `json:"retry_after"`
			}
			_ = json.Unmarshal(body, &rl)
			wait := time.Duration(rl.RetryAfter * float64(time.Second))
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if status < 200 || status > 299 {
			s := string(body)
			if len(s) > maxErrBody {
				s = s[:maxErrBody] + "..."
			}
			err := fmt.Errorf("discord: GET %s: status %d: %s", u, status, s)
			if isNotFound(status, body) {
				err = fmt.Errorf("%w: %w", ErrNotFound, err)
			}
			return nil, err
		}
		return body, nil
	}
}

// isNotFound reports whether a response means the resource doesn't exist or
// isn't visible to the bot: 404, or 403 with Discord error code 50001.
func isNotFound(status int, body []byte) bool {
	switch status {
	case http.StatusNotFound:
		return true
	case http.StatusForbidden:
		var e struct {
			Code int `json:"code"`
		}
		return json.Unmarshal(body, &e) == nil && e.Code == 50001
	}
	return false
}

func (c *Client) do(ctx context.Context, u string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bot "+c.token)
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("discord: request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("discord: read body: %w", err)
	}
	return resp.StatusCode, body, nil
}
