// Package youtube parses YouTube URLs and fetches video and playlist metadata
// from the YouTube Data API v3.
package youtube

import (
	"errors"
	"net/http"
	"time"
)

// ErrNotFound is returned when a video or playlist doesn't exist or isn't public.
var ErrNotFound = errors.New("youtube: not found")

// MaxPlaylistItems caps how many playlist entries are fetched.
const MaxPlaylistItems = 500

type Video struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	ChannelID       string    `json:"channel_id"`
	ChannelTitle    string    `json:"channel_title"`
	PublishedAt     time.Time `json:"published_at"`
	DurationSeconds int64     `json:"duration_seconds"`
	URL             string    `json:"url"`           // https://www.youtube.com/watch?v={id}
	ThumbnailURL    string    `json:"thumbnail_url"` // ThumbnailURL(id)
}

type Playlist struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	ChannelTitle string `json:"channel_title"`
	URL          string `json:"url"`           // https://www.youtube.com/playlist?list={id}
	ThumbnailURL string `json:"thumbnail_url"` // first available item's ThumbnailURL, or ""
}

// PlaylistItem is a playable video in a playlist. Position is 1-based within the
// playlist as YouTube orders it (deleted/private entries still count toward positions).
type PlaylistItem struct {
	Video
	Position int `json:"position"`
}

type Client struct {
	key     string
	baseURL string
	http    *http.Client
}

// NewClient returns a client using an API key; baseURL defaults to
// https://www.googleapis.com/youtube/v3.
func NewClient(apiKey string) *Client {
	return &Client{key: apiKey, baseURL: "https://www.googleapis.com/youtube/v3", http: &http.Client{Timeout: 15 * time.Second}}
}

// ThumbnailURL returns the medium (320x180) thumbnail for a video ID.
func ThumbnailURL(videoID string) string {
	return "https://i.ytimg.com/vi/" + videoID + "/mqdefault.jpg"
}
