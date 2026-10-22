import type { Venue, VideoStatus } from './api'

export const VENUE_LABELS: Record<Venue, string> = {
  watch_party: 'Watch party',
  stream: 'Stream',
}

export const STATUS_LABELS: Record<VideoStatus, string> = {
  queued: 'Queued',
  reacted: 'Reacted',
  editing: 'Editing',
  patreon: 'Patreon',
  youtube: 'YouTube',
  skipped: 'Archived',
}

export const STATUSES = Object.keys(STATUS_LABELS) as VideoStatus[]
export const VENUES = Object.keys(VENUE_LABELS) as Venue[]

export function formatDateTime(iso: string): string {
  const d = new Date(iso)
  return isNaN(d.getTime()) ? iso : d.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })
}

export function formatDate(iso: string): string {
  const d = new Date(iso)
  return isNaN(d.getTime()) ? iso : d.toLocaleDateString([], { dateStyle: 'medium' })
}

/** ISO timestamp -> value for <input type="date"> (local date). */
export function toDateInput(iso: string | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (isNaN(d.getTime())) return ''
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

/** <input type="date"> value -> RFC 3339 timestamp (local midnight). */
export function fromDateInput(v: string): string {
  return new Date(`${v}T00:00:00`).toISOString()
}

export const URL_RE = /https?:\/\/[^\s<>"')\]]+/g

export function firstUrl(text: string): string {
  return text.match(URL_RE)?.[0] ?? ''
}

/** ID of the first YouTube video URL in free text, or ''. */
export function firstYouTubeId(text: string): string {
  for (const m of text.matchAll(URL_RE)) {
    const id = parseYouTubeVideoId(m[0])
    if (id) return id
  }
  return ''
}

export function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

const VIDEO_ID_RE = /^[A-Za-z0-9_-]{11}$/
const YT_HOSTS = new Set(['youtube.com', 'www.youtube.com', 'm.youtube.com', 'music.youtube.com'])

/** Mirrors the server's youtube.ParseVideoID; returns '' when text is not a YouTube video. */
export function parseYouTubeVideoId(text: string): string {
  const s = text.trim()
  if (VIDEO_ID_RE.test(s)) return s
  let u: URL
  try {
    u = new URL(s)
  } catch {
    return ''
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return ''
  const host = u.hostname.toLowerCase()
  let id = ''
  if (host === 'youtu.be') {
    id = u.pathname.split('/')[1] ?? ''
  } else if (YT_HOSTS.has(host)) {
    const parts = u.pathname.split('/')
    if (u.pathname === '/watch') id = u.searchParams.get('v') ?? ''
    else if (['shorts', 'embed', 'live', 'v'].includes(parts[1])) id = parts[2] ?? ''
  }
  return VIDEO_ID_RE.test(id) ? id : ''
}

/** Thumbnail URL for a validated YouTube video ID, or '' for anything else. */
export function youtubeThumb(id: string): string {
  return VIDEO_ID_RE.test(id) ? `https://i.ytimg.com/vi/${id}/mqdefault.jpg` : ''
}

/** Seconds -> h:mm:ss or m:ss; '' for 0 or invalid. */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return ''
  const t = Math.floor(seconds)
  const h = Math.floor(t / 3600)
  const m = Math.floor((t % 3600) / 60)
  const sec = String(t % 60).padStart(2, '0')
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`
}
