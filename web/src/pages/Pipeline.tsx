import { useEffect, useState, type DragEvent, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { listSeries, listVideos, updateVideo, type Video, type VideoStatus } from '../api'
import KanbanCard from '../components/KanbanCard'
import { ErrorBox, Loading } from '../components/Status'
import VideoDetails from '../components/VideoDetails'
import { useServer } from '../session'
import { useFetch } from '../useFetch'
import { SORT_OPTIONS, isSortKey, sortVideos, type SortKey } from '../pipelineSort'
import { errMsg } from '../util'

type Column = 'reacted' | 'editing' | 'posted' | 'skipped'
const COLUMNS: Column[] = ['reacted', 'editing', 'posted', 'skipped']
const FETCH_STATUSES: VideoStatus[] = ['reacted', 'editing', 'patreon', 'youtube', 'skipped']
const LABELS: Record<Column, string> = {
  reacted: 'Reacted',
  editing: 'Editing',
  posted: 'Posted',
  skipped: 'Archived',
}
const DRAG_TYPE = 'application/x-video-id'
const MISSING_KEY = 'pipeline.missing'

type Platform = 'patreon' | 'youtube'
const PLATFORMS: {
  id: Platform
  label: string
  field: 'patreon_url' | 'youtube_url'
}[] = [
  { id: 'patreon', label: 'Patreon', field: 'patreon_url' },
  { id: 'youtube', label: 'YouTube', field: 'youtube_url' },
]

function columnOf(status: VideoStatus): Column | null {
  if (status === 'patreon' || status === 'youtube') return 'posted'
  return status === 'reacted' || status === 'editing' || status === 'skipped' ? status : null
}

// Remembered per browser; storage may be unavailable (private mode etc.).
function loadMissing(): Platform[] {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(MISSING_KEY) ?? '[]')
    if (!Array.isArray(v)) return []
    return PLATFORMS.map((p) => p.id).filter((id) => v.includes(id))
  } catch {
    return []
  }
}

function saveMissing(m: Platform[]) {
  try {
    localStorage.setItem(MISSING_KEY, JSON.stringify(m))
  } catch {
    // not persisted; the filter still works for this visit
  }
}

const SORT_KEY_PREFIX = 'pipeline.sort.'

// Remembered per browser and column; unknown values are ignored.
function loadSort(col: Column): SortKey {
  try {
    const v = localStorage.getItem(SORT_KEY_PREFIX + col)
    return isSortKey(v) ? v : 'queue'
  } catch {
    return 'queue'
  }
}

function saveSort(col: Column, key: SortKey) {
  try {
    localStorage.setItem(SORT_KEY_PREFIX + col, key)
  } catch {
    // not persisted; the sort still works for this visit
  }
}

/** Optimistic move; only applies while the fetched list is still the one it was made against. */
interface Move {
  to: VideoStatus
  base: Video[] | null
}

interface ColumnProps {
  status: Column
  count: number
  onDropVideo: (id: number) => void
  sort: SortKey
  onSort: (key: SortKey) => void
  extra?: ReactNode
  children: ReactNode
}

function BoardColumn({ status, count, onDropVideo, sort, onSort, extra, children }: ColumnProps) {
  // dragenter/dragleave also fire for child elements, so count them instead of toggling on each event
  const [depth, setDepth] = useState(0)
  const isOurs = (e: DragEvent) => e.dataTransfer.types.includes(DRAG_TYPE)

  // A cancelled drag (e.g. Escape) doesn't always send dragleave; clear the highlight when any drag ends.
  useEffect(() => {
    const reset = () => setDepth(0)
    window.addEventListener('dragend', reset)
    window.addEventListener('drop', reset)
    return () => {
      window.removeEventListener('dragend', reset)
      window.removeEventListener('drop', reset)
    }
  }, [])

  return (
    <section
      className={depth > 0 ? 'kanban-col drag-over' : 'kanban-col'}
      aria-label={LABELS[status]}
      onDragEnter={(e) => {
        if (!isOurs(e)) return
        e.preventDefault()
        setDepth((d) => d + 1)
      }}
      onDragOver={(e) => {
        if (!isOurs(e)) return
        e.preventDefault() // required, otherwise drop never fires
        e.dataTransfer.dropEffect = 'move'
      }}
      onDragLeave={(e) => {
        if (!isOurs(e)) return
        setDepth((d) => Math.max(0, d - 1))
      }}
      onDrop={(e) => {
        if (!isOurs(e)) return
        e.preventDefault()
        setDepth(0)
        const id = Number(e.dataTransfer.getData('text/plain'))
        if (Number.isInteger(id)) onDropVideo(id)
      }}
    >
      <div className="pipeline-head">
        <h2 className="kanban-head">
          {LABELS[status]} <span className="muted">({count})</span>
        </h2>
        <select
          className="pipeline-sort"
          aria-label={`Sort ${LABELS[status]}`}
          value={sort}
          onChange={(e) => isSortKey(e.target.value) && onSort(e.target.value)}
        >
          {SORT_OPTIONS.map((o) => (
            <option key={o.key} value={o.key}>
              {o.label}
            </option>
          ))}
        </select>
        {extra}
      </div>
      <div className="kanban-list">
        {children}
        {count === 0 && <p className="kanban-empty muted small">Drop here</p>}
      </div>
    </section>
  )
}

const SHOW_SKIPPED_KEY = 'pipeline.showSkipped'

// Remembered per browser; storage may be unavailable (private mode etc.).
function loadShowSkipped(): boolean {
  try {
    return localStorage.getItem(SHOW_SKIPPED_KEY) !== 'false'
  } catch {
    return true
  }
}

function saveShowSkipped(show: boolean) {
  try {
    localStorage.setItem(SHOW_SKIPPED_KEY, String(show))
  } catch {
    // not persisted; the toggle still works for this visit
  }
}

export default function Pipeline() {
  const { id: sid } = useServer()
  const [params, setParams] = useSearchParams()
  const videos = useFetch(() => listVideos(sid, { status: FETCH_STATUSES }), String(sid))
  const series = useFetch(() => listSeries(sid), String(sid))
  const [moves, setMoves] = useState<Record<number, Move>>({})
  const [dragId, setDragId] = useState<number | null>(null)
  // Dropping a card into another column remounts it, so the original element's
  // dragend never reaches React. Clear the drag state when any drag ends instead.
  useEffect(() => {
    const reset = () => setDragId(null)
    window.addEventListener('dragend', reset)
    window.addEventListener('drop', reset)
    return () => {
      window.removeEventListener('dragend', reset)
      window.removeEventListener('drop', reset)
    }
  }, [])
  const [moveError, setMoveError] = useState<string | null>(null)
  const [panelTop, setPanelTop] = useState(0)
  const [showSkipped, setShowSkipped] = useState(loadShowSkipped)
  const [missing, setMissing] = useState<Platform[]>(loadMissing)
  const [sorts, setSorts] = useState<Record<Column, SortKey>>(() => ({
    reacted: loadSort('reacted'),
    editing: loadSort('editing'),
    posted: loadSort('posted'),
    skipped: loadSort('skipped'),
  }))

  const selectedId = Number(params.get('video')) || null
  const open = params.has('video')

  function select(id: number | null) {
    setParams((p) => {
      const next = new URLSearchParams(p)
      if (id == null) next.delete('video')
      else next.set('video', String(id))
      return next
    })
  }

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape')
        setParams((p) => {
          const next = new URLSearchParams(p)
          next.delete('video')
          return next
        })
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, setParams])

  // Keep the fixed panel just below the (scrolling) header.
  useEffect(() => {
    const update = () => {
      const h = document.querySelector('.app-header')
      setPanelTop(h ? Math.max(0, Math.round(h.getBoundingClientRect().bottom)) : 0)
    }
    update()
    window.addEventListener('scroll', update, { passive: true })
    window.addEventListener('resize', update)
    return () => {
      window.removeEventListener('scroll', update)
      window.removeEventListener('resize', update)
    }
  }, [])

  if (videos.loading && !videos.data) return <Loading />
  if (videos.error && !videos.data) return <ErrorBox message={videos.error} onRetry={videos.reload} />

  const seriesById = new Map((series.data ?? []).map((s) => [s.id, s]))
  const all = (videos.data ?? []).map((v) => {
    const m = moves[v.id]
    return m && m.base === videos.data ? { ...v, status: m.to } : v
  })
  const selected = selectedId != null ? all.find((v) => v.id === selectedId) : undefined

  function toggleMissing(id: Platform) {
    const next = PLATFORMS.map((p) => p.id).filter((x) => (x === id ? !missing.includes(x) : missing.includes(x)))
    setMissing(next)
    saveMissing(next)
  }

  async function moveVideo(id: number, col: Column) {
    const v = all.find((x) => x.id === id)
    if (!v) return
    // Already in the target column (e.g. a 'youtube' video dropped on Posted): nothing to change.
    if (columnOf(v.status) === col) return
    // Posted holds two statuses; Patreon comes first.
    const to: VideoStatus = col === 'posted' ? 'patreon' : col
    if (v.status === to) return
    setMoveError(null)
    setMoves((m) => ({ ...m, [id]: { to, base: videos.data } }))
    const clear = () =>
      setMoves((m) => {
        const { [id]: _, ...rest } = m
        return rest
      })
    try {
      await updateVideo(sid, id, { status: to })
      videos.reload() // the optimistic move lapses once the refetched list arrives
    } catch (e) {
      clear()
      setMoveError(errMsg(e))
    }
  }

  const filter = (
    <div className="pipeline-filter" role="group" aria-label="Posted filter">
      <span className="muted">Not on:</span>
      {PLATFORMS.map((p) => (
        <button
          key={p.id}
          type="button"
          className="pipeline-chip"
          aria-pressed={missing.includes(p.id)}
          onClick={() => toggleMissing(p.id)}
        >
          {p.label}
        </button>
      ))}
    </div>
  )

  return (
    <div className={selected ? 'pipeline has-panel' : 'pipeline'}>
      <div className="page-head">
        <h1>Pipeline</h1>
        <label className="check">
          <input
            type="checkbox"
            checked={showSkipped}
            onChange={(e) => {
              setShowSkipped(e.target.checked)
              saveShowSkipped(e.target.checked)
            }}
          />
          Show archived
        </label>
      </div>
      {videos.error && <ErrorBox message={videos.error} onRetry={videos.reload} />}
      {moveError && <ErrorBox message={moveError} />}
      <div className="kanban">
        {COLUMNS.filter((status) => showSkipped || status !== 'skipped').map((status) => {
          const filtered = all.filter(
            (v) =>
              columnOf(v.status) === status &&
              (status !== 'posted' ||
                missing.length === 0 ||
                PLATFORMS.some((p) => missing.includes(p.id) && v[p.field] === '')),
          )
          const items = sortVideos(filtered, sorts[status], seriesById)
          return (
            <BoardColumn
              key={status}
              status={status}
              count={items.length}
              sort={sorts[status]}
              onSort={(key) => {
                setSorts((m) => ({ ...m, [status]: key }))
                saveSort(status, key)
              }}
              onDropVideo={(id) => moveVideo(id, status)}
              extra={status === 'posted' ? filter : undefined}
            >
              {items.map((v) => (
                <KanbanCard
                  key={v.id}
                  video={v}
                  column={status}
                  series={v.series_id != null ? seriesById.get(v.series_id) : undefined}
                  selected={v.id === selectedId}
                  dragging={v.id === dragId}
                  onOpen={() => select(v.id)}
                  onDragStart={(e) => {
                    e.dataTransfer.setData('text/plain', String(v.id))
                    e.dataTransfer.setData(DRAG_TYPE, String(v.id))
                    e.dataTransfer.effectAllowed = 'move'
                    setDragId(v.id)
                  }}
                  onDragEnd={() => setDragId(null)}
                />
              ))}
            </BoardColumn>
          )
        })}
      </div>
      {selected && (
        <div className="details-wrap" style={{ top: panelTop }}>
          <VideoDetails
            key={selected.id}
            video={selected}
            series={selected.series_id != null ? seriesById.get(selected.series_id) : undefined}
            onChanged={videos.reload}
            onClose={() => select(null)}
          />
        </div>
      )}
    </div>
  )
}
