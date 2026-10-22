package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	clipBatch   = 100
	maxBody     = 2 << 20 // response bytes read at most
	maxErrBody  = 200     // body bytes quoted in error messages
	tokenLeeway = 60 * time.Second
	maxRateWait = 10 * time.Second
	defaultWait = time.Second // when Ratelimit-Reset is missing or invalid
)

// ErrNotFound is returned for clips that don't exist (or were deleted).
var ErrNotFound = errors.New("twitch: not found")

// Clip is a clip from GET /helix/clips.
type Clip struct {
	ID              string    `json:"id"` // slug
	URL             string    `json:"url"`
	Title           string    `json:"title"`
	CreatorName     string    `json:"creator_name"` // who clipped it
	BroadcasterName string    `json:"broadcaster_name"`
	ThumbnailURL    string    `json:"thumbnail_url"`
	Duration        float64   `json:"duration"` // seconds
	CreatedAt       time.Time `json:"created_at"`
}

// Client calls the Twitch Helix API with an app access token (client
// credentials), fetched on first use and refreshed before it expires.
type Client struct {
	clientID, clientSecret string
	apiBase, authBase      string // overridable in tests
	http                   *http.Client

	now func() time.Time // nil means time.Now; overridable in tests

	mu      sync.Mutex
	token   string
	expires time.Time
}

func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID: clientID, clientSecret: clientSecret,
		apiBase: "https://api.twitch.tv/helix", authBase: "https://id.twitch.tv/oauth2",
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// redact removes the client secret and the current token from text. It must
// not be called with c.mu held; accessToken uses scrub directly.
func (c *Client) redact(s string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scrub(s)
}

// scrub is redact for callers that hold c.mu.
func (c *Client) scrub(s string) string {
	for _, secret := range []string{c.clientSecret, c.token} {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "REDACTED")
		}
	}
	return s
}

// errf formats an error whose text never contains the secret or token.
func (c *Client) errf(format string, args ...any) error {
	return errors.New(c.redact(fmt.Sprintf(format, args...)))
}

// transportErr describes a failed request without the URL.
func (c *Client) transportErr(ctx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return c.errf("twitch: %s: %s", what, err)
}

// errfLocked is errf for callers that hold c.mu.
func (c *Client) errfLocked(format string, args ...any) error {
	return errors.New(c.scrub(fmt.Sprintf(format, args...)))
}

func truncate(b []byte) string {
	if len(b) > maxErrBody {
		return string(b[:maxErrBody]) + "..."
	}
	return string(b)
}

// accessToken returns the cached app token, fetching a new one when there is
// none or it expires within a minute.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.clock().Add(tokenLeeway).Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"grant_type":    {"client_credentials"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authBase+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", c.errfLocked("twitch: token: %s", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return "", c.errfLocked("twitch: token: %s", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", c.errfLocked("twitch: token: read body: %s", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", c.errfLocked("twitch: token: status %d: %s", resp.StatusCode, truncate(body))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("twitch: token: decode: %w", err)
	}
	if out.AccessToken == "" {
		return "", errors.New("twitch: token: empty access_token")
	}
	c.token = out.AccessToken
	c.expires = c.clock().Add(time.Duration(out.ExpiresIn) * time.Second)
	return c.token, nil
}

// dropToken forgets tok if it is still the cached token.
func (c *Client) dropToken(tok string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == tok {
		c.token, c.expires = "", time.Time{}
	}
}

// rateWait is how long to wait for a 429's Ratelimit-Reset (unix seconds).
func (c *Client) rateWait(h http.Header) time.Duration {
	reset, err := strconv.ParseInt(h.Get("Ratelimit-Reset"), 10, 64)
	if err != nil {
		return defaultWait
	}
	return min(max(time.Unix(reset, 0).Sub(c.clock()), 0), maxRateWait)
}

// get performs an authenticated GET of path and decodes the JSON response. It
// re-authenticates once on a 401 and waits and retries once on a 429.
func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	reauthed, rateLimited := false, false
	for {
		tok, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+path+"?"+params.Encode(), nil)
		if err != nil {
			return c.errf("twitch: GET %s: %s", path, err)
		}
		req.Header.Set("Client-Id", c.clientID)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := c.http.Do(req)
		if err != nil {
			return c.transportErr(ctx, "GET "+path, err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		resp.Body.Close()
		if err != nil {
			return c.errf("twitch: GET %s: read body: %s", path, err)
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized && !reauthed:
			reauthed = true
			c.dropToken(tok)
			continue
		case resp.StatusCode == http.StatusTooManyRequests && !rateLimited:
			rateLimited = true
			t := time.NewTimer(c.rateWait(resp.Header))
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
			continue
		case resp.StatusCode < 200 || resp.StatusCode > 299:
			return c.errf("twitch: GET %s: status %d: %s", path, resp.StatusCode, truncate(body))
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("twitch: GET %s: decode: %w", path, err)
		}
		return nil
	}
}

// Clips fetches clips by slug, batched by 100; slugs that don't resolve are
// omitted. Order follows the input.
func (c *Client) Clips(ctx context.Context, slugs []string) ([]Clip, error) {
	found := make(map[string]Clip, len(slugs))
	for start := 0; start < len(slugs); start += clipBatch {
		batch := slugs[start:min(start+clipBatch, len(slugs))]
		var resp struct {
			Data []Clip `json:"data"`
		}
		if err := c.get(ctx, "/clips", url.Values{"id": batch}, &resp); err != nil {
			return nil, err
		}
		for _, cl := range resp.Data {
			found[cl.ID] = cl
		}
	}
	var out []Clip
	for _, s := range slugs {
		if cl, ok := found[s]; ok {
			out = append(out, cl)
			delete(found, s) // duplicate slugs yield one clip
		}
	}
	return out, nil
}
