import type { Video } from './api'

/** One row of the queue: a single video, or a collapsed series standing in for all its queued videos. */
export type QueueItem =
  | { kind: 'video'; key: string; video: Video }
  | { kind: 'group'; key: string; seriesId: number; videos: Video[] }

/**
 * Builds display items from videos already in position order. The first video of a collapsed
 * series yields a group holding all of that series' videos (in order); its later videos are skipped.
 */
export function buildItems(videos: Video[], collapsed: ReadonlySet<number>): QueueItem[] {
  const items: QueueItem[] = []
  const groups = new Map<number, Extract<QueueItem, { kind: 'group' }>>()
  for (const video of videos) {
    const sid = video.series_id
    if (sid != null && collapsed.has(sid)) {
      const g = groups.get(sid)
      if (g) {
        g.videos.push(video)
      } else {
        const group = { kind: 'group' as const, key: `s-${sid}`, seriesId: sid, videos: [video] }
        groups.set(sid, group)
        items.push(group)
      }
    } else {
      items.push({ kind: 'video', key: `v-${video.id}`, video })
    }
  }
  return items
}

/** Video ids in display order; a group expands to its videos, keeping their relative order. */
export function flattenIds(items: QueueItem[]): number[] {
  return items.flatMap((it) => (it.kind === 'group' ? it.videos.map((v) => v.id) : [it.video.id]))
}

/**
 * Moves the item at `from` so it lands before the item currently at slot `at` (0..n, n = end).
 * Removing the item first shifts later indices down by one, hence `at - 1` when moving down.
 * Returns the new item order, or null when the move is a no-op or out of range.
 */
export function moveItem(items: QueueItem[], from: number, at: number): QueueItem[] | null {
  if (from < 0 || from >= items.length || at < 0 || at > items.length) return null
  const to = at > from ? at - 1 : at
  if (to === from) return null
  const next = items.slice()
  const [moved] = next.splice(from, 1)
  next.splice(to, 0, moved)
  return next
}

/** Swaps the item at `i` with its neighbour (dir -1 up, 1 down); null when there is none. */
export function swapItem(items: QueueItem[], i: number, dir: -1 | 1): QueueItem[] | null {
  const j = i + dir
  if (i < 0 || i >= items.length || j < 0 || j >= items.length) return null
  const next = items.slice()
  ;[next[i], next[j]] = [next[j], next[i]]
  return next
}

const storageKey = (serverId: number) => `queue-collapsed-series:${serverId}`

export function loadCollapsed(serverId: number): Set<number> {
  try {
    const raw = localStorage.getItem(storageKey(serverId))
    const arr: unknown = raw ? JSON.parse(raw) : []
    return new Set(Array.isArray(arr) ? arr.filter((x): x is number => typeof x === 'number') : [])
  } catch {
    return new Set()
  }
}

export function saveCollapsed(serverId: number, ids: ReadonlySet<number>) {
  try {
    localStorage.setItem(storageKey(serverId), JSON.stringify([...ids]))
  } catch {
    // storage unavailable; the state still works for this session
  }
}
