import type { Series, Video } from './api'

export type SortKey = 'queue' | 'title' | 'series' | 'reacted-new' | 'reacted-old' | 'posted-new' | 'posted-old' | 'updated'

export const SORT_OPTIONS: { key: SortKey; label: string }[] = [
  { key: 'queue', label: 'Queue order' },
  { key: 'title', label: 'Title' },
  { key: 'series', label: 'Series & episode' },
  { key: 'reacted-new', label: 'Reacted: newest' },
  { key: 'reacted-old', label: 'Reacted: oldest' },
  { key: 'posted-new', label: 'Posted: newest' },
  { key: 'posted-old', label: 'Posted: oldest' },
  { key: 'updated', label: 'Last updated' },
]

export function isSortKey(v: unknown): v is SortKey {
  return SORT_OPTIONS.some((o) => o.key === v)
}

type SortVideo = Pick<
  Video,
  'id' | 'title' | 'episode' | 'series_id' | 'position' | 'reacted_at' | 'patreon_at' | 'youtube_at' | 'updated_at'
>

const text = (a: string, b: string) => a.localeCompare(b, undefined, { sensitivity: 'base' })

function time(iso: string | null | undefined): number | null {
  if (!iso) return null
  const t = Date.parse(iso)
  return isNaN(t) ? null : t
}

/** When the video was first posted: the earliest of the Patreon/YouTube dates that is set. */
function firstPosted(v: SortVideo): number | null {
  const ts = [time(v.patreon_at), time(v.youtube_at)].filter((t): t is number => t != null)
  return ts.length ? Math.min(...ts) : null
}

function queue(a: SortVideo, b: SortVideo): number {
  return a.position - b.position || a.id - b.id
}

/** Compare by an optional date; videos without one go last whatever the direction. Returns 0 for ties. */
function byDate(get: (v: SortVideo) => number | null, newestFirst: boolean) {
  return (a: SortVideo, b: SortVideo): number => {
    const x = get(a)
    const y = get(b)
    if (x == null || y == null) return x == null ? (y == null ? 0 : 1) : -1
    return newestFirst ? y - x : x - y
  }
}

/** Returns a comparator for the key; ties (and missing dates) fall back to queue order. */
export function comparator(key: SortKey, series: Map<number, Pick<Series, 'title'>>): (a: SortVideo, b: SortVideo) => number {
  let primary: (a: SortVideo, b: SortVideo) => number
  switch (key) {
    case 'queue':
      primary = () => 0
      break
    case 'title':
      primary = (a, b) => text(a.title, b.title)
      break
    case 'series':
      primary = (a, b) => {
        const sa = a.series_id != null ? series.get(a.series_id)?.title : undefined
        const sb = b.series_id != null ? series.get(b.series_id)?.title : undefined
        if (sa == null || sb == null) return sa == null ? (sb == null ? 0 : 1) : -1
        return (
          text(sa, sb) ||
          a.episode.localeCompare(b.episode, undefined, { numeric: true }) ||
          text(a.title, b.title)
        )
      }
      break
    case 'reacted-new':
      primary = byDate((v) => time(v.reacted_at), true)
      break
    case 'reacted-old':
      primary = byDate((v) => time(v.reacted_at), false)
      break
    case 'posted-new':
      primary = byDate(firstPosted, true)
      break
    case 'posted-old':
      primary = byDate(firstPosted, false)
      break
    case 'updated':
      primary = byDate((v) => time(v.updated_at), true)
      break
  }
  return (a, b) => primary(a, b) || queue(a, b)
}

export function sortVideos<T extends SortVideo>(items: T[], key: SortKey, series: Map<number, Pick<Series, 'title'>>): T[] {
  return [...items].sort(comparator(key, series))
}
