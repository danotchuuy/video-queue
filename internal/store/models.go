package store

import (
	"errors"
	"time"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned when a unique constraint would be violated.
var ErrConflict = errors.New("store: conflict")

type Venue string

const (
	VenueWatchParty Venue = "watch_party"
	VenueStream     Venue = "stream"
)

func (v Venue) Valid() bool { return v == VenueWatchParty || v == VenueStream }

type VideoStatus string

const (
	StatusQueued  VideoStatus = "queued"
	StatusReacted VideoStatus = "reacted"
	StatusEditing VideoStatus = "editing"
	StatusPatreon VideoStatus = "patreon"
	StatusYouTube VideoStatus = "youtube"
	StatusSkipped VideoStatus = "skipped"
)

func (s VideoStatus) Valid() bool {
	switch s {
	case StatusQueued, StatusReacted, StatusEditing, StatusPatreon, StatusYouTube, StatusSkipped:
		return true
	}
	return false
}

type SuggestionStatus string

const (
	SuggestionPending   SuggestionStatus = "pending"
	SuggestionApproved  SuggestionStatus = "approved"
	SuggestionDiscarded SuggestionStatus = "discarded"
)

func (s SuggestionStatus) Valid() bool {
	return s == SuggestionPending || s == SuggestionApproved || s == SuggestionDiscarded
}

type User struct {
	DiscordID   string    `json:"discord_id"`
	Username    string    `json:"username"`
	GlobalName  string    `json:"global_name"`
	Avatar      string    `json:"avatar"`
	CreatedAt   time.Time `json:"created_at"`
	LastLoginAt time.Time `json:"last_login_at"`
}

// Server is a registered Discord server (one per streamer). All series,
// videos and suggestions belong to exactly one server.
type Server struct {
	ID                   int64    `json:"id"`
	GuildID              string   `json:"guild_id"`
	Name                 string   `json:"name"`
	GuildName            string   `json:"guild_name"`
	Icon                 string   `json:"icon"`
	ModRoleIDs           []string `json:"mod_role_ids"`
	AllowedUserIDs       []string `json:"allowed_user_ids"`
	ClipEditorRoleIDs    []string `json:"clip_editor_role_ids"`  // see only the clip pages
	VideoEditorRoleIDs   []string `json:"video_editor_role_ids"` // see only the video pages
	SuggestionsChannelID string   `json:"suggestions_channel_id"`
	SuggestionsCursor    string   `json:"-"`
	// BlockSelfSuggestions auto-discards suggestions whose suggester's
	// Discord name matches the video's YouTube channel.
	BlockSelfSuggestions bool      `json:"block_self_suggestions"`
	ClipsChannelID       string    `json:"clips_channel_id"`
	ClipsCursor          string    `json:"-"`
	CreatedBy            string    `json:"created_by"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// ServerPatch holds optional updates; nil fields are left unchanged.
type ServerPatch struct {
	Name                 *string   `json:"name"`
	ModRoleIDs           *[]string `json:"mod_role_ids"`
	AllowedUserIDs       *[]string `json:"allowed_user_ids"`
	ClipEditorRoleIDs    *[]string `json:"clip_editor_role_ids"`
	VideoEditorRoleIDs   *[]string `json:"video_editor_role_ids"`
	SuggestionsChannelID *string   `json:"suggestions_channel_id"`
	BlockSelfSuggestions *bool     `json:"block_self_suggestions"`
	ClipsChannelID       *string   `json:"clips_channel_id"`
}

// AccessEntry is a cached permission check for one user on one server.
type AccessEntry struct {
	Allowed   bool // full access (mod): every section, including suggestions
	CanManage bool // may configure or delete the server
	CanVideos bool // may use the video pages (queue, pipeline, series)
	CanClips  bool // may use the clip pages (inbox, board)
	CheckedAt time.Time
}

// UserGuild is a Discord server the user is in, as of their last login.
type UserGuild struct {
	GuildID   string `json:"guild_id"`
	Name      string `json:"name"`
	Icon      string `json:"icon"`
	CanManage bool   `json:"can_manage"`
}

type Series struct {
	ID           int64  `json:"id"`
	ServerID     int64  `json:"server_id"`
	Title        string `json:"title"`
	Creator      string `json:"creator"`
	URL          string `json:"url"`
	DefaultVenue Venue  `json:"default_venue"`
	Notes        string `json:"notes"`
	// Set by the server from URL / playlist imports, not by clients.
	YouTubePlaylistID string    `json:"youtube_playlist_id"`
	ThumbnailVideoID  string    `json:"thumbnail_video_id"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// SeriesPatch holds optional updates; nil fields are left unchanged.
type SeriesPatch struct {
	Title        *string `json:"title"`
	Creator      *string `json:"creator"`
	URL          *string `json:"url"`
	DefaultVenue *Venue  `json:"default_venue"`
	Notes        *string `json:"notes"`
	// Server-derived; never decoded from client JSON.
	YouTubePlaylistID *string `json:"-"`
	ThumbnailVideoID  *string `json:"-"`
}

type Video struct {
	ID           int64       `json:"id"`
	ServerID     int64       `json:"server_id"`
	SeriesID     *int64      `json:"series_id"`
	Episode      string      `json:"episode"`
	Title        string      `json:"title"`
	URL          string      `json:"url"`
	Venue        Venue       `json:"venue"`
	Status       VideoStatus `json:"status"`
	Position     int64       `json:"position"`
	ScheduledFor *time.Time  `json:"scheduled_for"`
	ReactedAt    *time.Time  `json:"reacted_at"`
	VODURL       string      `json:"vod_url"`
	EditURL      string      `json:"edit_url"` // edited video file
	PatreonURL   string      `json:"patreon_url"`
	PatreonAt    *time.Time  `json:"patreon_at"`
	YouTubeURL   string      `json:"youtube_url"`
	YouTubeAt    *time.Time  `json:"youtube_at"`
	Notes        string      `json:"notes"`
	// YouTubeVideoID is derived by the server from URL.
	YouTubeVideoID  string `json:"youtube_video_id"`
	ChannelTitle    string `json:"channel_title"`
	DurationSeconds int64  `json:"duration_seconds"`
	// Set when the video was approved from a suggestion.
	SuggestedByID   string `json:"suggested_by_id"`
	SuggestedByName string `json:"suggested_by_name"`
	// Who is editing it: a Discord user ID (app user) and/or a display name.
	EditorID   string    `json:"editor_id"`
	EditorName string    `json:"editor_name"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// VideoPatch holds optional updates; nil fields are left unchanged.
// ClearSeries and ClearSchedule set series_id / scheduled_for to NULL.
type VideoPatch struct {
	SeriesID      *int64       `json:"series_id"`
	ClearSeries   bool         `json:"clear_series"`
	Episode       *string      `json:"episode"`
	Title         *string      `json:"title"`
	URL           *string      `json:"url"`
	Venue         *Venue       `json:"venue"`
	Status        *VideoStatus `json:"status"`
	ScheduledFor  *time.Time   `json:"scheduled_for"`
	ClearSchedule bool         `json:"clear_schedule"`
	ReactedAt     *time.Time   `json:"reacted_at"`
	VODURL        *string      `json:"vod_url"`
	EditURL       *string      `json:"edit_url"`
	PatreonURL    *string      `json:"patreon_url"`
	PatreonAt     *time.Time   `json:"patreon_at"`
	YouTubeURL    *string      `json:"youtube_url"`
	YouTubeAt     *time.Time   `json:"youtube_at"`
	Notes         *string      `json:"notes"`
	// YouTubeVideoID is server-derived from URL; never decoded from client JSON.
	YouTubeVideoID  *string `json:"-"`
	ChannelTitle    *string `json:"channel_title"`
	DurationSeconds *int64  `json:"duration_seconds"`
	EditorID        *string `json:"editor_id"`
	EditorName      *string `json:"editor_name"`
}

// VideoFilter narrows ListVideos. ServerID is required; other zero values mean "any".
type VideoFilter struct {
	ServerID int64
	Status   []VideoStatus
	Venue    Venue
	SeriesID int64
}

type Suggestion struct {
	ID               int64            `json:"id"`
	ServerID         int64            `json:"server_id"`
	DiscordMessageID string           `json:"discord_message_id"`
	ChannelID        string           `json:"channel_id"`
	AuthorID         string           `json:"author_id"`
	AuthorName       string           `json:"author_name"`
	AuthorUsername   string           `json:"author_username"` // Discord username; AuthorName is the display name
	Content          string           `json:"content"`
	ReactionCount    int64            `json:"reaction_count"`
	PostedAt         time.Time        `json:"posted_at"`
	Status           SuggestionStatus `json:"status"`
	ReviewedBy       string           `json:"reviewed_by"`
	ReviewedAt       *time.Time       `json:"reviewed_at"`
	VideoID          *int64           `json:"video_id"`
	// YouTube details of the first YouTube link in Content (see SuggestionVideo).
	YouTubeVideoID  string     `json:"youtube_video_id"`
	VideoTitle      string     `json:"video_title"`
	ChannelTitle    string     `json:"channel_title"`
	DurationSeconds int64      `json:"duration_seconds"`
	VideoLookupAt   *time.Time `json:"-"` // nil = not looked up yet
	ChannelHandle   string     `json:"channel_handle"`
	SelfSuggestion  bool       `json:"self_suggestion"`
	DiscardReason   string     `json:"discard_reason"` // "" or DiscardSelfSuggestion
	SelfOverride    bool       `json:"-"`
	RemovedAt       *time.Time `json:"-"` // hidden from the Suggestions page; never re-imported
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// SuggestionVideo is the result of looking up a suggestion's YouTube link.
// The zero value means "no YouTube link, or the video wasn't found".
type SuggestionVideo struct {
	YouTubeVideoID  string
	Title           string
	ChannelTitle    string
	DurationSeconds int64
	ChannelHandle   string // "@handle", or "" if unknown
	SelfSuggestion  bool
}

// DiscardSelfSuggestion is Suggestion.DiscardReason for automatic self-suggestion discards.
const DiscardSelfSuggestion = "self_suggestion"

// Suggester is one person who suggested a given YouTube video.
type Suggester struct {
	AuthorID   string    `json:"author_id"`
	AuthorName string    `json:"author_name"`
	FirstAt    time.Time `json:"first_at"` // their earliest suggestion of it
}

type SuggestionSort string

const (
	SortNewest SuggestionSort = "newest"
	SortOldest SuggestionSort = "oldest"
	SortVotes  SuggestionSort = "votes" // reaction_count desc, then newest
)

// SuggestionFilter narrows ListSuggestions. ServerID is required; empty Status means "any".
type SuggestionFilter struct {
	ServerID int64
	AuthorID string // optional
	Status   SuggestionStatus
	Sort     SuggestionSort // default SortNewest
}

type ClipStatus string

const (
	ClipNew       ClipStatus = "new"       // in the inbox, not reviewed
	ClipDiscarded ClipStatus = "discarded" // rejected from the inbox
	ClipPending   ClipStatus = "pending"   // selected, waiting to be edited
	ClipEdited    ClipStatus = "edited"
	ClipPosted    ClipStatus = "posted"
)

func (s ClipStatus) Valid() bool {
	switch s {
	case ClipNew, ClipDiscarded, ClipPending, ClipEdited, ClipPosted:
		return true
	}
	return false
}

// Platform is a social platform a finished clip is posted to.
type Platform string

const (
	PlatformYouTube   Platform = "youtube"
	PlatformInstagram Platform = "instagram"
	PlatformX         Platform = "x"
	PlatformTikTok    Platform = "tiktok"
)

func (p Platform) Valid() bool {
	switch p {
	case PlatformYouTube, PlatformInstagram, PlatformX, PlatformTikTok:
		return true
	}
	return false
}

// Clip is a Twitch clip shared in a server's clips channel.
type Clip struct {
	ID               int64      `json:"id"`
	ServerID         int64      `json:"server_id"`
	DiscordMessageID string     `json:"discord_message_id"`
	ChannelID        string     `json:"channel_id"`
	MessageURL       string     `json:"message_url"`
	AuthorID         string     `json:"author_id"` // Discord user who shared it
	AuthorName       string     `json:"author_name"`
	AuthorUsername   string     `json:"author_username"`
	Content          string     `json:"content"`
	PostedAt         time.Time  `json:"posted_at"` // Discord message time
	ClipURL          string     `json:"clip_url"`
	TwitchClipID     string     `json:"twitch_clip_id"`
	Title            string     `json:"title"`
	ClipperName      string     `json:"clipper_name"` // Twitch account that made the clip
	BroadcasterName  string     `json:"broadcaster_name"`
	ThumbnailURL     string     `json:"thumbnail_url"`
	DurationSeconds  int64      `json:"duration_seconds"`
	ClipCreatedAt    *time.Time `json:"clip_created_at"`
	TwitchLookupAt   *time.Time `json:"-"`
	Status           ClipStatus `json:"status"`
	StatusAt         time.Time  `json:"status_at"`
	ReviewedBy       string     `json:"reviewed_by"`
	ReviewedAt       *time.Time `json:"reviewed_at"`
	EditedTitle      string     `json:"edited_title"`
	EditedURL        string     `json:"edited_url"`
	YouTubeURL       string     `json:"youtube_url"`
	InstagramURL     string     `json:"instagram_url"`
	XURL             string     `json:"x_url"`
	TikTokURL        string     `json:"tiktok_url"`
	Notes            string     `json:"notes"`
	FirstPostedAt    *time.Time `json:"first_posted_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// ClipPatch holds optional updates from mods; nil fields are left unchanged.
type ClipPatch struct {
	Status       *ClipStatus `json:"status"`
	EditedTitle  *string     `json:"edited_title"`
	EditedURL    *string     `json:"edited_url"`
	YouTubeURL   *string     `json:"youtube_url"`
	InstagramURL *string     `json:"instagram_url"`
	XURL         *string     `json:"x_url"`
	TikTokURL    *string     `json:"tiktok_url"`
	Notes        *string     `json:"notes"`
}

type ClipSort string

const (
	ClipSortNewest   ClipSort = "newest"    // Discord message time, newest first
	ClipSortOldest   ClipSort = "oldest"    // Discord message time, oldest first
	ClipSortStatusAt ClipSort = "status_at" // when it entered its current status, oldest first (board columns)
)

// ClipFilter narrows ListClips. ServerID is required; empty Statuses means any.
type ClipFilter struct {
	ServerID int64
	Statuses []ClipStatus
	Sort     ClipSort // default ClipSortNewest
	// Missing keeps only clips with no link yet for at least one of these platforms.
	Missing []Platform
}

// ClipTwitch is the result of looking a clip up in the Twitch API. The zero
// value means the clip wasn't found.
type ClipTwitch struct {
	Title           string
	ClipperName     string
	BroadcasterName string
	ThumbnailURL    string
	DurationSeconds int64
	CreatedAt       *time.Time
}
