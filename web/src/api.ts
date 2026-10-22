export type Venue = 'watch_party' | 'stream'
export type VideoStatus = 'queued' | 'reacted' | 'editing' | 'patreon' | 'youtube' | 'skipped'
export type SuggestionStatus = 'pending' | 'approved' | 'discarded'
export type SuggestionSort = 'newest' | 'oldest' | 'votes'

export interface User {
  discord_id: string
  username: string
  global_name: string
  avatar: string
  created_at: string
  last_login_at: string
}

export interface Series {
  id: number
  server_id: number
  title: string
  creator: string
  url: string
  default_venue: Venue | ''
  notes: string
  youtube_playlist_id: string
  thumbnail_video_id: string
  created_at: string
  updated_at: string
}

export interface SeriesInput {
  title: string
  creator?: string
  url?: string
  default_venue?: Venue | ''
  notes?: string
}

export type SeriesPatch = Partial<SeriesInput>

export interface Video {
  id: number
  server_id: number
  series_id: number | null
  episode: string
  title: string
  url: string
  venue: Venue
  status: VideoStatus
  position: number
  scheduled_for: string | null
  reacted_at: string | null
  vod_url: string
  edit_url: string
  patreon_url: string
  patreon_at: string | null
  youtube_url: string
  youtube_at: string | null
  notes: string
  youtube_video_id: string
  channel_title: string
  duration_seconds: number
  suggested_by_id: string
  suggested_by_name: string
  editor_id: string
  editor_name: string
  created_by: string
  created_at: string
  updated_at: string
}

export interface VideoCreate {
  title: string
  venue: Venue
  series_id?: number | null
  episode?: string
  url?: string
  scheduled_for?: string | null
  notes?: string
  channel_title?: string
  duration_seconds?: number
}

export interface VideoPatch {
  series_id?: number
  clear_series?: boolean
  episode?: string
  title?: string
  url?: string
  venue?: Venue
  status?: VideoStatus
  scheduled_for?: string
  clear_schedule?: boolean
  reacted_at?: string
  vod_url?: string
  edit_url?: string
  patreon_url?: string
  patreon_at?: string
  youtube_url?: string
  youtube_at?: string
  notes?: string
  channel_title?: string
  duration_seconds?: number
  editor_id?: string
  editor_name?: string
}

export interface Member {
  discord_id: string
  name: string
  avatar: string
}

export interface VideoFilter {
  status?: VideoStatus[]
  venue?: Venue
  series_id?: number
}

export interface Suggester {
  author_id: string
  author_name: string
  first_at: string
}

export interface Suggestion {
  id: number
  server_id: number
  discord_message_id: string
  channel_id: string
  author_id: string
  author_name: string
  author_username: string
  content: string
  reaction_count: number
  posted_at: string
  youtube_video_id: string
  video_title: string
  channel_title: string
  channel_handle: string
  self_suggestion: boolean
  discard_reason: '' | 'self_suggestion'
  duration_seconds: number
  status: SuggestionStatus
  reviewed_by: string
  reviewed_at: string | null
  video_id: number | null
  suggesters: Suggester[]
  created_at: string
  updated_at: string
}

export interface ServerSections {
  videos: boolean
  clips: boolean
  suggestions: boolean
}

export interface Server {
  id: number
  guild_id: string
  name: string
  guild_name: string
  icon: string
  mod_role_ids: string[]
  clip_editor_role_ids: string[]
  video_editor_role_ids: string[]
  allowed_user_ids: string[]
  suggestions_channel_id: string
  block_self_suggestions: boolean
  clips_channel_id: string
  can_manage: boolean
  sections: ServerSections
  created_by: string
  created_at: string
  updated_at: string
}

export interface ServerPatch {
  name?: string
  mod_role_ids?: string[]
  clip_editor_role_ids?: string[]
  video_editor_role_ids?: string[]
  allowed_user_ids?: string[]
  suggestions_channel_id?: string
  block_self_suggestions?: boolean
  clips_channel_id?: string
}

export interface Me {
  user: User
  is_admin: boolean
  can_register: boolean
  youtube_enabled: boolean
  twitch_enabled: boolean
}

export interface Guild {
  id: string
  name: string
  icon: string
  server_id: number | null
  bot_present: boolean
  invite_url: string
}

export interface GuildsResponse {
  invite_url: string
  guilds: Guild[]
}

export interface Role {
  id: string
  name: string
  color: number
  position: number
  managed: boolean
}

export interface Channel {
  id: string
  name: string
  type: number
  position: number
  parent_id: string
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

let onUnauthorized: (() => void) | null = null

/** Register a callback run whenever any request returns 401. */
export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: 'same-origin', headers: {} }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  let res: Response
  try {
    res = await fetch(path, init)
  } catch {
    throw new ApiError(0, 'Network error: could not reach the server')
  }
  if (!res.ok) {
    let message = res.statusText || `Request failed (${res.status})`
    try {
      const data = (await res.json()) as { error?: string }
      if (data.error) message = data.error
    } catch {
      // body was not JSON; keep the status text
    }
    if (res.status === 401) onUnauthorized?.()
    throw new ApiError(res.status, message)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

const get = <T>(path: string) => request<T>('GET', path)
const post = <T>(path: string, body?: unknown) => request<T>('POST', path, body)
const patch = <T>(path: string, body: unknown) => request<T>('PATCH', path, body)
const del = (path: string) => request<void>('DELETE', path)

// Auth
export const getMe = () => get<Me>('/api/me')
export const logout = () => post<void>('/api/auth/logout')

// Servers
export const listServers = () => get<Server[]>('/api/servers')
export const getServer = (sid: number) => get<Server>(`/api/servers/${sid}`)
export const createServer = (input: { guild_id: string; name?: string }) => post<Server>('/api/servers', input)
export const updateServer = (sid: number, p: ServerPatch) => patch<Server>(`/api/servers/${sid}`, p)
export const deleteServer = (sid: number) => del(`/api/servers/${sid}`)
export const listRoles = (sid: number) => get<Role[]>(`/api/servers/${sid}/discord/roles`)
export const listChannels = (sid: number) => get<Channel[]>(`/api/servers/${sid}/discord/channels`)
export const listGuilds = () => get<GuildsResponse>('/api/guilds')

/** Guild icon URL for a server, or '' when it has none. */
export const guildIconUrl = (s: Pick<Server, 'guild_id' | 'icon'>) =>
  s.icon ? `https://cdn.discordapp.com/icons/${s.guild_id}/${s.icon}.png?size=64` : ''

const base = (sid: number) => `/api/servers/${sid}`

// Series
export const listSeries = (sid: number) => get<Series[]>(`${base(sid)}/series`)
export const createSeries = (sid: number, input: SeriesInput) => post<Series>(`${base(sid)}/series`, input)
export const updateSeries = (sid: number, id: number, p: SeriesPatch) =>
  patch<Series>(`${base(sid)}/series/${id}`, p)
/** Deletes a series; with deleteQueued, its videos still in the queue are deleted too. */
export const deleteSeries = (sid: number, id: number, deleteQueued = false) =>
  del(`${base(sid)}/series/${id}${deleteQueued ? '?delete_queued=true' : ''}`)

// Videos
export function listVideos(sid: number, f: VideoFilter = {}) {
  const q = new URLSearchParams()
  if (f.status?.length) q.set('status', f.status.join(','))
  if (f.venue) q.set('venue', f.venue)
  if (f.series_id) q.set('series_id', String(f.series_id))
  const qs = q.toString()
  return get<Video[]>(`${base(sid)}/videos` + (qs ? `?${qs}` : ''))
}
export const createVideo = (sid: number, input: VideoCreate) => post<Video>(`${base(sid)}/videos`, input)
export const getVideo = (sid: number, id: number) => get<Video>(`${base(sid)}/videos/${id}`)
export const updateVideo = (sid: number, id: number, p: VideoPatch) =>
  patch<Video>(`${base(sid)}/videos/${id}`, p)
export const deleteVideo = (sid: number, id: number) => del(`${base(sid)}/videos/${id}`)
export const reorderVideos = (sid: number, ids: number[]) => post<void>(`${base(sid)}/videos/reorder`, { ids })

export const listMembers = (sid: number) => get<Member[]>(`${base(sid)}/members`)

// Suggestions
export function listSuggestions(sid: number, status: SuggestionStatus, sort: SuggestionSort, authorId?: string) {
  const q = new URLSearchParams({ status, sort })
  if (authorId) q.set('author_id', authorId)
  return get<Suggestion[]>(`${base(sid)}/suggestions?${q}`)
}
export const approveSuggestion = (sid: number, id: number, input: VideoCreate) =>
  post<{ suggestion: Suggestion; video: Video }>(`${base(sid)}/suggestions/${id}/approve`, input)
export const discardSuggestion = (sid: number, id: number) =>
  post<Suggestion>(`${base(sid)}/suggestions/${id}/discard`)
export const restoreSuggestion = (sid: number, id: number) =>
  post<Suggestion>(`${base(sid)}/suggestions/${id}/restore`)
export const removeSuggestions = (sid: number, ids: number[]) =>
  post<{ removed: number }>(`${base(sid)}/suggestions/remove`, { ids })
export const unremoveSuggestion = (sid: number, id: number) =>
  post<void>(`${base(sid)}/suggestions/${id}/unremove`)
export const syncSuggestions = (sid: number) => post<void>(`${base(sid)}/suggestions/sync`)

// Clips (Twitch clips shared in a server's clips channel)

export type ClipStatus = 'new' | 'discarded' | 'pending' | 'edited' | 'posted'
export type Platform = 'youtube' | 'instagram' | 'x' | 'tiktok'

export const PLATFORMS: { id: Platform; label: string; field: 'youtube_url' | 'instagram_url' | 'x_url' | 'tiktok_url' }[] = [
  { id: 'youtube', label: 'YouTube', field: 'youtube_url' },
  { id: 'instagram', label: 'Instagram', field: 'instagram_url' },
  { id: 'x', label: 'X', field: 'x_url' },
  { id: 'tiktok', label: 'TikTok', field: 'tiktok_url' },
]

export interface Clip {
  id: number
  server_id: number
  discord_message_id: string
  channel_id: string
  message_url: string
  author_id: string // Discord user who shared it
  author_name: string
  author_username: string
  content: string
  posted_at: string
  clip_url: string
  twitch_clip_id: string
  title: string
  clipper_name: string // Twitch account that made the clip
  broadcaster_name: string
  thumbnail_url: string
  duration_seconds: number
  clip_created_at: string | null
  status: ClipStatus
  status_at: string
  reviewed_by: string
  reviewed_at: string | null
  edited_title: string
  edited_url: string
  youtube_url: string
  instagram_url: string
  x_url: string
  tiktok_url: string
  notes: string
  first_posted_at: string | null
  created_at: string
  updated_at: string
}

export interface ClipPatch {
  status?: ClipStatus
  edited_title?: string
  edited_url?: string
  youtube_url?: string
  instagram_url?: string
  x_url?: string
  tiktok_url?: string
  notes?: string
}

export type ClipSort = 'newest' | 'oldest' | 'status_at'

export function listClips(sid: number, opts: { status?: ClipStatus[]; sort?: ClipSort; missing?: Platform[] } = {}) {
  const q = new URLSearchParams()
  if (opts.status?.length) q.set('status', opts.status.join(','))
  if (opts.sort) q.set('sort', opts.sort)
  if (opts.missing?.length) q.set('missing', opts.missing.join(','))
  const qs = q.toString()
  return get<Clip[]>(`${base(sid)}/clips${qs ? `?${qs}` : ''}`)
}
export const getClip = (sid: number, id: number) => get<Clip>(`${base(sid)}/clips/${id}`)
export const updateClip = (sid: number, id: number, p: ClipPatch) => patch<Clip>(`${base(sid)}/clips/${id}`, p)
export const syncClips = (sid: number) => post<void>(`${base(sid)}/clips/sync`)

// YouTube
export interface YouTubeVideo {
  id: string
  title: string
  channel_title: string
  published_at: string
  duration_seconds: number
  url: string
  thumbnail_url: string
}

export interface YouTubePlaylist {
  id: string
  title: string
  channel_title: string
  url: string
  thumbnail_url: string
}

export interface YouTubePlaylistItem extends YouTubeVideo {
  position: number
}

export interface PlaylistPreview {
  playlist: YouTubePlaylist
  items: YouTubePlaylistItem[]
}

export interface ImportInput {
  playlist_url?: string
  title?: string
  default_venue?: Venue | ''
  video_ids?: string[]
}

export interface ImportResult {
  series: Series
  videos: Video[]
}

export const lookupVideo = (url: string) =>
  get<YouTubeVideo>(`/api/youtube/video?${new URLSearchParams({ url })}`)
export const lookupPlaylist = (url: string) =>
  get<PlaylistPreview>(`/api/youtube/playlist?${new URLSearchParams({ url })}`)
export const importSeries = (sid: number, body: ImportInput) =>
  post<ImportResult>(`${base(sid)}/series/import`, body)
export const importIntoSeries = (sid: number, seriesId: number, body: ImportInput) =>
  post<ImportResult>(`${base(sid)}/series/${seriesId}/import`, body)
