package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
)

// ErrNotFound is returned when Discord reports the guild, member or channel
// doesn't exist or the bot can't see it (404, or 403 Missing Access).
var ErrNotFound = errors.New("discord: not found")

type Guild struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Icon    string `json:"icon"`
	OwnerID string `json:"owner_id"` // set by Guild, not by BotGuilds
	Roles   []Role `json:"roles"`    // set by Guild, not by BotGuilds
}

type Role struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Color       int    `json:"color"`
	Position    int    `json:"position"`
	Managed     bool   `json:"managed"`
	Permissions string `json:"permissions"` // bitfield as a decimal string
}

type Channel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     int    `json:"type"`
	Position int    `json:"position"`
	ParentID string `json:"parent_id"`
}

type Member struct {
	Roles []string `json:"roles"`
}

// getJSON GETs path (relative to the base URL) and decodes the JSON body into v.
func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	body, err := c.get(ctx, c.baseURL+path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("discord: decode %s: %w", path, err)
	}
	return nil
}

const guildPageSize = 200

// BotGuilds lists every guild the bot is in (GET /users/@me/guilds, paginated).
func (c *Client) BotGuilds(ctx context.Context) ([]Guild, error) {
	var all []Guild
	after := ""
	for {
		path := "/users/@me/guilds?limit=" + strconv.Itoa(guildPageSize)
		if after != "" {
			path += "&after=" + url.QueryEscape(after)
		}
		var page []Guild
		if err := c.getJSON(ctx, path, &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < guildPageSize {
			return all, nil
		}
		after = page[len(page)-1].ID
	}
}

// Guild fetches one guild the bot is in.
func (c *Client) Guild(ctx context.Context, guildID string) (Guild, error) {
	var g Guild
	err := c.getJSON(ctx, "/guilds/"+url.PathEscape(guildID), &g)
	return g, err
}

// GuildRoles lists assignable roles: excludes @everyone and managed (bot/integration)
// roles, ordered by position descending (highest first, like Discord's UI).
func (c *Client) GuildRoles(ctx context.Context, guildID string) ([]Role, error) {
	var all []Role
	if err := c.getJSON(ctx, "/guilds/"+url.PathEscape(guildID)+"/roles", &all); err != nil {
		return nil, err
	}
	roles := make([]Role, 0, len(all))
	for _, r := range all {
		if r.ID == guildID || r.Managed {
			continue
		}
		roles = append(roles, r)
	}
	sort.SliceStable(roles, func(i, j int) bool { return roles[i].Position > roles[j].Position })
	return roles, nil
}

// GuildTextChannels lists text (0) and announcement (5) channels ordered by position.
func (c *Client) GuildTextChannels(ctx context.Context, guildID string) ([]Channel, error) {
	var all []Channel
	if err := c.getJSON(ctx, "/guilds/"+url.PathEscape(guildID)+"/channels", &all); err != nil {
		return nil, err
	}
	chans := make([]Channel, 0, len(all))
	for _, ch := range all {
		if ch.Type == 0 || ch.Type == 5 {
			chans = append(chans, ch)
		}
	}
	sort.SliceStable(chans, func(i, j int) bool {
		if chans[i].Position != chans[j].Position {
			return chans[i].Position < chans[j].Position
		}
		return snowflakeGreater(chans[j].ID, chans[i].ID)
	})
	return chans, nil
}

// GuildMember fetches a member; ErrNotFound if the user isn't in the guild.
func (c *Client) GuildMember(ctx context.Context, guildID, userID string) (Member, error) {
	var m Member
	err := c.getJSON(ctx, "/guilds/"+url.PathEscape(guildID)+"/members/"+url.PathEscape(userID), &m)
	return m, err
}

// Permission bits used to decide who manages a server.
const (
	PermAdministrator uint64 = 1 << 3
	PermManageGuild   uint64 = 1 << 5
)

// HasManagePermission reports whether a permissions bitfield string (as
// returned by Discord) includes Administrator or Manage Server.
func HasManagePermission(perms string) bool {
	p, err := strconv.ParseUint(perms, 10, 64)
	return err == nil && p&(PermAdministrator|PermManageGuild) != 0
}

// CanManage reports whether userID, a member of g with member m, may manage
// the server: the owner, or anyone whose @everyone + role permissions include
// Administrator or Manage Server. g must come from Guild (it needs OwnerID and Roles).
func CanManage(g Guild, m Member, userID string) bool {
	if g.OwnerID != "" && g.OwnerID == userID {
		return true
	}
	for _, r := range g.Roles {
		// @everyone shares the guild's ID and applies to every member.
		if (r.ID == g.ID || slices.Contains(m.Roles, r.ID)) && HasManagePermission(r.Permissions) {
			return true
		}
	}
	return false
}
