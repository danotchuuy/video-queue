import { useEffect, useState, type DragEvent, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { PLATFORMS, listClips, updateClip, type Clip, type ClipStatus, type Platform } from '../api'
import ClipCard from '../components/ClipCard'
import ClipDetails from '../components/ClipDetails'
import { ErrorBox, Loading } from '../components/Status'
import { useServer } from '../session'
import { useFetch } from '../useFetch'
import { errMsg } from '../util'
import '../clipBoard.css'

type Column = 'pending' | 'edited' | 'posted'
const COLUMNS: Column[] = ['pending', 'edited', 'posted']
const LABELS: Record<Column, string> = { pending: 'Pending', edited: 'Edited', posted: 'Posted' }
const DRAG_TYPE = 'application/x-clip-id'
const MISSING_KEY = 'clipboard.missing'

/** Optimistic move; only applies while the fetched list is still the one it was made against. */
interface Move {
  to: ClipStatus
  base: Clip[] | null
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

interface ColumnProps {
  status: Column
  count: number
  onDropClip: (id: number) => void
  extra?: ReactNode
  children: ReactNode
}

function BoardColumn({ status, count, onDropClip, extra, children }: ColumnProps) {
  // dragenter/dragleave also fire for child elements, so count them instead of toggling on each event
  const [depth, setDepth] = useState(0)
  const isOurs = (e: DragEvent) => e.dataTransfer.types.includes(DRAG_TYPE)

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
        e.preventDefault()
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
        if (Number.isInteger(id)) onDropClip(id)
      }}
    >
      <div className="clip-head">
        <h2 className="kanban-head">
          {LABELS[status]} <span className="muted">({count})</span>
        </h2>
        {extra}
      </div>
      <div className="kanban-list">
        {children}
        {count === 0 && <p className="kanban-empty muted small">Drop here</p>}
      </div>
    </section>
  )
}

export default function ClipBoard() {
  const { id: sid } = useServer()
  const [params, setParams] = useSearchParams()
  const [missing, setMissing] = useState<Platform[]>(loadMissing)
  const clips = useFetch(async () => {
    if (missing.length === 0) return listClips(sid, { status: COLUMNS, sort: 'status_at' })
    const [rest, posted] = await Promise.all([
      listClips(sid, { status: ['pending', 'edited'], sort: 'status_at' }),
      listClips(sid, { status: ['posted'], sort: 'status_at', missing }),
    ])
    return [...rest, ...posted]
  }, `${sid}:${missing.join(',')}`)
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

  const selectedId = Number(params.get('clip')) || null
  const open = params.has('clip')

  function select(id: number | null) {
    setParams((p) => {
      const next = new URLSearchParams(p)
      if (id == null) next.delete('clip')
      else next.set('clip', String(id))
      return next
    })
  }

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape')
        setParams((p) => {
          const next = new URLSearchParams(p)
          next.delete('clip')
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

  if (clips.loading && !clips.data) return <Loading />
  if (clips.error && !clips.data) return <ErrorBox message={clips.error} onRetry={clips.reload} />

  const all = (clips.data ?? []).map((c) => {
    const m = moves[c.id]
    return m && m.base === clips.data ? { ...c, status: m.to } : c
  })
  const selected = selectedId != null ? all.find((c) => c.id === selectedId) : undefined

  function toggleMissing(id: Platform) {
    const next = PLATFORMS.map((p) => p.id).filter((x) => (x === id ? !missing.includes(x) : missing.includes(x)))
    setMissing(next)
    saveMissing(next)
  }

  async function moveClip(id: number, to: Column) {
    const c = all.find((x) => x.id === id)
    if (!c || c.status === to) return
    setMoveError(null)
    setMoves((m) => ({ ...m, [id]: { to, base: clips.data } }))
    const clear = () =>
      setMoves((m) => {
        const { [id]: _, ...rest } = m
        return rest
      })
    try {
      await updateClip(sid, id, { status: to })
      clips.reload() // the optimistic move lapses once the refetched list arrives
    } catch (e) {
      clear()
      setMoveError(errMsg(e))
    }
  }

  const filter = (
    <div className="clip-filter" role="group" aria-label="Posted filter">
      <span className="muted">Not on:</span>
      {PLATFORMS.map((p) => (
        <button
          key={p.id}
          type="button"
          className="clip-chip"
          aria-pressed={missing.includes(p.id)}
          onClick={() => toggleMissing(p.id)}
        >
          {p.label}
        </button>
      ))}
    </div>
  )

  return (
    <div className={selected ? 'clipboard has-panel' : 'clipboard'}>
      <div className="page-head">
        <h1>Clip board</h1>
      </div>
      {clips.error && <ErrorBox message={clips.error} onRetry={clips.reload} />}
      {moveError && <ErrorBox message={moveError} />}
      <div className="kanban">
        {COLUMNS.map((status) => {
          const items = all.filter((c) => c.status === status)
          return (
            <BoardColumn
              key={status}
              status={status}
              count={items.length}
              onDropClip={(id) => moveClip(id, status)}
              extra={status === 'posted' ? filter : undefined}
            >
              {items.map((c) => (
                <ClipCard
                  key={c.id}
                  clip={c}
                  selected={c.id === selectedId}
                  dragging={c.id === dragId}
                  onOpen={() => select(c.id)}
                  onDragStart={(e) => {
                    e.dataTransfer.setData('text/plain', String(c.id))
                    e.dataTransfer.setData(DRAG_TYPE, String(c.id))
                    e.dataTransfer.effectAllowed = 'move'
                    setDragId(c.id)
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
          <ClipDetails key={selected.id} clip={selected} onChanged={clips.reload} onClose={() => select(null)} />
        </div>
      )}
    </div>
  )
}
