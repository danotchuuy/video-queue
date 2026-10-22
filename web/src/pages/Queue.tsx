import { Fragment, useEffect, useRef, useState, type DragEvent, type PointerEvent } from 'react'
import {
  createVideo,
  deleteVideo,
  listSeries,
  listVideos,
  reorderVideos,
  updateVideo,
  type Venue,
  type Video,
} from '../api'
import { ErrorBox, Loading, Tabs } from '../components/Status'
import Thumbnail from '../components/Thumbnail'
import VideoForm from '../components/VideoForm'
import { buildItems, flattenIds, loadCollapsed, moveItem, saveCollapsed, swapItem } from '../queueItems'
import { VENUES, VENUE_LABELS, errMsg, formatDate } from '../util'
import { useServer } from '../session'
import { useFetch } from '../useFetch'
import { emptyValues, toCreate, toPatch, valuesFromVideo } from '../videoValues'

const DRAG_TYPE = 'application/x-queue-video-id'
// Elements a drag must not start from, so their own clicks/text selection keep working.
const NO_DRAG = 'button, input, select, textarea, label, a'

/** Optimistic order of the visible venue's ids; only applies while the fetched list is still the one it was made against. */
interface Reorder {
  ids: number[]
  base: Video[] | null
}

export default function Queue() {
  const { id: sid } = useServer()
  const [venue, setVenue] = useState<Venue>('watch_party')
  const [adding, setAdding] = useState(false)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const videos = useFetch(() => listVideos(sid, { status: ['queued'] }), String(sid))
  const series = useFetch(() => listSeries(sid), String(sid))
  const [reorder, setReorder] = useState<Reorder | null>(null)
  const [dragKey, setDragKey] = useState<string | null>(null)
  // Insertion slot while dragging: 0..n, meaning "before item at that index" (n = after the last item).
  const [slot, setSlot] = useState<number | null>(null)
  const pressedInteractive = useRef(false)
  const [collapsedState, setCollapsedState] = useState(() => ({ sid, ids: loadCollapsed(sid) }))
  if (collapsedState.sid !== sid) setCollapsedState({ sid, ids: loadCollapsed(sid) })
  const collapsed = collapsedState.sid === sid ? collapsedState.ids : new Set<number>()

  // A cancelled drag (e.g. Escape) doesn't always send dragleave; clear drag state when any drag ends.
  useEffect(() => {
    const reset = () => {
      setDragKey(null)
      setSlot(null)
    }
    window.addEventListener('dragend', reset)
    window.addEventListener('drop', reset)
    return () => {
      window.removeEventListener('dragend', reset)
      window.removeEventListener('drop', reset)
    }
  }, [])

  if (videos.loading && !videos.data) return <Loading />
  if (videos.error && !videos.data) return <ErrorBox message={videos.error} onRetry={videos.reload} />

  const seriesList = series.data ?? []
  const seriesById = new Map(seriesList.map((s) => [s.id, s]))
  const list = (videos.data ?? [])
    .filter((v) => v.venue === venue)
    .sort((a, b) => a.position - b.position)
  if (reorder && reorder.base === videos.data) {
    const rank = new Map(reorder.ids.map((id, i) => [id, i]))
    if (list.every((v) => rank.has(v.id))) list.sort((a, b) => rank.get(a.id)! - rank.get(b.id)!)
  }
  const items = buildItems(list, collapsed)
  const dragIndex = dragKey != null ? items.findIndex((it) => it.key === dragKey) : -1
  // No indicator where a drop would be a no-op (just before or just after the dragged item).
  const showSlot = slot != null && dragIndex >= 0 && slot !== dragIndex && slot !== dragIndex + 1

  async function act(fn: () => Promise<unknown>) {
    setBusy(true)
    setActionError(null)
    try {
      await fn()
      videos.reload()
    } catch (e) {
      setActionError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  function move(i: number, dir: -1 | 1) {
    const next = swapItem(items, i, dir)
    if (!next) return
    const ids = flattenIds(next)
    return act(() => reorderVideos(sid, ids))
  }

  function setCollapsed(ids: Set<number>) {
    setCollapsedState({ sid, ids })
    saveCollapsed(sid, ids)
  }

  function toggleSeries(seriesId: number) {
    const ids = new Set(collapsed)
    if (!ids.delete(seriesId)) ids.add(seriesId)
    setCollapsed(ids)
  }

  const venueSeriesIds = [...new Set(list.flatMap((v) => (v.series_id != null ? [v.series_id] : [])))]
  const allCollapsed = venueSeriesIds.length > 0 && venueSeriesIds.every((id) => collapsed.has(id))

  function toggleAll() {
    const ids = new Set(collapsed)
    for (const id of venueSeriesIds) {
      if (allCollapsed) ids.delete(id)
      else ids.add(id)
    }
    setCollapsed(ids)
  }

  const isOurs = (e: DragEvent) => dragKey != null && e.dataTransfer.types.includes(DRAG_TYPE)

  function onListDragOver(e: DragEvent<HTMLOListElement>) {
    if (!isOurs(e)) return
    e.preventDefault() // required, otherwise drop never fires
    e.dataTransfer.dropEffect = 'move'
    const li = (e.target as HTMLElement).closest<HTMLLIElement>('li[data-index]')
    if (li) {
      const r = li.getBoundingClientRect()
      const after = e.clientY > r.top + r.height / 2
      const next = Number(li.dataset.index) + (after ? 1 : 0)
      setSlot((s) => (s === next ? s : next))
    } else {
      // In a gap between cards or below the list: only the area past the last card changes the slot.
      const last = e.currentTarget.lastElementChild
      if (last && e.clientY > last.getBoundingClientRect().bottom) setSlot(items.length)
    }
  }

  function onListDrop(e: DragEvent<HTMLOListElement>) {
    if (!isOurs(e)) return
    e.preventDefault()
    const from = dragIndex
    const at = slot
    setDragKey(null)
    setSlot(null)
    if (from < 0 || at == null) return
    // moveItem applies the downward-move adjustment (removing the dragged item shifts later indices down by one).
    const next = moveItem(items, from, at)
    if (next) void dropReorder(flattenIds(next))
  }

  async function dropReorder(ids: number[]) {
    setActionError(null)
    setReorder({ ids, base: videos.data })
    try {
      await reorderVideos(sid, ids)
      videos.reload() // the optimistic order lapses once the refetched list arrives
    } catch (e) {
      setReorder(null)
      setActionError(errMsg(e))
    }
  }

  function remove(v: Video) {
    if (!window.confirm(`Delete "${v.title}"?`)) return
    return act(() => deleteVideo(sid, v.id))
  }

  return (
    <>
      <div className="page-head">
        <h1>Queue</h1>
        <div className="btn-group">
          {venueSeriesIds.length > 0 && (
            <button type="button" onClick={toggleAll}>
              {allCollapsed ? 'Expand all series' : 'Collapse all series'}
            </button>
          )}
          {!adding && (
            <button type="button" className="primary" onClick={() => setAdding(true)}>
              Add video
            </button>
          )}
        </div>
      </div>
      {adding && (
        <section className="card">
          <h2>Add video</h2>
          <VideoForm
            series={seriesList}
            initial={emptyValues(venue)}
            submitLabel="Add"
            onCancel={() => setAdding(false)}
            onSubmit={async (v) => {
              const created = await createVideo(sid, toCreate(v))
              setAdding(false)
              setVenue(created.venue)
              videos.reload()
            }}
          />
        </section>
      )}
      <Tabs
        tabs={VENUES.map((v) => ({ value: v, label: VENUE_LABELS[v] }))}
        value={venue}
        onChange={setVenue}
      />
      {(actionError || series.error) && <ErrorBox message={(actionError ?? series.error)!} />}
      {list.length === 0 ? (
        <p className="muted">Nothing queued for {VENUE_LABELS[venue].toLowerCase()}.</p>
      ) : (
        <ol
          className="queue"
          onDragOver={onListDragOver}
          onDragLeave={(e) => {
            if (!isOurs(e)) return
            // dragleave also fires when moving between child elements; only clear when leaving the list itself
            if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setSlot(null)
          }}
          onDrop={onListDrop}
        >
          {items.map((item, i) => {
            // Consecutive episodes of an expanded series get a small heading above them
            // and are indented. A series spread through the queue gets a heading per run.
            const seriesOf = (it: (typeof items)[number] | undefined) =>
              it?.kind === 'video' ? it.video.series_id : null
            const runSeries = seriesOf(item)
            const startsRun = runSeries != null && seriesOf(items[i - 1]) !== runSeries
            let runLength = 0
            if (startsRun) {
              while (seriesOf(items[i + runLength]) === runSeries) runLength++
            }
            const dropHere = showSlot && slot === i
            const cls = [
              'card',
              item.kind === 'group' ? 'series-group' : '',
              runSeries != null ? 'in-series' : '',
              item.key === dragKey ? 'dragging' : '',
              // Inserting before a run belongs above its heading, so the line is drawn there instead.
              dropHere && !startsRun ? 'drop-before' : '',
              showSlot && slot === items.length && i === items.length - 1 ? 'drop-after' : '',
            ]
              .filter(Boolean)
              .join(' ')
            const dragProps = {
              draggable: !busy && editingId == null,
              onPointerDown: (e: PointerEvent<HTMLLIElement>) => {
                // Pointer events cover mouse, touch and pen, so the flag is never left over from another input.
                pressedInteractive.current = !!(e.target as HTMLElement).closest(NO_DRAG)
              },
              onDragStart: (e: DragEvent<HTMLLIElement>) => {
                // The drag source is the item itself, so check whether the press began on a control.
                const fromControl = pressedInteractive.current
                pressedInteractive.current = false
                if (fromControl) {
                  e.preventDefault()
                  return
                }
                e.dataTransfer.setData('text/plain', item.key)
                e.dataTransfer.setData(DRAG_TYPE, item.key)
                e.dataTransfer.effectAllowed = 'move'
                setDragKey(item.key)
              },
              onDragEnd: () => {
                setDragKey(null)
                setSlot(null)
              },
            }
            const moveButtons = (
              <>
                <button
                  type="button"
                  aria-label="Move up"
                  disabled={busy || i === 0}
                  onClick={() => move(i, -1)}
                >
                  ↑
                </button>
                <button
                  type="button"
                  aria-label="Move down"
                  disabled={busy || i === items.length - 1}
                  onClick={() => move(i, 1)}
                >
                  ↓
                </button>
              </>
            )

            if (item.kind === 'group') {
              const gs = seriesById.get(item.seriesId)
              const first = item.videos[0]
              const n = item.videos.length
              const title = gs?.title ?? 'Series'
              return (
                <li key={item.key} data-index={i} className={cls} {...dragProps}>
                  <div className="group-row">
                    <button
                      type="button"
                      className="toggle"
                      aria-label={`Expand ${title}`}
                      aria-expanded={false}
                      onClick={() => toggleSeries(item.seriesId)}
                    >
                      ▸
                    </button>
                    <Thumbnail
                      videoId={gs?.thumbnail_video_id || first.youtube_video_id}
                      title={title}
                      size="sm"
                    />
                    <div className="grow group-text">
                      <div className="card-title">{title}</div>
                      <div className="muted small group-sub">
                        {n} {n === 1 ? 'episode' : 'episodes'} · next: {first.episode && `#${first.episode} `}
                        {first.title}
                      </div>
                    </div>
                    <div className="btn-group">{moveButtons}</div>
                  </div>
                </li>
              )
            }

            const v = item.video
            const s = v.series_id != null ? seriesById.get(v.series_id) : undefined
            return (
              <Fragment key={item.key}>
              {startsRun && (
                <li className={dropHere ? 'series-head drop-before' : 'series-head'}>
                  <button
                    type="button"
                    className="toggle"
                    aria-label={`Collapse ${s?.title ?? 'series'}`}
                    aria-expanded={true}
                    onClick={() => toggleSeries(runSeries)}
                  >
                    ▾
                  </button>
                  <span className="series-head-title">{s?.title ?? 'Series'}</span>
                  <span className="muted small">
                    {runLength} {runLength === 1 ? 'episode' : 'episodes'}
                  </span>
                </li>
              )}
              <li data-index={i} className={cls} {...dragProps}>
                {editingId === v.id ? (
                  <VideoForm
                    series={seriesList}
                    initial={valuesFromVideo(v)}
                    submitLabel="Save"
                    onCancel={() => setEditingId(null)}
                    onSubmit={async (vals) => {
                      await updateVideo(sid, v.id, toPatch(vals))
                      setEditingId(null)
                      videos.reload()
                    }}
                  />
                ) : (
                  <div className="card-head">
                    <div className="media grow">
                      <Thumbnail videoId={v.youtube_video_id} title={v.title} seconds={v.duration_seconds} />
                      <div className="grow">
                      <div className="card-title">
                        {v.episode && <span className="muted">#{v.episode} </span>}
                        {v.url ? (
                          <a href={v.url} target="_blank" rel="noreferrer noopener">
                            {v.title}
                          </a>
                        ) : (
                          v.title
                        )}
                      </div>
                      <div className="muted small">
                        {v.scheduled_for && `Scheduled ${formatDate(v.scheduled_for)}`}
                        {v.scheduled_for && v.notes && ' · '}
                        {v.notes}
                      </div>
                      {v.suggested_by_name && <div className="muted small">Suggested by {v.suggested_by_name}</div>}
                      </div>
                    </div>
                    <div className="btn-group">
                      {moveButtons}
                      <button
                        type="button"
                        className="primary"
                        disabled={busy}
                        onClick={() => act(() => updateVideo(sid, v.id, { status: 'reacted' }))}
                      >
                        Mark reacted
                      </button>
                      <button type="button" disabled={busy} onClick={() => setEditingId(v.id)}>
                        Edit
                      </button>
                      <button type="button" className="danger" disabled={busy} onClick={() => remove(v)}>
                        Delete
                      </button>
                    </div>
                  </div>
                )}
              </li>
              </Fragment>
            )
          })}
        </ol>
      )}
    </>
  )
}
