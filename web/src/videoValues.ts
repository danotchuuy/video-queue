import type { Series, Venue, Video, VideoCreate, VideoPatch } from './api'
import { fromDateInput, toDateInput } from './util'

export interface VideoValues {
  title: string
  venue: Venue
  series_id: number | null
  episode: string
  url: string
  scheduled_for: string // yyyy-mm-dd or ''
  notes: string
  channel_title: string
  duration_seconds: number
}

export function emptyValues(venue: Venue = 'watch_party'): VideoValues {
  return { title: '', venue, series_id: null, episode: '', url: '', scheduled_for: '', notes: '', channel_title: '', duration_seconds: 0 }
}

export function valuesFromVideo(v: Video): VideoValues {
  return {
    title: v.title,
    venue: v.venue,
    series_id: v.series_id,
    episode: v.episode,
    url: v.url,
    scheduled_for: toDateInput(v.scheduled_for),
    notes: v.notes,
    channel_title: v.channel_title,
    duration_seconds: v.duration_seconds,
  }
}

export function toCreate(v: VideoValues): VideoCreate {
  return {
    title: v.title.trim(),
    venue: v.venue,
    series_id: v.series_id,
    episode: v.episode.trim(),
    url: v.url.trim(),
    scheduled_for: v.scheduled_for ? fromDateInput(v.scheduled_for) : null,
    notes: v.notes.trim(),
    channel_title: v.channel_title,
    duration_seconds: v.duration_seconds,
  }
}

export function toPatch(v: VideoValues): VideoPatch {
  const p: VideoPatch = {
    title: v.title.trim(),
    venue: v.venue,
    episode: v.episode.trim(),
    url: v.url.trim(),
    notes: v.notes.trim(),
    channel_title: v.channel_title,
    duration_seconds: v.duration_seconds,
  }
  if (v.series_id == null) p.clear_series = true
  else p.series_id = v.series_id
  if (v.scheduled_for) p.scheduled_for = fromDateInput(v.scheduled_for)
  else p.clear_schedule = true
  return p
}

export function seriesDefaultVenue(series: Series[], id: number | null): Venue | null {
  const s = series.find((x) => x.id === id)
  return s && s.default_venue ? s.default_venue : null
}
