package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxBody     = 2 << 20 // response bytes read at most
	maxErrBody  = 200     // body bytes quoted in error messages
	videoBatch  = 50
	pageSize    = 50
	watchURL    = "https://www.youtube.com/watch?v="
	playlistURL = "https://www.youtube.com/playlist?list="
)

var keyParamRe = regexp.MustCompile(`(?i)([?&]?key=)[^&\s"']*`)

// redact removes the API key from text that may contain URLs.
func (c *Client) redact(s string) string {
	if c.key != "" {
		s = strings.ReplaceAll(s, c.key, "REDACTED")
	}
	return keyParamRe.ReplaceAllString(s, "${1}REDACTED")
}

// get performs a GET of path with params, adds the key, and decodes the JSON
// response into out. Errors never contain the key.
func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	q := url.Values{}
	for k, v := range params {
		q[k] = v
	}
	q.Set("key", c.key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("youtube: GET %s: %s", path, c.redact(err.Error()))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL, which carries the key
		}
		return fmt.Errorf("youtube: GET %s: %s", path, c.redact(err.Error()))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("youtube: GET %s: read body: %s", path, c.redact(err.Error()))
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := string(body)
		if len(msg) > maxErrBody {
			msg = msg[:maxErrBody] + "..."
		}
		return fmt.Errorf("youtube: GET %s: status %d: %s", path, resp.StatusCode, c.redact(msg))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("youtube: GET %s: decode: %w", path, err)
	}
	return nil
}

type videoResource struct {
	ID      string `json:"id"`
	Snippet struct {
		Title        string `json:"title"`
		ChannelID    string `json:"channelId"`
		ChannelTitle string `json:"channelTitle"`
		PublishedAt  string `json:"publishedAt"`
	} `json:"snippet"`
	ContentDetails struct {
		Duration string `json:"duration"`
	} `json:"contentDetails"`
}

// Videos fetches metadata for ids (batched by 50), in input order; IDs that
// don't resolve are omitted.
func (c *Client) Videos(ctx context.Context, ids []string) ([]Video, error) {
	found := make(map[string]Video, len(ids))
	for start := 0; start < len(ids); start += videoBatch {
		batch := ids[start:min(start+videoBatch, len(ids))]
		var resp struct {
			Items []videoResource `json:"items"`
		}
		err := c.get(ctx, "/videos", url.Values{
			"part":       {"snippet,contentDetails"},
			"id":         {strings.Join(batch, ",")},
			"maxResults": {strconv.Itoa(videoBatch)},
		}, &resp)
		if err != nil {
			return nil, err
		}
		for _, it := range resp.Items {
			found[it.ID] = toVideo(it)
		}
	}
	out := make([]Video, 0, len(ids))
	for _, id := range ids {
		if v, ok := found[id]; ok {
			out = append(out, v)
		}
	}
	return out, nil
}

func toVideo(r videoResource) Video {
	published, _ := time.Parse(time.RFC3339, r.Snippet.PublishedAt) // zero if unparsable
	return Video{
		ID:              r.ID,
		Title:           r.Snippet.Title,
		ChannelID:       r.Snippet.ChannelID,
		ChannelTitle:    r.Snippet.ChannelTitle,
		PublishedAt:     published,
		DurationSeconds: parseDuration(r.ContentDetails.Duration),
		URL:             watchURL + r.ID,
		ThumbnailURL:    ThumbnailURL(r.ID),
	}
}

// ChannelHandles returns the @handle (snippet.customUrl, e.g. "@creator") of
// each channel ID, batched by 50. Channels without a handle are omitted.
func (c *Client) ChannelHandles(ctx context.Context, channelIDs []string) (map[string]string, error) {
	var ids []string
	seen := make(map[string]bool, len(channelIDs))
	for _, id := range channelIDs {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	out := make(map[string]string, len(ids))
	for start := 0; start < len(ids); start += videoBatch {
		batch := ids[start:min(start+videoBatch, len(ids))]
		var resp struct {
			Items []struct {
				ID      string `json:"id"`
				Snippet struct {
					CustomURL string `json:"customUrl"`
				} `json:"snippet"`
			} `json:"items"`
		}
		err := c.get(ctx, "/channels", url.Values{
			"part":       {"snippet"},
			"id":         {strings.Join(batch, ",")},
			"maxResults": {strconv.Itoa(videoBatch)},
		}, &resp)
		if err != nil {
			return nil, err
		}
		for _, it := range resp.Items {
			h := strings.TrimSpace(it.Snippet.CustomURL)
			if h == "" || it.ID == "" {
				continue
			}
			if !strings.HasPrefix(h, "@") {
				h = "@" + h
			}
			out[it.ID] = h
		}
	}
	return out, nil
}

// Video fetches one video; ErrNotFound if it doesn't resolve.
func (c *Client) Video(ctx context.Context, id string) (Video, error) {
	vs, err := c.Videos(ctx, []string{id})
	if err != nil {
		return Video{}, err
	}
	if len(vs) == 0 {
		return Video{}, ErrNotFound
	}
	return vs[0], nil
}

// Playlist fetches a playlist and up to MaxPlaylistItems of its playable videos
// with durations, ordered by position.
func (c *Client) Playlist(ctx context.Context, id string) (Playlist, []PlaylistItem, error) {
	var pl struct {
		Items []struct {
			Snippet struct {
				Title        string `json:"title"`
				ChannelTitle string `json:"channelTitle"`
			} `json:"snippet"`
		} `json:"items"`
	}
	err := c.get(ctx, "/playlists", url.Values{"part": {"snippet"}, "id": {id}}, &pl)
	if err != nil {
		return Playlist{}, nil, err
	}
	if len(pl.Items) == 0 {
		return Playlist{}, nil, ErrNotFound
	}
	p := Playlist{
		ID:           id,
		Title:        pl.Items[0].Snippet.Title,
		ChannelTitle: pl.Items[0].Snippet.ChannelTitle,
		URL:          playlistURL + id,
	}

	type entry struct {
		videoID  string
		position int
	}
	var entries []entry
	read := 0
	token := ""
	for read < MaxPlaylistItems {
		params := url.Values{
			"part":       {"snippet,contentDetails"},
			"playlistId": {id},
			"maxResults": {strconv.Itoa(pageSize)},
		}
		if token != "" {
			params.Set("pageToken", token)
		}
		var page struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				Snippet struct {
					Title    string `json:"title"`
					Position int    `json:"position"`
				} `json:"snippet"`
				ContentDetails struct {
					VideoID string `json:"videoId"`
				} `json:"contentDetails"`
			} `json:"items"`
		}
		if err := c.get(ctx, "/playlistItems", params, &page); err != nil {
			return Playlist{}, nil, err
		}
		for _, it := range page.Items {
			if read >= MaxPlaylistItems {
				break
			}
			read++
			t := it.Snippet.Title
			if it.ContentDetails.VideoID == "" || t == "Deleted video" || t == "Private video" {
				continue
			}
			entries = append(entries, entry{it.ContentDetails.VideoID, it.Snippet.Position + 1})
		}
		token = page.NextPageToken
		if token == "" {
			break
		}
	}

	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.videoID
	}
	videos, err := c.Videos(ctx, ids)
	if err != nil {
		return Playlist{}, nil, err
	}
	byID := make(map[string]Video, len(videos))
	for _, v := range videos {
		byID[v.ID] = v
	}
	items := make([]PlaylistItem, 0, len(entries))
	for _, e := range entries {
		if v, ok := byID[e.videoID]; ok {
			items = append(items, PlaylistItem{Video: v, Position: e.position})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Position < items[j].Position })
	if len(items) > 0 {
		p.ThumbnailURL = items[0].ThumbnailURL
	}
	return p, items, nil
}

var durationRe = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)(?:\.\d+)?S)?)?$`)

// parseDuration converts an ISO 8601 duration (days and below) to seconds.
// Malformed input yields 0.
func parseDuration(s string) int64 {
	m := durationRe.FindStringSubmatch(s)
	if m == nil || s == "P" || strings.HasSuffix(s, "T") {
		return 0
	}
	var total int64
	for i, mult := range []int64{86400, 3600, 60, 1} {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.ParseInt(m[i+1], 10, 64)
		if err != nil {
			return 0
		}
		total += n * mult
	}
	return total
}
